package screens

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/internal/record"
)

// TestRecordScreenActionsEmitMsg verifies u/s/d on a record screen backed by a
// real record emit the matching RecordActionMsg the root model routes.
func TestRecordScreenActionsEmitMsg(t *testing.T) {
	store := record.NewStore(t.TempDir())
	rec := &record.StackRecord{
		StackName: "alb-dev", Project: "demo", Env: "dev",
		Manifest: record.ManifestRef{Source: "alb.yaml"},
	}
	if err := store.Write(rec); err != nil {
		t.Fatal(err)
	}
	s := NewRecord("demo", "dev", "alb-dev", store)
	if s.rec == nil {
		t.Fatal("setup: record not loaded")
	}

	cases := []struct {
		key  rune
		want RecordAction
	}{
		{'u', RecordActionUpdate},
		{'s', RecordActionScale},
		{'d', RecordActionDelete},
	}
	for _, c := range cases {
		_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{c.key}})
		if cmd == nil {
			t.Fatalf("key %q produced a nil cmd", string(c.key))
		}
		msg, ok := cmd().(RecordActionMsg)
		if !ok {
			t.Fatalf("key %q produced %T, want RecordActionMsg", string(c.key), cmd())
		}
		if msg.Action != c.want {
			t.Errorf("key %q: action = %v, want %v", string(c.key), msg.Action, c.want)
		}
		if msg.Stack != "alb-dev" {
			t.Errorf("key %q: stack = %q, want alb-dev", string(c.key), msg.Stack)
		}
	}
}

// TestRecordScreenNoActionsWithoutRecord verifies the actions are inert for a
// not-yet-deployed stack (no record) while Esc still pops.
func TestRecordScreenNoActionsWithoutRecord(t *testing.T) {
	s := NewRecord("demo", "dev", "missing", nil) // nil store → rec stays nil
	if s.rec != nil {
		t.Fatal("expected no record for a nil store")
	}
	if _, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}}); cmd != nil {
		t.Error("'u' with no record produced a cmd; want nil (action inert)")
	}
	if _, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEsc}); cmd == nil {
		t.Error("esc produced a nil cmd; want a PopMsg command")
	}
}
