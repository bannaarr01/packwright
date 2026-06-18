package scaling

import "testing"

func TestBuildFormPairsSpecsWithCurrentValues(t *testing.T) {
	specs := []Spec{
		{Param: "DesiredCount", Kind: KindInteger},
		{Param: "TaskCpu", Kind: KindEnum, Values: []string{"256", "512"}},
		{Param: "Tag", Kind: KindString},
	}
	current := map[string]string{
		"DesiredCount": "2",
		"TaskCpu":      "256",
		// Tag not yet set — Form should carry the empty string.
	}

	form := BuildForm("alb-dev-stack", "dev", current, specs)

	if form.StackName != "alb-dev-stack" {
		t.Errorf("Form.StackName = %q, want %q", form.StackName, "alb-dev-stack")
	}
	if form.Env != "dev" {
		t.Errorf("Form.Env = %q, want %q", form.Env, "dev")
	}
	if got, want := len(form.Targets), len(specs); got != want {
		t.Fatalf("len(Form.Targets) = %d, want %d", got, want)
	}
	for i, target := range form.Targets {
		if target.Spec.Param != specs[i].Param {
			t.Errorf("Targets[%d].Spec.Param = %q, want %q (order must mirror specs)",
				i, target.Spec.Param, specs[i].Param)
		}
	}
	if form.Targets[2].Current != "" {
		t.Errorf("Targets[Tag].Current = %q, want empty for unset parameter", form.Targets[2].Current)
	}
	if form.Targets[0].Current != "2" {
		t.Errorf("Targets[DesiredCount].Current = %q, want %q", form.Targets[0].Current, "2")
	}
}

func TestBuildFormHandlesNilCurrent(t *testing.T) {
	specs := []Spec{{Param: "DesiredCount", Kind: KindInteger}}
	form := BuildForm("s", "dev", nil, specs)
	if len(form.Targets) != 1 || form.Targets[0].Current != "" {
		t.Fatalf("BuildForm(nil current) = %+v, want one target with empty Current", form.Targets)
	}
}
