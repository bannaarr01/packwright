package scaling

import (
	"strings"
	"testing"
)

func TestBuildParamsCopiesCurrentAndAppliesDeltas(t *testing.T) {
	current := map[string]string{
		"Project":      "acme",
		"Environment":  "dev",
		"DesiredCount": "2",
		"TaskCpu":      "256",
	}
	specs := []Spec{
		{Param: "DesiredCount", Kind: KindInteger, Min: IntPtr(1), Max: IntPtr(50)},
		{Param: "TaskCpu", Kind: KindEnum, Values: []string{"256", "512", "1024"}},
	}
	deltas := map[string]string{
		"DesiredCount": "8",
		"TaskCpu":      "512",
	}

	res, err := BuildParams(current, deltas, "dev", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v, want nil", err)
	}
	if res.Params["DesiredCount"] != "8" || res.Params["TaskCpu"] != "512" {
		t.Errorf("Params = %v, want DesiredCount=8 TaskCpu=512", res.Params)
	}
	if res.Params["Project"] != "acme" || res.Params["Environment"] != "dev" {
		t.Errorf("non-scaling params dropped: %v", res.Params)
	}
	if len(res.Clamps) != 0 {
		t.Errorf("Clamps = %+v, want none for in-range deltas", res.Clamps)
	}
	if res.RequireConsent {
		t.Error("RequireConsent = true, want false (no env guard fires)")
	}
}

func TestBuildParamsDoesNotMutateCurrent(t *testing.T) {
	current := map[string]string{"DesiredCount": "2"}
	specs := []Spec{{Param: "DesiredCount", Kind: KindInteger}}
	deltas := map[string]string{"DesiredCount": "5"}

	_, err := BuildParams(current, deltas, "dev", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v", err)
	}
	if current["DesiredCount"] != "2" {
		t.Errorf("current mutated: DesiredCount = %q, want unchanged 2", current["DesiredCount"])
	}
}

func TestBuildParamsHandlesNilCurrent(t *testing.T) {
	specs := []Spec{{Param: "DesiredCount", Kind: KindInteger}}
	deltas := map[string]string{"DesiredCount": "5"}
	res, err := BuildParams(nil, deltas, "dev", specs)
	if err != nil {
		t.Fatalf("BuildParams(nil current) err = %v, want nil", err)
	}
	if res.Params["DesiredCount"] != "5" {
		t.Errorf("Params[DesiredCount] = %q, want 5", res.Params["DesiredCount"])
	}
}

func TestBuildParamsClampsAboveProdMaxAndLogsEvent(t *testing.T) {
	// Mirrors the DoD scenario: prd guard sets max=20; user submits 30.
	specs := []Spec{
		{
			Param: "DesiredCount",
			Kind:  KindInteger,
			Min:   IntPtr(1),
			Max:   IntPtr(50),
			EnvGuards: map[string]EnvGuard{
				"prd": {Min: IntPtr(2), Max: IntPtr(20), RequireConfirmation: true},
				"dev": {Min: IntPtr(0), Max: IntPtr(50)},
			},
		},
	}
	current := map[string]string{"DesiredCount": "5"}
	res, err := BuildParams(current, map[string]string{"DesiredCount": "30"}, "prd", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v, want nil", err)
	}
	if res.Params["DesiredCount"] != "20" {
		t.Errorf("Params[DesiredCount] = %q, want clamped to 20", res.Params["DesiredCount"])
	}
	if len(res.Clamps) != 1 {
		t.Fatalf("Clamps = %+v, want exactly one event", res.Clamps)
	}
	c := res.Clamps[0]
	if c.Param != "DesiredCount" || c.Env != "prd" || c.Bound != "max" || c.Limit != 20 {
		t.Errorf("ClampEvent = %+v, want DesiredCount/prd/max/20", c)
	}
	if c.Requested != "30" || c.Effective != "20" {
		t.Errorf("ClampEvent values: requested=%q effective=%q, want 30 → 20", c.Requested, c.Effective)
	}
	if !res.RequireConsent || res.ConsentReason != "scale on prd env" {
		t.Errorf("RequireConsent=%v reason=%q, want true / 'scale on prd env'", res.RequireConsent, res.ConsentReason)
	}
}

func TestBuildParamsClampDevWidensSpecBounds(t *testing.T) {
	// dev guard explicitly widens the spec's min from 1 to 0.
	specs := []Spec{{
		Param: "DesiredCount", Kind: KindInteger,
		Min:       IntPtr(1),
		Max:       IntPtr(50),
		EnvGuards: map[string]EnvGuard{"dev": {Min: IntPtr(0)}},
	}}
	res, err := BuildParams(nil, map[string]string{"DesiredCount": "0"}, "dev", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v", err)
	}
	if res.Params["DesiredCount"] != "0" {
		t.Errorf("DesiredCount = %q, want 0 (dev guard min=0)", res.Params["DesiredCount"])
	}
	if len(res.Clamps) != 0 {
		t.Errorf("Clamps = %+v, want none (dev guard widened min)", res.Clamps)
	}
}

func TestBuildParamsNoEnvGuardUsesSpecBounds(t *testing.T) {
	// No env guard for "stg" — spec.Max=50 still applies.
	specs := []Spec{{
		Param: "DesiredCount", Kind: KindInteger,
		Min:       IntPtr(1),
		Max:       IntPtr(50),
		EnvGuards: map[string]EnvGuard{"prd": {Max: IntPtr(20)}},
	}}
	res, err := BuildParams(nil, map[string]string{"DesiredCount": "100"}, "stg", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v", err)
	}
	if res.Params["DesiredCount"] != "50" {
		t.Errorf("DesiredCount = %q, want clamped to spec max 50", res.Params["DesiredCount"])
	}
	if len(res.Clamps) != 1 || res.Clamps[0].Limit != 50 {
		t.Errorf("Clamps = %+v, want one event at spec max 50", res.Clamps)
	}
	if res.RequireConsent {
		t.Error("RequireConsent = true, want false (stg has no guard)")
	}
}

func TestBuildParamsConsentReasonOnlyWhenGuardFires(t *testing.T) {
	// Two specs: only DesiredCount has require_confirmation. Submitting a
	// non-confirming delta for TaskCpu should not flip RequireConsent.
	specs := []Spec{
		{Param: "DesiredCount", Kind: KindInteger,
			EnvGuards: map[string]EnvGuard{"prd": {RequireConfirmation: true}}},
		{Param: "TaskCpu", Kind: KindEnum, Values: []string{"256", "512"}},
	}
	res, err := BuildParams(nil, map[string]string{"TaskCpu": "512"}, "prd", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v", err)
	}
	if res.RequireConsent {
		t.Error("RequireConsent = true, want false (DesiredCount not in deltas)")
	}
}

func TestBuildParamsConsentReasonFiresOncePerScale(t *testing.T) {
	// Two require_confirmation guards in the same delta set — the reason
	// is composed once per env (not once per spec). The flag must be
	// true.
	specs := []Spec{
		{Param: "DesiredCount", Kind: KindInteger,
			EnvGuards: map[string]EnvGuard{"prd": {RequireConfirmation: true}}},
		{Param: "TaskCpu", Kind: KindEnum, Values: []string{"256", "512"},
			EnvGuards: map[string]EnvGuard{"prd": {RequireConfirmation: true}}},
	}
	res, err := BuildParams(nil,
		map[string]string{"DesiredCount": "1", "TaskCpu": "512"}, "prd", specs)
	if err != nil {
		t.Fatalf("BuildParams err = %v", err)
	}
	if !res.RequireConsent || res.ConsentReason != "scale on prd env" {
		t.Errorf("RequireConsent=%v reason=%q, want true / 'scale on prd env'",
			res.RequireConsent, res.ConsentReason)
	}
}

func TestBuildParamsUnknownDeltaParamRejected(t *testing.T) {
	specs := []Spec{{Param: "DesiredCount", Kind: KindInteger}}
	_, err := BuildParams(nil, map[string]string{"NotScalable": "x"}, "dev", specs)
	if err == nil {
		t.Fatal("BuildParams(unknown param) err = nil, want error")
	}
	if !strings.Contains(err.Error(), "NotScalable") {
		t.Errorf("err = %v, want it to name the offending param", err)
	}
}

func TestBuildParamsEmptySpecParamRejected(t *testing.T) {
	specs := []Spec{{Kind: KindInteger}}
	if _, err := BuildParams(nil, nil, "dev", specs); err == nil {
		t.Fatal("BuildParams(empty param spec) err = nil, want error")
	}
}

func TestBuildParamsClampOrderingIsDeterministic(t *testing.T) {
	// Two parameters that both clamp; Clamps slice must be sorted by
	// param name so callers and tests see deterministic output. Map
	// iteration in Go is randomised; BuildParams sorts before iterating.
	specs := []Spec{
		{Param: "A", Kind: KindInteger, Max: IntPtr(1)},
		{Param: "B", Kind: KindInteger, Max: IntPtr(1)},
	}
	for i := 0; i < 5; i++ {
		res, err := BuildParams(nil, map[string]string{"A": "9", "B": "9"}, "dev", specs)
		if err != nil {
			t.Fatalf("iter %d: err = %v", i, err)
		}
		if len(res.Clamps) != 2 {
			t.Fatalf("iter %d: Clamps len = %d, want 2", i, len(res.Clamps))
		}
		if res.Clamps[0].Param != "A" || res.Clamps[1].Param != "B" {
			t.Errorf("iter %d: Clamps order = [%s, %s], want [A, B]",
				i, res.Clamps[0].Param, res.Clamps[1].Param)
		}
	}
}
