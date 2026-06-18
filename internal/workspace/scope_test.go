package workspace

import (
	"path/filepath"
	"testing"
)

func TestScopeOf(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Scope
	}{
		{
			name: "project manifest",
			path: filepath.Join("projects", "acme", "dev", "manifests", "foo.yaml"),
			want: Scope{Kind: ScopeProject, Project: "acme", Env: "dev"},
		},
		{
			name: "project draft",
			path: filepath.Join("projects", "acme", "dev", "drafts", "alb-copy-1.yaml"),
			want: Scope{Kind: ScopeDraft, Project: "acme", Env: "dev"},
		},
		{
			name: "pack manifest",
			path: filepath.Join("packs", "reference", "manifests", "alb.yaml"),
			want: Scope{Kind: ScopePack, Pack: "reference"},
		},
		{
			name: "user command",
			path: filepath.Join("commands", "deploy.yaml"),
			want: Scope{Kind: ScopeUser},
		},
		{
			name: "user monitor",
			path: filepath.Join("monitors", "uptime.yaml"),
			want: Scope{Kind: ScopeUser},
		},
		{
			name: "absolute path under home is supported",
			path: filepath.Join("/home", "alice", ".config", "packwright",
				"projects", "acme", "prd", "manifests", "alb.yaml"),
			want: Scope{Kind: ScopeProject, Project: "acme", Env: "prd"},
		},
		{
			name: "unknown root",
			path: filepath.Join("cache", "foo.json"),
			want: Scope{Kind: ScopeUnknown},
		},
		{
			name: "empty path",
			path: "",
			want: Scope{Kind: ScopeUnknown},
		},
		{
			name: "project path with invalid env slug",
			path: filepath.Join("projects", "acme", "Dev", "manifests", "foo.yaml"),
			want: Scope{Kind: ScopeUnknown},
		},
		{
			name: "project directory only (no manifest kind segment)",
			path: filepath.Join("projects", "acme", "dev"),
			want: Scope{Kind: ScopeUnknown},
		},
		{
			name: "stacks/ is not a manifest scope",
			path: filepath.Join("projects", "acme", "dev", "stacks", "alb.json"),
			want: Scope{Kind: ScopeUnknown},
		},
		{
			name: "packs root with no manifests subdir",
			path: filepath.Join("packs", "reference"),
			want: Scope{Kind: ScopeUnknown},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeOf(tc.path)
			if got != tc.want {
				t.Errorf("ScopeOf(%q) = %+v, want %+v", tc.path, got, tc.want)
			}
		})
	}
}

func TestScopeKindString(t *testing.T) {
	cases := map[ScopeKind]string{
		ScopeProject: "project",
		ScopePack:    "pack",
		ScopeUser:    "user",
		ScopeDraft:   "draft",
		ScopeUnknown: "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("ScopeKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}
