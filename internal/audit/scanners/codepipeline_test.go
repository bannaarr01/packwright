package scanners

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	cptypes "github.com/aws/aws-sdk-go-v2/service/codepipeline/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

type fakeCodePipeline struct {
	pages  []*codepipeline.ListPipelinesOutput
	states map[string]*codepipeline.GetPipelineStateOutput
	tokens []*string
	calls  int
}

func (f *fakeCodePipeline) ListPipelines(_ context.Context, in *codepipeline.ListPipelinesInput, _ ...func(*codepipeline.Options)) (*codepipeline.ListPipelinesOutput, error) {
	f.tokens = append(f.tokens, in.NextToken)
	if len(f.pages) == 0 {
		return &codepipeline.ListPipelinesOutput{}, nil
	}
	f.calls++
	out := f.pages[0]
	f.pages = f.pages[1:]
	return out, nil
}

func (f *fakeCodePipeline) GetPipelineState(_ context.Context, in *codepipeline.GetPipelineStateInput, _ ...func(*codepipeline.Options)) (*codepipeline.GetPipelineStateOutput, error) {
	if out, ok := f.states[aws.ToString(in.Name)]; ok {
		return out, nil
	}
	return &codepipeline.GetPipelineStateOutput{}, nil
}

func TestCodePipelineScannerWalksNextToken(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeCodePipeline{
		pages: []*codepipeline.ListPipelinesOutput{
			{Pipelines: []cptypes.PipelineSummary{{Name: aws.String("deploy-web"), Created: &when}}, NextToken: aws.String("page-2")},
			{Pipelines: []cptypes.PipelineSummary{{Name: aws.String("deploy-api")}}},
		},
	}
	c := audit.NewForTest(audit.WithCodePipeline(fake), audit.WithRegion("eu-west-1"))
	got, err := CodePipelineArtifacts{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].Name != "deploy-web" || got[1].Name != "deploy-api" {
		t.Errorf("got %+v, want deploy-web + deploy-api", got)
	}
	if got[0].Region != "eu-west-1" {
		t.Errorf("region = %q, want eu-west-1", got[0].Region)
	}
	if fake.calls != 2 {
		t.Errorf("ListPipelines calls = %d, want 2 (NextToken pagination)", fake.calls)
	}
	if len(fake.tokens) < 2 || aws.ToString(fake.tokens[1]) != "page-2" {
		t.Errorf("second-call NextToken = %v, want page-2", fake.tokens)
	}
}
