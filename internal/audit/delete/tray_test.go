package delete

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resourceVolume(id string) Resource {
	return Resource{Kind: KindEC2Volume, Identifier: id, Region: "ap-northeast-1"}
}

func mustOpenTray(t *testing.T) (*Tray, string) {
	t.Helper()
	home := t.TempDir()
	tray, err := OpenTray(home)
	if err != nil {
		t.Fatalf("OpenTray: %v", err)
	}
	return tray, home
}

func TestTray_AddPersistsAcrossOpen(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	tray, err := OpenTray(home)
	if err != nil {
		t.Fatalf("OpenTray: %v", err)
	}
	row, err := tray.Add(resourceVolume("vol-1"), "free 10GB")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if row.ID == "" {
		t.Fatal("Add returned empty row ID")
	}

	again, err := OpenTray(home)
	if err != nil {
		t.Fatalf("OpenTray (reopen): %v", err)
	}
	rows := again.List()
	if len(rows) != 1 {
		t.Fatalf("reopened tray has %d rows, want 1", len(rows))
	}
	if rows[0].ID != row.ID {
		t.Errorf("reopened row ID = %q, want %q", rows[0].ID, row.ID)
	}
	if rows[0].Note != "free 10GB" {
		t.Errorf("note = %q, want %q", rows[0].Note, "free 10GB")
	}
}

func TestTray_AddDeduplicatesOnKindIdentifier(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	first, err := tray.Add(resourceVolume("vol-1"), "first note")
	if err != nil {
		t.Fatal(err)
	}
	second, err := tray.Add(resourceVolume("vol-1"), "second note")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("dedup failed: IDs differ (%s vs %s)", first.ID, second.ID)
	}
	if tray.Len() != 1 {
		t.Errorf("tray Len = %d, want 1", tray.Len())
	}
	got, err := tray.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "second note" {
		t.Errorf("note after re-add = %q, want %q", got.Note, "second note")
	}
}

func TestTray_RemoveByID(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	r1, _ := tray.Add(resourceVolume("vol-1"), "")
	r2, _ := tray.Add(resourceVolume("vol-2"), "")
	if err := tray.Remove(r1.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	rows := tray.List()
	if len(rows) != 1 || rows[0].ID != r2.ID {
		t.Errorf("after remove, rows = %+v", rows)
	}
}

func TestTray_RemoveMissingReturnsErr(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	if err := tray.Remove("not-a-real-id"); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("Remove unknown id = %v, want ErrRowNotFound", err)
	}
}

func TestTray_Clear(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	if _, err := tray.Add(resourceVolume("vol-1"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tray.Add(resourceVolume("vol-2"), ""); err != nil {
		t.Fatal(err)
	}
	if err := tray.Clear(); err != nil {
		t.Fatal(err)
	}
	if tray.Len() != 0 {
		t.Errorf("after Clear, Len = %d, want 0", tray.Len())
	}
}

func TestTray_RejectsUnknownKind(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	_, err := tray.Add(Resource{Kind: "s3/bucket", Identifier: "my-bucket"}, "")
	if err == nil {
		t.Fatal("Add(s3/bucket) succeeded, want error")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("error = %q, want it to mention 'unknown kind'", err.Error())
	}
}

func TestTray_RejectsEmptyIdentifier(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	_, err := tray.Add(Resource{Kind: KindEC2Volume, Identifier: ""}, "")
	if err == nil {
		t.Fatal("Add with empty identifier succeeded, want error")
	}
}

func TestTray_RejectsECRImageMissingRepo(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	_, err := tray.Add(Resource{
		Kind:       KindECRImage,
		Identifier: "sha256:abc",
	}, "")
	if err == nil {
		t.Fatal("Add ecr image without repo succeeded, want error")
	}
	if !strings.Contains(err.Error(), "repository_name") {
		t.Errorf("error = %q, want it to mention 'repository_name'", err.Error())
	}
}

func TestTray_AtomicWriteLeavesNoTempFile(t *testing.T) {
	t.Parallel()
	tray, home := mustOpenTray(t)
	if _, err := tray.Add(resourceVolume("vol-1"), ""); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, Subdir, TrayFilename)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected tray file at %q: %v", p, err)
	}
	if _, err := os.Stat(p + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected .tmp file left behind: %v", err)
	}
}

func TestTray_OpenRefusesNewerVersion(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, Subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, TrayFilename)
	if err := os.WriteFile(path, []byte(`{"version":999,"rows":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTray(home); err == nil {
		t.Fatal("OpenTray accepted future version, want error")
	}
}

func TestTray_OpenEmptyDirReturnsEmptyTray(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	if tray.Len() != 0 {
		t.Errorf("fresh tray Len = %d, want 0", tray.Len())
	}
}

func TestTray_SetNote(t *testing.T) {
	t.Parallel()
	tray, _ := mustOpenTray(t)
	row, _ := tray.Add(resourceVolume("vol-1"), "old")
	if err := tray.SetNote(row.ID, "new"); err != nil {
		t.Fatal(err)
	}
	got, _ := tray.Get(row.ID)
	if got.Note != "new" {
		t.Errorf("note = %q, want %q", got.Note, "new")
	}
}
