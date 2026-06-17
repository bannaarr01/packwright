package scanners

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/bannaarr01/packwright/internal/audit"
)

type fakeS3 struct {
	listOut     *s3.ListBucketsOutput
	tagOut      map[string]*s3.GetBucketTaggingOutput
	tagErr      map[string]error
	locationOut map[string]*s3.GetBucketLocationOutput
	locationErr map[string]error
}

func (f *fakeS3) ListBuckets(_ context.Context, _ *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	if f.listOut == nil {
		return &s3.ListBucketsOutput{}, nil
	}
	return f.listOut, nil
}

func (f *fakeS3) GetBucketTagging(_ context.Context, in *s3.GetBucketTaggingInput, _ ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	name := aws.ToString(in.Bucket)
	if err, ok := f.tagErr[name]; ok {
		return nil, err
	}
	if out, ok := f.tagOut[name]; ok {
		return out, nil
	}
	return &s3.GetBucketTaggingOutput{}, nil
}

func (f *fakeS3) GetBucketLocation(_ context.Context, in *s3.GetBucketLocationInput, _ ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	name := aws.ToString(in.Bucket)
	if err, ok := f.locationErr[name]; ok {
		return nil, err
	}
	if out, ok := f.locationOut[name]; ok {
		return out, nil
	}
	return &s3.GetBucketLocationOutput{}, nil
}

// noSuchTagSet mirrors the SDK shape AWS returns for an un-tagged
// bucket: a smithy.APIError carrying the well-known code.
type noSuchTagSet struct{}

func (noSuchTagSet) Error() string                 { return "NoSuchTagSet" }
func (noSuchTagSet) ErrorCode() string             { return "NoSuchTagSet" }
func (noSuchTagSet) ErrorMessage() string          { return "no tag set" }
func (noSuchTagSet) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestS3BucketScannerMapsTagsAndRegion(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeS3{
		listOut: &s3.ListBucketsOutput{Buckets: []s3types.Bucket{
			{Name: aws.String("alpha"), CreationDate: &when},
			{Name: aws.String("beta"), CreationDate: &when},
		}},
		tagOut: map[string]*s3.GetBucketTaggingOutput{
			"alpha": {TagSet: []s3types.Tag{{Key: aws.String("env"), Value: aws.String("prod")}}},
		},
		locationOut: map[string]*s3.GetBucketLocationOutput{
			"alpha": {LocationConstraint: s3types.BucketLocationConstraintEuWest1},
			// beta omitted → us-east-1 fallback via empty LocationConstraint
		},
	}
	c := audit.NewForTest(audit.WithS3(fake), audit.WithRegion("us-east-1"))
	got, err := S3Bucket{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2", len(got))
	}
	if got[0].Name != "alpha" || got[0].Tags["env"] != "prod" || got[0].Region != "eu-west-1" {
		t.Errorf("alpha = %+v, want region=eu-west-1 env=prod", got[0])
	}
	if got[1].Region != "us-east-1" {
		t.Errorf("beta region = %q, want us-east-1 (empty LocationConstraint)", got[1].Region)
	}
	if got[0].ID != "arn:aws:s3:::alpha" {
		t.Errorf("alpha ID = %q, want canonical ARN", got[0].ID)
	}
}

// TestS3BucketScannerSwallowsNoSuchTagSet asserts that the common
// untagged-bucket case does not generate a Warn event — AWS uses the
// NoSuchTagSet error code in place of an empty 200, and the scanner
// converts it to a nil tag map silently.
func TestS3BucketScannerSwallowsNoSuchTagSet(t *testing.T) {
	fake := &fakeS3{
		listOut: &s3.ListBucketsOutput{Buckets: []s3types.Bucket{{Name: aws.String("alpha")}}},
		tagErr:  map[string]error{"alpha": &smithyAPIErr{code: "NoSuchTagSet"}},
	}
	c := audit.NewForTest(audit.WithS3(fake))
	emit := &audit.RecordingEmitter{}
	got, err := S3Bucket{}.Scan(context.Background(), c, emit)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || len(got[0].Tags) != 0 {
		t.Errorf("got %+v, want one bucket with no tags", got)
	}
	if len(emit.Warns) != 0 {
		t.Errorf("Warn events = %v, want none for NoSuchTagSet", emit.Warns)
	}
}

// TestS3BucketScannerWarnsOnTagFailure asserts that an unexpected tag
// fetch error surfaces as a Warn but does not abort the scan or drop
// the bucket row — the user still wants to see the bucket.
func TestS3BucketScannerWarnsOnTagFailure(t *testing.T) {
	fake := &fakeS3{
		listOut: &s3.ListBucketsOutput{Buckets: []s3types.Bucket{{Name: aws.String("alpha")}}},
		tagErr:  map[string]error{"alpha": errors.New("access denied")},
	}
	c := audit.NewForTest(audit.WithS3(fake))
	emit := &audit.RecordingEmitter{}
	got, err := S3Bucket{}.Scan(context.Background(), c, emit)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d buckets, want 1", len(got))
	}
	if len(emit.Warns) != 1 {
		t.Errorf("Warn events = %v, want exactly one", emit.Warns)
	}
}

// smithyAPIErr is the tiny APIError stand-in s3_bucket_test.go uses to
// drive the NoSuchTagSet branch without depending on the SDK's
// concrete error types.
type smithyAPIErr struct {
	code string
}

func (e *smithyAPIErr) Error() string                 { return e.code }
func (e *smithyAPIErr) ErrorCode() string             { return e.code }
func (e *smithyAPIErr) ErrorMessage() string          { return e.code }
func (e *smithyAPIErr) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }
