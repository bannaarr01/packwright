package resource_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/manifest"
)

func TestValidate_DistinctAZRejectsSingleAZ(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{
				ID: "SubnetIds", Type: manifest.TypeAWSSubnetIDs,
				Required: true,
				Validate: []manifest.ValidatorSpec{
					{Rule: "distinct-az", Message: "subnets must span at least two AZs"},
				},
			},
		},
	}
	sameAZ := func(_ context.Context, _ string) (string, error) {
		return "us-east-1a", nil
	}

	errs := resource.Validate(
		context.Background(),
		m,
		resource.Inputs{"SubnetIds": []string{"subnet-1", "subnet-2", "subnet-3"}},
		sameAZ,
	)
	if errs == nil {
		t.Fatal("distinct-az should fail when all subnets are in the same AZ")
	}
	if got := errs.Map()["SubnetIds"]; !strings.Contains(got, "span at least two") {
		t.Errorf("error = %q, want manifest message", got)
	}
}

func TestValidate_DistinctAZAcceptsMultipleAZs(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{
				ID: "SubnetIds", Type: manifest.TypeAWSSubnetIDs,
				Required: true,
				Validate: []manifest.ValidatorSpec{{Rule: "distinct-az"}},
			},
		},
	}
	table := map[string]string{"subnet-1": "us-east-1a", "subnet-2": "us-east-1b"}
	lookup := func(_ context.Context, id string) (string, error) {
		return table[id], nil
	}

	if errs := resource.Validate(
		context.Background(),
		m,
		resource.Inputs{"SubnetIds": []string{"subnet-1", "subnet-2"}},
		lookup,
	); errs != nil {
		t.Errorf("distinct-az should pass; got %v", errs)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{ID: "Name", Type: manifest.TypeString, Required: true},
		},
	}
	errs := resource.Validate(context.Background(), m, resource.Inputs{}, nil)
	if errs == nil {
		t.Fatal("missing required field should fail validation")
	}
	if got := errs.Map()["Name"]; !strings.Contains(got, "required") {
		t.Errorf("error = %q, want 'is required'", got)
	}
}

func TestValidate_EnumMembership(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{ID: "Env", Type: manifest.TypeEnum, Values: []string{"dev", "stg", "prd"}},
		},
	}
	errs := resource.Validate(context.Background(), m, resource.Inputs{"Env": "stg"}, nil)
	if errs != nil {
		t.Errorf("valid enum value should pass; got %v", errs)
	}

	errs = resource.Validate(context.Background(), m, resource.Inputs{"Env": "prod"}, nil)
	if errs == nil {
		t.Error("invalid enum value should fail")
	}
}

func TestValidate_LengthBounds(t *testing.T) {
	min2, max5 := 2, 5
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{ID: "List", Type: manifest.TypeMultistring, Min: &min2, Max: &max5},
		},
	}
	cases := []struct {
		name  string
		value []string
		wantE bool
	}{
		{"under-min", []string{"one"}, true},
		{"at-min", []string{"one", "two"}, false},
		{"in-range", []string{"a", "b", "c"}, false},
		{"at-max", []string{"a", "b", "c", "d", "e"}, false},
		{"over-max", []string{"a", "b", "c", "d", "e", "f"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := resource.Validate(
				context.Background(),
				m,
				resource.Inputs{"List": tc.value},
				nil,
			)
			if tc.wantE && errs == nil {
				t.Errorf("%v: want error", tc.value)
			}
			if !tc.wantE && errs != nil {
				t.Errorf("%v: unexpected error: %v", tc.value, errs)
			}
		})
	}
}

func TestValidate_TypeMismatch(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{ID: "N", Type: manifest.TypeInt},
		},
	}
	errs := resource.Validate(context.Background(), m, resource.Inputs{"N": "twelve"}, nil)
	if errs == nil {
		t.Fatal("string for int field should fail")
	}
	if got := errs.Map()["N"]; !strings.Contains(got, "expected int") {
		t.Errorf("error = %q, want type-mismatch message", got)
	}
}

func TestValidate_DistinctAZ_LookupErrorIsReturned(t *testing.T) {
	m := &manifest.Manifest{
		Form: []manifest.Field{
			{
				ID: "SubnetIds", Type: manifest.TypeAWSSubnetIDs,
				Validate: []manifest.ValidatorSpec{{Rule: "distinct-az"}},
			},
		},
	}
	boom := errors.New("describe-subnets failed")
	lookup := func(_ context.Context, _ string) (string, error) { return "", boom }

	errs := resource.Validate(
		context.Background(),
		m,
		resource.Inputs{"SubnetIds": []string{"subnet-1"}},
		lookup,
	)
	if errs == nil {
		t.Fatal("lookup error should surface as validation error")
	}
	if got := errs.Map()["SubnetIds"]; !strings.Contains(got, "describe-subnets failed") {
		t.Errorf("error = %q, want lookup error message", got)
	}
}

func TestDiff_DetectsAddRemoveModify(t *testing.T) {
	cur := map[string]any{
		"VpcId":           "vpc-old",
		"HealthCheckPath": "/health",
		"Stale":           "x",
	}
	next := map[string]any{
		"VpcId":           "vpc-new",
		"HealthCheckPath": "/health",
		"Fresh":           "y",
	}
	changes := resource.Diff(cur, next)
	got := make(map[string]resource.ChangeKind, len(changes))
	for _, c := range changes {
		got[c.Key] = c.Kind
	}
	want := map[string]resource.ChangeKind{
		"VpcId": resource.Modified,
		"Stale": resource.Removed,
		"Fresh": resource.Added,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Diff[%s] = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["HealthCheckPath"]; ok {
		t.Error("unchanged key should not appear in Diff output")
	}
}
