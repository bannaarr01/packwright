package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// CodePipelineClient is the narrow CodePipeline surface [Pipeline]
// uses. LatestExecutionStart returns the latestExecution.startTime per
// pipeline, or nil with nil error when the pipeline has no executions.
type CodePipelineClient interface {
	LatestExecutionStart(ctx context.Context, pipelineName string) (*time.Time, error)
}

// PipelineInput collects the per-pipeline facts the scanner has from
// ListPipelines.
type PipelineInput struct {
	// PipelineName is the pipeline's name, passed to GetPipelineState.
	PipelineName string
	// Now is the reference time.
	Now time.Time
}

// Pipeline composes the ADR-0041 signals for a
// codepipeline/artifacts entry: latestExecution.startTime per pipeline.
// Pipelines un-run for ≥ 60 days are flagged.
//
// The "artifact bucket size trend" hinted at in ADR-0041 is intentionally
// not implemented here: it requires cross-pipeline correlation that
// PR-05's cache + UI summary is better placed to do without paying for
// every pipeline up front. Add it as an additional source in a follow-
// up when that data becomes available.
func Pipeline(ctx context.Context, c CodePipelineClient, in PipelineInput) lastused.LastUsedSignal {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		pipelineExecutionSource(ctx, c, in.PipelineName),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		exec := lastused.SourceByName(ss, "pipeline.latest-execution")
		switch {
		case exec != nil && exec.HasValue() && lastused.Within(*exec.Value, now, lastused.Days(60)):
			return lastused.High, ""
		case best.IsZero():
			return lastused.Unknown, ""
		case !lastused.Within(best, now, lastused.Days(60)):
			return lastused.Low, "Pipeline has not run in ≥60 d — likely unused."
		default:
			return lastused.Medium, ""
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}

// pipelineExecutionSource calls the CodePipeline client and turns the
// result into a LastUsedSource. Cost is 1 (one GetPipelineState call).
func pipelineExecutionSource(ctx context.Context, c CodePipelineClient, name string) lastused.LastUsedSource {
	src := lastused.LastUsedSource{Name: "pipeline.latest-execution"}
	if c == nil {
		return src
	}
	src.Cost = 1
	if t, err := c.LatestExecutionStart(ctx, name); err == nil {
		src.Value = sources.CopyTimePtr(t)
	}
	return src
}
