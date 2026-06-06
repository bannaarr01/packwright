package awsx

import "log/slog"

// NewForTest returns a Client populated only with the given profile and region.
// No AWS shared-config is loaded and the EC2 / ELBv2 / ACM service clients are
// left nil — calling any picker method (ListVPCs, ListSubnets, ...) on the
// returned Client will panic.
//
// It exists so packages that depend on *Client only for Profile() / Region()
// — notably the resource engine, which threads those into env-template data —
// can construct one in unit tests without a live AWS profile on disk.
func NewForTest(profile, region string) *Client {
	return &Client{profile: profile, region: region, log: slog.Default()}
}
