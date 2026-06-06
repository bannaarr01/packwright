package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
)

// ACMCert is the trimmed-down view of an ACM certificate the picker UI needs.
// ACM is regional, so the listing reflects the client's bound region only.
type ACMCert struct {
	ARN    string `json:"arn"`
	Domain string `json:"domain"`
	Status string `json:"status"`
}

// ListCertificates returns every ACM certificate in the client's region, fully
// paginated. Results are cached per (profile, region).
func (c *Client) ListCertificates(ctx context.Context) ([]ACMCert, error) {
	return GetOrFetch(ctx, c.cache, Key{
		Profile: c.profile, Region: c.region, Fn: "ListCertificates",
	}, func(ctx context.Context) ([]ACMCert, error) {
		out := []ACMCert{}
		var token *string
		for {
			r, err := c.acmAPI.ListCertificates(ctx, &acm.ListCertificatesInput{NextToken: token})
			if err != nil {
				return nil, fmt.Errorf("awsx: listing acm certificates: %w", err)
			}
			for _, c := range r.CertificateSummaryList {
				out = append(out, toACMCert(c))
			}
			if aws.ToString(r.NextToken) == "" {
				return out, nil
			}
			token = r.NextToken
		}
	})
}

func toACMCert(c acmtypes.CertificateSummary) ACMCert {
	return ACMCert{
		ARN:    aws.ToString(c.CertificateArn),
		Domain: aws.ToString(c.DomainName),
		Status: string(c.Status),
	}
}
