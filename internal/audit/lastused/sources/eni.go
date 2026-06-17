package sources

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// ENIClient is the narrow EC2 surface [ENILastStatusChange] uses.
//
// LastStatusChange returns the most-recent Attachment.AttachTime (or
// equivalent status-change timestamp) across the given ENIs. A nil
// return with nil error means none of the ENIs exposed an attachment
// time.
type ENIClient interface {
	LastStatusChange(ctx context.Context, eniIDs []string) (*time.Time, error)
}

// ENILastStatusChange returns a source named n built from the most-
// recent ENI status change across eniIDs. With no ENI IDs the source
// returns a nil Value and zero cost — no AWS call is made.
//
// Errors from the client are swallowed and result in a nil Value
// (same outcome as "no attachment time"), per the partial-scan rule
// of ADR-0041.
//
// Cost is 1 (one DescribeNetworkInterfaces call) when at least one ENI
// is inspected.
func ENILastStatusChange(ctx context.Context, n string, c ENIClient, eniIDs []string) lastused.LastUsedSource {
	src := lastused.LastUsedSource{Name: n}
	if len(eniIDs) == 0 || c == nil {
		return src
	}
	src.Cost = 1
	if t, err := c.LastStatusChange(ctx, eniIDs); err == nil {
		src.Value = CopyTimePtr(t)
	}
	return src
}
