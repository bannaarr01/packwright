package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// ECRClient is the narrow ECR surface [ECRRepository] uses. Each call
// returns nil with nil error when the repository has no images of that
// kind in window.
type ECRClient interface {
	// LatestImagePushed returns the highest imagePushedAt across the
	// repository's images.
	LatestImagePushed(ctx context.Context, repoName string) (*time.Time, error)
	// LatestImagePulled returns the highest imageLastPullTime across
	// the repository's images. Only meaningful when scan-on-push (or
	// equivalent pull-time tracking) is enabled; otherwise nil.
	LatestImagePulled(ctx context.Context, repoName string) (*time.Time, error)
}

// ECRRepositoryInput collects the per-repo facts the scanner has from
// DescribeRepositories.
type ECRRepositoryInput struct {
	// RepositoryName is the ECR repo name, used to call DescribeImages.
	RepositoryName string
	// Now is the reference time.
	Now time.Time
}

// ECRRepository composes the ADR-0041 signals for an ecr/repository:
// the most-recent imagePushedAt and the most-recent imageLastPullTime.
// Confidence is Low (flagged) when no pulls have happened in 90 d.
func ECRRepository(ctx context.Context, c ECRClient, in ECRRepositoryInput) lastused.LastUsedSignal {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		ecrTimestamp(ctx, "ecr.image-pushed", in.RepositoryName, c, true),
		ecrTimestamp(ctx, "ecr.image-pulled", in.RepositoryName, c, false),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		pulled := lastused.SourceByName(ss, "ecr.image-pulled")
		pushed := lastused.SourceByName(ss, "ecr.image-pushed")
		pulledRecent := pulled != nil && pulled.HasValue() && lastused.Within(*pulled.Value, now, lastused.Days(90))

		switch {
		case pulledRecent:
			return lastused.High, ""
		case pulled == nil || !pulled.HasValue():
			if pushed == nil || !pushed.HasValue() {
				return lastused.Unknown, ""
			}
			return lastused.Low, "Pull times unavailable or no pulls in ≥90 d — likely unused image."
		case !lastused.Within(*pulled.Value, now, lastused.Days(90)):
			return lastused.Low, "No image pulls in ≥90 d — likely unused image."
		default:
			return lastused.Medium, ""
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}

// ecrTimestamp queries the ECR client for either the latest pushed
// (pushed=true) or pulled (pushed=false) timestamp.
//
// Cost is 1 per call.
func ecrTimestamp(ctx context.Context, name, repo string, c ECRClient, pushed bool) lastused.LastUsedSource {
	src := lastused.LastUsedSource{Name: name}
	if c == nil {
		return src
	}
	src.Cost = 1
	var (
		t   *time.Time
		err error
	)
	if pushed {
		t, err = c.LatestImagePushed(ctx, repo)
	} else {
		t, err = c.LatestImagePulled(ctx, repo)
	}
	if err == nil {
		src.Value = sources.CopyTimePtr(t)
	}
	return src
}
