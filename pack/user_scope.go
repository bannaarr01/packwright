package pack

import (
	"path/filepath"

	"github.com/bannaarr01/packwright/manifest"
)

// monitorsSubdir is the directory under homeDir that holds user-scoped
// dashboards. Parallels commandsSubdir (defined alongside Discover).
const monitorsSubdir = "monitors"

// loadUserScope walks <homeDir>/commands/*.yaml and <homeDir>/monitors/*.yaml
// and returns a synthetic *Pack named UserScopeName that contains every
// manifest found. Manifests are sorted lexically within each subdirectory and
// emitted commands-then-monitors so the result is deterministic across
// operating systems — the same guarantee Discover provides for pack scope.
//
// A missing commands or monitors subdirectory is not an error: fresh installs
// have an empty home (and the config package only materializes the directory
// tree on first call to config.Home). LoadUserScope, the exported entry
// point in discover.go, delegates here so the loader logic and its tests
// live next to the Scope plumbing in scope.go.
func loadUserScope(homeDir string) (*Pack, error) {
	var manifests []*manifest.Manifest
	for _, sub := range []string{commandsSubdir, monitorsSubdir} {
		loaded, err := loadManifests(filepath.Join(homeDir, sub))
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, loaded...)
	}
	return &Pack{
		Name:      UserScopeName,
		Dir:       filepath.Join(homeDir, commandsSubdir),
		Manifests: manifests,
	}, nil
}
