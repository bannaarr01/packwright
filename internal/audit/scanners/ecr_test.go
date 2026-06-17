package scanners

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

type fakeECR struct {
	repos  []*ecr.DescribeRepositoriesOutput
	images []*ecr.DescribeImagesOutput

	repoCalls int
}

func (f *fakeECR) DescribeRepositories(_ context.Context, _ *ecr.DescribeRepositoriesInput, _ ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error) {
	if len(f.repos) == 0 {
		return &ecr.DescribeRepositoriesOutput{}, nil
	}
	f.repoCalls++
	out := f.repos[0]
	f.repos = f.repos[1:]
	return out, nil
}

func (f *fakeECR) DescribeImages(_ context.Context, _ *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	if len(f.images) == 0 {
		return &ecr.DescribeImagesOutput{}, nil
	}
	out := f.images[0]
	f.images = f.images[1:]
	return out, nil
}

func TestECRRepositoryScannerSurfacesCreatedAt(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeECR{
		repos: []*ecr.DescribeRepositoriesOutput{
			{Repositories: []ecrtypes.Repository{
				{RepositoryArn: aws.String("arn:repo-1"), RepositoryName: aws.String("api"), CreatedAt: &when},
				{RepositoryArn: aws.String("arn:repo-2"), RepositoryName: aws.String("worker")},
			}},
		},
	}
	c := audit.NewForTest(audit.WithECR(fake))
	got, err := ECRRepository{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].Name != "api" || got[1].Name != "worker" {
		t.Errorf("got %+v, want api + worker", got)
	}
	if !got[0].CreatedAt.Equal(when) {
		t.Errorf("CreatedAt = %v, want %v", got[0].CreatedAt, when)
	}
}

// TestECRRepositoryPermissions locks down the declared permission set:
// DescribeImages is named here even though Scan does not call it, so a
// future change that drops it from Permissions() trips this test.
func TestECRRepositoryPermissions(t *testing.T) {
	perms := ECRRepository{}.Permissions()
	want := map[string]bool{"ecr:DescribeRepositories": true, "ecr:DescribeImages": true}
	if len(perms) != len(want) {
		t.Fatalf("Permissions = %v, want both repository + image declarations", perms)
	}
	for _, p := range perms {
		if !want[p] {
			t.Errorf("unexpected permission %q", p)
		}
	}
}
