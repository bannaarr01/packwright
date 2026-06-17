package delete

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ConfirmWord is the literal string the user must type into the
// batch consent modal before any Delete* call may fire. ADR-0043
// makes this the data-layer gate — every executor entry point
// rejects a Batch whose TypedConfirm is anything other than this
// constant.
const ConfirmWord = "DELETE"

// RowDecision is the user's choice for a single tray row in the
// consent modal. Selected defaults to false (unchecked) per
// ADR-0043: the modal must not pre-tick boxes.
type RowDecision struct {
	// RowID is the tray row's stable ID.
	RowID string `json:"row_id"`
	// Selected is true when the user explicitly checked the row's
	// box. A blocked row (RowDependencies.Blocked) must never have
	// Selected==true — Batch.Validate enforces this.
	Selected bool `json:"selected"`
}

// Batch is the structured contract returned by the consent modal.
// Validate enforces every property ADR-0043 requires before the
// Executor runs: typed-DELETE confirmation, per-row IDs from the
// tray, and no selection of a blocked row.
type Batch struct {
	// TypedConfirm is the verbatim string the user typed into the
	// "Type the word DELETE" field. The Executor refuses to proceed
	// unless this equals ConfirmWord exactly.
	TypedConfirm string `json:"typed_confirm"`
	// Decisions is one entry per tray row at consent time. The
	// modal MUST surface every row from the tray so the user
	// makes an explicit choice for each — even if the choice is
	// "leave it unchecked". The Executor matches Decisions against
	// the rows it iterates.
	Decisions []RowDecision `json:"decisions"`
}

// Errors returned by Batch.Validate and the executor preflight.
var (
	// ErrNotConfirmed means the user did not type ConfirmWord
	// (case-sensitive, no leading/trailing whitespace) into the
	// modal's confirmation field. The data-layer gate refuses to
	// invoke any AWS Delete* call until this passes.
	ErrNotConfirmed = errors.New("delete: typed-DELETE confirmation missing or incorrect")
	// ErrSelectedBlockedRow means at least one Decision is Selected
	// for a row whose RowDependencies report Blocked. The UI must
	// disable selection for blocked rows; the data layer enforces
	// the invariant.
	ErrSelectedBlockedRow = errors.New("delete: a blocked row was selected for deletion")
	// ErrUnknownRowDecision means the Batch contains a RowID that
	// does not correspond to any row in the tray.
	ErrUnknownRowDecision = errors.New("delete: batch contains a decision for an unknown row id")
)

// Validate enforces every consent-time invariant. It is called by
// the Executor before any AWS Delete* dispatch and may also be used
// by the modal to enable/disable the "Execute selected" button
// without committing.
//
// rows is the snapshot of the tray at modal time. deps is the
// dependency probe output for the same rows (positional alignment
// not required — both are looked up by ID). Either may be empty.
func (b Batch) Validate(rows []Row, deps []RowDependencies) error {
	if b.TypedConfirm != ConfirmWord {
		return ErrNotConfirmed
	}
	byID := make(map[string]Row, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	blocked := make(map[string]bool, len(deps))
	for _, d := range deps {
		blocked[d.RowID] = d.Blocked
	}
	for _, dec := range b.Decisions {
		if _, ok := byID[dec.RowID]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownRowDecision, dec.RowID)
		}
		if dec.Selected && blocked[dec.RowID] {
			return fmt.Errorf("%w: %s", ErrSelectedBlockedRow, dec.RowID)
		}
	}
	return nil
}

// SelectedRows returns the rows from tray whose Decision is
// Selected, in tray order. Rows not mentioned in b.Decisions are
// treated as unselected — ADR-0043's "default unchecked" rule
// applied to whatever the consent modal omitted.
func (b Batch) SelectedRows(tray []Row) []Row {
	selected := make(map[string]bool, len(b.Decisions))
	for _, dec := range b.Decisions {
		if dec.Selected {
			selected[dec.RowID] = true
		}
	}
	out := make([]Row, 0, len(selected))
	for _, r := range tray {
		if selected[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// Hash returns a stable sha256 digest of the consent contents. The
// audit log records this so a later audit can pin a Delete* line
// to the exact modal state the user accepted. Decisions are sorted
// by RowID before hashing so unrelated order differences in the
// caller's slice do not perturb the hash.
func (b Batch) Hash() string {
	type entry struct {
		ID       string `json:"id"`
		Selected bool   `json:"sel"`
	}
	es := make([]entry, len(b.Decisions))
	for i, d := range b.Decisions {
		es[i] = entry{ID: d.RowID, Selected: d.Selected}
	}
	sort.Slice(es, func(i, j int) bool { return es[i].ID < es[j].ID })
	payload, _ := json.Marshal(struct {
		Confirm   string  `json:"confirm"`
		Decisions []entry `json:"decisions"`
	}{
		Confirm:   b.TypedConfirm,
		Decisions: es,
	})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
