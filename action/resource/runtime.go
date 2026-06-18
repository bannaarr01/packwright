// Package resource is the runtime for a manifest's "resource" command kind:
// validate the user-supplied inputs, render the parameters.json that the
// underlying infrastructure template expects, drive the deploy script, and
// stream both the script's output and the upstream cloud-provider events into
// a single event channel that a TUI or GUI can consume.
//
// The engine is deliberately headless: it knows nothing about Bubble Tea,
// Wails, or any rendering surface. The TUI / GUI front-ends call Execute and
// consume the returned Result.
package resource

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/internal/validate"
	"github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/render/cfn"
)

// Inputs is a manifest's form data, keyed by field ID. Strings, ints, bools
// and []string are the value types the engine emits to parameters.json;
// front-ends are free to pass other types (e.g. *string from a picker) as
// long as MarshalParameters can encode them.
type Inputs map[string]any

// Source identifies which stream produced an Event.
type Source string

// Recognised event sources.
const (
	SourceStdout Source = "stdout"
	SourceStderr Source = "stderr"
	SourceCFN    Source = "cfn"
)

// Event is a unified item on the Result.Events channel. Exactly one of Line
// or Stack is populated, decided by Source.
type Event struct {
	Time   time.Time
	Source Source
	Line   string          // for stdout / stderr
	Stack  *cfn.StackEvent // for cfn
}

// Result is what Execute returns once the deploy has been started. The
// subprocess and (optional) event poller are running asynchronously; the
// caller drains Events until it closes and then calls Wait to learn the
// final exit error.
//
// Status and Outputs are reserved fields from the plan signature. The MVP-1
// script driver leaves them zero-valued because the deploy script owns the
// final status — they're populated by the SDK driver landing in MVP-2/3.
type Result struct {
	StackName string
	Status    string
	Outputs   map[string]string
	Events    <-chan Event
	wait      func() error
}

// Wait blocks until both the deploy subprocess and the event poller have
// exited, then returns the deploy script's exit error (nil on success).
//
// Wait must be called exactly once. Calling it before draining Events risks
// deadlock if the script produces more output than the channel buffer can
// hold.
func (r *Result) Wait() error {
	if r == nil || r.wait == nil {
		return nil
	}
	return r.wait()
}

// RecordHook is invoked once the deploy script and CFN poller have both
// terminated. PR-02 wires it to a stack-record harvest (DescribeStacks +
// DescribeStackResources); the engine itself only knows the signature and
// stays free of any AWS-record dependency.
//
// stackName is the resolved STACK_NAME from the manifest's env templating;
// deployErr is the script's exit status (nil on success). The hook must NOT
// return an error: harvest failures are best-effort and the implementation
// logs them internally. The deploy never fails because the hook did.
type RecordHook func(ctx context.Context, stackName string, deployErr error)

// Option configures optional Execute behaviour. The base signature follows
// the plan literally; everything beyond a manifest + inputs + awsx.Client is
// an Option so the surface stays stable as later PRs grow it.
type Option func(*config)

type config struct {
	baseDir            string
	events             cfn.EventsAPI
	az                 AZLookup
	recordHook         RecordHook
	validatorsDisabled bool
	pipeline           validate.Pipeline
	log                *slog.Logger
	update             *UpdateOptions
}

// UpdateOptions switches Execute into the ADR-0048 in-place update flow:
// the engine bypasses the deploy script and instead drives the change-set
// lifecycle (Create → Describe → consent → Execute) directly through
// internal/update. The script driver remains the runtime for `create`
// deploys; this is the only ADR-0008 carve-out.
type UpdateOptions struct {
	// StackName is the existing stack to update. When empty, Execute
	// reads STACK_NAME from the resolved env (the same env var the
	// script driver consumes).
	StackName string
	// PreviousParameters is the snapshot of the stack's current
	// parameter values, used to compute parameter deltas in the diff.
	// Caller (PR-02 stack-record loader) supplies this; nil is
	// acceptable and disables the old → new comparison.
	PreviousParameters map[string]string
	// Capabilities lists the IAM capabilities CFN must acknowledge to
	// process this template. ADR-0048 will eventually surface these
	// through the manifest schema; until then, the caller (stack-record
	// loader / form layer) forwards them on each update.
	Capabilities []string
	// API is the change-set client. Required.
	API cfn.ChangeSetAPI
	// Consent is the replacement-consent gate (ADR-0036). Nil = always
	// approve (suitable for headless tests).
	Consent update.ConsentGate
	// Harvest is the post-execute record write (PR-02). Nil = skip.
	Harvest update.Harvester
	// Stream is the CFN-events seam used after Execute. Nil = no
	// streaming (the caller can build their own poller off StackName).
	Stream update.EventStreamer
	// PollInterval is the DescribeChangeSet poll cadence. Zero = 1 Hz.
	PollInterval time.Duration
	// Description optionally annotates the change set inside AWS.
	Description string
	// ChangeSetName overrides the auto-generated name; tests pin it.
	ChangeSetName string
}

// WithBaseDir tells the renderer how to interpret the manifest's relative
// paths (template, parameters_file, script). Defaults to "." if unset.
func WithBaseDir(dir string) Option {
	return func(c *config) { c.baseDir = dir }
}

// WithEvents wires a CloudFormation event source into the runtime. The engine
// polls it at 1 Hz once the deploy script has started. Nil means "do not poll"
// — appropriate for tests and for the awsx-less code paths.
func WithEvents(api cfn.EventsAPI) Option {
	return func(c *config) { c.events = api }
}

// WithAZLookup overrides the AZ-lookup function used by the distinct-az
// validator. Without it, the engine derives a lookup from awsClient.ListSubnets
// keyed off the manifest's "VpcId" input (see subnetAZLookup). Tests pass a
// deterministic in-memory lookup.
func WithAZLookup(fn AZLookup) Option {
	return func(c *config) { c.az = fn }
}

// WithRecordHook installs a post-deploy hook (typically a stack-record
// harvest — see internal/record). The engine invokes hook once the deploy
// subprocess and CFN poller have both exited, before Wait returns. A nil
// hook (the default) disables the call.
func WithRecordHook(hook RecordHook) Option {
	return func(c *config) { c.recordHook = hook }
}

// WithValidators toggles the pre-render template validator pipeline (ADR-0050).
// Enabled by default; passing false threads the --no-validate flag through to
// Execute so the YAML lint and CloudFormation ValidateTemplate stages are
// skipped. The skip is session-scoped and never persisted; Execute logs once
// when validators are disabled so post-hoc auditing of the operational log
// remains honest.
func WithValidators(enabled bool) Option {
	return func(c *config) { c.validatorsDisabled = !enabled }
}

// WithValidatorPipeline injects a custom validate.Pipeline. The default is
// validate.NewDefault(), which runs the YAML lint stage and the CFN
// ValidateTemplate stage. Tests inject a fake pipeline to assert on the
// engine's response to specific findings without depending on a live AWS
// account.
func WithValidatorPipeline(p validate.Pipeline) Option {
	return func(c *config) { c.pipeline = p }
}

// WithLogger overrides the slog.Logger Execute uses for operational lines
// (e.g. "validators skipped via --no-validate"). Defaults to slog.Default()
// when unset. The deploy script's stdout/stderr is unaffected — it always
// flows through Result.Events regardless of this logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.log = l }
}

// WithUpdate switches Execute into the in-place update flow (ADR-0048).
// When set, Execute bypasses the script driver and drives a change-set
// preview / execute through internal/update instead. The supplied
// UpdateOptions carry the change-set client, consent gate, harvester, and
// streaming seam. Passing a nil API panics at call time — the update
// branch cannot proceed without it.
func WithUpdate(opts UpdateOptions) Option {
	return func(c *config) {
		o := opts
		c.update = &o
	}
}

// Execute validates the inputs against the manifest, writes parameters.json,
// spawns the manifest's deploy script with the resolved env, and starts the
// CloudFormation event poller (when one is configured). It returns a Result
// whose Events channel multiplexes script output and CFN events; the channel
// closes once both sources are done.
//
// awsClient supplies the Profile and Region that templated env vars reference
// (see ADR-0008) and the VPC-scoped subnet listing used by the fallback
// AZLookup. It must be non-nil.
func Execute(
	ctx context.Context,
	m *manifest.Manifest,
	inputs Inputs,
	awsClient *awsx.Client,
	opts ...Option,
) (*Result, error) {
	if m == nil {
		return nil, fmt.Errorf("resource: manifest is nil")
	}
	if m.Kind != "" && m.Kind != manifest.KindResource {
		return nil, fmt.Errorf("resource: manifest kind is %q, expected %q", m.Kind, manifest.KindResource)
	}
	if m.Deploy == nil {
		return nil, fmt.Errorf("resource: manifest has no deploy spec")
	}
	if awsClient == nil {
		return nil, fmt.Errorf("resource: awsClient is nil")
	}

	cfg := config{baseDir: "."}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.az == nil {
		cfg.az = subnetAZLookup(awsClient, inputs)
	}

	if errs := Validate(ctx, m, inputs, cfg.az); len(errs) > 0 {
		return nil, errs
	}

	env, err := resolveEnv(m.Deploy.Env, inputs, awsClient)
	if err != nil {
		return nil, fmt.Errorf("resource: resolve env: %w", err)
	}
	stackName := env["STACK_NAME"]

	if failure, err := runValidators(ctx, &cfg, m, inputs, stackName, awsClient); err != nil {
		return nil, err
	} else if failure != nil {
		return nil, failure
	}

	// ADR-0048 update branch: skip the script driver entirely and drive
	// the change-set lifecycle through internal/update. The deploy script
	// remains the runtime for `create` deploys (cfg.update == nil).
	if cfg.update != nil {
		return executeUpdate(ctx, m, inputs, cfg, stackName)
	}

	r := &cfn.Renderer{BaseDir: cfg.baseDir}
	if err := r.Render(m, map[string]any(inputs)); err != nil {
		return nil, fmt.Errorf("resource: render parameters: %w", err)
	}

	lines, waitDeploy, err := r.Deploy(ctx, m, env)
	if err != nil {
		return nil, fmt.Errorf("resource: start deploy: %w", err)
	}

	var stackEvents <-chan cfn.StackEvent
	if cfg.events != nil && stackName != "" {
		poller := &cfn.Poller{API: cfg.events, Interval: time.Second}
		stackEvents = poller.Poll(ctx, stackName)
	} else {
		closed := make(chan cfn.StackEvent)
		close(closed)
		stackEvents = closed
	}

	merged := make(chan Event, 16)
	var fanIn sync.WaitGroup
	fanIn.Add(2)

	// The two fan-in goroutines below drain their upstream channels even
	// after ctx is cancelled: the upstreams (the renderer's pumps and the
	// CFN poller) need their consumers to keep reading until they close,
	// or the deploy script's pipes back up and the engine deadlocks.
	go func() {
		defer fanIn.Done()
		cancelled := false
		for ln := range lines {
			if cancelled {
				continue
			}
			src := SourceStdout
			if ln.Source == cfn.StderrLine {
				src = SourceStderr
			}
			select {
			case merged <- Event{Time: ln.Time, Source: src, Line: ln.Text}:
			case <-ctx.Done():
				cancelled = true
			}
		}
	}()
	go func() {
		defer fanIn.Done()
		cancelled := false
		for ev := range stackEvents {
			if cancelled {
				continue
			}
			e := ev
			select {
			case merged <- Event{Time: e.Time, Source: SourceCFN, Stack: &e}:
			case <-ctx.Done():
				cancelled = true
			}
		}
	}()

	deployErr := make(chan error, 1)
	go func() {
		fanIn.Wait()
		close(merged)
		err := waitDeploy()
		// Best-effort post-deploy harvest. The hook owns its own
		// error handling — it must not panic and must not block
		// the deploy result. We run it before publishing the
		// deploy error so callers blocked on Wait observe both
		// the exit status and any record write atomically.
		if cfg.recordHook != nil && stackName != "" {
			cfg.recordHook(ctx, stackName, err)
		}
		deployErr <- err
	}()

	return &Result{
		StackName: stackName,
		Events:    merged,
		wait:      func() error { return <-deployErr },
	}, nil
}

// resolveEnv renders each manifest env value through text/template using the
// inputs plus the awsClient's Profile and Region as template data.
//
// Template syntax matches ADR-0026 (bounded Go-template DSL): users write
// "{{ .Project }}" / "{{ .Region }}" etc. with no funcs beyond the stdlib
// defaults.
func resolveEnv(envSpec map[string]string, inputs Inputs, awsClient *awsx.Client) (map[string]string, error) {
	data := make(map[string]any, len(inputs)+2)
	for k, v := range inputs {
		data[k] = v
	}
	data["Profile"] = awsClient.Profile()
	data["Region"] = awsClient.Region()

	out := make(map[string]string, len(envSpec))
	for k, raw := range envSpec {
		t, err := template.New(k).Option("missingkey=error").Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("env %q: parse: %w", k, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("env %q: execute: %w", k, err)
		}
		out[k] = buf.String()
	}
	return out, nil
}

// executeUpdate is the ADR-0048 update-flow branch of Execute. It reads
// the template body from the manifest, builds the change-set input from
// the form snapshot, calls update.Stack, and adapts the typed StackResult
// onto the same *Result shape Execute returns for the script-deploy path.
// The Events channel multiplexes the streamed cfn.StackEvents into our
// Event type (Source=SourceCFN) so consumers (TUI/GUI) don't need to
// distinguish the two paths.
func executeUpdate(
	ctx context.Context,
	m *manifest.Manifest,
	inputs Inputs,
	cfg config,
	stackName string,
) (*Result, error) {
	opts := cfg.update
	if opts.API == nil {
		return nil, fmt.Errorf("resource: update mode requires a ChangeSetAPI")
	}
	name := opts.StackName
	if name == "" {
		name = stackName
	}
	if name == "" {
		return nil, fmt.Errorf("resource: update mode requires a stack name (set UpdateOptions.StackName or manifest env STACK_NAME)")
	}

	// The change-set API consumes the template body inline. We resolve
	// the manifest's template path against cfg.baseDir and read it from
	// disk — there is no script involvement.
	if m.Template == nil || m.Template.Path == "" {
		return nil, fmt.Errorf("resource: update mode requires manifest.template.path")
	}
	templatePath := filepath.Join(cfg.baseDir, m.Template.Path)
	if filepath.IsAbs(m.Template.Path) {
		templatePath = m.Template.Path
	}
	body, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("resource: read template: %w", err)
	}

	params := parametersFromInputs(m, inputs)

	res, err := update.Stack(ctx, update.StackInput{
		StackName:          name,
		TemplateBody:       string(body),
		Parameters:         params,
		PreviousParameters: opts.PreviousParameters,
		Capabilities:       append([]string(nil), opts.Capabilities...),
		Description:        opts.Description,
		ChangeSetName:      opts.ChangeSetName,
	}, update.StackOptions{
		API:          opts.API,
		Consent:      opts.Consent,
		Harvest:      opts.Harvest,
		Stream:       opts.Stream,
		PollInterval: opts.PollInterval,
	})
	if err != nil {
		return nil, err
	}

	return adaptUpdateResult(name, res), nil
}

// adaptUpdateResult wraps an update.StackResult onto the *Result shape
// callers consume. Outputs and Status are left zero; the caller surfaces
// res.Notice / res.Diff directly through the typed StackResult attached
// to ctx in PR-09 / PR-10.
//
// For the no-changes / consent-denied paths the wrapped channel is
// already closed (matching the script-deploy convention of "Events
// channel closed once both sources are done").
func adaptUpdateResult(stackName string, r update.StackResult) *Result {
	events := make(chan Event, 8)
	go func() {
		defer close(events)
		if r.Events == nil {
			return
		}
		for ev := range r.Events {
			e := ev
			events <- Event{Time: e.Time, Source: SourceCFN, Stack: &e}
		}
	}()
	return &Result{
		StackName: stackName,
		Events:    events,
		wait:      func() error { return nil },
	}
}

// parametersFromInputs converts the engine's typed Inputs into the
// string-keyed map the change-set API requires. Only manifest-declared
// fields are emitted, matching the renderer's MarshalParameters convention.
func parametersFromInputs(m *manifest.Manifest, inputs Inputs) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(inputs))
	for _, f := range m.Form {
		v, ok := inputs[f.ID]
		if !ok {
			continue
		}
		s := stringifyInput(v)
		if s == "" {
			continue
		}
		out[f.ID] = s
	}
	return out
}

// stringifyInput returns the wire form of a manifest-input value. Strings
// pass through; numbers / bools stringify; comma-joined for slices.
func stringifyInput(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []string:
		joined := ""
		for i, s := range x {
			if i > 0 {
				joined += ","
			}
			joined += s
		}
		return joined
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// subnetAZLookup adapts awsx.Client.ListSubnets (which is VPC-scoped) to the
// per-subnet AZLookup signature the distinct-az validator expects. It pulls
// the VPC ID from inputs["VpcId"] — the conventional field name shared across
// the resource manifests — and resolves subnets by scanning the cached list.
// Returns nil when no VpcId input is present, which causes distinctAZ to
// skip the rule rather than fail.
//
// ListSubnets caches per (profile, region, vpcID), so the first lookup hits
// AWS once and every subsequent subnet check is in-memory.
func subnetAZLookup(awsClient *awsx.Client, inputs Inputs) AZLookup {
	vpcID, ok := inputs["VpcId"].(string)
	if !ok || vpcID == "" {
		return nil
	}
	return func(ctx context.Context, subnetID string) (string, error) {
		subnets, err := awsClient.ListSubnets(ctx, vpcID)
		if err != nil {
			return "", err
		}
		for _, s := range subnets {
			if s.ID == subnetID {
				return s.AvailabilityZone, nil
			}
		}
		return "", fmt.Errorf("subnet %s not found in VPC %s", subnetID, vpcID)
	}
}

// runValidators applies the template validator pipeline (ADR-0050). When
// validators are disabled via WithValidators(false), it logs one operational
// line and returns without touching the AWS network. Otherwise it builds the
// default pipeline against the manifest's template paths and the awsClient's
// CloudFormation surface, runs it, and returns a typed ValidationFailure on
// the first error-severity finding so the existing error model (ADR-0016)
// can render it.
//
// The cfg may carry a caller-supplied pipeline (tests, future PR-06); when
// nil, the default pipeline is used. Findings that are not error-severity
// (capability and parameter infos from Stage 2) are dropped here — PR-06
// will surface them through the form flow once the update path lands.
func runValidators(
	ctx context.Context,
	cfg *config,
	m *manifest.Manifest,
	inputs Inputs,
	stackName string,
	awsClient *awsx.Client,
) (*validate.ValidationFailure, error) {
	log := cfg.log
	if log == nil {
		log = slog.Default()
	}
	if cfg.validatorsDisabled {
		log.Info("validators skipped via --no-validate", slog.String("stack", stackName))
		return nil, nil
	}
	if m.Template == nil || m.Template.Path == "" {
		// No template declared: nothing for the validators to inspect.
		// resource_runner.Validate already rejects manifests with no
		// template spec; the field-level guard here keeps Execute usable
		// for tests that construct a partial manifest by hand.
		return nil, nil
	}

	pipeline := cfg.pipeline
	useDefaultCFN := pipeline == nil
	if pipeline == nil {
		pipeline = validate.NewDefault()
	}

	in := validate.Input{
		TemplatePath: resolveValidatorPath(cfg.baseDir, m.Template.Path),
		StackName:    stackName,
	}
	if m.Template.ParametersFile != "" {
		in.ParametersPath = resolveValidatorPath(cfg.baseDir, m.Template.ParametersFile)
	}
	// Only build the live CloudFormation client when the engine is running
	// the default pipeline. Tests that inject their own pipeline pass the
	// fake AWS surface through the pipeline itself, so the engine never
	// needs to touch awsClient.CloudFormation() on the test path — that
	// matters because awsx.NewForTest produces a Client without SDK
	// config and CloudFormation() would panic.
	if useDefaultCFN {
		in.AWS = awsClient.CloudFormation()
	}

	findings, err := pipeline.Run(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("resource: validate: %w", err)
	}
	if failure := validate.FailureFromFindings(findings, stackName, map[string]any(inputs)); failure != nil {
		return failure, nil
	}
	return nil, nil
}

func resolveValidatorPath(baseDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}
