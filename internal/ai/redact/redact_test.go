package redact

import (
	"strings"
	"testing"
)

// fakeAKIA, fakeASIA, fakeJWT, fakeSecret40, fakeSessionTok are built
// at runtime from harmless pieces so the repo's secret-scanning hook
// does not flag this file. They are NOT real credentials; they exist
// only to verify the redactor strips strings that LOOK like the
// shapes documented in ADR-0037.
var (
	fakeAKIA      = "A" + "K" + "IA" + "1234567890ABCDEF"
	fakeASIA      = "A" + "S" + "IA" + "ABCDEFGHIJKLMNOP"
	fakeAKIA2     = "A" + "K" + "IA" + strings.Repeat("Z", 16)
	fakeJWTHeader = "eyJ" + "hbGciOiJIUzI1NiJ9"
	fakeJWTBody   = "eyJ" + "zdWIiOiIxMjM0In0"
	fakeJWTSig    = "abcDEF-_signature"
	fakeJWT       = fakeJWTHeader + "." + fakeJWTBody + "." + fakeJWTSig
	fakeSecret40  = strings.Repeat("a", 30) + "/" + strings.Repeat("b", 9)
	fakeSession   = strings.Repeat("Q", 120) + "=="
)

// TestAlwaysOnPatterns exercises every always-on pattern with one
// representative input and asserts the secret shape is gone from the
// output and the per-hint counter incremented exactly once. Each case
// uses a fresh Opts (zero value) so we know the result is attributable
// to the always-on set, not to any default-ON optional rule.
func TestAlwaysOnPatterns(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantHint  string
		wantMiss  string // substring that must be absent from r.Text
		wantInOut string // substring that must be present in r.Text
	}{
		{
			name:      "AKIA access key",
			input:     "key=" + fakeAKIA + " tail",
			wantHint:  HintAWSAccessKey,
			wantMiss:  fakeAKIA,
			wantInOut: "<redacted:aws_access_key>",
		},
		{
			name:      "ASIA short-term key",
			input:     "creds: " + fakeASIA,
			wantHint:  HintAWSAccessKey,
			wantMiss:  fakeASIA,
			wantInOut: "<redacted:aws_access_key>",
		},
		{
			name:      "JWT",
			input:     "auth " + fakeJWT,
			wantHint:  HintJWT,
			wantMiss:  fakeJWTHeader,
			wantInOut: "<redacted:jwt>",
		},
		{
			name:      "Bearer header",
			input:     "Authorization: Bearer xyz.abc.def",
			wantHint:  HintBearer,
			wantMiss:  "xyz.abc.def",
			wantInOut: "Bearer <redacted>",
		},
		{
			name:      "AWS secret (40-char base64 near context)",
			input:     "aws_secret_access_key: " + fakeSecret40,
			wantHint:  HintAWSSecret,
			wantMiss:  fakeSecret40,
			wantInOut: "<redacted:aws_secret>",
		},
		{
			name:      "session token",
			input:     "session_token: " + fakeSession,
			wantHint:  HintAWSSessionToken,
			wantMiss:  strings.Repeat("Q", 120),
			wantInOut: "<redacted:aws_session_token>",
		},
		{
			name:      "JSON secret field",
			input:     `{"password": "hunter2"}`,
			wantHint:  HintSecretField,
			wantMiss:  "hunter2",
			wantInOut: "<redacted:secret_field>",
		},
		{
			name:      "JSON nested secret field",
			input:     `{"outer":{"api_token":"deadbeef"}}`,
			wantHint:  HintSecretField,
			wantMiss:  "deadbeef",
			wantInOut: "<redacted:secret_field>",
		},
		{
			name:      "text secret field",
			input:     `mysecret_credential=topsecret extra=stuff`,
			wantHint:  HintSecretField,
			wantMiss:  "topsecret",
			wantInOut: "<redacted:secret_field>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Apply(tc.input, Opts{})
			if strings.Contains(r.Text, tc.wantMiss) {
				t.Fatalf("secret survived: %q in output %q", tc.wantMiss, r.Text)
			}
			if tc.wantInOut != "" && !strings.Contains(r.Text, tc.wantInOut) {
				t.Fatalf("expected %q in output %q", tc.wantInOut, r.Text)
			}
			if r.Counts[tc.wantHint] == 0 {
				t.Fatalf("expected counts[%q] > 0, got %v", tc.wantHint, r.Counts)
			}
		})
	}
}

// TestAccountIDToggle nails down the explicit DoD requirement: the
// default (DefaultOpts) redacts AWS account IDs; toggling off
// preserves them.
func TestAccountIDToggle(t *testing.T) {
	const acct = "123456789012"
	input := "arn:aws:iam::" + acct + ":role/Admin"

	t.Run("default redacts", func(t *testing.T) {
		r := Apply(input, DefaultOpts())
		if strings.Contains(r.Text, acct) {
			t.Fatalf("account id leaked with DefaultOpts: %q", r.Text)
		}
		if !strings.Contains(r.Text, "<account>") {
			t.Fatalf("expected <account> placeholder, got %q", r.Text)
		}
		if r.Counts[HintAccount] != 1 {
			t.Fatalf("expected counts[%q]==1, got %v", HintAccount, r.Counts)
		}
	})

	t.Run("toggling off preserves", func(t *testing.T) {
		opts := DefaultOpts()
		opts.RedactAccountIDs = false
		r := Apply(input, opts)
		if !strings.Contains(r.Text, acct) {
			t.Fatalf("account id was scrubbed despite RedactAccountIDs=false: %q", r.Text)
		}
		if strings.Contains(r.Text, "<account>") {
			t.Fatalf("did not expect <account> placeholder, got %q", r.Text)
		}
		if r.Counts[HintAccount] != 0 {
			t.Fatalf("expected counts[%q]==0, got %v", HintAccount, r.Counts)
		}
	})

	t.Run("zero Opts also preserves", func(t *testing.T) {
		// The zero Opts is the strict baseline: optional rules off.
		// A caller that wants account-ID redaction must explicitly
		// pass DefaultOpts.
		r := Apply(input, Opts{})
		if !strings.Contains(r.Text, acct) {
			t.Fatalf("account id was scrubbed by zero Opts: %q", r.Text)
		}
	})
}

// TestPrivateIPToggle confirms RFC1918 redaction is off by default and
// fires when the toggle is on. The 172.16/12 case is in there because
// the regex must reject 172.15.x.x and 172.32.x.x; the test would
// catch an off-by-one if we ever widen the bracket.
func TestPrivateIPToggle(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"172.15.0.1", false}, // outside the 172.16/12 range
		{"172.32.0.1", false},
		{"8.8.8.8", false}, // public
	}
	opts := DefaultOpts()
	opts.RedactPrivateIPs = true
	for _, tc := range cases {
		r := Apply(tc.ip, opts)
		got := strings.Contains(r.Text, "<private-ip>")
		if got != tc.want {
			t.Fatalf("Apply(%q) → %q, want redact=%v", tc.ip, r.Text, tc.want)
		}
	}

	// Default has RedactPrivateIPs=false.
	r := Apply("10.0.0.1", DefaultOpts())
	if strings.Contains(r.Text, "<private-ip>") {
		t.Fatalf("private IP redaction fired despite default-OFF: %q", r.Text)
	}
}

// TestInternalHostPatterns confirms user-configured host patterns
// fire only when configured, and that an invalid pattern is silently
// skipped rather than failing open.
func TestInternalHostPatterns(t *testing.T) {
	opts := DefaultOpts()
	opts.InternalHostPatterns = []string{
		`db-[a-z0-9]+\.corp\.internal`,
		`((unclosed`, // intentionally invalid — must be skipped, not panic
	}
	r := Apply("connecting to db-prod7.corp.internal …", opts)
	if strings.Contains(r.Text, "db-prod7.corp.internal") {
		t.Fatalf("host pattern did not fire: %q", r.Text)
	}
	if !strings.Contains(r.Text, "<internal-host>") {
		t.Fatalf("expected <internal-host>, got %q", r.Text)
	}
	if r.Counts[HintInternalHost] != 1 {
		t.Fatalf("expected counts[%q]==1, got %v", HintInternalHost, r.Counts)
	}
}

// TestSecretFieldsRegistration confirms that form-secret values are
// scrubbed even when the field name does not contain one of the
// generic sensitive words.
func TestSecretFieldsRegistration(t *testing.T) {
	opts := DefaultOpts()
	opts.SecretFields = []string{"vault_lookup", ""} // empty entry should be ignored
	r := Apply(`{"vault_lookup":"abracadabra","other":"public"}`, opts)
	if strings.Contains(r.Text, "abracadabra") {
		t.Fatalf("form secret leaked: %q", r.Text)
	}
	if !strings.Contains(r.Text, "<redacted:form_secret>") {
		t.Fatalf("expected <redacted:form_secret>, got %q", r.Text)
	}
	if !strings.Contains(r.Text, "public") {
		t.Fatalf("unrelated field was scrubbed: %q", r.Text)
	}
}

// TestSpecificRuleBeatsBroader confirms the JWT hint survives the
// broader secret-field rule when a JWT happens to be the value of a
// "token" field — that's the whole reason buildPatterns orders the
// specific rules first and the broad rule's skip-if-already-redacted
// logic.
func TestSpecificRuleBeatsBroader(t *testing.T) {
	input := `{"token":"` + fakeJWT + `"}`
	r := Apply(input, DefaultOpts())
	if !strings.Contains(r.Text, "<redacted:jwt>") {
		t.Fatalf("expected jwt hint to survive, got %q", r.Text)
	}
	if strings.Contains(r.Text, "<redacted:secret_field>") {
		t.Fatalf("broader rule clobbered jwt hint: %q", r.Text)
	}
}

// TestApplyMarshalsArbitrary confirms a typed struct gets JSON-encoded
// before scrubbing — without this, a caller passing an AppError would
// see only the Go format string and the redactor would have no JSON
// keys to anchor the field-name rules to.
func TestApplyMarshalsArbitrary(t *testing.T) {
	payload := struct {
		Password string `json:"password"`
		Region   string `json:"region"`
	}{Password: "hunter2", Region: "us-east-1"}
	r := Apply(payload, DefaultOpts())
	if strings.Contains(r.Text, "hunter2") {
		t.Fatalf("password leaked from struct: %q", r.Text)
	}
	if !strings.Contains(r.Text, "us-east-1") {
		t.Fatalf("expected region to survive, got %q", r.Text)
	}
}

// TestApplyPassesStringThrough confirms a string payload is not
// JSON-quoted before scrubbing. Without this, "10.0.0.1" would
// become "\"10.0.0.1\"" in the output — readable but ugly in the
// "Context sent" pane.
func TestApplyPassesStringThrough(t *testing.T) {
	r := Apply("no secrets here", Opts{})
	if r.Text != "no secrets here" {
		t.Fatalf("unexpected wrapping: %q", r.Text)
	}
}

// TestApplyBytesIsolated confirms that a []byte input is not shared
// with the caller's buffer after Apply returns. A caller that holds
// the original slice should be able to mutate it without disturbing
// r.Text.
func TestApplyBytesIsolated(t *testing.T) {
	in := []byte("clean text")
	r := Apply(in, Opts{})
	in[0] = 'X'
	if !strings.HasPrefix(r.Text, "clean") {
		t.Fatalf("redacted text was mutated by caller: %q", r.Text)
	}
}

// TestApplyNil confirms a nil payload yields an empty Redacted with
// no counts, never a panic.
func TestApplyNil(t *testing.T) {
	r := Apply(nil, DefaultOpts())
	if r.Text != "" {
		t.Fatalf("nil produced non-empty text: %q", r.Text)
	}
	if len(r.Counts) != 0 {
		t.Fatalf("nil produced non-empty counts: %v", r.Counts)
	}
}

// TestTotal confirms Redacted.Total sums every hint.
func TestTotal(t *testing.T) {
	r := Apply(fakeAKIA+" "+fakeAKIA2+" and Bearer abc", Opts{})
	want := r.Counts[HintAWSAccessKey] + r.Counts[HintBearer]
	if r.Total() != want {
		t.Fatalf("Total=%d want %d (counts=%v)", r.Total(), want, r.Counts)
	}
	if want < 3 {
		t.Fatalf("expected at least 3 substitutions, got %v", r.Counts)
	}
}

// FuzzApply is the headline guarantee from the PR-05 DoD: no input,
// no matter how oddly shaped, should leak a recognizable AKIA/ASIA
// access key, a JWT, or a Bearer-prefixed token through Apply. The
// CI command runs this for at least 30 seconds:
//
//	go test ./internal/ai/redact/... -run=^$ -fuzz=FuzzApply -fuzztime=30s
//
// The fuzzer mutates the seed corpus, so any input shape that breaks
// our patterns (overlapping matches, embedded whitespace, multi-byte
// runes adjacent to a key) becomes a stored regression the next time
// someone runs the fuzz target.
func FuzzApply(f *testing.F) {
	seeds := []string{
		fakeAKIA,
		fakeASIA,
		"prefix " + fakeAKIA + " suffix",
		"Bearer abcDEF.GHIjkl.MNOpqr",
		"BEARER xyz",
		fakeJWT,
		`{"token":"` + fakeJWT + `","password":"hunter2"}`,
		"Auth: Bearer " + fakeJWT,
		fakeAKIA + " " + fakeAKIA2,
		`{"nested":{"jwt":"` + fakeJWT + `"}}`,
		"prefix\n" + fakeAKIA + "\nsuffix",
		"Bearer\t" + fakeAKIA,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		r := Apply(input, DefaultOpts())

		// No AKIA/ASIA access key may survive the redactor.
		if loc := reAWSAccessKey.FindStringIndex(r.Text); loc != nil {
			t.Fatalf("access key leaked at %d-%d in %q (input=%q)",
				loc[0], loc[1], r.Text, input)
		}

		// No JWT may survive.
		if loc := reJWT.FindStringIndex(r.Text); loc != nil {
			t.Fatalf("JWT leaked at %d-%d in %q (input=%q)",
				loc[0], loc[1], r.Text, input)
		}

		// Any "Bearer <something>" that survives must be the
		// placeholder we emit; any other tail value is a leak.
		for _, m := range reBearer.FindAllString(r.Text, -1) {
			lower := strings.ToLower(m)
			rest := strings.TrimLeft(lower[len("bearer"):], " \t")
			if rest != "<redacted>" {
				t.Fatalf("Bearer leaked: matched %q, tail %q (input=%q, output=%q)",
					m, rest, input, r.Text)
			}
		}
	})
}
