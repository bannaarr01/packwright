package scanners

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/bannaarr01/packwright/internal/audit"
)

// S3Bucket enumerates every S3 bucket the caller owns. ListBuckets is
// account-global (it returns every bucket in every region), so this
// scanner is the one place the audit's per-region focus widens — the
// returned Resource.Region is the bucket's actual location resolved by
// GetBucketLocation, not the audit Client's region.
type S3Bucket struct{}

// Kind reports the stable kind identifier.
func (S3Bucket) Kind() string { return "s3/bucket" }

// Permissions reports the IAM actions Scan touches. We never list
// object contents — ADR-0040 is explicit about that.
func (S3Bucket) Permissions() []string {
	return []string{"s3:ListBuckets", "s3:GetBucketTagging", "s3:GetBucketLocation"}
}

// Scan calls ListBuckets, then per bucket resolves the bucket's region
// (GetBucketLocation) and tags (GetBucketTagging). A NoSuchTagSet error
// is the common case for un-tagged buckets and is silently treated as
// "no tags"; every other sub-call failure is surfaced as a Warn and
// the bucket row is still emitted with what we have.
func (S3Bucket) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.S3()
	if api == nil {
		return nil, fmt.Errorf("s3/bucket: s3 client is not configured")
	}
	tb := c.Throttle("s3")
	if err := tb.Wait(ctx); err != nil {
		return nil, err
	}

	page, err := api.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("s3/bucket: listing buckets: %w", err)
	}

	out := make([]audit.Resource, 0, len(page.Buckets))
	for _, b := range page.Buckets {
		name := aws.ToString(b.Name)
		tags := getBucketTags(ctx, api, tb, name, emit)
		region := getBucketRegion(ctx, api, tb, name, emit, c.Region())
		res := audit.Resource{
			Kind:    "s3/bucket",
			ID:      "arn:aws:s3:::" + name,
			Region:  region,
			Account: c.Account(),
			Name:    name,
			Tags:    tags,
		}
		if b.CreationDate != nil {
			res.CreatedAt = *b.CreationDate
		}
		out = append(out, res)
		emit.Progress(len(out))
	}
	return out, nil
}

// getBucketTags fetches the bucket's tag set. A missing tag set is the
// common AWS shape for an un-tagged bucket and is collapsed to nil;
// other errors are warned and treated as no-tags so one denied call
// does not orphan the whole row.
func getBucketTags(ctx context.Context, api audit.S3API, tb *audit.Bucket, name string, emit audit.ScannerEmitter) map[string]string {
	if err := tb.Wait(ctx); err != nil {
		return nil
	}
	out, err := api.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(name)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchTagSet" {
			return nil
		}
		emit.Warn(fmt.Sprintf("s3/bucket: %s: tag fetch failed: %v", name, err))
		return nil
	}
	return s3TagsToMap(out.TagSet)
}

// getBucketRegion fetches the bucket's region via GetBucketLocation.
// AWS returns "" for us-east-1 (legacy), so we coalesce to the canonical
// name. On error we fall back to the audit Client's region so the row
// still has a sensible value.
func getBucketRegion(ctx context.Context, api audit.S3API, tb *audit.Bucket, name string, emit audit.ScannerEmitter, fallback string) string {
	if err := tb.Wait(ctx); err != nil {
		return fallback
	}
	out, err := api.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(name)})
	if err != nil {
		emit.Warn(fmt.Sprintf("s3/bucket: %s: location fetch failed: %v", name, err))
		return fallback
	}
	if out.LocationConstraint == "" {
		return "us-east-1"
	}
	return string(out.LocationConstraint)
}

// s3TagsToMap collapses an S3 tag slice into a {key: value} map.
func s3TagsToMap(tags []s3types.Tag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		k := aws.ToString(t.Key)
		if k == "" {
			continue
		}
		out[k] = aws.ToString(t.Value)
	}
	return out
}

func init() { audit.Register(S3Bucket{}) }
