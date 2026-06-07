package awsx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// fakeSTS is a hand-rolled stub for the stsAPI interface so the tests can
// drive deterministic happy paths and error shapes without touching AWS.
type fakeSTS struct {
	out *sts.GetCallerIdentityOutput
	err error
}

func (f *fakeSTS) GetCallerIdentity(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	return f.out, f.err
}

// withSTSFactory swaps the package-level stsFactory for the duration of a test
// and restores it via t.Cleanup so test ordering can never leak between cases.
func withSTSFactory(t *testing.T, f func(ctx context.Context, profile, region string) (stsAPI, error)) {
	t.Helper()
	orig := stsFactory
	stsFactory = f
	t.Cleanup(func() { stsFactory = orig })
}

func TestVerifyReturnsIdentityOnSuccess(t *testing.T) {
	withSTSFactory(t, func(_ context.Context, profile, region string) (stsAPI, error) {
		if profile != "alpha" {
			t.Errorf("factory got profile=%q, want %q", profile, "alpha")
		}
		if region != "eu-west-1" {
			t.Errorf("factory got region=%q, want %q", region, "eu-west-1")
		}
		return &fakeSTS{out: &sts.GetCallerIdentityOutput{
			Account: aws.String("123456789012"),
			Arn:     aws.String("arn:aws:iam::123456789012:user/jdoe"),
			UserId:  aws.String("AIDAEXAMPLE"),
		}}, nil
	})
	id, err := Verify(context.Background(), NewForTest("alpha", "eu-west-1"))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if id.Account != "123456789012" || id.Arn == "" || id.UserId == "" {
		t.Fatalf("Identity not populated: %+v", id)
	}
	if id.Profile != "alpha" || id.Region != "eu-west-1" {
		t.Errorf("Identity profile/region not echoed back: %+v", id)
	}
}

func TestVerifyReturnsStructuredSSOError(t *testing.T) {
	ssoErr := errors.New("operation error STS: GetCallerIdentity, api error ExpiredToken: The security token included in the request is expired")
	withSTSFactory(t, func(_ context.Context, _, _ string) (stsAPI, error) {
		return &fakeSTS{err: ssoErr}, nil
	})
	_, err := Verify(context.Background(), NewForTest("alpha", "eu-west-1"))
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("Verify err = %T, want *VerifyError", err)
	}
	if !errors.Is(err, ssoErr) {
		t.Error("VerifyError does not unwrap to the underlying SDK error")
	}
	if len(ve.Suggested) == 0 {
		t.Fatal("Suggested is empty; expected an `aws sso login` hint")
	}
	if !strings.Contains(ve.Suggested[0], "aws sso login --profile alpha") {
		t.Errorf("Suggested[0] = %q, want it to contain 'aws sso login --profile alpha'", ve.Suggested[0])
	}
}

func TestVerifyEmptyProfileSuggestsDefault(t *testing.T) {
	withSTSFactory(t, func(_ context.Context, _, _ string) (stsAPI, error) {
		return &fakeSTS{err: errors.New("no valid providers in chain")}, nil
	})
	_, err := Verify(context.Background(), NewForTest("", "us-east-1"))
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("Verify err = %T, want *VerifyError", err)
	}
	if len(ve.Suggested) == 0 || !strings.Contains(ve.Suggested[0], "--profile default") {
		t.Errorf("Suggested = %v, want it to fall back to --profile default", ve.Suggested)
	}
}

func TestVerifyNonSSOErrorOmitsSuggestion(t *testing.T) {
	withSTSFactory(t, func(_ context.Context, _, _ string) (stsAPI, error) {
		return &fakeSTS{err: errors.New("RequestTimeout: dial tcp: i/o timeout")}, nil
	})
	_, err := Verify(context.Background(), NewForTest("alpha", "eu-west-1"))
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("Verify err = %T, want *VerifyError", err)
	}
	if len(ve.Suggested) != 0 {
		t.Errorf("Suggested = %v, want empty for non-auth errors", ve.Suggested)
	}
}

func TestVerifyRejectsNilClient(t *testing.T) {
	if _, err := Verify(context.Background(), nil); err == nil {
		t.Fatal("Verify(nil) returned no error")
	}
}
