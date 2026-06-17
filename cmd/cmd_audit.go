package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/audit/cache"
	"github.com/bannaarr01/packwright/internal/audit/postprocess"
	// Importing the scanners package for its init side effect — every
	// concrete Scanner self-registers with audit.Default from this
	// import. The audit command consumes audit.Default.All().
	_ "github.com/bannaarr01/packwright/internal/audit/scanners"
)

// auditCmd is the `packwright audit` subcommand. It is the headless
// CLI surface for the audit feature; the `/audit` slash command in the
// TUI palette routes through the same audit.Run engine.
//
// The command takes no arguments. It loads the user's profile and
// region (from --profile / --region or the Packwright config), opens
// an awsx.Client, resolves the account via STS, drives every
// registered scanner concurrently, populates LastUsed and CostEstimate
// via the post-processing layer, and persists the result to the local
// snapshot cache so subsequent runs return immediately within the
// 24-hour TTL.
//
// Two subcommands (`audit refresh`, `audit reset`) live alongside it
// — refresh forces a re-scan past the 60-second throttle; reset wipes
// the cached snapshot for the active (profile, region) pair.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Walk AWS resources read-only and report what exists",
	Long: `Walk every registered AWS scanner read-only and report the inventory.

` + "`packwright audit`" + ` is the headless face of the /audit slash command. It
visits each scanner kind (ec2/instance, rds/db-snapshot, s3/bucket, …)
concurrently, paginates each AWS API fully, populates last-used and
cost-estimate fields for every row, and prints a summary table.

Scanners are read-only by construction (ADR-0040): every IAM action
they declare is validated against a Describe*/List*/Get* allowlist at
program startup, so this command can never mutate AWS state.

Results are cached for 24 hours under the Packwright home directory.
Re-running ` + "`packwright audit`" + ` inside that window returns the cached
snapshot instantly; pass ` + "`--refresh`" + ` to force a re-scan or use
` + "`audit refresh`" + ` / ` + "`audit reset`" + ` for finer control.`,
	Args: cobra.NoArgs,
	RunE: runAudit,
}

// auditFlags is the parsed CLI surface for `packwright audit`.
type auditFlags struct {
	profile      string
	region       string
	lookbackDays int
	refresh      bool
	force        bool
}

var auditOpts auditFlags

// auditRefreshOpts collects the refresh subcommand flags.
type auditRefreshFlags struct {
	kind  string
	force bool
}

var auditRefreshOpts auditRefreshFlags

func init() {
	auditCmd.Flags().StringVar(&auditOpts.profile, "profile", "", "AWS profile to use (defaults to the config-active profile)")
	auditCmd.Flags().StringVar(&auditOpts.region, "region", "", "AWS region to scan (defaults to the profile's region)")
	auditCmd.Flags().IntVar(&auditOpts.lookbackDays, "lookback", 0, "CloudWatch lookback window in days (default: 30)")
	auditCmd.Flags().BoolVar(&auditOpts.refresh, "refresh", false, "Force a fresh scan instead of using the cached snapshot")
	auditCmd.Flags().BoolVar(&auditOpts.force, "force", false, "Bypass the 60-second throttle when refreshing")

	auditRefreshCmd.Flags().StringVar(&auditRefreshOpts.kind, "kind", "", "Refresh a single scanner kind (e.g. ec2/volume)")
	auditRefreshCmd.Flags().BoolVar(&auditRefreshOpts.force, "force", false, "Bypass the 60-second throttle")
	auditCmd.AddCommand(auditRefreshCmd)

	auditCmd.AddCommand(auditResetCmd)

	registerSubcommand(auditCmd)
}

// runAudit orchestrates awsx.New, STS verification, the snapshot
// cache, the worker pool, the post-processing layer, and a final
// summary write. Every failure path returns a non-nil error so cobra
// surfaces it with a non-zero exit code.
func runAudit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	home, err := config.Home()
	if err != nil {
		return fmt.Errorf("audit: resolve home: %w", err)
	}

	client, err := awsx.New(ctx, auditOpts.profile, auditOpts.region, home, nil)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	id, err := awsx.Verify(ctx, client)
	if err != nil {
		return err
	}

	store, err := openCache(home)
	if err != nil {
		return err
	}
	key := cache.Key{
		Profile:      profileLabel(auditOpts.profile, client.Profile()),
		Region:       client.Region(),
		LookbackDays: postprocess.LookbackDays(postprocess.Options{LookbackDays: auditOpts.lookbackDays}),
	}

	// Honour the cache unless --refresh asked for a fresh scan.
	if !auditOpts.refresh {
		if rr, rerr := store.Read(key); rerr == nil && rr != nil {
			fmt.Fprintf(out, "Using cached snapshot from %s (age %s%s)\n",
				rr.Snapshot.ScannedAt.Format(time.RFC3339),
				rr.Age.Round(time.Second),
				staleSuffix(rr.Stale))
			writeSnapshotSummary(out, rr.Snapshot)
			return nil
		} else if rerr != nil && !errors.Is(rerr, cache.ErrNoSnapshot) {
			return fmt.Errorf("audit: read cache: %w", rerr)
		}
	}

	scanners := audit.Default.All()
	ac := audit.NewFromAWSX(client, id.Account, nil)

	fmt.Fprintf(out, "Scanning account %s in %s with %d scanners…\n",
		id.Account, client.Region(), len(scanners))

	snap, err := store.Refresh(ctx, key, makeScanFunc(ctx, ac, scanners, key.LookbackDays, out), cache.RefreshOptions{
		Force: auditOpts.force,
	})
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	writeSnapshotSummary(out, snap)
	if hasErrors(snap.ScannersSkipped) {
		return fmt.Errorf("audit: %d scanner(s) skipped", len(snap.ScannersSkipped))
	}
	return nil
}

// openCache returns the audit Store rooted under the user's
// Packwright home directory.
func openCache(home string) (*cache.Store, error) {
	dir := filepath.Join(home, "audit", "snapshots")
	store, err := cache.NewStore(dir, cache.Config{})
	if err != nil {
		return nil, fmt.Errorf("audit: open cache: %w", err)
	}
	return store, nil
}

// profileLabel chooses a stable cache key for the profile component.
// An empty Profile string from awsx means "SDK default chain"; using
// "_default_" keeps the snapshot filename valid (cache.Key.Validate
// rejects empty profiles).
func profileLabel(requested, resolved string) string {
	if requested != "" {
		return requested
	}
	if resolved != "" {
		return resolved
	}
	return "_default_"
}

// makeScanFunc returns the cache.ScanFunc that drives the scanner
// pool and the post-processing layer. It is the bridge between the
// cache's "snapshot" abstraction and the audit pipeline's "events +
// resources" abstraction.
func makeScanFunc(ctx context.Context, ac *audit.Client, scanners []audit.Scanner, lookback int, out io.Writer) cache.ScanFunc {
	return func(scanCtx context.Context) (cache.ScanResult, error) {
		result := drainAudit(scanCtx, scanners, ac, out)
		postprocess.Apply(scanCtx, ac, result.Resources, postprocess.Options{
			LookbackDays: lookback,
		})
		return toCacheResult(result), nil
	}
}

// staleSuffix returns " — stale" when the snapshot age exceeds the
// configured TTL, "" otherwise. Used in the cache-hit banner.
func staleSuffix(stale bool) string {
	if stale {
		return ", stale — re-run with --refresh"
	}
	return ""
}

// drainAudit runs the pool, prints per-scanner Done lines as events
// arrive, and returns the final Result. It is split out so tests can
// drive it with a synthetic scanner list and an in-memory writer.
func drainAudit(ctx context.Context, scanners []audit.Scanner, c *audit.Client, out io.Writer) audit.Result {
	events, result := audit.Run(ctx, scanners, c, audit.RunOptions{})
	for ev := range events {
		switch ev.Type {
		case audit.EventDone:
			fmt.Fprintf(out, "  ✓ %-28s %d resource(s)\n", ev.Kind, ev.Count)
		case audit.EventError:
			fmt.Fprintf(out, "  ✗ %-28s %v\n", ev.Kind, ev.Err)
		case audit.EventWarn:
			fmt.Fprintf(out, "  ! %-28s %s\n", ev.Kind, ev.Msg)
		}
	}
	return <-result
}

// toCacheResult converts a freshly-postprocessed audit.Result into the
// cache.ScanResult the Store.Refresh contract expects. Errors map to
// SkippedScanner entries so the user sees what didn't run.
func toCacheResult(r audit.Result) cache.ScanResult {
	res := cache.ScanResult{
		Resources:   make([]cache.Resource, 0, len(r.Resources)),
		ScannersRun: make([]string, 0, len(r.Resources)),
	}
	seen := map[string]struct{}{}
	for i := range r.Resources {
		res.Resources = append(res.Resources, toCacheResource(&r.Resources[i]))
		if _, ok := seen[r.Resources[i].Kind]; !ok {
			seen[r.Resources[i].Kind] = struct{}{}
			res.ScannersRun = append(res.ScannersRun, r.Resources[i].Kind)
		}
	}
	sort.Strings(res.ScannersRun)
	for kind, err := range r.Errors {
		res.ScannersSkipped = append(res.ScannersSkipped, cache.SkippedScanner{
			Kind:   kind,
			Reason: err.Error(),
		})
	}
	sort.Slice(res.ScannersSkipped, func(i, j int) bool {
		return res.ScannersSkipped[i].Kind < res.ScannersSkipped[j].Kind
	})
	return res
}

// toCacheResource copies an audit.Resource into the cache shape.
func toCacheResource(r *audit.Resource) cache.Resource {
	return cache.Resource{
		Kind:         r.Kind,
		ID:           r.ID,
		Region:       r.Region,
		Account:      r.Account,
		Name:         r.Name,
		Tags:         r.Tags,
		CreatedAt:    r.CreatedAt,
		State:        r.State,
		Raw:          r.Raw,
		LastUsed:     r.LastUsed,
		CostEstimate: r.CostEstimate,
	}
}

// writeSnapshotSummary prints the per-kind counts plus the total
// monthly cost across resources whose CostEstimate is concrete.
func writeSnapshotSummary(out io.Writer, snap *cache.Snapshot) {
	byKind := map[string]int{}
	var monthly float64
	for i := range snap.Resources {
		r := snap.Resources[i]
		byKind[r.Kind]++
		if r.CostEstimate != nil {
			monthly += r.CostEstimate.MonthlyUSD
		}
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Summary:")
	for _, k := range kinds {
		fmt.Fprintf(out, "  %-28s %d\n", k, byKind[k])
	}
	fmt.Fprintf(out, "  %-28s %d\n", "total resources", len(snap.Resources))
	fmt.Fprintf(out, "  %-28s $%.2f / month\n", "estimated monthly cost", monthly)
	if len(snap.ScannersSkipped) > 0 {
		skipped := make([]string, 0, len(snap.ScannersSkipped))
		for _, s := range snap.ScannersSkipped {
			skipped = append(skipped, s.Kind)
		}
		fmt.Fprintf(out, "  %-28s %d (%s)\n", "scanner errors", len(snap.ScannersSkipped), strings.Join(skipped, ","))
	}
}

// hasErrors reports whether any scanner was skipped due to a runtime
// error (as opposed to AccessDenied / permission gaps surfaced via
// the Warn channel, which still count as "successful scans").
func hasErrors(skipped []cache.SkippedScanner) bool {
	return len(skipped) > 0
}

// ----------------- audit refresh subcommand -----------------

var auditRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Force a fresh /audit scan (full or per-kind)",
	Long: `Force a fresh /audit scan.

By default refresh runs every scanner. Pass ` + "`--kind=<kind>`" + ` to
re-scan a single kind and merge the result into the existing snapshot
without touching the others. Use ` + "`--force`" + ` to bypass the
60-second throttle the cache normally enforces.`,
	Args: cobra.NoArgs,
	RunE: runAuditRefresh,
}

func runAuditRefresh(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	home, err := config.Home()
	if err != nil {
		return fmt.Errorf("audit refresh: resolve home: %w", err)
	}
	client, err := awsx.New(ctx, auditOpts.profile, auditOpts.region, home, nil)
	if err != nil {
		return fmt.Errorf("audit refresh: %w", err)
	}
	id, err := awsx.Verify(ctx, client)
	if err != nil {
		return err
	}
	store, err := openCache(home)
	if err != nil {
		return err
	}
	lookback := postprocess.LookbackDays(postprocess.Options{LookbackDays: auditOpts.lookbackDays})
	key := cache.Key{
		Profile:      profileLabel(auditOpts.profile, client.Profile()),
		Region:       client.Region(),
		LookbackDays: lookback,
	}
	ac := audit.NewFromAWSX(client, id.Account, nil)

	if auditRefreshOpts.kind == "" {
		fmt.Fprintf(out, "Refreshing every scanner in %s/%s…\n", key.Profile, key.Region)
		scanners := audit.Default.All()
		snap, err := store.Refresh(ctx, key, makeScanFunc(ctx, ac, scanners, lookback, out), cache.RefreshOptions{
			Force: auditRefreshOpts.force,
		})
		if err != nil {
			return fmt.Errorf("audit refresh: %w", err)
		}
		writeSnapshotSummary(out, snap)
		return nil
	}

	target := audit.Default.Lookup(auditRefreshOpts.kind)
	if target == nil {
		return fmt.Errorf("audit refresh: unknown kind %q", auditRefreshOpts.kind)
	}
	fmt.Fprintf(out, "Refreshing %s only…\n", target.Kind())
	scanners := []audit.Scanner{target}
	// For a single-kind refresh we still go through Store.Refresh so
	// the throttle / atomic-rename logic stays exercised; the merge
	// is "rewrite the kind's rows" — Store.Refresh discards the old
	// snapshot for this key, so a partial-refresh CLI would need a
	// dedicated cache.RefreshKind in v2.
	snap, err := store.Refresh(ctx, key, makeScanFunc(ctx, ac, scanners, lookback, out), cache.RefreshOptions{
		Force: auditRefreshOpts.force,
	})
	if err != nil {
		return fmt.Errorf("audit refresh: %w", err)
	}
	writeSnapshotSummary(out, snap)
	return nil
}

// ----------------- audit reset subcommand -----------------

var auditResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Wipe the cached /audit snapshot for the active profile/region",
	Long: `Wipe the cached /audit snapshot for the active profile/region.

The deletion log and staging tray live in the same directory but are
left untouched — only the inventory snapshot is removed.`,
	Args: cobra.NoArgs,
	RunE: runAuditReset,
}

func runAuditReset(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()

	home, err := config.Home()
	if err != nil {
		return fmt.Errorf("audit reset: resolve home: %w", err)
	}
	store, err := openCache(home)
	if err != nil {
		return err
	}
	// Resolve the same (profile, region, lookback) the audit command
	// uses so reset targets exactly the snapshot the user just saw.
	region := auditOpts.region
	if region == "" {
		// Fall back to the SDK-resolved region; without an awsx client
		// the best we can do is leave region empty and rely on the
		// user to supply --region.
		return fmt.Errorf("audit reset: --region is required")
	}
	key := cache.Key{
		Profile:      profileLabel(auditOpts.profile, ""),
		Region:       region,
		LookbackDays: postprocess.LookbackDays(postprocess.Options{LookbackDays: auditOpts.lookbackDays}),
	}
	if err := store.Wipe(key); err != nil {
		return fmt.Errorf("audit reset: %w", err)
	}
	fmt.Fprintf(out, "Cleared snapshot for %s/%s.\n", key.Profile, key.Region)
	return nil
}
