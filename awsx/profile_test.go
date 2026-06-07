package awsx

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a small test helper to drop a fixture file into a tempdir.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestListProfilesUnionsConfigAndCredentials(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config"), `
[default]
region = us-east-1

[profile prod]
region = eu-west-1
sso_start_url = https://example.awsapps.com/start

[sso-session corp]
sso_region = eu-west-1

[profile staging]
# no region here on purpose; region should remain empty
`)
	writeFile(t, filepath.Join(dir, "credentials"), `
[default]
aws_access_key_id = AKIA...
aws_secret_access_key = secret

[legacy]
aws_access_key_id = AKIA...
aws_secret_access_key = secret
`)

	got, err := listProfilesIn(dir)
	if err != nil {
		t.Fatalf("listProfilesIn: %v", err)
	}

	want := map[string]Profile{
		"default": {Name: "default", Region: "us-east-1", Source: SourceConfig | SourceCredentials},
		"prod":    {Name: "prod", Region: "eu-west-1", Source: SourceConfig},
		"staging": {Name: "staging", Source: SourceConfig},
		"legacy":  {Name: "legacy", Source: SourceCredentials},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d profiles, want %d: %+v", len(got), len(want), got)
	}
	for _, p := range got {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected profile %q", p.Name)
			continue
		}
		if p.Region != w.Region {
			t.Errorf("profile %q Region = %q, want %q", p.Name, p.Region, w.Region)
		}
		if p.Source != w.Source {
			t.Errorf("profile %q Source = %b, want %b", p.Name, p.Source, w.Source)
		}
	}
}

func TestListProfilesSkipsSSOSessionSections(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config"), `
[sso-session corp]
sso_region = eu-west-1

[services my-services]
ec2 =
  endpoint_url = https://example.com
`)
	got, err := listProfilesIn(dir)
	if err != nil {
		t.Fatalf("listProfilesIn: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no profiles from non-profile sections, got %+v", got)
	}
}

func TestListProfilesMissingDirYieldsEmpty(t *testing.T) {
	got, err := listProfilesIn(filepath.Join(t.TempDir(), "does", "not", "exist"))
	if err != nil {
		t.Fatalf("listProfilesIn on missing dir returned err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %+v", got)
	}
}

func TestListProfilesSortedByName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config"), `
[profile zeta]
[profile alpha]
[profile mu]
`)
	got, err := listProfilesIn(dir)
	if err != nil {
		t.Fatalf("listProfilesIn: %v", err)
	}
	want := []string{"alpha", "mu", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("got %d profiles, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("got[%d] = %q, want %q", i, got[i].Name, name)
		}
	}
}

func TestListProfilesCommentsAreSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config"), `
# leading comment
; semicolon comment

[profile only]
# region = should-be-ignored
region = ca-central-1
`)
	got, err := listProfilesIn(dir)
	if err != nil {
		t.Fatalf("listProfilesIn: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d profiles, want 1", len(got))
	}
	if got[0].Name != "only" || got[0].Region != "ca-central-1" {
		t.Errorf("got %+v, want only/ca-central-1", got[0])
	}
}
