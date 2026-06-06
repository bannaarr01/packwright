package awsx

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
)

type fakeACM struct {
	pages []*acm.ListCertificatesOutput
	calls int
}

func (f *fakeACM) ListCertificates(_ context.Context, _ *acm.ListCertificatesInput, _ ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	if len(f.pages) == 0 {
		return nil, errNoMorePages
	}
	f.calls++
	out := f.pages[0]
	f.pages = f.pages[1:]
	return out, nil
}

func newACMClient(t *testing.T, fake *fakeACM) *Client {
	t.Helper()
	c := newTestClient(t)
	c.acmAPI = fake
	return c
}

func TestListCertificatesPaginatesAndCaches(t *testing.T) {
	fake := &fakeACM{
		pages: []*acm.ListCertificatesOutput{
			{
				CertificateSummaryList: []acmtypes.CertificateSummary{{
					CertificateArn: aws.String("arn:cert-1"),
					DomainName:     aws.String("example.com"),
					Status:         acmtypes.CertificateStatusIssued,
				}},
				NextToken: aws.String("more"),
			},
			{
				CertificateSummaryList: []acmtypes.CertificateSummary{{
					CertificateArn: aws.String("arn:cert-2"),
					DomainName:     aws.String("example.org"),
					Status:         acmtypes.CertificateStatusPendingValidation,
				}},
			},
		},
	}
	c := newACMClient(t, fake)
	ctx := context.Background()

	got, err := c.ListCertificates(ctx)
	if err != nil {
		t.Fatalf("ListCertificates: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("ListCertificates calls = %d, want 2 (paginated)", fake.calls)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ARN != "arn:cert-1" || got[0].Domain != "example.com" || got[0].Status != string(acmtypes.CertificateStatusIssued) {
		t.Fatalf("[0] = %+v", got[0])
	}
	if got[1].ARN != "arn:cert-2" || got[1].Status != string(acmtypes.CertificateStatusPendingValidation) {
		t.Fatalf("[1] = %+v", got[1])
	}

	// Second call must come from the cache.
	if _, err := c.ListCertificates(ctx); err != nil {
		t.Fatalf("second ListCertificates: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("ListCertificates calls after cache hit = %d, want 2", fake.calls)
	}
}
