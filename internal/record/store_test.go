package record

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// freshRecord returns a syntactically-valid v1 record suitable for round-trip
// testing. Times are fixed so JSON byte equality is meaningful.
func freshRecord() *StackRecord {
	t := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	return &StackRecord{
		SchemaVersion: SchemaVersion,
		StackName:     "alb-dev-stack",
		Manifest:      ManifestRef{Slash: "/alb", Source: "packs/reference/manifests/alb.yaml"},
		Project:       "acme",
		Env:           "dev",
		Profile:       "acme-dev",
		Region:        "eu-west-1",
		Account:       "123456789012",
		Status: Status{
			CFN:          "CREATE_COMPLETE",
			Broad:        BroadDeployed,
			ReconciledAt: t,
		},
		DeployedAt:    t,
		LastUpdatedAt: t,
		Parameters:    Parameters{"VpcId": "vpc-1"},
		Outputs:       []Output{{Key: "DNS", Value: "alb-dev.example"}},
		Resources: []Resource{
			{LogicalID: "LB", PhysicalID: "arn:lb", Type: "AWS::ELBv2::LoadBalancer", Status: "CREATE_COMPLETE"},
		},
		History: []HistoryEntry{{At: t, Kind: KindCreate, Result: ResultSuccess}},
	}
}

func TestStore_PathLayout(t *testing.T) {
	s := NewStore("/root")
	if got, want := s.Path("acme", "dev", "alb-dev-stack"),
		filepath.Clean("/root/projects/acme/dev/stacks/alb-dev-stack.json"); got != want {
		t.Errorf("project path = %q, want %q", got, want)
	}
	if got, want := s.Path("", "", "shared-vpc"),
		filepath.Clean("/root/independent/stacks/shared-vpc.json"); got != want {
		t.Errorf("independent path = %q, want %q", got, want)
	}
}

func TestStore_WriteAndRead_RoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	want := freshRecord()
	if err := s.Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.Read("acme", "dev", "alb-dev-stack")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.StackName != want.StackName ||
		got.Status.CFN != want.Status.CFN ||
		got.Status.Broad != want.Status.Broad ||
		got.Resources[0].LogicalID != "LB" ||
		got.Outputs[0].Key != "DNS" ||
		got.Parameters["VpcId"] != "vpc-1" ||
		len(got.History) != 1 {
		t.Fatalf("round-trip mismatch: got %#v", got)
	}
}

func TestStore_Read_NotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Read("p", "e", "missing")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read on missing path: err = %v, want fs.ErrNotExist", err)
	}
}

func TestStore_Write_Atomic_NoTempLeftOver(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	if err := s.Write(freshRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := filepath.Join(root, "projects", "acme", "dev", "stacks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestStore_Write_ProducesValidJSON(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)
	if err := s.Write(freshRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(s.Path("acme", "dev", "alb-dev-stack"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// The file must be valid JSON; a re-Read parses it through encoding/json.
	rec, err := s.Read("acme", "dev", "alb-dev-stack")
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if rec.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %q, want %q", rec.SchemaVersion, SchemaVersion)
	}
	// And must end with a trailing newline.
	if data[len(data)-1] != '\n' {
		t.Errorf("expected trailing newline, last byte = %q", data[len(data)-1])
	}
}

func TestStore_List(t *testing.T) {
	s := NewStore(t.TempDir())
	r1 := freshRecord()
	r2 := freshRecord()
	r2.StackName = "rds-dev-stack"
	if err := s.Write(r1); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(r2); err != nil {
		t.Fatal(err)
	}
	got, err := s.List("acme", "dev")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
}

func TestStore_List_MissingDirIsEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.List("nope", "dev")
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List on missing dir = %d entries, want 0", len(got))
	}
}

func TestStore_Find_AcrossProjects(t *testing.T) {
	s := NewStore(t.TempDir())
	r1 := freshRecord()
	r1.Project, r1.Env, r1.StackName = "alpha", "dev", "alb-alpha"
	r2 := freshRecord()
	r2.Project, r2.Env, r2.StackName = "beta", "prod", "alb-beta"
	if err := s.Write(r1); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(r2); err != nil {
		t.Fatal(err)
	}
	got, err := s.Find("alb-beta")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Project != "beta" {
		t.Errorf("Find returned project %q, want beta", got.Project)
	}
}

func TestStore_Find_NotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Find("nope")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Find missing: err = %v, want fs.ErrNotExist", err)
	}
}

func TestStore_Read_RejectsUnknownSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "projects", "p", "e", "stacks", "x.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"packwright.stack-record.v999","stack_name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(root)
	if _, err := s.Read("p", "e", "x"); err == nil {
		t.Errorf("Read on unknown schema: err = nil, want unknown-schema error")
	}
}

func TestCapHistory_DropsOldest(t *testing.T) {
	hist := make([]HistoryEntry, 0, 60)
	for i := 0; i < 60; i++ {
		hist = append(hist, HistoryEntry{
			At:   time.Date(2026, 6, 18, 0, i, 0, 0, time.UTC),
			Kind: KindCreate,
		})
	}
	got := capHistory(hist, MaxHistoryEntries)
	if len(got) != MaxHistoryEntries {
		t.Fatalf("len = %d, want %d", len(got), MaxHistoryEntries)
	}
	// Oldest dropped, newest kept.
	if got[0].At.Minute() != 10 {
		t.Errorf("first kept entry minute = %d, want 10 (dropped 10 oldest)", got[0].At.Minute())
	}
	if got[len(got)-1].At.Minute() != 59 {
		t.Errorf("last kept entry minute = %d, want 59", got[len(got)-1].At.Minute())
	}
}
