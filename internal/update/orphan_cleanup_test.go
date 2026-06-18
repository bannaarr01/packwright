package update

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/render/cfn"
)

// stubChangeSetAPI is a minimal cfn.ChangeSetAPI implementation that backs
// ListChangeSets / DeleteChangeSet for the orphan-cleanup tests. The other
// methods return zero values so the type compiles; the tests don't call
// them.
type stubChangeSetAPI struct {
	listFunc    func(*cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error)
	deleteFunc  func(*cloudformation.DeleteChangeSetInput) (*cloudformation.DeleteChangeSetOutput, error)
	deleteCalls []string
}

func (s *stubChangeSetAPI) CreateChangeSet(_ context.Context, _ *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	return &cloudformation.CreateChangeSetOutput{}, nil
}
func (s *stubChangeSetAPI) DescribeChangeSet(_ context.Context, _ *cloudformation.DescribeChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	return &cloudformation.DescribeChangeSetOutput{}, nil
}
func (s *stubChangeSetAPI) ExecuteChangeSet(_ context.Context, _ *cloudformation.ExecuteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}
func (s *stubChangeSetAPI) DeleteChangeSet(_ context.Context, in *cloudformation.DeleteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error) {
	s.deleteCalls = append(s.deleteCalls, aws.ToString(in.ChangeSetName))
	if s.deleteFunc != nil {
		return s.deleteFunc(in)
	}
	return &cloudformation.DeleteChangeSetOutput{}, nil
}
func (s *stubChangeSetAPI) ListChangeSets(_ context.Context, in *cloudformation.ListChangeSetsInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListChangeSetsOutput, error) {
	if s.listFunc != nil {
		return s.listFunc(in)
	}
	return &cloudformation.ListChangeSetsOutput{}, nil
}

// mkSummary builds a ChangeSetSummary fixture concisely. tShift is the
// negative offset from "now" — i.e. tShift=25*time.Hour means "26 hours
// old" relative to the test's fixed clock.
func mkSummary(name string, tShift time.Duration, status, exec cfntypes.ChangeSetStatus, execStatus cfntypes.ExecutionStatus) cfntypes.ChangeSetSummary {
	_ = exec
	return cfntypes.ChangeSetSummary{
		ChangeSetId:     aws.String("arn:cs:" + name),
		ChangeSetName:   aws.String(name),
		StackName:       aws.String("acme-dev-alb"),
		Status:          status,
		ExecutionStatus: execStatus,
		CreationTime:    aws.Time(now.Add(-tShift)),
	}
}

var now = time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

func TestOrphanScan_EnumeratesTwoOlderPackwrightCandidates(t *testing.T) {
	api := &stubChangeSetAPI{}
	api.listFunc = func(_ *cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error) {
		return &cloudformation.ListChangeSetsOutput{
			Summaries: []cfntypes.ChangeSetSummary{
				// Two Packwright-owned, > 24h old, un-executed: match.
				mkSummary("packwright-1700000000", 30*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
				mkSummary("packwright-1700000100", 26*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
				// AWS-managed, ignored.
				mkSummary("aws-cs-stack-1", 30*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
				// Recent Packwright, ignored.
				mkSummary("packwright-1700009999", 30*time.Minute, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
				// Old Packwright but already executed, ignored.
				mkSummary("packwright-1700001234", 48*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusExecuteComplete),
			},
		}, nil
	}

	scan := &OrphanScanner{API: api, Cooldown: time.Millisecond}
	res, err := scan.Scan(context.Background(), OrphanScanRequest{
		StackNames: []string{"acme-dev-alb"},
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Scan err = %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", res.Errors)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("Candidates = %d, want 2 — got: %+v", len(res.Candidates), res.Candidates)
	}
	// Sorted oldest first.
	if res.Candidates[0].ChangeSetName != "packwright-1700000000" {
		t.Errorf("first candidate = %q, want oldest packwright-1700000000", res.Candidates[0].ChangeSetName)
	}
	if res.Candidates[1].ChangeSetName != "packwright-1700000100" {
		t.Errorf("second candidate = %q, want packwright-1700000100", res.Candidates[1].ChangeSetName)
	}
	for _, c := range res.Candidates {
		if !cfn.IsPackwrightChangeSet(c.ChangeSetName) {
			t.Errorf("candidate %q is not a packwright change set", c.ChangeSetName)
		}
		if !strings.HasPrefix(c.ChangeSetID, "arn:cs:") {
			t.Errorf("candidate %q has unexpected ARN %q", c.ChangeSetName, c.ChangeSetID)
		}
	}
}

func TestOrphanScan_NoCandidatesWhenAllRecent(t *testing.T) {
	api := &stubChangeSetAPI{listFunc: func(_ *cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error) {
		return &cloudformation.ListChangeSetsOutput{
			Summaries: []cfntypes.ChangeSetSummary{
				mkSummary("packwright-fresh", 1*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
			},
		}, nil
	}}
	scan := &OrphanScanner{API: api, Cooldown: time.Millisecond}
	res, _ := scan.Scan(context.Background(), OrphanScanRequest{
		StackNames: []string{"acme-dev-alb"},
		Now:        func() time.Time { return now },
	})
	if len(res.Candidates) != 0 {
		t.Errorf("Candidates = %d, want 0", len(res.Candidates))
	}
}

func TestOrphanScan_PerStackErrorsAreAggregated(t *testing.T) {
	api := &stubChangeSetAPI{}
	api.listFunc = func(in *cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error) {
		if aws.ToString(in.StackName) == "bad" {
			return nil, errors.New("access denied")
		}
		return &cloudformation.ListChangeSetsOutput{
			Summaries: []cfntypes.ChangeSetSummary{
				mkSummary("packwright-good", 30*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
			},
		}, nil
	}
	scan := &OrphanScanner{API: api, Cooldown: time.Millisecond}
	res, err := scan.Scan(context.Background(), OrphanScanRequest{
		StackNames: []string{"good", "bad"},
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("Scan err = %v (want partial success)", err)
	}
	if len(res.Candidates) != 1 {
		t.Errorf("Candidates = %d, want 1", len(res.Candidates))
	}
	if res.Errors["bad"] == nil {
		t.Errorf("Errors[bad] = nil, want access-denied err")
	}
}

func TestOrphanScan_RateLimitServesCachedResult(t *testing.T) {
	listCalls := 0
	api := &stubChangeSetAPI{listFunc: func(_ *cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error) {
		listCalls++
		return &cloudformation.ListChangeSetsOutput{
			Summaries: []cfntypes.ChangeSetSummary{
				mkSummary("packwright-old", 30*time.Hour, cfntypes.ChangeSetStatusCreateComplete, "", cfntypes.ExecutionStatusAvailable),
			},
		}, nil
	}}
	scan := &OrphanScanner{API: api, Cooldown: 10 * time.Minute}
	tNow := now
	for i := 0; i < 3; i++ {
		if _, err := scan.Scan(context.Background(), OrphanScanRequest{
			StackNames: []string{"acme-dev-alb"},
			Now:        func() time.Time { return tNow },
		}); err != nil {
			t.Fatalf("Scan[%d] err = %v", i, err)
		}
	}
	if listCalls != 1 {
		t.Errorf("List calls = %d, want 1 (cache should serve later scans)", listCalls)
	}
	if got := scan.CacheHits(); got != 2 {
		t.Errorf("CacheHits = %d, want 2", got)
	}

	// Past the cooldown the cache expires.
	tNow = tNow.Add(20 * time.Minute)
	if _, err := scan.Scan(context.Background(), OrphanScanRequest{
		StackNames: []string{"acme-dev-alb"},
		Now:        func() time.Time { return tNow },
	}); err != nil {
		t.Fatalf("Scan post-cooldown err = %v", err)
	}
	if listCalls != 2 {
		t.Errorf("List calls = %d, want 2 after cooldown", listCalls)
	}
}

func TestOrphanCleanup_DeletesCandidatesAndInvalidatesCache(t *testing.T) {
	api := &stubChangeSetAPI{}
	scan := &OrphanScanner{API: api, Cooldown: time.Hour}

	// Seed a stale cache entry.
	scan.lastScan = OrphanScanResult{ScannedAt: now}
	scan.lastAt = now

	res := scan.Cleanup(context.Background(), []OrphanCandidate{
		{ChangeSetSummary: cfn.ChangeSetSummary{ChangeSetID: "arn:cs:1", StackName: "s"}},
		{ChangeSetSummary: cfn.ChangeSetSummary{ChangeSetID: "arn:cs:2", StackName: "s"}},
	})
	if len(res) != 0 {
		t.Errorf("errors = %v, want empty", res)
	}
	if len(api.deleteCalls) != 2 {
		t.Errorf("delete calls = %d, want 2", len(api.deleteCalls))
	}
	// Cache invalidated.
	if !scan.lastAt.IsZero() {
		t.Errorf("lastAt = %v, want zero after successful cleanup", scan.lastAt)
	}
}

func TestOrphanCleanup_FailureKeepsCache(t *testing.T) {
	api := &stubChangeSetAPI{}
	api.deleteFunc = func(_ *cloudformation.DeleteChangeSetInput) (*cloudformation.DeleteChangeSetOutput, error) {
		return nil, errors.New("boom")
	}
	scan := &OrphanScanner{API: api, Cooldown: time.Hour}
	scan.lastScan = OrphanScanResult{ScannedAt: now}
	scan.lastAt = now

	res := scan.Cleanup(context.Background(), []OrphanCandidate{
		{ChangeSetSummary: cfn.ChangeSetSummary{ChangeSetID: "arn:cs:1", StackName: "s"}},
	})
	if len(res) != 1 {
		t.Errorf("errors = %v, want 1", res)
	}
	if scan.lastAt.IsZero() {
		t.Error("lastAt = zero, want preserved when cleanup partially failed")
	}
}
