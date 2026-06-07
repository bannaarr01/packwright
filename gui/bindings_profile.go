package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bannaarr01/packwright/awsx"
)

// PR-07 spreads the GUI App's exported methods across two files (bindings.go
// from PR-09 + this one). Go allows methods on the same struct in different
// files of the same package, so we can add ListProfiles / SwitchProfile /
// VerifyCurrent without touching the PR-09-owned bindings.go.

// ProfileEntry is one row in the GUI profile switcher. Active flags the
// profile currently selected so the frontend can render a "→" marker
// matching the TUI.
type ProfileEntry struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Active bool   `json:"active"`
}

// IdentityPayload mirrors awsx.Identity for the frontend. It is JSON-tagged so
// the Svelte side sees lowercase keys; the Go-side Wails marshaller honours
// these tags.
type IdentityPayload struct {
	Profile string `json:"profile"`
	Region  string `json:"region"`
	Account string `json:"account"`
	Arn     string `json:"arn"`
	UserId  string `json:"userId"`
}

// SwitchResult is the SwitchProfile / VerifyCurrent return shape. On success
// Identity is set; on failure Error + Suggested populate the frontend's
// "Re-authenticate" card per ADR-0019.
//
// We return errors as a struct field rather than a Go error so the Wails
// runtime delivers a normal RPC resolution and the frontend can branch on
// `result.ok` instead of try/catching every call. Suggested[] is a strict
// echo of awsx.VerifyError.Suggested.
type SwitchResult struct {
	Ok        bool             `json:"ok"`
	Identity  *IdentityPayload `json:"identity,omitempty"`
	Error     string           `json:"error,omitempty"`
	Suggested []string         `json:"suggested,omitempty"`
}

// ProfileSwitchedEvent is the Wails event SwitchProfile emits on success so
// header / status components can refresh without polling. The payload is the
// same IdentityPayload returned synchronously to the caller.
const ProfileSwitchedEvent = "packwright:profile-switched"

// profileSwitcherDeps lets tests override the slow / filesystem-touching parts
// of SwitchProfile and ListProfiles without injecting a wider abstraction
// through the App struct (which we cannot extend from this file).
var profileSwitcherDeps = struct {
	listProfiles func() ([]awsx.Profile, error)
	newClient    func(ctx context.Context, profile, region, cacheHome string) (*awsx.Client, error)
	verify       func(ctx context.Context, c *awsx.Client) (*awsx.Identity, error)
	cacheHome    func() (string, error)
	verifyTO     time.Duration
}{
	listProfiles: awsx.ListProfiles,
	newClient: func(ctx context.Context, profile, region, cacheHome string) (*awsx.Client, error) {
		return awsx.New(ctx, profile, region, cacheHome, nil)
	},
	verify:    awsx.Verify,
	cacheHome: defaultCacheHome,
	verifyTO:  5 * time.Second,
}

// ListProfiles enumerates AWS profiles discovered from ~/.aws/config and
// ~/.aws/credentials. No AWS API calls. Active flags the profile currently
// resolved by $AWS_PROFILE (or "default" when unset) so the frontend can
// highlight it on first render.
func (a *App) ListProfiles() ([]ProfileEntry, error) {
	profiles, err := profileSwitcherDeps.listProfiles()
	if err != nil {
		return nil, fmt.Errorf("gui: listing profiles: %w", err)
	}
	active := currentProfileName()
	out := make([]ProfileEntry, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, ProfileEntry{Name: p.Name, Region: p.Region, Active: p.Name == active})
	}
	return out, nil
}

// SwitchProfile re-initialises the awsx Client for the given profile/region
// pair and verifies it via STS. On success it emits a ProfileSwitchedEvent so
// any header subscriber can refresh, and returns Identity in the result. On
// failure it returns the structured error + Suggested[] from awsx.VerifyError
// so the frontend can render a "Re-authenticate" card.
//
// SwitchProfile does not persist the choice to config.yaml — that lives in a
// later PR. The Active flag therefore reflects $AWS_PROFILE, not the last
// switcher pick, until persistence lands.
func (a *App) SwitchProfile(profile, region string) SwitchResult {
	return a.runSwitch(profile, region)
}

// VerifyCurrent runs STS against the current default credentials so the
// frontend can populate Identity on startup without forcing a profile change.
// The empty profile argument falls through to the SDK's default chain.
func (a *App) VerifyCurrent() SwitchResult {
	return a.runSwitch(currentProfileName(), "")
}

// runSwitch is the shared inner of SwitchProfile / VerifyCurrent. Extracted so
// the two RPC methods share the timeout and event-emission logic.
func (a *App) runSwitch(profile, region string) SwitchResult {
	ctx := a.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}
	cacheHome, err := profileSwitcherDeps.cacheHome()
	if err != nil {
		return SwitchResult{Error: fmt.Sprintf("resolving cache home: %v", err)}
	}
	client, err := profileSwitcherDeps.newClient(ctx, profile, region, cacheHome)
	if err != nil {
		return SwitchResult{Error: err.Error()}
	}

	vctx, cancel := context.WithTimeout(ctx, profileSwitcherDeps.verifyTO)
	defer cancel()

	id, err := profileSwitcherDeps.verify(vctx, client)
	if err != nil {
		var ve *awsx.VerifyError
		if errors.As(err, &ve) {
			return SwitchResult{Error: ve.Error(), Suggested: ve.Suggested}
		}
		return SwitchResult{Error: err.Error()}
	}

	payload := &IdentityPayload{
		Profile: id.Profile,
		Region:  id.Region,
		Account: id.Account,
		Arn:     id.Arn,
		UserId:  id.UserId,
	}
	if a.wailsCtx != nil {
		runtime.EventsEmit(a.wailsCtx, ProfileSwitchedEvent, payload)
	}
	if a.logger != nil {
		a.logger.Info("gui profile switched",
			"profile", payload.Profile,
			"account", payload.Account,
			"arn", payload.Arn)
	}
	return SwitchResult{Ok: true, Identity: payload}
}

// currentProfileName mirrors bindings.go's Profile() resolution (without
// importing it across the file boundary): $AWS_PROFILE wins, else "default".
// Kept in sync manually — there is no shared helper to import in this package.
func currentProfileName() string {
	if v := os.Getenv("AWS_PROFILE"); v != "" {
		return v
	}
	return "default"
}

// defaultCacheHome resolves a per-user cache root for the awsx disk cache.
// Honours XDG_CACHE_HOME when set, then os.UserCacheDir, then os.TempDir as a
// last resort so SwitchProfile never fails purely on cache-home discovery.
func defaultCacheHome() (string, error) {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "packwright", "awsx"), nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "packwright", "awsx"), nil
	}
	return filepath.Join(dir, "packwright", "awsx"), nil
}
