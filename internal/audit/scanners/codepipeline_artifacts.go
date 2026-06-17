package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"

	"github.com/bannaarr01/packwright/internal/audit"
)

// CodePipelineArtifacts enumerates every CodePipeline pipeline in the
// audit Client's region. The "artifacts" name in ADR-0040 reflects the
// intent — the user-facing scanner is "things that produce CI/CD
// artifacts we may be paying for and forgetting about" — but the
// inventory unit is the pipeline itself; the artifact store sits behind
// each row via GetPipelineState.
type CodePipelineArtifacts struct{}

// Kind reports the stable kind identifier.
func (CodePipelineArtifacts) Kind() string { return "codepipeline/artifacts" }

// Permissions reports the IAM actions Scan touches. GetPipelineState is
// declared because the per-row "is this pipeline idle" detail call uses
// it; declaring it here keeps the audit registry's permission summary
// faithful even though Scan itself only calls ListPipelines.
func (CodePipelineArtifacts) Permissions() []string {
	return []string{"codepipeline:ListPipelines", "codepipeline:GetPipelineState"}
}

// Scan walks ListPipelines pages and returns one Resource per
// pipeline, fully paginated. The SDK does not provide a paginator
// helper for ListPipelines, so we follow the NextToken by hand.
func (CodePipelineArtifacts) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.CodePipeline()
	if api == nil {
		return nil, fmt.Errorf("codepipeline/artifacts: codepipeline client is not configured")
	}
	tb := c.Throttle("codepipeline")

	var out []audit.Resource
	var token *string
	for {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := api.ListPipelines(ctx, &codepipeline.ListPipelinesInput{NextToken: token})
		if err != nil {
			return out, fmt.Errorf("codepipeline/artifacts: listing pipelines: %w", err)
		}
		for _, p := range page.Pipelines {
			res := audit.Resource{
				Kind:    "codepipeline/artifacts",
				ID:      aws.ToString(p.Name),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(p.Name),
			}
			if p.Created != nil {
				res.CreatedAt = *p.Created
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
		if aws.ToString(page.NextToken) == "" {
			return out, nil
		}
		token = page.NextToken
	}
}

func init() { audit.Register(CodePipelineArtifacts{}) }
