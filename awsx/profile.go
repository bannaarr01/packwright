package awsx

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Profile is one AWS profile discovered from the shared config or credentials
// files. Region is best-effort: it is the value of `region = ...` inside the
// profile's section, or "" if the section did not declare one (in which case
// the SDK falls back to AWS_REGION / AWS_DEFAULT_REGION).
type Profile struct {
	Name   string
	Region string
	Source ProfileSource
}

// ProfileSource is a bitmask tracking which file(s) declared the profile. A
// profile present in both config and credentials has the union (SourceConfig|
// SourceCredentials); the UI uses it to flag profiles that exist only in one
// file (a frequent source of "why isn't my profile usable?" confusion).
type ProfileSource uint8

const (
	// SourceConfig means the profile was found in ~/.aws/config.
	SourceConfig ProfileSource = 1 << iota
	// SourceCredentials means the profile was found in ~/.aws/credentials.
	SourceCredentials
)

// ListProfiles enumerates AWS profiles from the conventional locations
// (~/.aws/config and ~/.aws/credentials) and returns the union, sorted by
// name. No AWS API calls are made — this is purely a filesystem read.
//
// Missing files are not an error; a fresh machine returns an empty slice and
// the UI can show a first-run hint.
func ListProfiles() ([]Profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("awsx: resolving home dir: %w", err)
	}
	return listProfilesIn(filepath.Join(home, ".aws"))
}

// listProfilesIn is the test seam: it takes the .aws directory explicitly so
// tests can drop fixtures into a t.TempDir() without setting HOME.
func listProfilesIn(awsDir string) ([]Profile, error) {
	byName := map[string]Profile{}
	if err := parseAWSConfig(filepath.Join(awsDir, "config"), byName); err != nil {
		return nil, err
	}
	if err := parseAWSCredentials(filepath.Join(awsDir, "credentials"), byName); err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parseAWSConfig handles ~/.aws/config syntax, where every profile section
// except the default one is prefixed with `profile `. Other section kinds
// (sso-session, services) are skipped — they describe shared resources, not
// profiles the SDK will accept via WithSharedConfigProfile.
func parseAWSConfig(path string, dst map[string]Profile) error {
	return parseSections(path, dst, SourceConfig, func(section string) string {
		if section == "default" {
			return "default"
		}
		if strings.HasPrefix(section, "profile ") {
			return strings.TrimSpace(strings.TrimPrefix(section, "profile "))
		}
		return ""
	})
}

// parseAWSCredentials handles ~/.aws/credentials, where every section header
// is the bare profile name with no prefix.
func parseAWSCredentials(path string, dst map[string]Profile) error {
	return parseSections(path, dst, SourceCredentials, func(section string) string { return section })
}

// parseSections is a small INI walker shared by config and credentials. It
// only extracts the section name and (within a profile) the `region` key —
// no other settings inform the switcher UI today.
func parseSections(path string, dst map[string]Profile, src ProfileSource, sectionName func(string) string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("awsx: reading %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var current string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = sectionName(strings.TrimSpace(line[1 : len(line)-1]))
			if current == "" {
				continue
			}
			p := dst[current]
			p.Name = current
			p.Source |= src
			dst[current] = p
			continue
		}
		if current == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "region" && val != "" {
			p := dst[current]
			if p.Region == "" {
				p.Region = val
				dst[current] = p
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("awsx: scanning %s: %w", path, err)
	}
	return nil
}
