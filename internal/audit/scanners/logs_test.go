package scanners

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

type fakeLogs struct {
	pages []*cloudwatchlogs.DescribeLogGroupsOutput
	calls int
}

func (f *fakeLogs) DescribeLogGroups(_ context.Context, _ *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	if len(f.pages) == 0 {
		return &cloudwatchlogs.DescribeLogGroupsOutput{}, nil
	}
	f.calls++
	out := f.pages[0]
	f.pages = f.pages[1:]
	return out, nil
}

func TestLogsLogGroupScannerDecodesMillis(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_000)
	createdMillis := int64(1_700_000_000_000)
	fake := &fakeLogs{
		pages: []*cloudwatchlogs.DescribeLogGroupsOutput{
			{LogGroups: []logstypes.LogGroup{{
				Arn:          aws.String("arn:lg-1"),
				LogGroupName: aws.String("/aws/lambda/web"),
				CreationTime: &createdMillis,
			}}},
		},
	}
	c := audit.NewForTest(audit.WithLogs(fake))
	got, err := LogsLogGroup{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].Name != "/aws/lambda/web" {
		t.Errorf("got %+v, want /aws/lambda/web", got)
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v (CloudWatch returns millis since epoch)", got[0].CreatedAt, created)
	}
}
