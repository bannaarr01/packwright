package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/bannaarr01/packwright/internal/audit"
)

// ECRRepository enumerates every ECR repository in the audit Client's
// region. ADR-0040 lists DescribeImages here too because the row-open
// detail view wants per-image metadata, but enumerating every image in
// every repo is too expensive to do as part of the base inventory walk
// — this scanner records the repository row, and the row-open hook
// (ADR-0044's cache fetch) calls DescribeImages lazily.
type ECRRepository struct{}

// Kind reports the stable kind identifier.
func (ECRRepository) Kind() string { return "ecr/repository" }

// Permissions reports the IAM actions Scan touches. DescribeImages is
// declared even though Scan does not invoke it directly: the row-open
// detail hook (lazy load) needs it, and we want a single permission
// audit per kind rather than scattering the requirement across hooks.
func (ECRRepository) Permissions() []string {
	return []string{"ecr:DescribeRepositories", "ecr:DescribeImages"}
}

// Scan walks DescribeRepositories paginators and returns one Resource
// per repository, fully paginated.
func (ECRRepository) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.ECR()
	if api == nil {
		return nil, fmt.Errorf("ecr/repository: ecr client is not configured")
	}
	tb := c.Throttle("ecr")

	var out []audit.Resource
	pager := ecr.NewDescribeRepositoriesPaginator(api, &ecr.DescribeRepositoriesInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("ecr/repository: describing repositories: %w", err)
		}
		for _, r := range page.Repositories {
			res := audit.Resource{
				Kind:    "ecr/repository",
				ID:      aws.ToString(r.RepositoryArn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(r.RepositoryName),
			}
			if r.CreatedAt != nil {
				res.CreatedAt = *r.CreatedAt
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(ECRRepository{}) }
