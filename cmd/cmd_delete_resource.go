package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/internal/delete"
)

// deleteResourceCmd is the `packwright delete-resource` slash command
// surface for MVP-7 PR-08 (ADR-0053). The same code path is the
// function call the GUI sidebar "Delete" entry point uses (see
// HandleDelete in internal/delete) — the CLI is the headless route.
//
// The command requires a stack record on disk and a logical id. With
// only those two inputs it runs delete.Resolve and reports the mode
// the resolver picked, so the caller can pipe the result into a
// confirmation prompt (UI) or into another invocation that picks the
// mode explicitly (CLI scripting).
var deleteResourceCmd = &cobra.Command{
	Use:   "delete-resource",
	Short: "Delete a resource from a CloudFormation stack via shrink, stack-delete, or adopt-and-delete",
	Long: `Cascading delete for a single CFN-managed resource (ADR-0053).

Three deletion modes are dispatched from this command:

  * template-shrink   — remove the resource block from the local CFN
                        template and re-deploy. Used when the stack
                        still has other resources after the removal.

  * stack-delete      — DeleteStack the whole stack. Used when the
                        target is the last surviving resource and the
                        user confirms via the "last resource" prompt.

  * adopt-and-delete  — mark the resource Retain in the template,
                        re-deploy to dissociate, then hand the orphan
                        to the MVP-6 batch-consent deletion modal.

Without --mode, the command runs ` + "`delete.Resolve`" + ` and reports the
mode it would pick. Pass --mode explicitly to dispatch the
corresponding flow. --force overrides the dangling-reference refusal
for template-shrink.`,
	Args: cobra.NoArgs,
	RunE: runDeleteResource,
}

// deleteResourceFlags is the parsed flag surface for delete-resource.
type deleteResourceFlags struct {
	stackRecordPath string
	logicalID       string
	mode            string
	force           bool
	dryRun          bool
}

// deleteResourceOpts holds the flag values for the current invocation.
var deleteResourceOpts deleteResourceFlags

func init() {
	deleteResourceCmd.Flags().StringVar(&deleteResourceOpts.stackRecordPath, "stack-record", "", "Path to the stack record JSON file produced by PR-02 (required)")
	deleteResourceCmd.Flags().StringVar(&deleteResourceOpts.logicalID, "logical-id", "", "CFN logical resource id to delete (required)")
	deleteResourceCmd.Flags().StringVar(&deleteResourceOpts.mode, "mode", "", "Force a specific mode: template-shrink | stack-delete | adopt-and-delete (default: resolver picks)")
	deleteResourceCmd.Flags().BoolVar(&deleteResourceOpts.force, "force", false, "Override dangling-reference refusal for template-shrink")
	deleteResourceCmd.Flags().BoolVar(&deleteResourceOpts.dryRun, "dry-run", false, "Resolve the mode and print the plan; do not edit the template or call AWS")
	_ = deleteResourceCmd.MarkFlagRequired("stack-record")
	_ = deleteResourceCmd.MarkFlagRequired("logical-id")
	registerSubcommand(deleteResourceCmd)
}

// runDeleteResource is the cobra dispatch handler. It loads the
// stack record, runs the resolver, and (when --dry-run is unset)
// dispatches to the appropriate flow. The AWS-touching branches
// (stack-delete and the update step of the other two) return an
// explicit "not wired in this build" error when their backend is
// not registered — production binaries get the wiring via the cmd
// PR that follows PR-08; tests use the in-package fakes.
func runDeleteResource(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	rec, err := loadStackRecord(deleteResourceOpts.stackRecordPath)
	if err != nil {
		return err
	}
	res, err := delete.Resolve(rec, deleteResourceOpts.logicalID)
	if err != nil {
		return err
	}
	mode, err := delete.ParseMode(deleteResourceOpts.mode)
	if err != nil {
		return err
	}
	if mode == "" {
		mode = res.Mode
	}
	printPlan(out, rec, res, mode)
	if deleteResourceOpts.dryRun {
		return nil
	}
	if res.NeedsPrompt && deleteResourceOpts.mode == "" {
		return fmt.Errorf("delete-resource: stack %q has only one resource left — pass --mode=stack-delete or --mode=adopt-and-delete to acknowledge the last-resource prompt", rec.StackName)
	}
	return dispatchDelete(ctx, out, rec, mode, res)
}

// printPlan writes the resolver verdict + the chosen mode to out. It
// is intentionally chatty (one line per fact) so a developer running
// the CLI under a CI log sees the exact mode the command would take.
func printPlan(out io.Writer, rec delete.StackRecord, res delete.Resolution, mode delete.Mode) {
	fmt.Fprintf(out, "Stack:      %s\n", rec.StackName)
	fmt.Fprintf(out, "Resource:   %s (%s)\n", res.Target.LogicalID, res.Target.Type)
	fmt.Fprintf(out, "Remaining:  %d resource(s) after this delete\n", res.Remaining)
	fmt.Fprintf(out, "Resolved:   %s", res.Mode)
	if res.NeedsPrompt {
		fmt.Fprintf(out, " (needs confirmation: last-resource prompt)")
	}
	fmt.Fprintln(out, "")
	if mode != res.Mode {
		fmt.Fprintf(out, "Selected:   %s (overridden via --mode)\n", mode)
	}
}

// dispatchDelete routes to the right delete flow. The cmd layer does
// not own the AWS client wiring (a follow-up cmd PR registers an
// UpdateRunner and a CFN client adapter); when those are absent,
// the flows return clear errors and the CLI surfaces them.
func dispatchDelete(ctx context.Context, out io.Writer, rec delete.StackRecord, mode delete.Mode, res delete.Resolution) error {
	switch mode {
	case delete.ModeTemplateShrink:
		r, err := delete.Shrink(ctx, rec, res.Target.LogicalID, delete.ShrinkOptions{Force: deleteResourceOpts.force})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Wrote shrunk template: %s\n", r.ShrunkPath)
		fmt.Fprintf(out, "Previous template:     %s\n", r.PrevPath)
		if r.RemovedDependsOnEdits > 0 {
			fmt.Fprintf(out, "Updated DependsOn on %d resource(s).\n", r.RemovedDependsOnEdits)
		}
		return nil
	case delete.ModeStackDelete:
		return errors.New("delete-resource: stack-delete requires a CFN client adapter (wired in a follow-up cmd PR); rerun with --dry-run to see the plan")
	case delete.ModeAdoptAndDelete:
		r, err := delete.Adopt(ctx, rec, res.Target.LogicalID, delete.AdoptOptions{})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Wrote retain-edited template: %s\n", r.ShrunkPath)
		fmt.Fprintf(out, "Previous template:            %s\n", r.PrevPath)
		fmt.Fprintf(out, "Update ran:                   %t\n", r.UpdateRan)
		fmt.Fprintf(out, "Bridge request:               %d orphan(s) to confirm\n", len(r.Request.Items))
		return nil
	default:
		return fmt.Errorf("delete-resource: unsupported mode %q", mode)
	}
}

// loadStackRecord reads a stack record JSON file. PR-02 owns the
// canonical record schema; PR-08 keeps a minimal local read so the
// command can compile and be exercised before PR-02 lands. When
// PR-02 ships the cmd layer will swap this for the real reader.
//
// The JSON shape accepted here is the subset of ADR-0046 that
// PR-08 actually consumes:
//
//	{
//	  "stack_name": "alb-dev-stack",
//	  "template_path": "manifests/alb.yaml",
//	  "manifest_path": "manifests/alb.manifest.yaml",
//	  "resources": [
//	    {"logical_id": "TG", "physical_id": "arn:...", "type": "AWS::ElasticLoadBalancingV2::TargetGroup"},
//	    ...
//	  ]
//	}
func loadStackRecord(path string) (delete.StackRecord, error) {
	if path == "" {
		return delete.StackRecord{}, errors.New("delete-resource: --stack-record is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return delete.StackRecord{}, fmt.Errorf("delete-resource: read stack record %q: %w", path, err)
	}
	var doc stackRecordDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return delete.StackRecord{}, fmt.Errorf("delete-resource: parse stack record %q: %w", path, err)
	}
	out := delete.StackRecord{
		StackName:    doc.StackName,
		TemplatePath: doc.TemplatePath,
		ManifestPath: doc.ManifestPath,
	}
	for _, r := range doc.Resources {
		out.Resources = append(out.Resources, delete.Resource{
			LogicalID:  r.LogicalID,
			PhysicalID: r.PhysicalID,
			Type:       r.Type,
			Meta:       r.Meta,
		})
	}
	return out, nil
}

// stackRecordDoc mirrors the JSON shape loadStackRecord accepts. The
// field names match the PR-02 plan's serialisation contract so the
// file format is portable forward.
type stackRecordDoc struct {
	StackName    string             `json:"stack_name"`
	TemplatePath string             `json:"template_path"`
	ManifestPath string             `json:"manifest_path,omitempty"`
	Resources    []stackResourceDoc `json:"resources"`
}

// stackResourceDoc is one row in the persisted resources list.
type stackResourceDoc struct {
	LogicalID  string `json:"logical_id"`
	PhysicalID string `json:"physical_id,omitempty"`
	Type       string `json:"type,omitempty"`
	Meta       bool   `json:"meta,omitempty"`
}
