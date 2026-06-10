package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/manifest"
)

// init registers the migrate-manifests subcommand on the root command. The
// subcommand is constructed via a constructor so each test can build an
// isolated cobra tree.
func init() {
	registerSubcommand(newMigrateCmd())
}

// migrateOptions captures the flags the migrate-manifests subcommand reads.
// Kept as a struct so tests can drive runMigrate directly without going
// through cobra's flag parser.
type migrateOptions struct {
	home   string
	dryRun bool
}

// newMigrateCmd constructs the `packwright migrate-manifests` cobra command.
// The split between this and runMigrate / migratePack / migrateOne keeps the
// cobra surface thin and the pure logic test-friendly.
func newMigrateCmd() *cobra.Command {
	opts := &migrateOptions{}
	c := &cobra.Command{
		Use:   "migrate-manifests",
		Short: "Rewrite installed pack manifests to the current schema version",
		Long: `Walk every installed pack under the Packwright home and rewrite any
manifest whose schema_version predates the running build's current schema. The
previous content of each rewritten file is preserved alongside it as
<file>.bak so the migration is reversible.

v1 is the current schema. Today this command is effectively a no-op for
existing packs, but the migration shape is wired in so the day v2 lands a
single packwright migrate-manifests sweep brings every checked-out pack
forward.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMigrate(cmd.OutOrStdout(), opts)
		},
	}
	c.Flags().StringVar(&opts.home, "home", "",
		"override the Packwright home directory (defaults to the resolved user home)")
	c.Flags().BoolVar(&opts.dryRun, "dry-run", false,
		"report the migrations that would run without writing any files")
	return c
}

// migrationSummary aggregates counts across one migrate-manifests invocation
// so the final line prints a single status row instead of one-per-pack noise.
type migrationSummary struct {
	scanned, migrated, current int
}

// runMigrate is the pure entry point: takes a destination writer and
// options, walks the pack tree, and returns an error suitable for cobra to
// surface. Splitting it from the cobra command lets tests drive it without
// building a command tree.
func runMigrate(out io.Writer, opts *migrateOptions) error {
	home := opts.home
	if home == "" {
		h, err := config.Home()
		if err != nil {
			return fmt.Errorf("migrate-manifests: resolve home: %w", err)
		}
		home = h
	}

	packsRoot := filepath.Join(home, "packs")
	entries, err := os.ReadDir(packsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(out, "migrate-manifests: no packs directory at %q; nothing to do\n", packsRoot)
			return nil
		}
		return fmt.Errorf("migrate-manifests: read %q: %w", packsRoot, err)
	}

	var summary migrationSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		packDir := filepath.Join(packsRoot, e.Name())
		if err := migratePack(out, packDir, opts.dryRun, &summary); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "migrate-manifests: %d manifests scanned, %d migrated, %d already current\n",
		summary.scanned, summary.migrated, summary.current)
	return nil
}

// migratePack visits one pack's manifests/ subdirectory. A missing
// manifests/ directory is silently skipped — a templates-only pack is
// legal per ADR-0009.
func migratePack(out io.Writer, packDir string, dryRun bool, sum *migrationSummary) error {
	manifestsDir := filepath.Join(packDir, "manifests")
	entries, err := os.ReadDir(manifestsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("migrate-manifests: read %q: %w", manifestsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(manifestsDir, e.Name())
		if err := migrateOne(out, path, dryRun, sum); err != nil {
			return err
		}
	}
	return nil
}

// schemaVersionHead is the minimal struct used to peek at the schema_version
// of a manifest without committing to the full Manifest schema (which would
// reject unknown fields under KnownFields(true)).
type schemaVersionHead struct {
	SchemaVersion string `yaml:"schema_version"`
}

// migrateOne reads a single manifest file, decides which migration step (if
// any) applies, and rewrites the file in place with a .bak backup. Files
// already at the current schema major are left untouched.
func migrateOne(out io.Writer, path string, dryRun bool, sum *migrationSummary) error {
	sum.scanned++
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("migrate-manifests: read %q: %w", path, err)
	}

	var head schemaVersionHead
	if err := yaml.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("migrate-manifests: decode %q: %w", path, err)
	}

	srcMajor := 0
	if head.SchemaVersion != "" {
		m, parseErr := manifest.ParseSchemaMajor(head.SchemaVersion)
		if parseErr != nil {
			return fmt.Errorf("migrate-manifests: %q: %v", path, parseErr)
		}
		srcMajor = m
	}

	if srcMajor == manifest.CurrentSchemaMajor {
		sum.current++
		return nil
	}

	migrated, err := migrateFrom(srcMajor, data)
	if err != nil {
		return fmt.Errorf("migrate-manifests: %q: %w", path, err)
	}

	fmt.Fprintf(out, "migrate-manifests: %s v%d → v%d\n", path, srcMajor, manifest.CurrentSchemaMajor)
	sum.migrated++

	if dryRun {
		return nil
	}
	if err := os.WriteFile(path+".bak", data, 0o600); err != nil {
		return fmt.Errorf("migrate-manifests: write backup %q: %w", path+".bak", err)
	}
	if err := os.WriteFile(path, migrated, 0o600); err != nil {
		return fmt.Errorf("migrate-manifests: write %q: %w", path, err)
	}
	return nil
}

// migrateFrom transforms manifest bytes from srcMajor up to the current
// schema major. The chain is deliberately small today (only v0→v1) and is
// extended in lockstep with the manifest package when a future v2 lands.
func migrateFrom(srcMajor int, data []byte) ([]byte, error) {
	switch srcMajor {
	case 0:
		return migrateV0ToV1(data)
	case manifest.CurrentSchemaMajor:
		return data, nil
	default:
		return nil, fmt.Errorf("no migration path from schema v%d to v%d",
			srcMajor, manifest.CurrentSchemaMajor)
	}
}

// schemaVersionLineRe matches an existing schema_version line so a v0
// manifest that happens to carry "schema_version: packwright.manifest.v0"
// can be rewritten rather than duplicated.
var schemaVersionLineRe = regexp.MustCompile(`(?m)^schema_version\s*:.*$`)

// migrateV0ToV1 is the v0→v1 stub. v0 manifests were the pre-shipped era
// (no formal schema_version) so in practice this function sees one of two
// inputs: a file with no schema_version, or a file that incorrectly carries
// "packwright.manifest.v0". Both end up at "packwright.manifest.v1".
//
// The transform is line-level rather than YAML-roundtrip so comments,
// formatting, and ordering survive untouched — the rewritten file should
// still match the author's intent byte-for-byte except for the bumped
// schema_version line.
func migrateV0ToV1(data []byte) ([]byte, error) {
	target := "schema_version: " + manifest.FormatSchemaVersion(1)
	if schemaVersionLineRe.Match(data) {
		return schemaVersionLineRe.ReplaceAll(data, []byte(target)), nil
	}
	// Preserve a leading YAML document marker by inserting after it.
	if len(data) >= 4 && string(data[:4]) == "---\n" {
		out := append([]byte("---\n"+target+"\n"), data[4:]...)
		return out, nil
	}
	return append([]byte(target+"\n"), data...), nil
}
