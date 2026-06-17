package pricing

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestLoadEmbedded_AllShippedFilesValid(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	keys := tbl.Keys()
	if len(keys) == 0 {
		t.Fatal("expected at least one shipped pricing entry")
	}
	// Spot-check a few well-known entries to catch typos / wrong fields.
	cases := []struct {
		provider, model string
		wantIn, wantOut float64
	}{
		{"anthropic", "claude-sonnet-4-6", 0.003, 0.015},
		{"openai", "gpt-4o", 0.0025, 0.01},
		{"ollama", "local", 0, 0},
	}
	for _, c := range cases {
		p, err := tbl.Lookup(c.provider, c.model)
		if err != nil {
			t.Errorf("Lookup(%s/%s): %v", c.provider, c.model, err)
			continue
		}
		if p.InputPer1K != c.wantIn || p.OutputPer1K != c.wantOut {
			t.Errorf("Lookup(%s/%s) = in:%v out:%v, want in:%v out:%v",
				c.provider, c.model, p.InputPer1K, p.OutputPer1K, c.wantIn, c.wantOut)
		}
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if _, err := tbl.Lookup("Anthropic", "Claude-Sonnet-4-6"); err != nil {
		t.Errorf("case-insensitive lookup failed: %v", err)
	}
}

func TestLookupUnknownReturnsSentinel(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	_, err = tbl.Lookup("acme", "bogus")
	if !errors.Is(err, ErrUnknownModel) {
		t.Errorf("expected ErrUnknownModel, got %v", err)
	}
}

func TestEstimateUSD(t *testing.T) {
	t.Parallel()
	p := Pricing{InputPer1K: 0.003, OutputPer1K: 0.015}
	got := p.EstimateUSD(10000, 1000)
	// 10000/1000 * 0.003 + 1000/1000 * 0.015 = 0.030 + 0.015 = 0.045
	if math.Abs(got-0.045) > 1e-9 {
		t.Errorf("EstimateUSD = %v, want 0.045", got)
	}
}

func TestEstimateUSDNegativeTokensTreatedAsZero(t *testing.T) {
	t.Parallel()
	p := Pricing{InputPer1K: 0.003, OutputPer1K: 0.015}
	if got := p.EstimateUSD(-100, -100); got != 0 {
		t.Errorf("EstimateUSD with negatives = %v, want 0", got)
	}
}

func TestOverrideReplacesEntry(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	custom := Pricing{
		Provider:    "anthropic",
		Model:       "claude-sonnet-4-6",
		InputPer1K:  0.0015, // enterprise rate
		OutputPer1K: 0.0075,
		Currency:    "USD",
	}
	if err := tbl.Override(custom); err != nil {
		t.Fatalf("Override: %v", err)
	}
	got, err := tbl.Lookup("anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("Lookup after override: %v", err)
	}
	if got.InputPer1K != 0.0015 || got.OutputPer1K != 0.0075 {
		t.Errorf("override not applied: %+v", got)
	}
}

func TestOverrideAddsNewModel(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if err := tbl.Override(Pricing{
		Provider:    "anthropic",
		Model:       "claude-sonnet-5-0",
		InputPer1K:  0.004,
		OutputPer1K: 0.02,
	}); err != nil {
		t.Fatalf("Override: %v", err)
	}
	if _, err := tbl.Lookup("anthropic", "claude-sonnet-5-0"); err != nil {
		t.Errorf("Lookup new model: %v", err)
	}
}

func TestOverrideRejectsNegativePrice(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	err = tbl.Override(Pricing{
		Provider:    "anthropic",
		Model:       "claude-sonnet-4-6",
		InputPer1K:  -0.01,
		OutputPer1K: 0.015,
	})
	if err == nil {
		t.Fatal("expected schema error on negative input_per_1k")
	}
	if !strings.Contains(err.Error(), "input_per_1k") {
		t.Errorf("error should mention the offending field, got: %v", err)
	}
}

func TestOverrideRejectsMissingFields(t *testing.T) {
	t.Parallel()
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	// Empty Provider -> minLength violation surfaces as a schema error.
	err = tbl.Override(Pricing{Model: "x", InputPer1K: 0.001, OutputPer1K: 0.002})
	if err == nil {
		t.Fatal("expected schema error on empty provider")
	}
}

func TestSchemaRejectsAdditionalProperties(t *testing.T) {
	t.Parallel()
	rawSchema := []byte(`{
        "type": "object",
        "required": ["x"],
        "additionalProperties": false,
        "properties": {
            "x": {"type": "number", "minimum": 0}
        }
    }`)
	s, err := parseSchema(rawSchema)
	if err != nil {
		t.Fatalf("parseSchema: %v", err)
	}
	bad := map[string]any{"x": 1.0, "y": "extra"}
	if err := s.validate(bad); err == nil {
		t.Fatal("expected error for unexpected field")
	}
}

func TestSchemaRequiresKeys(t *testing.T) {
	t.Parallel()
	s, err := parseSchema([]byte(`{
        "type": "object",
        "required": ["a", "b"],
        "properties": {
            "a": {"type": "string"},
            "b": {"type": "number"}
        }
    }`))
	if err != nil {
		t.Fatalf("parseSchema: %v", err)
	}
	if err := s.validate(map[string]any{"a": "x"}); err == nil {
		t.Fatal("expected missing required field error")
	}
}
