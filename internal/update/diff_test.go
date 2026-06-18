package update

import (
	"reflect"
	"testing"

	"github.com/bannaarr01/packwright/render/cfn"
)

// syntheticDescribe returns a DescribeChangeSetResult with one Modify, one
// Replace (RDS), and one Add — the canonical fixture used across the diff
// and replacement-consent tests.
func syntheticDescribe() cfn.DescribeChangeSetResult {
	return cfn.DescribeChangeSetResult{
		Status: "CREATE_COMPLETE",
		Parameters: []cfn.Parameter{
			{Key: "VpcId", Value: "vpc-0abc"},
			{Key: "DBInstanceClass", Value: "db.t3.large"},
		},
		Changes: []cfn.Change{
			{
				Action:             "Modify",
				LogicalResourceID:  "ApplicationLoadBalancer",
				ResourceType:       "AWS::ElasticLoadBalancingV2::LoadBalancer",
				Replacement:        "False",
				PhysicalResourceID: "arn:elb:1",
			},
			{
				Action:             "Modify",
				LogicalResourceID:  "DBInstance",
				ResourceType:       "AWS::RDS::DBInstance",
				Replacement:        "True",
				PhysicalResourceID: "rds-instance-old",
				Details: []cfn.ChangeDetail{{
					Target: cfn.ChangeTarget{
						Attribute:          "Properties",
						Name:               "DBInstanceClass",
						RequiresRecreation: "Always",
					},
					CausingEntity: "DBInstanceClass",
				}},
			},
			{
				Action:            "Add",
				LogicalResourceID: "AdminRole",
				ResourceType:      "AWS::IAM::Role",
			},
		},
	}
}

func TestBuildDiff_BucketsResourcesAndDetectsIAM(t *testing.T) {
	prev := map[string]string{"VpcId": "vpc-0abc", "DBInstanceClass": "db.t3.medium"}
	d := BuildDiff(syntheticDescribe(), prev)

	adds, mods, reps, dels := d.Counts()
	if adds != 1 || mods != 1 || reps != 1 || dels != 0 {
		t.Errorf("counts = (%d, %d, %d, %d), want (1, 1, 1, 0)", adds, mods, reps, dels)
	}
	if d.Total() != 3 {
		t.Errorf("Total = %d, want 3", d.Total())
	}
	if !d.HasReplacements() {
		t.Error("HasReplacements = false on synthetic with one replace")
	}
	if d.NoChanges {
		t.Error("NoChanges = true on synthetic with three changes")
	}
	if got := d.Replaces[0]; got.LogicalID != "DBInstance" || !reflect.DeepEqual(got.PropertyCauses, []string{"DBInstanceClass"}) {
		t.Errorf("replace row = %+v, want DBInstance / [DBInstanceClass]", got)
	}
	if !d.Adds[0].IAM {
		t.Errorf("Add row %+v: IAM = false, want true for AWS::IAM::Role", d.Adds[0])
	}
	if d.Modifies[0].IAM {
		t.Errorf("ALB row IAM = true, want false")
	}
}

func TestBuildDiff_ParameterDeltaTagsReplacementCause(t *testing.T) {
	prev := map[string]string{"VpcId": "vpc-0abc", "DBInstanceClass": "db.t3.medium"}
	d := BuildDiff(syntheticDescribe(), prev)

	if len(d.ParameterDeltas) != 1 {
		t.Fatalf("ParameterDeltas = %d, want 1 (only DBInstanceClass changed)", len(d.ParameterDeltas))
	}
	pd := d.ParameterDeltas[0]
	if pd.Key != "DBInstanceClass" || pd.Old != "db.t3.medium" || pd.New != "db.t3.large" {
		t.Errorf("ParameterDelta = %+v, want DBInstanceClass medium→large", pd)
	}
	if !pd.CausedReplacement {
		t.Errorf("CausedReplacement = false, want true for DBInstanceClass")
	}
}

func TestBuildDiff_ParameterDeltaIncludesDroppedKeys(t *testing.T) {
	prev := map[string]string{"VpcId": "vpc-0abc", "RemovedKey": "old"}
	res := cfn.DescribeChangeSetResult{
		Status: "CREATE_COMPLETE",
		Parameters: []cfn.Parameter{
			{Key: "VpcId", Value: "vpc-0abc"},
		},
	}
	d := BuildDiff(res, prev)
	if len(d.ParameterDeltas) != 1 || d.ParameterDeltas[0].Key != "RemovedKey" || d.ParameterDeltas[0].New != "" {
		t.Errorf("expected dropped key in ParameterDeltas, got %+v", d.ParameterDeltas)
	}
}

func TestBuildDiff_NoChangesPropagates(t *testing.T) {
	res := cfn.DescribeChangeSetResult{
		Status:       "FAILED",
		StatusReason: "The submitted information didn't contain changes.",
		NoChanges:    true,
	}
	d := BuildDiff(res, nil)
	if !d.NoChanges {
		t.Error("NoChanges = false, want true")
	}
	if d.Total() != 0 {
		t.Errorf("Total = %d, want 0", d.Total())
	}
	if d.HasReplacements() {
		t.Error("HasReplacements = true on a no-changes diff")
	}
}

func TestClassifyAction_ModifyWithReplacementBecomesReplace(t *testing.T) {
	cases := []struct {
		action, repl string
		want         DiffAction
	}{
		{"Add", "", ActionAdd},
		{"Remove", "", ActionRemove},
		{"Modify", "False", ActionModify},
		{"Modify", "True", ActionReplace},
		{"Modify", "Conditional", ActionModify},
		{"Import", "", ActionImport},
		{"Dynamic", "", ActionDynamic},
		{"modify", "true", ActionReplace},
	}
	for _, c := range cases {
		got := classifyAction(c.action, c.repl)
		if got != c.want {
			t.Errorf("classifyAction(%q, %q) = %q, want %q", c.action, c.repl, got, c.want)
		}
	}
}

func TestBuildDiff_StableOrdering(t *testing.T) {
	res := cfn.DescribeChangeSetResult{
		Status: "CREATE_COMPLETE",
		Changes: []cfn.Change{
			{Action: "Add", LogicalResourceID: "Zeta"},
			{Action: "Add", LogicalResourceID: "Alpha"},
			{Action: "Add", LogicalResourceID: "Mu"},
		},
	}
	d := BuildDiff(res, nil)
	if len(d.Adds) != 3 || d.Adds[0].LogicalID != "Alpha" || d.Adds[2].LogicalID != "Zeta" {
		t.Errorf("Adds order = %+v, want sorted by LogicalID", d.Adds)
	}
}
