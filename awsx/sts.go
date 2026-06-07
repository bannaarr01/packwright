package awsx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Identity captures the sts:GetCallerIdentity payload Packwright surfaces in
// the header and uses to gate AWS-touching operations. Profile and Region are
// echoed from the Client the verification ran against so the caller has one
// struct to hand to the UI.
type Identity struct {
	Profile string
	Region  string
	Account string
	Arn     string
	UserId  string
}

// VerifyError is returned when STS verification fails. It carries the original
// SDK error in Cause and a short, user-facing remediation list in Suggested —
// per ADR-0019 the UI renders these as a "Re-authenticate" hint pointing at
// `aws sso login --profile <name>`.
//
// MVP-2 PR-04 introduces a richer AppError type for the error explainer; this
// type is intentionally a small local struct so PR-07 can ship before PR-04
// and be replaced wholesale once the explainer lands.
type VerifyError struct {
	Profile   string
	Region    string
	Cause     error
	Suggested []string
}

// Error formats the verification failure for log output. The user-facing
// presentation comes from Suggested and the explainer card, not this string.
func (e *VerifyError) Error() string {
	return fmt.Sprintf("awsx: verifying profile=%q region=%q: %v", e.Profile, e.Region, e.Cause)
}

// Unwrap exposes the SDK error so callers can use errors.Is/As to drill into
// the SDK's typed errors (e.g. *types.ExpiredToken from sts).
func (e *VerifyError) Unwrap() error { return e.Cause }

// stsAPI is the minimum STS surface Verify depends on. *sts.Client satisfies
// it structurally; tests inject their own implementation.
type stsAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, opts ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// stsFactory builds an STS API for the given (profile, region) pair using the
// same shared-config resolution awsx.New uses. It is a package-level var so
// tests can replace it with a fake that never touches the network.
var stsFactory = func(ctx context.Context, profile, region string) (stsAPI, error) {
	var opts []func(*config.LoadOptions) error
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return sts.NewFromConfig(cfg), nil
}

// Verify calls sts:GetCallerIdentity using the same shared-config the Client
// was built from. On success it returns the resolved Identity; on failure it
// returns a *VerifyError whose Suggested[] points at the SSO re-login command
// when the error shape looks like an expired or missing token.
//
// Verify is deliberately separate from New so the awsx Client can be built
// offline (tests, CI) and verification only runs when the UI actually needs
// to know who the user is.
func Verify(ctx context.Context, client *Client) (*Identity, error) {
	if client == nil {
		return nil, errors.New("awsx: Verify: client is nil")
	}
	api, err := stsFactory(ctx, client.profile, client.region)
	if err != nil {
		return nil, &VerifyError{
			Profile:   client.profile,
			Region:    client.region,
			Cause:     fmt.Errorf("loading AWS config: %w", err),
			Suggested: suggestedFor(client.profile, err),
		}
	}
	out, err := api.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, &VerifyError{
			Profile:   client.profile,
			Region:    client.region,
			Cause:     err,
			Suggested: suggestedFor(client.profile, err),
		}
	}
	return &Identity{
		Profile: client.profile,
		Region:  client.region,
		Account: aws.ToString(out.Account),
		Arn:     aws.ToString(out.Arn),
		UserId:  aws.ToString(out.UserId),
	}, nil
}

// suggestedFor inspects err and produces remediation commands the UI can show.
// SSO-shaped failures (expired token, no valid SSO session, generic credential
// resolution failures) get an `aws sso login --profile <name>` hint; profile
// defaults to "default" when the Client was constructed without an explicit
// profile so the suggestion is always actionable.
func suggestedFor(profile string, err error) []string {
	if err == nil {
		return nil
	}
	name := profile
	if name == "" {
		name = "default"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "sso") ||
		strings.Contains(msg, "token") ||
		strings.Contains(msg, "expired") ||
		strings.Contains(msg, "credentials") ||
		strings.Contains(msg, "no valid providers") {
		return []string{fmt.Sprintf("aws sso login --profile %s", name)}
	}
	return nil
}
