package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// EC2NATGatewayInput collects the per-NAT-gateway facts the scanner
// already has from DescribeNatGateways.
type EC2NATGatewayInput struct {
	// NATGatewayID is the nat-XXXXXXXX identifier used as the
	// CloudWatch dimension.
	NATGatewayID string
	// CreateTime is the gateway's CreateTime. nil when the scanner
	// couldn't read it.
	CreateTime *time.Time
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the CW
	// BytesOutToDestination scan.
	LookbackDays int
	// Now is the reference time for "within N days" confidence checks.
	Now time.Time
}

// EC2NATGateway composes the ADR-0041 signals for an
// ec2/nat-gateway: BytesOutToDestination (CW) and CreateTime.
// Confidence is High when there is traffic in the last 7 days, Low when
// there is zero traffic for ≥ 7 days.
func EC2NATGateway(ctx context.Context, m sources.MetricsClient, in EC2NATGatewayInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		sources.Static("nat.create-time", in.CreateTime),
		sources.Metric(ctx, "cw.bytes-out", m, sources.MetricQuery{
			Namespace: "AWS/NATGateway",
			Metric:    "BytesOutToDestination",
			Dimensions: []sources.Dimension{
				{Name: "NatGatewayId", Value: in.NATGatewayID},
			},
			Statistic: "Sum",
			Lookback:  lastused.Days(in.LookbackDays),
			Period:    time.Hour,
		}),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		bytes := lastused.SourceByName(ss, "cw.bytes-out")
		if bytes != nil && bytes.HasValue() && lastused.Within(*bytes.Value, now, lastused.Days(7)) {
			return lastused.High, ""
		}
		if bytes == nil || !bytes.HasValue() {
			return lastused.Low, "Zero outbound traffic for ≥7 d — likely idle (NAT GW charges hourly)."
		}
		if best.IsZero() {
			return lastused.Unknown, ""
		}
		return lastused.Medium, "Outbound traffic seen, but not in the last 7 d."
	}

	return lastused.Compose(srcs, rule, in.Now)
}
