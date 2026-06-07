package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/monitorx"
)

// fakeCW satisfies monitorx.MetricsAPI. Each call returns the canned
// response, regardless of input; one CW client backs every metric panel in
// the test so we can assert on call counts.
type fakeCW struct {
	calls atomic.Int64
	value float64
}

func (f *fakeCW) GetMetricData(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.calls.Add(1)
	return &cloudwatch.GetMetricDataOutput{
		MetricDataResults: []cwtypes.MetricDataResult{{
			Id:         aws.String("m1"),
			Timestamps: []time.Time{time.Now()},
			Values:     []float64{f.value},
		}},
	}, nil
}

// fakeLogsClient satisfies monitorx.LogsAPI.
type fakeLogsClient struct {
	calls atomic.Int64
}

func (f *fakeLogsClient) FilterLogEvents(_ context.Context, _ *cloudwatchlogs.FilterLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	f.calls.Add(1)
	return &cloudwatchlogs.FilterLogEventsOutput{
		Events: []cwltypes.FilteredLogEvent{
			{Timestamp: aws.Int64(time.Now().UnixMilli()), Message: aws.String("hi"), LogStreamName: aws.String("s1")},
		},
	}, nil
}

// silentLogger returns a slog.Logger that discards every record; using it
// in tests keeps the harness output clean and avoids -v noise on failure.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fixtureManifest is the YAML fixture the PR-03 acceptance test calls out:
// two cloudwatch/metric panels and one cloudwatch/logs-tail panel.
const fixtureManifest = `
title: "API service"
refresh_every: 50ms
panels:
  - id: cpu
    title: "EC2 CPU"
    kind: cloudwatch/metric
    spec:
      namespace: AWS/EC2
      metric: CPUUtilization
      statistic: Average
      lookback: 1h
      period: 60
      unit: Percent
      dimensions:
        InstanceId: i-0123456789abcdef0
  - id: net
    title: "EC2 NetworkIn"
    kind: cloudwatch/metric
    spec:
      namespace: AWS/EC2
      metric: NetworkIn
      statistic: Sum
      lookback: 1h
      period: 60
      unit: Bytes
  - id: errors
    title: "API errors"
    kind: cloudwatch/logs-tail
    spec:
      log_group: /aws/lambda/api
      filter: "ERROR"
      lookback: 5m
      limit: 50
`

func TestDecodeSpecAndValidate(t *testing.T) {
	spec, err := DecodeSpec([]byte(fixtureManifest))
	if err != nil {
		t.Fatalf("DecodeSpec: %v", err)
	}
	if spec.Title != "API service" {
		t.Fatalf("title = %q", spec.Title)
	}
	if spec.RefreshEvery != 50*time.Millisecond {
		t.Fatalf("refresh = %v", spec.RefreshEvery)
	}
	if len(spec.Panels) != 3 {
		t.Fatalf("panels = %d, want 3", len(spec.Panels))
	}
	r := New(monitorx.Deps{
		Metrics: &fakeCW{},
		Logs:    &fakeLogsClient{},
		Log:     silentLogger(),
	})
	if err := r.Validate(spec); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDecodeSpecRejectsUnknownKeys(t *testing.T) {
	if _, err := DecodeSpec([]byte(`bogus: true`)); err == nil {
		t.Fatal("DecodeSpec(unknown key): err = nil, want one")
	}
}

func TestKindIsMonitor(t *testing.T) {
	r := New(monitorx.Deps{})
	if r.Kind() != manifest.KindMonitor {
		t.Fatalf("Kind = %q, want %q", r.Kind(), manifest.KindMonitor)
	}
}

func TestValidateRejectsDuplicateIDs(t *testing.T) {
	spec := &Spec{Panels: []PanelSpec{
		{ID: "a", Kind: "cloudwatch/metric", Spec: map[string]any{"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "1h"}},
		{ID: "a", Kind: "cloudwatch/metric", Spec: map[string]any{"namespace": "AWS/EC2", "metric": "Y", "statistic": "Average", "lookback": "1h"}},
	}}
	if err := New(monitorx.Deps{}).Validate(spec); err == nil {
		t.Fatal("Validate(duplicate id): err = nil, want one")
	}
}

func TestRunFixtureManifestRefreshes(t *testing.T) {
	spec, err := DecodeSpec([]byte(fixtureManifest))
	if err != nil {
		t.Fatalf("DecodeSpec: %v", err)
	}

	cw, logsc := &fakeCW{value: 42}, &fakeLogsClient{}
	runner := New(monitorx.Deps{
		Metrics: cw,
		Logs:    logsc,
		Log:     silentLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := runner.Run(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := make(map[string]Update, 3)
	deadline := time.After(time.Second)
	for len(got) < 3 {
		select {
		case u, ok := <-res.Updates:
			if !ok {
				t.Fatalf("Updates closed early (got %d/3)", len(got))
			}
			if u.Err != nil {
				t.Fatalf("panel %q errored: %v", u.PanelID, u.Err)
			}
			got[u.PanelID] = u
		case <-deadline:
			t.Fatalf("timed out waiting for 3 updates; got %d", len(got))
		}
	}

	if _, ok := got["cpu"].Data.(monitorx.SeriesData); !ok {
		t.Fatalf("cpu data type = %T, want SeriesData", got["cpu"].Data)
	}
	if _, ok := got["net"].Data.(monitorx.SeriesData); !ok {
		t.Fatalf("net data type = %T, want SeriesData", got["net"].Data)
	}
	if _, ok := got["errors"].Data.(monitorx.LogLinesData); !ok {
		t.Fatalf("errors data type = %T, want LogLinesData", got["errors"].Data)
	}

	res.Stop()
	if err := res.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}
	// Drain anything still in the channel post-Stop.
	for range res.Updates {
	}

	if cw.calls.Load() < 2 {
		t.Fatalf("cw calls = %d, want >= 2 (one per metric panel on first tick)", cw.calls.Load())
	}
	if logsc.calls.Load() < 1 {
		t.Fatalf("logs calls = %d, want >= 1", logsc.calls.Load())
	}
}

func TestRunBuildFailureSurfacesAsUpdate(t *testing.T) {
	spec := &Spec{
		RefreshEvery: 100 * time.Millisecond,
		Panels: []PanelSpec{
			{ID: "good", Kind: "cloudwatch/metric", Spec: map[string]any{
				"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "1h",
			}},
			{ID: "bad", Kind: "cloudwatch/metric", Spec: map[string]any{
				// missing required keys
			}},
		},
	}
	runner := New(monitorx.Deps{Metrics: &fakeCW{}, Log: silentLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := runner.Run(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() {
		res.Stop()
		_ = res.Wait()
		for range res.Updates {
		}
	}()

	gotBuildErr := false
	gotGood := false
	deadline := time.After(time.Second)
	for !(gotBuildErr && gotGood) {
		select {
		case u, ok := <-res.Updates:
			if !ok {
				t.Fatalf("Updates closed early")
			}
			if u.PanelID == "bad" && u.Err != nil {
				gotBuildErr = true
			}
			if u.PanelID == "good" && u.Err == nil {
				gotGood = true
			}
		case <-deadline:
			t.Fatalf("timed out: gotBuildErr=%v gotGood=%v", gotBuildErr, gotGood)
		}
	}
}

// slowPanel is a test-only panel kind that blocks Refresh for delay or until
// ctx is cancelled. It lets us prove that a slow panel does not gate the
// progress of sibling panels in the same tick.
type slowPanel struct {
	delay time.Duration
}

func (s *slowPanel) Kind() string { return "test/slow" }
func (s *slowPanel) Validate(spec map[string]any) error {
	d, _ := spec["delay"].(string)
	dur, err := time.ParseDuration(d)
	if err != nil {
		return err
	}
	s.delay = dur
	return nil
}
func (s *slowPanel) Refresh(ctx context.Context, _ monitorx.Deps) (monitorx.PanelData, error) {
	t := time.NewTimer(s.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return monitorx.SeriesData{Series: []monitorx.Series{{Label: "slow"}}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func init() {
	monitorx.Register("test/slow", func() monitorx.Panel { return &slowPanel{} })
}

func TestRunSlowPanelDoesNotBlockSiblings(t *testing.T) {
	spec := &Spec{
		RefreshEvery: 2 * time.Second,
		Panels: []PanelSpec{
			{ID: "slow", Kind: "test/slow", Spec: map[string]any{"delay": "1s"}},
			{ID: "fast1", Kind: "cloudwatch/metric", Spec: map[string]any{
				"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "1h",
			}},
			{ID: "fast2", Kind: "cloudwatch/metric", Spec: map[string]any{
				"namespace": "AWS/EC2", "metric": "Y", "statistic": "Average", "lookback": "1h",
			}},
		},
	}
	runner := New(monitorx.Deps{Metrics: &fakeCW{}, Log: silentLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := runner.Run(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() {
		res.Stop()
		for range res.Updates {
		}
		_ = res.Wait()
	}()

	// Both fast panels must arrive well before the 1s slow panel — that's
	// the contract: per-tick fan-out is concurrent. The deadline is set
	// generously (400ms) so the test is not brittle on slow CI runners
	// while still proving fast panels complete in a fraction of the
	// slow panel's duration.
	gotFast := 0
	deadline := time.After(400 * time.Millisecond)
fast:
	for gotFast < 2 {
		select {
		case u := <-res.Updates:
			if u.Err != nil {
				t.Fatalf("fast panel %q errored: %v", u.PanelID, u.Err)
			}
			if u.PanelID == "fast1" || u.PanelID == "fast2" {
				gotFast++
			}
		case <-deadline:
			break fast
		}
	}
	if gotFast != 2 {
		t.Fatalf("only %d/2 fast panels arrived in 400ms (slow panel blocked them)", gotFast)
	}
}

func TestRunCancelPropagates(t *testing.T) {
	spec := &Spec{
		RefreshEvery: 100 * time.Millisecond,
		Panels: []PanelSpec{
			{ID: "blocker", Kind: "test/slow", Spec: map[string]any{"delay": "10s"}},
		},
	}
	runner := New(monitorx.Deps{Log: silentLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	res, err := runner.Run(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Cancel before the slow panel could possibly complete; the panel
	// must see ctx done and the loop must drain promptly.
	cancel()
	doneCh := make(chan error, 1)
	go func() { doneCh <- res.Wait() }()

	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return within 1s of cancel — cancellation did not propagate")
	}
	for range res.Updates {
	}
}
