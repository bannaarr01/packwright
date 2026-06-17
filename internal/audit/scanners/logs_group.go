package scanners

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/bannaarr01/packwright/internal/audit"
)

// LogsLogGroup enumerates every CloudWatch Logs log group in the audit
// Client's region.
type LogsLogGroup struct{}

// Kind reports the stable kind identifier.
func (LogsLogGroup) Kind() string { return "logs/log-group" }

// Permissions reports the IAM actions Scan touches.
func (LogsLogGroup) Permissions() []string { return []string{"logs:DescribeLogGroups"} }

// Scan walks DescribeLogGroups paginators and returns one Resource per
// log group, fully paginated.
func (LogsLogGroup) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.Logs()
	if api == nil {
		return nil, fmt.Errorf("logs/log-group: cloudwatchlogs client is not configured")
	}
	tb := c.Throttle("logs")

	var out []audit.Resource
	pager := cloudwatchlogs.NewDescribeLogGroupsPaginator(api, &cloudwatchlogs.DescribeLogGroupsInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("logs/log-group: describing log groups: %w", err)
		}
		for _, g := range page.LogGroups {
			res := audit.Resource{
				Kind:    "logs/log-group",
				ID:      aws.ToString(g.Arn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(g.LogGroupName),
			}
			if g.CreationTime != nil {
				// CreationTime is millis since epoch.
				res.CreatedAt = time.UnixMilli(*g.CreationTime)
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(LogsLogGroup{}) }
