package scaling

import "testing"

func TestEffectiveBoundEnvGuardOverridesSpec(t *testing.T) {
	specMax := 50
	guardMax := 20
	got := effectiveBound(&specMax, &guardMax)
	if got == nil || *got != 20 {
		t.Errorf("effectiveBound(spec=50, guard=20) = %v, want pointer to 20", got)
	}
}

func TestEffectiveBoundSpecFallbackWhenGuardNil(t *testing.T) {
	specMax := 50
	got := effectiveBound(&specMax, nil)
	if got == nil || *got != 50 {
		t.Errorf("effectiveBound(spec=50, guard=nil) = %v, want pointer to 50", got)
	}
}

func TestEffectiveBoundNilOnBothSides(t *testing.T) {
	if got := effectiveBound(nil, nil); got != nil {
		t.Errorf("effectiveBound(nil, nil) = %v, want nil", got)
	}
}

func TestEffectiveGuardMissingEnvIsZeroValue(t *testing.T) {
	spec := Spec{
		EnvGuards: map[string]EnvGuard{"prd": {Max: IntPtr(20)}},
	}
	if got := effectiveGuard(spec, "dev"); got.Max != nil || got.RequireConfirmation {
		t.Errorf("effectiveGuard(missing) = %+v, want zero EnvGuard", got)
	}
}

func TestApplyIntegerWithinBoundsReturnsCanonicalForm(t *testing.T) {
	spec := Spec{Param: "DesiredCount", Kind: KindInteger, Min: IntPtr(1), Max: IntPtr(50)}
	v, ev, err := applyValue(spec, EnvGuard{}, "dev", "07")
	if err != nil {
		t.Fatalf("applyValue(in-range) err = %v, want nil", err)
	}
	if ev != nil {
		t.Errorf("applyValue(in-range) clamp = %+v, want nil", ev)
	}
	if v != "7" {
		t.Errorf("applyValue(in-range) = %q, want canonical %q", v, "7")
	}
}

func TestApplyIntegerAboveMaxClamps(t *testing.T) {
	spec := Spec{Param: "DesiredCount", Kind: KindInteger, Min: IntPtr(1), Max: IntPtr(50),
		EnvGuards: map[string]EnvGuard{"prd": {Max: IntPtr(20)}}}
	guard := effectiveGuard(spec, "prd")
	v, ev, err := applyValue(spec, guard, "prd", "30")
	if err != nil {
		t.Fatalf("applyValue err = %v, want nil", err)
	}
	if v != "20" {
		t.Errorf("clamped value = %q, want %q", v, "20")
	}
	if ev == nil {
		t.Fatal("expected ClampEvent, got nil")
	}
	if ev.Bound != "max" || ev.Limit != 20 || ev.Requested != "30" || ev.Effective != "20" || ev.Env != "prd" {
		t.Errorf("ClampEvent = %+v, want {param=DesiredCount env=prd req=30 eff=20 bound=max limit=20}", ev)
	}
}

func TestApplyIntegerBelowMinClamps(t *testing.T) {
	spec := Spec{Param: "DesiredCount", Kind: KindInteger,
		EnvGuards: map[string]EnvGuard{"prd": {Min: IntPtr(2)}}}
	guard := effectiveGuard(spec, "prd")
	v, ev, err := applyValue(spec, guard, "prd", "0")
	if err != nil {
		t.Fatalf("applyValue err = %v, want nil", err)
	}
	if v != "2" || ev == nil || ev.Bound != "min" || ev.Limit != 2 {
		t.Errorf("apply(below min) = (%q, %+v), want (2, min/2)", v, ev)
	}
}

func TestApplyIntegerRejectsNonNumeric(t *testing.T) {
	spec := Spec{Param: "DesiredCount", Kind: KindInteger}
	_, _, err := applyValue(spec, EnvGuard{}, "dev", "lots")
	if err == nil {
		t.Fatal("applyValue(non-int) err = nil, want error")
	}
}

func TestApplyEnumAcceptsMember(t *testing.T) {
	spec := Spec{Param: "TaskCpu", Kind: KindEnum, Values: []string{"256", "512", "1024"}}
	v, ev, err := applyValue(spec, EnvGuard{}, "dev", "512")
	if err != nil {
		t.Fatalf("applyValue(enum hit) err = %v, want nil", err)
	}
	if v != "512" || ev != nil {
		t.Errorf("applyValue(enum hit) = (%q, %+v), want (512, nil)", v, ev)
	}
}

func TestApplyEnumRejectsNonMember(t *testing.T) {
	spec := Spec{Param: "TaskCpu", Kind: KindEnum, Values: []string{"256", "512"}}
	_, _, err := applyValue(spec, EnvGuard{}, "dev", "999")
	if err == nil {
		t.Fatal("applyValue(enum miss) err = nil, want error")
	}
}

func TestApplyStringPassesThrough(t *testing.T) {
	spec := Spec{Param: "Tag", Kind: KindString}
	v, ev, err := applyValue(spec, EnvGuard{}, "dev", "any thing 123")
	if err != nil || ev != nil || v != "any thing 123" {
		t.Fatalf("applyValue(string) = (%q, %+v, %v), want passthrough", v, ev, err)
	}
}

func TestApplyUnknownKindRejected(t *testing.T) {
	spec := Spec{Param: "P", Kind: Kind("bogus")}
	if _, _, err := applyValue(spec, EnvGuard{}, "dev", "x"); err == nil {
		t.Fatal("applyValue(unknown kind) err = nil, want error")
	}
}

func TestApplyEmptyKindRejected(t *testing.T) {
	spec := Spec{Param: "P"}
	if _, _, err := applyValue(spec, EnvGuard{}, "dev", "x"); err == nil {
		t.Fatal("applyValue(empty kind) err = nil, want error")
	}
}
