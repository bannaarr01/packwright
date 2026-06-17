package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai"
	"github.com/bannaarr01/packwright/internal/ai/keys"
)

// aiCmd is the `packwright ai` subcommand tree (MVP-5). It is the CLI surface
// for configuring the opt-in AI assistant: the TUI/GUI `/ai` palette entry
// opens the chat panel once AI is enabled, but enabling it — picking a
// provider, storing the API key in the OS keychain, and flipping the gate — is
// a deliberate, scriptable step that lives here so it works headlessly too.
//
// The assistant is off by default (ADR-0033). `ai setup` is the only thing
// that turns it on; `ai status` reports the current posture; `ai disable`
// turns it back off without discarding the saved provider/model.
var aiCmd = &cobra.Command{
	Use:   "ai",
	Short: "Configure Packwright's opt-in AI assistant",
	Long: `Configure the AI assistant (off by default, bring-your-own-key).

  packwright ai setup     pick a provider/model, store the API key, enable AI
  packwright ai status    show whether AI is enabled and how the key resolves
  packwright ai disable    turn AI off (keeps your provider/model)

See ADR-0033–0039. The API key is stored in the OS keychain — never in
config.yaml — with an environment-variable fallback for headless systems.`,
}

// stdinPrompter is the foreground [ai.Prompter] the CLI setup flow drives. It
// renders a numbered menu for Select and a "label [default]:" line for Input,
// reading the user's reply from stdin. It is intentionally minimal — the
// richer interactive wizard belongs to the TUI/GUI — but it is enough to make
// `ai setup` fully usable on its own.
type stdinPrompter struct {
	r   *bufio.Reader
	out io.Writer
}

// Select prints a numbered menu and accepts either the 1-based index or the
// option's literal text. An empty submit returns "" so the caller can fall
// back to a current/default value.
func (p *stdinPrompter) Select(label string, options []string) (string, error) {
	fmt.Fprintf(p.out, "%s:\n", label)
	for i, o := range options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, o)
	}
	fmt.Fprint(p.out, "> ")
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return "", nil
	}
	if n, convErr := strconv.Atoi(s); convErr == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	for _, o := range options {
		if strings.EqualFold(o, s) {
			return o, nil
		}
	}
	return s, nil
}

// Input prints "label [default]: " and returns the trimmed reply, or "" when
// the user accepts the default by submitting an empty line.
func (p *stdinPrompter) Input(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Info writes a non-blocking message to the command's output.
func (p *stdinPrompter) Info(msg string) { fmt.Fprintln(p.out, msg) }

// envVarFor returns the environment variable a SaaS provider's key falls back
// to. Bedrock (AWS chain) and Ollama (local) use no key and return "".
func envVarFor(provider string) string {
	switch provider {
	case ai.ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case ai.ProviderOpenAI:
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}

// runAISetup composes the full enable flow: provider/model selection (the
// PR-01 wizard), API-key storage in the keychain, and the enabled-flag flip —
// validated against the safety invariants before it is persisted.
func runAISetup(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := &stdinPrompter{r: bufio.NewReader(cmd.InOrStdin()), out: out}

	// Step 1: provider + model (persists them; does not enable).
	if err := ai.Setup(ctx, cfg, p); err != nil {
		return err
	}
	settings := ai.LoadSettings(cfg)

	// Step 2: API key, for providers that need one. Stored in the OS keychain;
	// on a headless box where the keychain is unavailable, we point the user at
	// the env-var fallback rather than failing the whole setup.
	if env := envVarFor(settings.Provider); env != "" {
		key, err := p.Input(fmt.Sprintf("API key for %s (blank to use the %s env var)", settings.Provider, env), "")
		if err != nil {
			return err
		}
		if key != "" {
			if setErr := keys.Set(keys.Provider(settings.Provider), key); setErr != nil {
				fmt.Fprintf(out, "warning: could not store the key in the OS keychain (%v).\n"+
					"Set %s in your environment instead; it is read as a fallback.\n", setErr, env)
			} else {
				fmt.Fprintln(out, "API key stored in the OS keychain.")
			}
		} else {
			fmt.Fprintf(out, "No key entered; %s will be read from the environment at run time.\n", env)
		}
	}

	// Step 3: enable, but only if the resulting config is safe.
	if cfg.AI == nil {
		cfg.AI = map[string]any{}
	}
	cfg.AI["enabled"] = true
	if err := ai.Validate(cfg); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("ai: setup: save config: %w", err)
	}

	fmt.Fprintf(out, "\nAI enabled (provider %q, model %q). Open it with /ai in the TUI or GUI.\n",
		settings.Provider, settings.Model)
	return nil
}

// runAIStatus reports the current AI posture without mutating anything.
func runAIStatus(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	s := ai.LoadSettings(cfg)
	fmt.Fprintf(out, "enabled:  %v\n", s.Enabled)
	fmt.Fprintf(out, "provider: %s\n", emptyDash(s.Provider))
	fmt.Fprintf(out, "model:    %s\n", emptyDash(s.Model))

	if s.Provider != "" {
		key, src, kErr := keys.Get(keys.Provider(s.Provider))
		switch {
		case kErr == nil && key != "":
			fmt.Fprintf(out, "api key:  present (%s)\n", src)
		case errors.Is(kErr, keys.ErrNoKey):
			fmt.Fprintln(out, "api key:  not required for this provider")
		default:
			fmt.Fprintln(out, "api key:  not configured")
		}
	}
	if vErr := ai.Validate(cfg); vErr != nil {
		fmt.Fprintf(out, "config:   INVALID — %v\n", vErr)
	}
	return nil
}

// runAIDisable turns AI off while preserving the saved provider/model so a
// later `ai setup` (or a manual edit) can re-enable without re-entering them.
func runAIDisable(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AI == nil {
		cfg.AI = map[string]any{}
	}
	cfg.AI["enabled"] = false
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("ai: disable: save config: %w", err)
	}
	fmt.Fprintln(out, "AI disabled. No outbound LLM calls will be made.")
	return nil
}

// emptyDash renders an unset string as a dash for status output.
func emptyDash(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func init() {
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Pick a provider/model, store the API key, and enable AI",
		Args:  cobra.NoArgs,
		RunE:  runAISetup,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether AI is enabled and how its key resolves",
		Args:  cobra.NoArgs,
		RunE:  runAIStatus,
	}
	disable := &cobra.Command{
		Use:   "disable",
		Short: "Turn AI off (keeps your provider/model)",
		Args:  cobra.NoArgs,
		RunE:  runAIDisable,
	}
	aiCmd.AddCommand(setup, status, disable)
	registerSubcommand(aiCmd)
}
