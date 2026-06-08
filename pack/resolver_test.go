package pack

import (
	"reflect"
	"testing"

	"github.com/bannaarr01/packwright/manifest"
)

func TestParseQualifiedBareSlash(t *testing.T) {
	q, err := ParseQualified("/alb")
	if err != nil {
		t.Fatalf("ParseQualified(/alb) error = %v", err)
	}
	if q.Slash != "/alb" || q.Pack != "" {
		t.Errorf("ParseQualified(/alb) = %+v, want {/alb }", q)
	}
}

func TestParseQualifiedWithPack(t *testing.T) {
	q, err := ParseQualified("/alb@acme")
	if err != nil {
		t.Fatalf("ParseQualified(/alb@acme) error = %v", err)
	}
	if q.Slash != "/alb" || q.Pack != "acme" {
		t.Errorf("ParseQualified(/alb@acme) = %+v, want {/alb acme}", q)
	}
}

func TestParseQualifiedHyphenatedPackName(t *testing.T) {
	// ADR-0023 example uses hyphenated names like "acme-platform".
	q, err := ParseQualified("/alb@acme-platform")
	if err != nil {
		t.Fatalf("ParseQualified error = %v", err)
	}
	if q.Pack != "acme-platform" {
		t.Errorf("Pack = %q, want %q", q.Pack, "acme-platform")
	}
}

func TestParseQualifiedExtraAtIsPartOfPackName(t *testing.T) {
	// strings.Cut on the first '@' so unusual pack names with '@' survive
	// rather than being silently truncated.
	q, err := ParseQualified("/alb@a@b")
	if err != nil {
		t.Fatalf("ParseQualified error = %v", err)
	}
	if q.Pack != "a@b" {
		t.Errorf("Pack = %q, want %q", q.Pack, "a@b")
	}
}

func TestParseQualifiedErrors(t *testing.T) {
	cases := []string{
		"",       // empty
		"alb",    // missing leading slash
		"/",      // empty slash command
		"/@acme", // empty slash command before @
		"/alb@",  // empty pack after @
		"@acme",  // no slash at all
	}
	for _, in := range cases {
		if _, err := ParseQualified(in); err == nil {
			t.Errorf("ParseQualified(%q) succeeded, want error", in)
		}
	}
}

func TestQualifiedStringRoundTrip(t *testing.T) {
	cases := []string{"/alb", "/alb@acme", "/x/y", "/foo@user"}
	for _, in := range cases {
		q, err := ParseQualified(in)
		if err != nil {
			t.Fatalf("ParseQualified(%q) error = %v", in, err)
		}
		if got := q.String(); got != in {
			t.Errorf("round-trip %q -> %q", in, got)
		}
	}
}

func TestQualifiedStringZeroValue(t *testing.T) {
	if got := (Qualified{}).String(); got != "" {
		t.Errorf("Qualified{}.String() = %q, want empty", got)
	}
}

func TestResolveEmpty(t *testing.T) {
	if got := Resolve(nil, Qualified{Slash: "/alb"}, ""); got != nil {
		t.Errorf("Resolve(nil) = %v, want nil", got)
	}
	if got := Resolve(nil, Qualified{}, ""); got != nil {
		t.Errorf("Resolve(empty Qualified) = %v, want nil", got)
	}
}

func TestResolveMissReturnsNil(t *testing.T) {
	packs := []*Pack{{Name: "p", Manifests: []*manifest.Manifest{mkManifest("/foo", "F")}}}
	if got := Resolve(packs, Qualified{Slash: "/missing"}, ""); got != nil {
		t.Errorf("Resolve miss = %v, want nil", got)
	}
}

func TestResolveSingleUserScope(t *testing.T) {
	m := mkManifest("/alb", "user ALB")
	packs := []*Pack{{Name: UserScopeName, Manifests: []*manifest.Manifest{m}}}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "")
	want := []Resolution{{Manifest: m, Scope: ScopeUser, SourcePack: ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
}

func TestResolveSinglePack(t *testing.T) {
	m := mkManifest("/alb", "acme ALB")
	packs := []*Pack{{Name: "acme", Manifests: []*manifest.Manifest{m}}}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "")
	want := []Resolution{{Manifest: m, Scope: ScopePack, SourcePack: "acme"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
}

func TestResolveUserWinsOverPacksUnpinned(t *testing.T) {
	user := mkManifest("/alb", "user")
	acme := mkManifest("/alb", "acme")
	team := mkManifest("/alb", "team")

	packs := []*Pack{
		{Name: UserScopeName, Manifests: []*manifest.Manifest{user}},
		{Name: "acme", Manifests: []*manifest.Manifest{acme}},
		{Name: "team", Manifests: []*manifest.Manifest{team}},
	}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "")
	// User scope leads, then packs in reverse input order (team, then acme).
	wantOrder := []string{"user", "team", "acme"}
	gotOrder := titles(got)
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("Resolve titles = %v, want %v", gotOrder, wantOrder)
	}
}

func TestResolveMostRecentPackWinsTiebreak(t *testing.T) {
	// ADR-0023: with no pin and no user-scope entry, the most-recently-added
	// pack is the default.
	acme := mkManifest("/alb", "acme")
	team := mkManifest("/alb", "team")

	packs := []*Pack{
		{Name: "acme", Manifests: []*manifest.Manifest{acme}},
		{Name: "team", Manifests: []*manifest.Manifest{team}},
	}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "")
	if len(got) != 2 || got[0].Manifest != team || got[1].Manifest != acme {
		t.Fatalf("Resolve = %v, want [team, acme]", titles(got))
	}
}

func TestResolvePinPromotesPack(t *testing.T) {
	// DoD: a fixture with /alb in two packs resolves the pinned one first.
	acme := mkManifest("/alb", "acme")
	team := mkManifest("/alb", "team")

	packs := []*Pack{
		{Name: "acme", Manifests: []*manifest.Manifest{acme}},
		{Name: "team", Manifests: []*manifest.Manifest{team}},
	}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "pack:acme")
	if len(got) != 2 || got[0].Manifest != acme || got[1].Manifest != team {
		t.Fatalf("Resolve pinned = %v, want [acme, team]", titles(got))
	}
	if got[0].Scope != ScopePack || got[0].SourcePack != "acme" {
		t.Errorf("Resolve[0] = {%v %q}, want {ScopePack acme}", got[0].Scope, got[0].SourcePack)
	}
}

func TestResolvePinPromotesUserOverMoreRecentPack(t *testing.T) {
	// Without the pin the most-recent pack (team) would win; with "user" the
	// user scope leads instead.
	user := mkManifest("/alb", "user")
	team := mkManifest("/alb", "team")

	packs := []*Pack{
		{Name: UserScopeName, Manifests: []*manifest.Manifest{user}},
		{Name: "team", Manifests: []*manifest.Manifest{team}},
	}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "user")
	if len(got) != 2 || got[0].Manifest != user || got[1].Manifest != team {
		t.Fatalf("Resolve user-pinned = %v, want [user, team]", titles(got))
	}
}

func TestResolveStalePinFallsBackToNaturalOrder(t *testing.T) {
	// Pin points at a pack that has no matching manifest (e.g. the pack was
	// uninstalled). Resolve must not blow up and must return the natural
	// order so the palette still has a sensible default.
	acme := mkManifest("/alb", "acme")
	packs := []*Pack{{Name: "acme", Manifests: []*manifest.Manifest{acme}}}

	got := Resolve(packs, Qualified{Slash: "/alb"}, "pack:removed-pack")
	if len(got) != 1 || got[0].Manifest != acme {
		t.Errorf("Resolve stale pin = %v, want [acme]", titles(got))
	}
}

func TestResolveMalformedPinTreatedAsUnpinned(t *testing.T) {
	acme := mkManifest("/alb", "acme")
	team := mkManifest("/alb", "team")
	packs := []*Pack{
		{Name: "acme", Manifests: []*manifest.Manifest{acme}},
		{Name: "team", Manifests: []*manifest.Manifest{team}},
	}

	for _, bad := range []string{"random", "pack:", "pack", "user:", ":"} {
		got := Resolve(packs, Qualified{Slash: "/alb"}, bad)
		if len(got) != 2 || got[0].Manifest != team {
			t.Errorf("Resolve pin=%q = %v, want natural order [team, acme]", bad, titles(got))
		}
	}
}

func TestResolveQualifiedInvocationFiltersToSource(t *testing.T) {
	acme := mkManifest("/alb", "acme")
	team := mkManifest("/alb", "team")
	user := mkManifest("/alb", "user")
	packs := []*Pack{
		{Name: UserScopeName, Manifests: []*manifest.Manifest{user}},
		{Name: "acme", Manifests: []*manifest.Manifest{acme}},
		{Name: "team", Manifests: []*manifest.Manifest{team}},
	}

	// /alb@acme returns only acme — pin is irrelevant.
	got := Resolve(packs, Qualified{Slash: "/alb", Pack: "acme"}, "pack:team")
	if len(got) != 1 || got[0].Manifest != acme {
		t.Errorf("Resolve(/alb@acme) = %v, want [acme]", titles(got))
	}

	// /alb@user returns only the user scope entry.
	got = Resolve(packs, Qualified{Slash: "/alb", Pack: UserScopeName}, "pack:team")
	if len(got) != 1 || got[0].Manifest != user {
		t.Errorf("Resolve(/alb@user) = %v, want [user]", titles(got))
	}

	// Unknown source yields nil.
	if got := Resolve(packs, Qualified{Slash: "/alb", Pack: "nope"}, ""); got != nil {
		t.Errorf("Resolve(/alb@nope) = %v, want nil", got)
	}
}

func TestResolveSkipsNilPacksAndManifests(t *testing.T) {
	good := mkManifest("/alb", "good")
	packs := []*Pack{
		nil,
		{Name: "p", Manifests: []*manifest.Manifest{nil, good, nil}},
	}
	got := Resolve(packs, Qualified{Slash: "/alb"}, "")
	if len(got) != 1 || got[0].Manifest != good {
		t.Errorf("Resolve = %v, want [good]", titles(got))
	}
}

func titles(rs []Resolution) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Manifest.Title
	}
	return out
}
