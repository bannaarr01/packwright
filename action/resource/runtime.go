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
	"sync"
	"text/template"
	"time"

	"github.com/bannaarr01/packwright/awsx"
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
	baseDir    string
	events     cfn.EventsAPI
	az         AZLookup
	recordHook RecordHook
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
