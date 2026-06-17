package delete

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileLog_AppendsAcrossOpens(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	l1, err := OpenLog(home)
	if err != nil {
		t.Fatalf("OpenLog 1: %v", err)
	}
	if err := l1.Write(LogEntry{
		RowID: "r1", Kind: "ec2/volume", Identifier: "vol-1",
		Outcome: OutcomeDeleted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := l1.Close(); err != nil {
		t.Fatal(err)
	}

	l2, err := OpenLog(home)
	if err != nil {
		t.Fatalf("OpenLog 2: %v", err)
	}
	if err := l2.Write(LogEntry{
		RowID: "r2", Kind: "ec2/snapshot", Identifier: "snap-1",
		Outcome: OutcomeFailed, Reason: "InvalidSnapshot.InUse",
	}); err != nil {
		t.Fatal(err)
	}
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, Subdir, LogFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (data=%q)", len(lines), data)
	}

	var rec map[string]any
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("decode line 0: %v", err)
	}
	if rec["row_id"] != "r1" || rec["outcome"] != "deleted" {
		t.Errorf("line 0 = %v", rec)
	}
}

func TestFileLog_TimeIsAutoFilled(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	l, err := OpenLog(home)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(LogEntry{RowID: "r", Kind: "ec2/volume", Identifier: "v", Outcome: OutcomeDeleted}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, Subdir, LogFilename))
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["time"] == "" || rec["time"] == nil {
		t.Errorf("time field empty: %v", rec)
	}
}

func TestWriterLog_RecordsToBuffer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	wl := &WriterLog{W: &buf}
	if err := wl.Write(LogEntry{RowID: "r", Kind: "ec2/volume", Identifier: "vol-1", Outcome: OutcomeDeleted}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"row_id":"r"`)) {
		t.Errorf("buffer missing row_id field: %s", buf.String())
	}
}
