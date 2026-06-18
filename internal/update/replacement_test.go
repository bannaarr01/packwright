package update

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/render/cfn"
)

func TestBuildReplacementPayload_EmptyWhenNoReplaces(t *testing.T) {
	res := cfn.DescribeChangeSetResult{Status: "CREATE_COMPLETE"}
	d := BuildDiff(res, nil)
	p := BuildReplacementPayload("acme-dev-alb", d)
	if p.HasReplacements() {
		t.Errorf("HasReplacements = true, want false on empty diff")
	}
	if p.Count != 0 || len(p.Rows) != 0 {
		t.Errorf("payload = %+v, want zero", p)
	}
}

func TestBuildReplacementPayload_PopulatesFromDiff(t *testing.T) {
	d := BuildDiff(syntheticDescribe(), map[string]string{"DBInstanceClass": "db.t3.medium"})
	p := BuildReplacementPayload("acme-dev-alb", d)

	if !p.HasReplacements() {
		t.Fatal("HasReplacements = false on diff with one replace")
	}
	if p.Count != 1 || len(p.Rows) != 1 {
		t.Fatalf("Count = %d, Rows = %d; want 1/1", p.Count, len(p.Rows))
	}
	row := p.Rows[0]
	if row.LogicalID != "DBInstance" || row.ResourceType != "AWS::RDS::DBInstance" {
		t.Errorf("row = %+v, want DBInstance/RDS", row)
	}
	if len(row.PropertyCauses) != 1 || row.PropertyCauses[0] != "DBInstanceClass" {
		t.Errorf("PropertyCauses = %+v, want [DBInstanceClass]", row.PropertyCauses)
	}
	if got := p.ConsentReason(); got != "human-confirmed replacement of 1 resource" {
		t.Errorf("ConsentReason = %q", got)
	}
}

func TestBuildReplacementPayload_ConsentReasonPluralisation(t *testing.T) {
	d := Diff{
		Replaces: []ResourceDelta{
			{LogicalID: "A", ResourceType: "AWS::RDS::DBInstance"},
			{LogicalID: "B", ResourceType: "AWS::RDS::DBInstance"},
		},
	}
	p := BuildReplacementPayload("s", d)
	if got := p.ConsentReason(); got != "human-confirmed replacement of 2 resources" {
		t.Errorf("ConsentReason = %q, want plural form", got)
	}
}

func TestBuildReplacementPayload_BlastHintCompactsTypes(t *testing.T) {
	d := Diff{
		Replaces: []ResourceDelta{
			{LogicalID: "DBInstance", ResourceType: "AWS::RDS::DBInstance", PropertyCauses: []string{"DBInstanceClass"}},
			{LogicalID: "AdminRole", ResourceType: "AWS::IAM::Role"},
		},
	}
	p := BuildReplacementPayload("s", d)
	hint := p.BlastHint()
	if !strings.Contains(hint, "RDS::DBInstance") {
		t.Errorf("BlastHint missing RDS::DBInstance: %q", hint)
	}
	if !strings.Contains(hint, "(DBInstanceClass)") {
		t.Errorf("BlastHint missing property cause: %q", hint)
	}
	if !strings.Contains(hint, "IAM::AdminRole") {
		t.Errorf("BlastHint missing IAM::AdminRole: %q", hint)
	}
}

func TestBuildReplacementPayload_MarshalArgsRoundTripsStable(t *testing.T) {
	d := BuildDiff(syntheticDescribe(), map[string]string{"DBInstanceClass": "db.t3.medium"})
	p := BuildReplacementPayload("acme-dev-alb", d)
	args, err := p.MarshalArgs()
	if err != nil {
		t.Fatalf("MarshalArgs err = %v", err)
	}
	var back ReplacementPayload
	if err := json.Unmarshal(args, &back); err != nil {
		t.Fatalf("Unmarshal err = %v", err)
	}
	if back.StackName != p.StackName || back.Count != p.Count || len(back.Rows) != len(p.Rows) {
		t.Errorf("round-tripped payload = %+v, want %+v", back, p)
	}
}

func TestConsentToolNameStable(t *testing.T) {
	// Tool name is wired into the consent gate; flipping it silently
	// would break a deployed audit pipeline. Lock it down.
	if ConsentToolName != "stack/update" {
		t.Errorf("ConsentToolName = %q, want stack/update", ConsentToolName)
	}
}
