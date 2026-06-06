package resource_test

import (
	"testing"

	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/manifest"
)

func TestFormState_DependsOnGating(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{ID: "VpcId", Type: manifest.TypeAWSVpcID},
			{ID: "SubnetIds", Type: manifest.TypeAWSSubnetIDs, DependsOn: []string{"VpcId"}},
		},
	}
	fs := resource.NewFormState(m)

	if fs.Available("SubnetIds") {
		t.Error("SubnetIds should be unavailable until VpcId is set")
	}
	if !fs.Available("VpcId") {
		t.Error("VpcId should be available with no dependencies")
	}

	fs.Set("VpcId", "")
	if fs.Available("SubnetIds") {
		t.Error("SubnetIds should still be unavailable when VpcId is empty string")
	}

	fs.Set("VpcId", "vpc-123")
	if !fs.Available("SubnetIds") {
		t.Error("SubnetIds should be available once VpcId has a value")
	}

	if ids := fs.AvailableIDs(); len(ids) != 2 {
		t.Errorf("AvailableIDs = %v, want both fields", ids)
	}
}

func TestFormState_TouchedAndErrors(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{{ID: "Name", Type: manifest.TypeString}},
	}
	fs := resource.NewFormState(m)

	if got := fs.Get("Name"); got.Touched {
		t.Error("new field should not be Touched")
	}

	fs.Set("Name", "alb")
	if got := fs.Get("Name"); !got.Touched || got.Value != "alb" {
		t.Errorf("after Set: %+v", got)
	}

	fs.SetError("Name", "too short")
	if got := fs.Get("Name").Error; got != "too short" {
		t.Errorf("SetError didn't propagate; got %q", got)
	}

	// Set clears the prior error so the user sees fresh validation only.
	fs.Set("Name", "alb-prd")
	if got := fs.Get("Name").Error; got != "" {
		t.Errorf("Set should clear Error; got %q", got)
	}
}

func TestFormState_InputsOmitsUntouched(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{ID: "A", Type: manifest.TypeString},
			{ID: "B", Type: manifest.TypeString},
		},
	}
	fs := resource.NewFormState(m)
	fs.Set("A", "a-value")

	in := fs.Inputs()
	if _, ok := in["A"]; !ok {
		t.Error("Inputs missing touched field A")
	}
	if _, ok := in["B"]; ok {
		t.Error("Inputs included untouched field B")
	}
}

func TestFormState_UnknownFieldSetIsIgnored(t *testing.T) {
	fs := resource.NewFormState(&manifest.Manifest{})
	fs.Set("nope", "value") // must not panic
	if got := fs.Get("nope").Touched; got {
		t.Error("unknown field should not become Touched after Set")
	}
}
