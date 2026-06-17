package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// S3SampleClient is the narrow S3 surface [S3Bucket] uses. ADR-0041
// explicitly forbids listing every object; the sampler returns the
// most-recent LastModified across at most sampleSize objects probed
// from common prefixes, plus the actual number probed.
type S3SampleClient interface {
	SampleLatestObject(ctx context.Context, bucket string, sampleSize int) (latest *time.Time, probed int, err error)
}

// S3BucketInput collects the per-bucket facts the scanner has from
// ListBuckets + per-bucket metadata.
type S3BucketInput struct {
	// BucketName is the bucket's name (S3 buckets are global; the
	// region was resolved by the scanner).
	BucketName string
	// SampleSize caps how many objects [S3SampleClient] is allowed to
	// probe. Zero falls back to DefaultS3SampleSize.
	SampleSize int
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the
	// BucketSizeBytes CW scan.
	LookbackDays int
	// Now is the reference time.
	Now time.Time
}

// DefaultS3SampleSize is the per-bucket object sampling cap per
// ADR-0041 ("limited to 100 objects across common prefixes").
const DefaultS3SampleSize = 100

// S3Bucket composes the ADR-0041 signals for an s3/bucket: the latest
// LastModified across a bounded object sample, plus the most-recent
// BucketSizeBytes change from CloudWatch. Confidence is capped at
// Medium because the sample is incomplete; the note records that
// limitation when the sample was small.
func S3Bucket(ctx context.Context, m sources.MetricsClient, s S3SampleClient, in S3BucketInput) lastused.LastUsedSignal {
	if in.SampleSize == 0 {
		in.SampleSize = DefaultS3SampleSize
	}
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	objSrc, probed := s3ObjectSampleSource(ctx, s, in.BucketName, in.SampleSize)
	srcs := []lastused.LastUsedSource{
		objSrc,
		sources.Metric(ctx, "cw.bucket-size", m, sources.MetricQuery{
			Namespace: "AWS/S3",
			Metric:    "BucketSizeBytes",
			Dimensions: []sources.Dimension{
				{Name: "BucketName", Value: in.BucketName},
				{Name: "StorageType", Value: "StandardStorage"},
			},
			Statistic: "Maximum",
			Lookback:  lastused.Days(in.LookbackDays),
			Period:    24 * time.Hour,
		}),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		obj := lastused.SourceByName(ss, "s3.object-sample")
		size := lastused.SourceByName(ss, "cw.bucket-size")
		anyRecent := (obj != nil && obj.HasValue() && lastused.Within(*obj.Value, now, lastused.Days(30))) ||
			(size != nil && size.HasValue() && lastused.Within(*size.Value, now, lastused.Days(30)))

		note := ""
		if probed > 0 && probed < in.SampleSize {
			note = "Object sample is incomplete — read-tier signal only."
		}

		switch {
		case best.IsZero():
			return lastused.Unknown, note
		case anyRecent:
			// Read-tier signal: never claim High confidence for S3.
			return lastused.Medium, note
		default:
			primary := "No activity within the lookback."
			if note != "" {
				return lastused.Low, primary + " " + note
			}
			return lastused.Low, primary
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}

// s3ObjectSampleSource calls the sampler and returns the resulting
// LastUsedSource plus the actual number probed (used by the confidence
// rule's "incomplete sample" note).
func s3ObjectSampleSource(ctx context.Context, s S3SampleClient, bucket string, cap int) (lastused.LastUsedSource, int) {
	src := lastused.LastUsedSource{Name: "s3.object-sample"}
	if s == nil {
		return src, 0
	}
	src.Cost = 1
	t, probed, err := s.SampleLatestObject(ctx, bucket, cap)
	if err == nil {
		src.Value = sources.CopyTimePtr(t)
	}
	return src, probed
}
