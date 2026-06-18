package delete

import (
	"context"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	auditdelete "github.com/bannaarr01/packwright/internal/audit/delete"
)

// AdoptOptions controls a single adopt-and-delete run.
type AdoptOptions struct {
	// Now is the timestamp used to derive the ".shrunk-<unix>.yaml"
	// sibling name (the adopt edit lands as a sibling, same as the
	// shrink edit). Zero falls back to time.Now().UTC().
	Now time.Time
}

// AdoptResult is the artefact set produced by a successful adopt
// edit. UpdateRan reports whether the registered UpdateRunner ran
// successfully (false for AdoptTemplate, true for Adopt).
//
// Request is the structured handoff to MVP-6's batch-consent flow;
// callers (the UI bridge) feed it into the consent modal verbatim.
type AdoptResult struct {
	ShrunkPath string
	PrevPath   string
	UpdateRan  bool
	Request    auditdelete.DeleteRequest
}

// AdoptTemplate edits the CFN template at record.TemplatePath to
// mark the resource with the supplied logicalID as Retain (so a
// subsequent UpdateStack dissociates it without deleting). The
// sibling-write / .prev-preserve contract matches ShrinkTemplate.
//
// On success, AdoptTemplate also constructs the audit/delete
// DeleteRequest for the orphan and returns it. The actual UPDATE
// and the orphan delete are NOT issued by this function — the
// caller chains them by passing the result to a wrapping flow (the
// Adopt function), or by driving them itself in tests.
func AdoptTemplate(record StackRecord, logicalID string, opts AdoptOptions) (AdoptResult, error) {
	if record.TemplatePath == "" {
		return AdoptResult{}, fmt.Errorf("delete: adopt: record has empty TemplatePath")
	}
	if logicalID == "" {
		return AdoptResult{}, fmt.Errorf("delete: adopt: logical id is empty")
	}
	target, ok := findResource(record.Resources, logicalID)
	if !ok {
		return AdoptResult{}, fmt.Errorf("%w: %q in %q", ErrResourceNotFound, logicalID, record.StackName)
	}
	raw, err := os.ReadFile(record.TemplatePath)
	if err != nil {
		return AdoptResult{}, fmt.Errorf("delete: adopt: read template: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return AdoptResult{}, fmt.Errorf("delete: adopt: parse template: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return AdoptResult{}, fmt.Errorf("delete: adopt: template root is not a mapping")
	}
	resources := findMapValue(root, "Resources")
	if resources == nil || resources.Kind != yaml.MappingNode {
		return AdoptResult{}, fmt.Errorf("delete: adopt: template has no Resources mapping")
	}
	body := findMapValue(resources, logicalID)
	if body == nil || body.Kind != yaml.MappingNode {
		return AdoptResult{}, fmt.Errorf("delete: adopt: %q not found under Resources in %s", logicalID, record.TemplatePath)
	}
	if err := setDeletionPolicyRetain(body); err != nil {
		return AdoptResult{}, fmt.Errorf("delete: adopt: %w", err)
	}
	out, err := encodeNode(&doc)
	if err != nil {
		return AdoptResult{}, fmt.Errorf("delete: adopt: encode template: %w", err)
	}
	shrunkPath, prevPath, err := writeShrunk(record.TemplatePath, out, opts.Now)
	if err != nil {
		return AdoptResult{}, err
	}
	item := auditdelete.DeleteItem{
		Kind:       auditdelete.KindFromCFNType(target.Type),
		PhysicalID: target.PhysicalID,
		Source: auditdelete.OrphanSource{
			StackName:       record.StackName,
			LogicalID:       target.LogicalID,
			CFNResourceType: target.Type,
			OriginatingFlow: auditdelete.FlowAdoptAndDelete,
		},
	}
	return AdoptResult{
		ShrunkPath: shrunkPath,
		PrevPath:   prevPath,
		UpdateRan:  false,
		Request:    auditdelete.ToAuditDeleteRequest([]auditdelete.DeleteItem{item}),
	}, nil
}

// Adopt is the high-level entry point used by the cmd surface and
// the sidebar wiring. It runs the template edit, calls the registered
// UpdateRunner to dissociate the resource from CFN, and returns the
// MVP-6 hand-off structure for the orphan. The caller (UI bridge)
// then feeds Request into MVP-6's batch-consent modal.
//
// Adopt does NOT issue the orphan Delete* call directly — the whole
// point of ADR-0053's "three modes, one consent gate" is that the
// last destructive step always passes through MVP-6's typed-DELETE
// confirmation.
func Adopt(ctx context.Context, record StackRecord, logicalID string, opts AdoptOptions) (AdoptResult, error) {
	res, err := AdoptTemplate(record, logicalID, opts)
	if err != nil {
		return AdoptResult{}, err
	}
	if err := runUpdate(ctx, UpdateRequest{
		StackName:    record.StackName,
		TemplatePath: res.ShrunkPath,
		ManifestPath: record.ManifestPath,
		Reason:       fmt.Sprintf("adopt-and-delete: retain %s", logicalID),
	}); err != nil {
		return res, fmt.Errorf("delete: adopt %q: update: %w", record.StackName, err)
	}
	res.UpdateRan = true
	return res, nil
}

// findResource returns the resource with logicalID and a found flag.
func findResource(rs []Resource, logicalID string) (Resource, bool) {
	for _, r := range rs {
		if r.LogicalID == logicalID {
			return r, true
		}
	}
	return Resource{}, false
}

// setDeletionPolicyRetain edits body (a Resources[<id>] mapping) so
// it carries `DeletionPolicy: Retain`. If a different DeletionPolicy
// is already present this updates it; if absent, a new key is
// inserted after the Type / Properties pair so the resulting YAML
// reads naturally to a human.
func setDeletionPolicyRetain(body *yaml.Node) error {
	if body == nil || body.Kind != yaml.MappingNode {
		return fmt.Errorf("resource body is not a mapping")
	}
	// Update in place if the key already exists.
	for i := 0; i+1 < len(body.Content); i += 2 {
		if body.Content[i].Value == "DeletionPolicy" {
			body.Content[i+1].Kind = yaml.ScalarNode
			body.Content[i+1].Tag = "!!str"
			body.Content[i+1].Style = 0
			body.Content[i+1].Value = "Retain"
			return nil
		}
	}
	// Insert after the last of {Type, Properties} for readability.
	insertAfter := -1
	for i := 0; i+1 < len(body.Content); i += 2 {
		k := body.Content[i].Value
		if k == "Type" || k == "Properties" {
			insertAfter = i + 1
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "DeletionPolicy"}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "Retain"}
	if insertAfter < 0 {
		body.Content = append(body.Content, keyNode, valNode)
		return nil
	}
	tail := append([]*yaml.Node{}, body.Content[insertAfter+1:]...)
	body.Content = append(body.Content[:insertAfter+1], keyNode, valNode)
	body.Content = append(body.Content, tail...)
	return nil
}
