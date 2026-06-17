package delete

import (
	"errors"
	"testing"
)

func TestBatch_Validate_RejectsMissingConfirm(t *testing.T) {
	t.Parallel()
	rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}}}
	cases := []string{"", "delete", " DELETE", "DELETE ", "DELETEX"}
	for _, c := range cases {
		b := Batch{TypedConfirm: c, Decisions: []RowDecision{{RowID: "r1", Selected: true}}}
		if err := b.Validate(rows, nil); !errors.Is(err, ErrNotConfirmed) {
			t.Errorf("Validate(confirm=%q) = %v, want ErrNotConfirmed", c, err)
		}
	}
}

func TestBatch_Validate_RejectsSelectedBlockedRow(t *testing.T) {
	t.Parallel()
	rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-1"}}}
	deps := []RowDependencies{{RowID: "r1", Blocked: true}}
	b := Batch{
		TypedConfirm: ConfirmWord,
		Decisions:    []RowDecision{{RowID: "r1", Selected: true}},
	}
	if err := b.Validate(rows, deps); !errors.Is(err, ErrSelectedBlockedRow) {
		t.Errorf("Validate = %v, want ErrSelectedBlockedRow", err)
	}
}

func TestBatch_Validate_AllowsBlockedRowWhenNotSelected(t *testing.T) {
	t.Parallel()
	rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-1"}}}
	deps := []RowDependencies{{RowID: "r1", Blocked: true}}
	b := Batch{
		TypedConfirm: ConfirmWord,
		Decisions:    []RowDecision{{RowID: "r1", Selected: false}},
	}
	if err := b.Validate(rows, deps); err != nil {
		t.Errorf("Validate = %v, want nil (unselected blocked row should be fine)", err)
	}
}

func TestBatch_Validate_RejectsUnknownRowID(t *testing.T) {
	t.Parallel()
	rows := []Row{{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}}}
	b := Batch{
		TypedConfirm: ConfirmWord,
		Decisions:    []RowDecision{{RowID: "ghost", Selected: false}},
	}
	if err := b.Validate(rows, nil); !errors.Is(err, ErrUnknownRowDecision) {
		t.Errorf("Validate(ghost) = %v, want ErrUnknownRowDecision", err)
	}
}

func TestBatch_SelectedRows_TreatsAbsentAsUnchecked(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}},
		{ID: "r2", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-2"}},
	}
	b := Batch{Decisions: []RowDecision{{RowID: "r1", Selected: true}}} // r2 missing
	sel := b.SelectedRows(rows)
	if len(sel) != 1 || sel[0].ID != "r1" {
		t.Errorf("SelectedRows = %v, want [r1] (r2 missing decision should default unchecked)", sel)
	}
}

func TestBatch_Hash_StableAcrossDecisionOrder(t *testing.T) {
	t.Parallel()
	a := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "alpha", Selected: true},
			{RowID: "beta", Selected: false},
		},
	}
	b := Batch{
		TypedConfirm: ConfirmWord,
		Decisions: []RowDecision{
			{RowID: "beta", Selected: false},
			{RowID: "alpha", Selected: true},
		},
	}
	if a.Hash() != b.Hash() {
		t.Errorf("Hash should be stable across decision order: %s vs %s", a.Hash(), b.Hash())
	}
}

func TestBatch_Hash_DiffersOnSelectionChange(t *testing.T) {
	t.Parallel()
	a := Batch{TypedConfirm: ConfirmWord, Decisions: []RowDecision{{RowID: "r1", Selected: true}}}
	b := Batch{TypedConfirm: ConfirmWord, Decisions: []RowDecision{{RowID: "r1", Selected: false}}}
	if a.Hash() == b.Hash() {
		t.Errorf("Hash should differ when selection changes (got %s)", a.Hash())
	}
}
