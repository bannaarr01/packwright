package postprocess

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cw "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cplog "github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bannaarr01/packwright/internal/audit"
	perkind "github.com/bannaarr01/packwright/internal/audit/lastused/per_kind"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// clients bundles every narrow interface the per-kind composers need,
// adapted from the audit.Client and (where needed) the underlying
// aws.Config. Built once per Apply call and shared across goroutines —
// every adapter is safe for concurrent use because the AWS SDK clients
// are.
type clients struct {
	ac            *audit.Client
	cfg           aws.Config
	metrics       sources.MetricsClient
	logs          sources.LogsClient
	eni           sources.ENIClient
	ami           perkind.AMIClient
	ecr           perkind.ECRClient
	elbAccessLogs perkind.ELBv2AccessLogsClient
	pipeline      perkind.CodePipelineClient
	rdsExists     perkind.RDSDBExistsClient
	s3Sample      perkind.S3SampleClient
	// lookbackDays is the lastused lookback window the composers use
	// for their CloudWatch scans. Zero falls back to the composer's
	// own default.
	lookbackDays int
}

// newClients builds the adapter bundle. cfgGetter retrieves the
// underlying aws.Config; tests can pass NewForTest-built clients that
// return a zero config and the metrics/logs/eni adapters will simply
// return no data.
func newClients(c *audit.Client, lookback int) *clients {
	var cfg aws.Config
	if g, ok := any(c).(interface{ Config() aws.Config }); ok {
		cfg = g.Config()
	}
	cl := &clients{ac: c, cfg: cfg, lookbackDays: lookback}
	if hasConfig(cfg) {
		cl.metrics = newMetricsAdapter(cw.NewFromConfig(cfg))
		cl.logs = newLogsAdapter(cloudwatchlogs.NewFromConfig(cfg))
		cl.eni = newENIAdapter(ec2.NewFromConfig(cfg))
		cl.ami = newAMIAdapter(ec2.NewFromConfig(cfg))
		cl.ecr = newECRAdapter(ecr.NewFromConfig(cfg))
		cl.elbAccessLogs = newELBAccessLogsAdapter()
		cl.pipeline = newPipelineAdapter(cplog.NewFromConfig(cfg))
		cl.rdsExists = newRDSExistsAdapter(rds.NewFromConfig(cfg))
		cl.s3Sample = newS3SampleAdapter(s3.NewFromConfig(cfg))
	}
	return cl
}

// hasConfig reports whether cfg looks like a production aws.Config
// (region set) rather than the zero value test-builds use.
func hasConfig(cfg aws.Config) bool { return cfg.Region != "" }

// ============== metrics adapter (sources.MetricsClient) ==============

type metricsAdapter struct{ cw *cw.Client }

func newMetricsAdapter(c *cw.Client) sources.MetricsClient { return &metricsAdapter{cw: c} }

// LastNonZero asks CloudWatch GetMetricStatistics for datapoints in
// the window and returns the most-recent non-zero one. The "best"
// signal is the latest datapoint with a positive value; a window with
// only zero datapoints returns (nil, nil) so the caller marks the
// source as "no datapoint in window".
func (a *metricsAdapter) LastNonZero(ctx context.Context, q sources.MetricQuery) (*time.Time, error) {
	if a == nil || a.cw == nil {
		return nil, nil
	}
	stat := q.Statistic
	if stat == "" {
		stat = "Maximum"
	}
	period := q.Period
	if period <= 0 {
		period = time.Hour
	}
	end := time.Now().UTC()
	start := end.Add(-q.Lookback)
	dims := make([]cwtypes.Dimension, 0, len(q.Dimensions))
	for _, d := range q.Dimensions {
		dims = append(dims, cwtypes.Dimension{Name: aws.String(d.Name), Value: aws.String(d.Value)})
	}
	out, err := a.cw.GetMetricStatistics(ctx, &cw.GetMetricStatisticsInput{
		Namespace:  aws.String(q.Namespace),
		MetricName: aws.String(q.Metric),
		Dimensions: dims,
		StartTime:  aws.Time(start),
		EndTime:    aws.Time(end),
		Period:     aws.Int32(int32(period.Seconds())),
		Statistics: []cwtypes.Statistic{cwtypes.Statistic(stat)},
	})
	if err != nil {
		// Errors are swallowed by intent: a partial scan should not
		// fail the whole audit. The caller marks the source as
		// "no datapoint" by returning a nil time and nil error.
		return nil, nil
	}
	var best *time.Time
	for i := range out.Datapoints {
		dp := out.Datapoints[i]
		val := dpValue(dp, stat)
		if val <= 0 {
			continue
		}
		ts := dp.Timestamp
		if ts == nil {
			continue
		}
		if best == nil || ts.After(*best) {
			t := *ts
			best = &t
		}
	}
	return best, nil
}

// dpValue extracts the right statistic from a Datapoint.
func dpValue(dp cwtypes.Datapoint, stat string) float64 {
	switch strings.ToLower(stat) {
	case "sum":
		if dp.Sum != nil {
			return *dp.Sum
		}
	case "average":
		if dp.Average != nil {
			return *dp.Average
		}
	case "maximum":
		if dp.Maximum != nil {
			return *dp.Maximum
		}
	case "minimum":
		if dp.Minimum != nil {
			return *dp.Minimum
		}
	case "samplecount":
		if dp.SampleCount != nil {
			return *dp.SampleCount
		}
	}
	if dp.Maximum != nil {
		return *dp.Maximum
	}
	if dp.Sum != nil {
		return *dp.Sum
	}
	return 0
}

// ============== logs adapter (sources.LogsClient) ==============

type logsAdapter struct{ cl *cloudwatchlogs.Client }

func newLogsAdapter(c *cloudwatchlogs.Client) sources.LogsClient { return &logsAdapter{cl: c} }

func (a *logsAdapter) MostRecentEventTime(ctx context.Context, group string) (*time.Time, error) {
	if a == nil || a.cl == nil {
		return nil, nil
	}
	desc := false
	one := int32(1)
	out, err := a.cl.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(group),
		OrderBy:      "LastEventTime",
		Descending:   &desc,
		Limit:        &one,
	})
	if err != nil || out == nil {
		return nil, nil
	}
	// DescribeLogStreams with Descending=false and Limit=1 isn't
	// the ideal ordering; flip to true to get the most-recent.
	descTrue := true
	out2, err := a.cl.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(group),
		OrderBy:      "LastEventTime",
		Descending:   &descTrue,
		Limit:        &one,
	})
	if err != nil || out2 == nil || len(out2.LogStreams) == 0 {
		return nil, nil
	}
	s := out2.LogStreams[0]
	if s.LastEventTimestamp == nil {
		return nil, nil
	}
	t := time.UnixMilli(*s.LastEventTimestamp).UTC()
	return &t, nil
}

// ============== ENI adapter (sources.ENIClient) ==============

type eniAdapter struct{ ec2 *ec2.Client }

func newENIAdapter(c *ec2.Client) sources.ENIClient { return &eniAdapter{ec2: c} }

func (a *eniAdapter) LastStatusChange(ctx context.Context, ids []string) (*time.Time, error) {
	if a == nil || a.ec2 == nil || len(ids) == 0 {
		return nil, nil
	}
	out, err := a.ec2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: ids,
	})
	if err != nil {
		return nil, nil
	}
	var best *time.Time
	for i := range out.NetworkInterfaces {
		ni := out.NetworkInterfaces[i]
		if ni.Attachment == nil || ni.Attachment.AttachTime == nil {
			continue
		}
		ts := *ni.Attachment.AttachTime
		if best == nil || ts.After(*best) {
			best = &ts
		}
	}
	return best, nil
}

// ============== AMI adapter (perkind.AMIClient) ==============

type amiAdapter struct{ ec2 *ec2.Client }

func newAMIAdapter(c *ec2.Client) perkind.AMIClient { return &amiAdapter{ec2: c} }

// LatestAMIReferencing walks the account's own AMIs and returns the
// most-recent CreationDate of any AMI whose block-device-mapping
// references snapshotID. Errors and missing data return (nil, nil).
func (a *amiAdapter) LatestAMIReferencing(ctx context.Context, snapshotID string) (*time.Time, error) {
	if a == nil || a.ec2 == nil {
		return nil, nil
	}
	out, err := a.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"self"},
	})
	if err != nil || out == nil {
		return nil, nil
	}
	var best *time.Time
	for _, img := range out.Images {
		for _, bm := range img.BlockDeviceMappings {
			if bm.Ebs == nil || bm.Ebs.SnapshotId == nil {
				continue
			}
			if *bm.Ebs.SnapshotId != snapshotID {
				continue
			}
			if img.CreationDate == nil {
				continue
			}
			t, perr := time.Parse(time.RFC3339, *img.CreationDate)
			if perr != nil {
				continue
			}
			if best == nil || t.After(*best) {
				bt := t
				best = &bt
			}
		}
	}
	return best, nil
}

// ============== ECR adapter (perkind.ECRClient) ==============

type ecrAdapter struct{ e *ecr.Client }

func newECRAdapter(c *ecr.Client) perkind.ECRClient { return &ecrAdapter{e: c} }

// LatestImagePushed returns the highest ImagePushedAt across the repo's
// images. Errors and missing data return (nil, nil).
func (a *ecrAdapter) LatestImagePushed(ctx context.Context, repo string) (*time.Time, error) {
	t, _, err := a.imageTimes(ctx, repo)
	return t, err
}

// LatestImagePulled returns the highest LastRecordedPullTime across
// the repo's images. Returns nil when no pulls were recorded (e.g.
// scan-on-push is disabled).
func (a *ecrAdapter) LatestImagePulled(ctx context.Context, repo string) (*time.Time, error) {
	_, t, err := a.imageTimes(ctx, repo)
	return t, err
}

// imageTimes is the shared helper: one DescribeImages call returns
// both pushed and pulled timestamps so a single composer pass costs
// one API call rather than two.
func (a *ecrAdapter) imageTimes(ctx context.Context, repo string) (push, pull *time.Time, err error) {
	if a == nil || a.e == nil {
		return nil, nil, nil
	}
	out, derr := a.e.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repo),
	})
	if derr != nil || out == nil {
		return nil, nil, nil
	}
	for i := range out.ImageDetails {
		d := out.ImageDetails[i]
		if d.ImagePushedAt != nil {
			ts := *d.ImagePushedAt
			if push == nil || ts.After(*push) {
				push = &ts
			}
		}
		if d.LastRecordedPullTime != nil {
			ts := *d.LastRecordedPullTime
			if pull == nil || ts.After(*pull) {
				pull = &ts
			}
		}
	}
	return push, pull, nil
}

// ============== ELBv2 access-logs adapter (perkind.ELBv2AccessLogsClient) ==============

// elbAccessLogsAdapter is a no-op stub: the access-log path requires
// walking an S3 prefix per LB, and most LBs run without
// access-logging enabled. Returning nil is honest "no signal"; the
// composer falls back to CloudWatch RequestCount. A real adapter can
// be plumbed later behind a config flag.
type elbAccessLogsAdapter struct{}

func newELBAccessLogsAdapter() perkind.ELBv2AccessLogsClient { return &elbAccessLogsAdapter{} }

func (elbAccessLogsAdapter) LatestAccessLog(ctx context.Context, bucket, prefix string) (*time.Time, error) {
	return nil, nil
}

// ============== S3 sample adapter (perkind.S3SampleClient) ==============

type s3SampleAdapter struct{ s *s3.Client }

func newS3SampleAdapter(c *s3.Client) perkind.S3SampleClient { return &s3SampleAdapter{s: c} }

// SampleLatestObject lists up to sampleSize objects in bucket and
// returns the highest LastModified plus the count probed. Errors
// return (nil, 0, nil) — the composer treats that as "no signal" so a
// single bucket failure does not derail the audit.
func (a *s3SampleAdapter) SampleLatestObject(ctx context.Context, bucket string, sampleSize int) (*time.Time, int, error) {
	if a == nil || a.s == nil || sampleSize <= 0 {
		return nil, 0, nil
	}
	max := int32(sampleSize)
	out, err := a.s.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		MaxKeys: &max,
	})
	if err != nil || out == nil {
		return nil, 0, nil
	}
	var best *time.Time
	for i := range out.Contents {
		o := out.Contents[i]
		if o.LastModified == nil {
			continue
		}
		ts := *o.LastModified
		if best == nil || ts.After(*best) {
			best = &ts
		}
	}
	return best, len(out.Contents), nil
}

// ============== CodePipeline adapter (perkind.CodePipelineClient) ==============

type pipelineAdapter struct{ p *cplog.Client }

func newPipelineAdapter(c *cplog.Client) perkind.CodePipelineClient {
	return &pipelineAdapter{p: c}
}

func (a *pipelineAdapter) LatestExecutionStart(ctx context.Context, name string) (*time.Time, error) {
	if a == nil || a.p == nil {
		return nil, nil
	}
	out, err := a.p.GetPipelineState(ctx, &cplog.GetPipelineStateInput{Name: aws.String(name)})
	if err != nil || out == nil {
		return nil, nil
	}
	var best *time.Time
	for _, st := range out.StageStates {
		if st.LatestExecution == nil {
			continue
		}
		// LatestExecution doesn't carry a timestamp; use the latest
		// action's StartTime as a proxy.
		for _, as := range st.ActionStates {
			if as.LatestExecution == nil || as.LatestExecution.LastStatusChange == nil {
				continue
			}
			ts := *as.LatestExecution.LastStatusChange
			if best == nil || ts.After(*best) {
				best = &ts
			}
		}
	}
	return best, nil
}

// ============== RDS-exists adapter (perkind.RDSDBExistsClient) ==============

type rdsExistsAdapter struct{ r *rds.Client }

func newRDSExistsAdapter(c *rds.Client) perkind.RDSDBExistsClient {
	return &rdsExistsAdapter{r: c}
}

func (a *rdsExistsAdapter) DBInstanceExists(ctx context.Context, id string) (bool, error) {
	if a == nil || a.r == nil {
		return false, nil
	}
	out, err := a.r.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	})
	if err != nil {
		// DBInstanceNotFoundFault returns an error; treat as "not
		// found" rather than propagate.
		return false, nil
	}
	return out != nil && len(out.DBInstances) > 0, nil
}
