package consent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withFreshState resets every package-level variable consent owns so
// tests don't leak state into one another. It snapshots the prior
// values and restores them on cleanup, which keeps the package's
// global surface usable by tests without flaking parallel runs.
//
// Every test in this file must call withFreshState as its first line.
func withFreshState(t *testing.T) {
	t.Helper()

	prevShowModal := ShowModal
	prevWarning := WarningBanner
	prevAutoApprove := autoApprove.Load()
	prevNow := Now
	prevPromptIn := PromptIn
	prevPromptOut := PromptOut
	prevBannerOut := BannerOut

	ShowModal = denyModal
	WarningBanner = defaultWarningBanner
	empty := []string{}
	autoApprove.Store(&empty)
	Now = time.Now
	PromptIn = strings.NewReader("")
	PromptOut = io.Discard
	BannerOut = io.Discard
	SetAuditWriter(io.Discard)
	ResetSession()

	t.Cleanup(func() {
		ShowModal = prevShowModal
		WarningBanner = prevWarning
		autoApprove.Store(prevAutoApprove)
		Now = prevNow
		PromptIn = prevPromptIn
		PromptOut = prevPromptOut
		BannerOut = prevBannerOut
		SetAuditWriter(io.Discard)
		ResetSession()
	})
}

// TestGate_DefaultDeniesWithoutUIOverride pins the bedrock safety
// property: a fake write-tool call returns Deny when no UI override
// has registered itself.
func TestGate_DefaultDeniesWithoutUIOverride(t *testing.T) {
	withFreshState(t)
	got := Gate(context.Background(), Request{
		Tool:   "cfn/update-stack",
		Reason: "fix health check",
	})
	if got != Deny {
		t.Fatalf("Gate without UI override = %v, want Deny", got)
	}
}

// TestGate_EmptyReasonShortCircuitsToDeny verifies ADR-0036's
// "non-empty reason REQUIRED" rule: the modal is never consulted
// when Reason is missing.
func TestGate_EmptyReasonShortCircuitsToDeny(t *testing.T) {
	withFreshState(t)
	var called bool
	ShowModal = func(Request) Decision {
		called = true
		return ApproveOnce
	}
	cases := []string{"", "   ", "\n\t"}
	for _, reason := range cases {
		got := Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: reason})
		if got != Deny {
			t.Errorf("Reason=%q → %v, want Deny", reason, got)
		}
	}
	if called {
		t.Error("ShowModal was consulted for an empty Reason; expected short-circuit")
	}
}

// TestGate_AutoApproveOnlyMatchingTool covers the DoD bullet:
// AutoApproveTools=["cfn/cancel-update-stack"] auto-approves only
// that tool and shows a registered warning banner.
func TestGate_AutoApproveOnlyMatchingTool(t *testing.T) {
	withFreshState(t)
	var banner bytes.Buffer
	BannerOut = &banner

	var modalCalls int
	ShowModal = func(Request) Decision {
		modalCalls++
		return Deny
	}

	SetAutoApprove([]string{"cfn/cancel-update-stack"})

	if !strings.Contains(banner.String(), "cfn/cancel-update-stack") {
		t.Errorf("banner missing auto-approve tool name; got %q", banner.String())
	}

	if got := Gate(context.Background(), Request{
		Tool:   "cfn/cancel-update-stack",
		Reason: "rollback stuck stack",
	}); got != ApproveOnce {
		t.Errorf("auto-approved tool = %v, want ApproveOnce", got)
	}

	if got := Gate(context.Background(), Request{
		Tool:   "cfn/update-stack",
		Reason: "tweak param",
	}); got != Deny {
		t.Errorf("non-listed tool = %v, want Deny", got)
	}
	if modalCalls != 1 {
		t.Errorf("ShowModal calls = %d, want 1 (only the non-listed tool should reach it)", modalCalls)
	}
}

// TestSetAutoApprove_RegistersBannerOnlyWhenNonEmpty verifies the
// banner contract: SetAutoApprove invokes WarningBanner with the
// sanitised list when it has at least one entry, and is silent
// otherwise.
func TestSetAutoApprove_RegistersBannerOnlyWhenNonEmpty(t *testing.T) {
	withFreshState(t)
	var got []string
	var calls int
	WarningBanner = func(tools []string) {
		calls++
		got = append([]string(nil), tools...)
	}

	SetAutoApprove(nil)
	if calls != 0 {
		t.Errorf("WarningBanner calls for nil list = %d, want 0", calls)
	}

	SetAutoApprove([]string{"cfn/cancel-update-stack", "cfn/delete-stack"})
	if calls != 1 {
		t.Fatalf("WarningBanner calls = %d, want 1", calls)
	}
	want := []string{"cfn/cancel-update-stack", "cfn/delete-stack"}
	if len(got) != len(want) {
		t.Fatalf("WarningBanner got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] %q != %q", i, got[i], w)
		}
	}
}

// TestSetAutoApprove_TrimsAndDedupes verifies SetAutoApprove
// canonicalises the input so configs with stray whitespace or
// repeats don't leak through to AutoApproved.
func TestSetAutoApprove_TrimsAndDedupes(t *testing.T) {
	withFreshState(t)
	SetAutoApprove([]string{
		"  cfn/cancel-update-stack  ",
		"cfn/cancel-update-stack",
		"",
		"  ",
		"cfn/delete-stack",
	})
	if !AutoApproved("cfn/cancel-update-stack") {
		t.Error("AutoApproved(cfn/cancel-update-stack) = false, want true")
	}
	if !AutoApproved("cfn/delete-stack") {
		t.Error("AutoApproved(cfn/delete-stack) = false, want true")
	}
	if AutoApproved("") {
		t.Error("AutoApproved(\"\") = true, want false")
	}
}

// TestGate_SessionApprovalSkipsModalOnSecondCall verifies that after
// ApproveSession, the next call of the same tool bypasses the modal.
func TestGate_SessionApprovalSkipsModalOnSecondCall(t *testing.T) {
	withFreshState(t)
	var calls int
	ShowModal = func(Request) Decision {
		calls++
		return ApproveSession
	}
	if got := Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "first"}); got != ApproveSession {
		t.Fatalf("first Gate = %v, want ApproveSession", got)
	}
	if got := Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "second"}); got != ApproveSession {
		t.Errorf("second Gate = %v, want ApproveSession", got)
	}
	if calls != 1 {
		t.Errorf("ShowModal calls = %d, want 1 (second should hit the session cache)", calls)
	}
}

// TestGate_SessionApprovalIsPerTool verifies that approving one tool
// for the session does not approve a sibling.
func TestGate_SessionApprovalIsPerTool(t *testing.T) {
	withFreshState(t)
	ShowModal = func(req Request) Decision {
		if req.Tool == "cfn/update-stack" {
			return ApproveSession
		}
		return Deny
	}
	if got := Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "go"}); got != ApproveSession {
		t.Fatalf("cfn/update-stack = %v, want ApproveSession", got)
	}
	if got := Gate(context.Background(), Request{Tool: "cfn/delete-stack", Reason: "go"}); got != Deny {
		t.Errorf("cfn/delete-stack = %v, want Deny", got)
	}
}

// TestGate_SessionApprovalExpiresAfterIdle verifies that a session
// approval lapses after sessionIdleTimeout of inactivity.
func TestGate_SessionApprovalExpiresAfterIdle(t *testing.T) {
	withFreshState(t)
	base := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	Now = func() time.Time { return base }
	var modalCalls int
	ShowModal = func(Request) Decision {
		modalCalls++
		return ApproveSession
	}
	Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "go"})

	Now = func() time.Time { return base.Add(sessionIdleTimeout + time.Minute) }
	Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "go"})

	if modalCalls != 2 {
		t.Errorf("ShowModal calls = %d, want 2 (idle session should re-prompt)", modalCalls)
	}
}

// TestResetSession_ClearsApprovalsAndRotatesID verifies /ai end: the
// previous session approvals lapse and the audit session_id changes.
func TestResetSession_ClearsApprovalsAndRotatesID(t *testing.T) {
	withFreshState(t)
	var modalCalls int
	ShowModal = func(Request) Decision {
		modalCalls++
		return ApproveSession
	}
	Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "x"})

	before := SessionID()
	ResetSession()
	after := SessionID()
	if before == after {
		t.Error("ResetSession did not rotate SessionID")
	}

	Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "x"})
	if modalCalls != 2 {
		t.Errorf("ShowModal calls = %d, want 2 (ResetSession should re-prompt)", modalCalls)
	}
}

// TestAudit_OneRecordPerDecision covers the DoD bullet: audit.jsonl
// receives one record per consent decision, with the documented
// schema.
func TestAudit_OneRecordPerDecision(t *testing.T) {
	withFreshState(t)
	ShowModal = func(Request) Decision { return ApproveOnce }
	var buf bytes.Buffer
	SetAuditWriter(&buf)

	Gate(context.Background(), Request{
		Tool:       "cfn/update-stack",
		Reason:     "fix",
		UserReason: "looks good",
		Args:       []byte(`{"StackName":"alb"}`),
	})
	Gate(context.Background(), Request{Tool: "cfn/delete-stack", Reason: "go"})

	lines := splitLines(buf.Bytes())
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d, want 2 (got %q)", len(lines), buf.String())
	}

	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("decode line 0: %v", err)
	}
	for _, k := range []string{"time", "session_id", "tool", "args_hash", "decision"} {
		if _, ok := first[k]; !ok {
			t.Errorf("missing key %q in record: %v", k, first)
		}
	}
	if got := first["tool"]; got != "cfn/update-stack" {
		t.Errorf("tool = %v, want cfn/update-stack", got)
	}
	if got := first["decision"]; got != "approve_once" {
		t.Errorf("decision = %v, want approve_once", got)
	}
	if got := first["user_reason"]; got != "looks good" {
		t.Errorf("user_reason = %v, want 'looks good'", got)
	}
	hash, _ := first["args_hash"].(string)
	if !strings.HasPrefix(hash, "sha256:") || len(hash) != len("sha256:")+64 {
		t.Errorf("args_hash = %q, want sha256:<64-hex>", hash)
	}
}

// TestAudit_DeniedRecordsToo verifies a denied call still produces
// one audit line — denials are the most important records to keep.
func TestAudit_DeniedRecordsToo(t *testing.T) {
	withFreshState(t)
	var buf bytes.Buffer
	SetAuditWriter(&buf)

	got := Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "fix"})
	if got != Deny {
		t.Fatalf("Gate = %v, want Deny", got)
	}
	rec := parseLastRecord(t, buf.Bytes())
	if rec["decision"] != "deny" {
		t.Errorf("decision = %v, want deny", rec["decision"])
	}
}

// TestAudit_EmptyReasonRecorded verifies the empty-reason
// short-circuit still leaves a paper trail (a denial with no
// user_reason).
func TestAudit_EmptyReasonRecorded(t *testing.T) {
	withFreshState(t)
	var buf bytes.Buffer
	SetAuditWriter(&buf)

	Gate(context.Background(), Request{Tool: "cfn/update-stack"})
	rec := parseLastRecord(t, buf.Bytes())
	if rec["decision"] != "deny" {
		t.Errorf("decision = %v, want deny", rec["decision"])
	}
}

// TestInitAudit_AppendsToOnDiskFile verifies the on-disk path
// matches ADR-0036 (<home>/ai/audit.jsonl) and that subsequent
// InitAudit calls append rather than truncate.
func TestInitAudit_AppendsToOnDiskFile(t *testing.T) {
	withFreshState(t)
	home := t.TempDir()
	if err := InitAudit(home); err != nil {
		t.Fatalf("InitAudit: %v", err)
	}
	Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "fix"})
	if err := InitAudit(home); err != nil {
		t.Fatalf("InitAudit (second): %v", err)
	}
	Gate(context.Background(), Request{Tool: "cfn/update-stack", Reason: "fix again"})

	path := filepath.Join(home, Subdir, Filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := splitLines(data)
	if len(lines) != 2 {
		t.Errorf("audit lines = %d, want 2 (file = %q)", len(lines), data)
	}
}

// TestPromptStdinModal_ApproveOnce verifies the headless fallback
// returns ApproveOnce when the user types "y".
func TestPromptStdinModal_ApproveOnce(t *testing.T) {
	withFreshState(t)
	PromptIn = strings.NewReader("y\n")
	if got := PromptStdinModal(Request{Tool: "cfn/update-stack", Reason: "go"}); got != ApproveOnce {
		t.Errorf("PromptStdinModal('y') = %v, want ApproveOnce", got)
	}
}

func TestPromptStdinModal_ApproveSession(t *testing.T) {
	withFreshState(t)
	PromptIn = strings.NewReader("s\n")
	if got := PromptStdinModal(Request{Tool: "cfn/update-stack", Reason: "go"}); got != ApproveSession {
		t.Errorf("PromptStdinModal('s') = %v, want ApproveSession", got)
	}
}

func TestPromptStdinModal_DefaultsToDenyOnEOF(t *testing.T) {
	withFreshState(t)
	PromptIn = strings.NewReader("")
	if got := PromptStdinModal(Request{Tool: "cfn/update-stack", Reason: "go"}); got != Deny {
		t.Errorf("PromptStdinModal(EOF) = %v, want Deny", got)
	}
}

func TestPromptStdinModal_DefaultsToDenyOnGarbage(t *testing.T) {
	withFreshState(t)
	PromptIn = strings.NewReader("???\n")
	if got := PromptStdinModal(Request{Tool: "cfn/update-stack", Reason: "go"}); got != Deny {
		t.Errorf("PromptStdinModal('???') = %v, want Deny", got)
	}
}

// TestPromptStdinModal_RendersRequest verifies the rendered prompt
// includes the tool, reason, and decision keys so a user staring at
// stderr knows what they are answering.
func TestPromptStdinModal_RendersRequest(t *testing.T) {
	withFreshState(t)
	var out bytes.Buffer
	PromptOut = &out
	PromptIn = strings.NewReader("n\n")
	PromptStdinModal(Request{
		Tool:      "cfn/update-stack",
		Account:   "654654333582",
		Profile:   "babe",
		Region:    "ap-northeast-1",
		Resource:  "alb-stack-babe-main-api-prd-node",
		Args:      []byte(`HealthCheckPath: "/api/health" → "/api/healthz"`),
		Reason:    "fix unhealthy hosts",
		BlastHint: "Live ALB; ~3 min UPDATE_IN_PROGRESS.",
	})
	for _, want := range []string{
		"cfn/update-stack",
		"654654333582",
		"babe",
		"ap-northeast-1",
		"alb-stack-babe-main-api-prd-node",
		"fix unhealthy hosts",
		"Live ALB",
		"approve once",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", want, out.String())
		}
	}
}

// TestDecisionString verifies the audit-log spellings match
// ADR-0036.
func TestDecisionString(t *testing.T) {
	cases := []struct {
		d    Decision
		want string
	}{
		{Deny, "deny"},
		{ApproveOnce, "approve_once"},
		{ApproveSession, "approve_session"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Errorf("Decision(%d).String() = %q, want %q", c.d, got, c.want)
		}
	}
}

// parseLastRecord decodes the final non-empty JSONL line in data.
// It fails the test if no record is present.
func parseLastRecord(t *testing.T, data []byte) map[string]any {
	t.Helper()
	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatalf("no audit lines in %q", data)
	}
	var rec map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &rec); err != nil {
		t.Fatalf("decode: %v (line=%q)", err, lines[len(lines)-1])
	}
	return rec
}

// splitLines returns the non-empty JSONL lines in data.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		out = append(out, line)
	}
	return out
}
