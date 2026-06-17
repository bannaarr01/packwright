package perkind

import (
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// EC2EIPInput collects the per-EIP facts the scanner already has from
// DescribeAddresses.
type EC2EIPInput struct {
	// AllocationID is the eipalloc-XXXXXXXX identifier.
	AllocationID string
	// AssociationID is the eipassoc-XXXXXXXX identifier when the EIP is
	// associated with a resource; empty when the EIP is dangling.
	AssociationID string
	// AllocationTime is when the EIP was allocated, if the scanner
	// could derive it (e.g. from a tag or CloudTrail). nil otherwise;
	// AWS does not expose an allocation timestamp on the EIP itself.
	AllocationTime *time.Time
	// Now is the reference time. It is also used as the "last used"
	// timestamp when the EIP is associated.
	Now time.Time
}

// EC2EIP composes the ADR-0041 signals for an ec2/eip. Unlike the other
// composers it makes no AWS call: associated EIPs are treated as
// actively used (Best = Now, Confidence High); unassociated EIPs are
// flagged as "definitely waste" regardless of allocation time, because
// they cost regardless of use.
func EC2EIP(in EC2EIPInput) lastused.LastUsedSignal {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		sources.Static("eip.allocation-time", in.AllocationTime),
	}
	if in.AssociationID != "" {
		now := in.Now
		srcs = append(srcs, lastused.LastUsedSource{
			Name:  "eip.associated",
			Value: &now,
		})
	}

	rule := func(_ []lastused.LastUsedSource, _ time.Time, _ time.Time) (lastused.Confidence, string) {
		if in.AssociationID != "" {
			return lastused.High, ""
		}
		return lastused.Low, "Unassociated EIP — definitely waste (billed regardless of use)."
	}

	return lastused.Compose(srcs, rule, in.Now)
}
