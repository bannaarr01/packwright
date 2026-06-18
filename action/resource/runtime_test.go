package resource_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/manifest"
)

// albManifest mirrors feature/featureDetails.md §7 closely enough to exercise
// the renderer's field ordering and the env-templating path. Project /
// Environment are intentionally absent from m.Form: they feed STACK_NAME via
// env templating but should not appear in parameters.json.
func albManifest(t *testing.T, baseDir string) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		ID:            "alb",
		Kind:          manifest.KindResource,
		Slash:         "/alb",
		Title:         "Application Load Balancer",
		Template: &manifest.TemplateSpec{
			Kind:           "cloudformation",
			Path:           "alb-template.yaml",
			ParametersFile: "parameters.json",
		},
		Deploy: &manifest.DeploySpec{
			Driver: "script",
			Script: "fake-deploy.sh",
			Env: map[string]string{
				"STACK_NAME":  "alb-stack-{{.Project}}-{{.Environment}}",
				"AWS_PROFILE": "{{.Profile}}",
				"AWS_REGION":  "{{.Region}}",
			},
		},
		Form: []manifest.Field{
			{ID: "VpcId", Type: manifest.TypeAWSVpcID, Required: true},
			{
				ID: "SubnetIds", Type: manifest.TypeAWSSubnetIDs,
				Required: true, DependsOn: []string{"VpcId"},
				Min: intPtr(2),
				Validate: []manifest.ValidatorSpec{
					{Rule: "distinct-az", Message: "subnets must span at least two AZs"},
				},
			},
			{ID: "SecurityGroupIds", Type: manifest.TypeAWSSGIDs, DependsOn: []string{"VpcId"}},
			{ID: "ALBName", Type: manifest.TypeString, Required: true},
			{ID: "TargetGroupName", Type: manifest.TypeString, Required: true},
			{ID: "HealthCheckPath", Type: manifest.TypeString},
			{ID: "CertificateArn", Type: manifest.TypeAWSACMArn},
		},
	}
}

func intPtr(i int) *int { return &i }

// canonicalInputs is the input set that produces alb-params.golden.json. The
// VPC/subnet/SG/ACM IDs are placeholders that match the values used in
// feature/featureDetails.md §4.4.
func canonicalInputs() resource.Inputs {
	return resource.Inputs{
		"Project":     "babe-main",
		"Environment": "prd",
		"VpcId":       "vpc-0a42b26c425daee77",
		"SubnetIds": []string{
			"subnet-036d50016c2f16a92",
			"subnet-081bcbee250e58e8d",
			"subnet-039e4084f247e7257",
		},
		"SecurityGroupIds": []string{"sg-0d1ef14a0b2c3d4e5"},
		"ALBName":          "babe-main-prd-alb",
		"TargetGroupName":  "babe-main-prd-tg",
		"HealthCheckPath":  "/health",
		"CertificateArn":   "arn:aws:acm:eu-west-1:111111111111:certificate/645fb788",
	}
}

// fakeAZLookup spreads the canonical subnets across three AZs so the
// distinct-az validator passes.
func fakeAZLookup() resource.AZLookup {
	table := map[string]string{
		"subnet-036d50016c2f16a92": "eu-west-1a",
		"subnet-081bcbee250e58e8d": "eu-west-1b",
		"subnet-039e4084f247e7257": "eu-west-1c",
	}
	return func(_ context.Context, id string) (string, error) {
		if az, ok := table[id]; ok {
			return az, nil
		}
		return "", nil
	}
}

func TestExecute_RendersGoldenParametersFile(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := resource.Execute(
		ctx,
		albManifest(t, dir),
		canonicalInputs(),
		awsx.NewForTest("test-profile", "eu-west-1"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Drain events so the fan-in goroutines exit.
	var stdout, stderr []string
	for ev := range res.Events {
		switch ev.Source {
		case resource.SourceStdout:
			stdout = append(stdout, ev.Line)
		case resource.SourceStderr:
			stderr = append(stderr, ev.Line)
		}
	}
	if err := res.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if got, want := res.StackName, "alb-stack-babe-main-prd"; got != want {
		t.Errorf("StackName = %q, want %q", got, want)
	}

	got, err := os.ReadFile(filepath.Join(dir, "parameters.json"))
	if err != nil {
		t.Fatalf("read parameters.json: %v", err)
	}
	want, err := os.ReadFile("testdata/alb-params.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("parameters.json mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	if !containsLine(stdout, "fake-deploy: STACK_NAME=alb-stack-babe-main-prd") {
		t.Errorf("stdout missing STACK_NAME line; got: %q", stdout)
	}
	if !containsLine(stdout, "fake-deploy: AWS_PROFILE=test-profile") {
		t.Errorf("stdout missing AWS_PROFILE line; got: %q", stdout)
	}
	if !containsLine(stderr, "warning on stderr") {
		t.Errorf("stderr missing warning line; got: %q", stderr)
	}
}

func TestExecute_RejectsValidationErrors(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)

	inputs := canonicalInputs()
	inputs["ALBName"] = ""           // required-but-empty would still pass; remove instead
	delete(inputs, "ALBName")        // required → fails Required
	inputs["TargetGroupName"] = 42   // wrong type → fails checkType
	inputs["SubnetIds"] = []string{} // empty array → fails Min, and distinct-az is skipped

	_, err := resource.Execute(
		context.Background(),
		albManifest(t, dir),
		inputs,
		awsx.NewForTest("p", "r"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
	)
	if err == nil {
		t.Fatal("Execute: want validation error, got nil")
	}
	verrs, ok := err.(resource.ValidationErrors)
	if !ok {
		t.Fatalf("Execute: error type = %T, want ValidationErrors", err)
	}
	m := verrs.Map()
	if _, ok := m["ALBName"]; !ok {
		t.Errorf("expected error on ALBName; got %v", m)
	}
	if _, ok := m["TargetGroupName"]; !ok {
		t.Errorf("expected error on TargetGroupName; got %v", m)
	}
	if _, ok := m["SubnetIds"]; !ok {
		t.Errorf("expected error on SubnetIds; got %v", m)
	}

	// Should never have rendered parameters.json on a validation failure.
	if _, err := os.Stat(filepath.Join(dir, "parameters.json")); !os.IsNotExist(err) {
		t.Errorf("parameters.json was written despite validation failure: err=%v", err)
	}
}

// TestExecute_CancelUnblocksWedgedConsumer regression-tests the fan-in
// deadlock that the first review caught: if a caller stops draining
// Result.Events and then cancels ctx, Wait must still return rather than
// blocking forever.
func TestExecute_CancelUnblocksWedgedConsumer(t *testing.T) {
	dir := t.TempDir()
	// A deploy script that prints a lot of output, more than the merged
	// channel can buffer, so the fan-in goroutine ends up blocked on a
	// send while waiting for the (wedged) consumer.
	chatty := `#!/bin/sh
i=0
while [ $i -lt 200 ]; do
  echo "line $i"
  i=$((i+1))
done
sleep 30
`
	scriptPath := filepath.Join(dir, "chatty.sh")
	if err := os.WriteFile(scriptPath, []byte(chatty), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	m := albManifest(t, dir)
	m.Deploy.Script = "chatty.sh"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := resource.Execute(
		ctx,
		m,
		canonicalInputs(),
		awsx.NewForTest("p", "r"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Drain just a handful of events, then cancel without further reading
	// — simulating a TUI that gave up mid-stream. The combination of a
	// full merged channel and a wedged consumer would have deadlocked the
	// old fan-in implementation.
	read := 0
	for ev := range res.Events {
		_ = ev
		read++
		if read == 4 {
			cancel()
		}
	}

	done := make(chan error, 1)
	go func() { done <- res.Wait() }()
	select {
	case <-done:
		// Whatever exit error the script produced is fine; we only care
		// that Wait actually returned.
	case <-time.After(15 * time.Second):
		t.Fatal("Wait blocked after cancel — fan-in deadlock regression")
	}
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// fakeCFNForRuntime is a minimal cloudFormationAPI that lets the runtime
// integration test exercise resource.WithRecordHook end-to-end without
// touching AWS. The fake is package-local to runtime_test so internal/record
// can stay free of resource-runtime adapters.
type fakeCFNForRuntime struct {
	stacks    []cfntypes.Stack
	resources []cfntypes.StackResource
}

func (f *fakeCFNForRuntime) DescribeStacks(_ context.Context, _ *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return &cloudformation.DescribeStacksOutput{Stacks: f.stacks}, nil
}

func (f *fakeCFNForRuntime) DescribeStackResources(_ context.Context, _ *cloudformation.DescribeStackResourcesInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStackResourcesOutput, error) {
	return &cloudformation.DescribeStackResourcesOutput{StackResources: f.resources}, nil
}

// albRecordInputs constructs an input set that resolves STACK_NAME to
// "alb-dev-stack" — the canonical fixture path the PR-02 acceptance test
// asserts the file appears at.
func albRecordInputs() resource.Inputs {
	in := canonicalInputs()
	in["Project"] = "acme"
	in["Environment"] = "dev"
	return in
}

// albRecordManifest tweaks the canonical ALB manifest so the templated
// STACK_NAME matches the ADR-0046 worked example (`alb-dev-stack`).
func albRecordManifest(t *testing.T, dir string) *manifest.Manifest {
	t.Helper()
	m := albManifest(t, dir)
	m.Deploy.Env["STACK_NAME"] = "alb-{{.Environment}}-stack"
	return m
}

// TestExecute_WritesStackRecordOnSuccessfulDeploy is the PR-02 acceptance test:
// run a real deploy script against a fake CFN client and assert that the file
// lands at the documented path with the documented shape.
func TestExecute_WritesStackRecordOnSuccessfulDeploy(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)

	stackName := "alb-dev-stack"
	now := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	cfn := &fakeCFNForRuntime{
		stacks: []cfntypes.Stack{{
			StackName:    aws.String(stackName),
			StackStatus:  cfntypes.StackStatusCreateComplete,
			CreationTime: aws.Time(now),
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String("LoadBalancerDNSName"), OutputValue: aws.String("alb-dev.example")},
			},
			Parameters: []cfntypes.Parameter{
				{ParameterKey: aws.String("VpcId"), ParameterValue: aws.String("vpc-0abc1234")},
			},
		}},
		resources: []cfntypes.StackResource{{
			LogicalResourceId:  aws.String("ApplicationLoadBalancer"),
			PhysicalResourceId: aws.String("arn:aws:elasticloadbalancing:eu-west-1:123:lb"),
			ResourceType:       aws.String("AWS::ElasticLoadBalancingV2::LoadBalancer"),
			ResourceStatus:     cfntypes.ResourceStatusCreateComplete,
		}},
	}

	store := record.NewStore(dir)
	rec := &record.Recorder{
		CFN:   cfn,
		Store: store,
		Identity: record.Identity{
			Project: "acme", Env: "dev",
			Profile: "acme-dev", Region: "eu-west-1",
			Account:  "123456789012",
			Manifest: record.ManifestRef{Slash: "/alb", Source: "packs/reference/manifests/alb.yaml"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := resource.Execute(
		ctx,
		albRecordManifest(t, dir),
		albRecordInputs(),
		awsx.NewForTest("acme-dev", "eu-west-1"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
		resource.WithRecordHook(rec.Hook()),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range res.Events {
		// drain
	}
	if err := res.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	wantPath := filepath.Join(dir, "projects", "acme", "dev", "stacks", "alb-dev-stack.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected record at %s: %v", wantPath, err)
	}

	got, err := store.Read("acme", "dev", "alb-dev-stack")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != record.BroadDeployed {
		t.Errorf("Status.Broad = %q, want %q", got.Status.Broad, record.BroadDeployed)
	}
	if len(got.History) != 1 {
		t.Errorf("history len = %d, want 1", len(got.History))
	}
	if got.Manifest.Slash != "/alb" {
		t.Errorf("manifest.slash = %q, want /alb", got.Manifest.Slash)
	}
}

// TestExecute_RecordHook_RedeployAppendsHistory is the second half of the
// PR-02 acceptance: re-running the deploy appends a single history entry
// without duplicating the outputs / resources / parameters.
func TestExecute_RecordHook_RedeployAppendsHistory(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)

	stackName := "alb-dev-stack"
	cfn := &fakeCFNForRuntime{
		stacks: []cfntypes.Stack{{
			StackName:    aws.String(stackName),
			StackStatus:  cfntypes.StackStatusCreateComplete,
			CreationTime: aws.Time(time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)),
			Outputs: []cfntypes.Output{
				{OutputKey: aws.String("DNSName"), OutputValue: aws.String("alb-dev.example")},
			},
			Parameters: []cfntypes.Parameter{
				{ParameterKey: aws.String("VpcId"), ParameterValue: aws.String("vpc-0abc1234")},
			},
		}},
		resources: []cfntypes.StackResource{{
			LogicalResourceId:  aws.String("ALB"),
			PhysicalResourceId: aws.String("arn:lb"),
			ResourceType:       aws.String("AWS::ElasticLoadBalancingV2::LoadBalancer"),
			ResourceStatus:     cfntypes.ResourceStatusCreateComplete,
		}},
	}
	store := record.NewStore(dir)
	rec := &record.Recorder{
		CFN:   cfn,
		Store: store,
		Identity: record.Identity{
			Project: "acme", Env: "dev",
			Profile: "acme-dev", Region: "eu-west-1",
			Account:  "123456789012",
			Manifest: record.ManifestRef{Slash: "/alb", Source: "packs/reference/manifests/alb.yaml"},
		},
	}

	deployOnce := func(t *testing.T) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := resource.Execute(
			ctx,
			albRecordManifest(t, dir),
			albRecordInputs(),
			awsx.NewForTest("acme-dev", "eu-west-1"),
			resource.WithBaseDir(dir),
			resource.WithAZLookup(fakeAZLookup()),
			resource.WithRecordHook(rec.Hook()),
		)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		for range res.Events {
		}
		if err := res.Wait(); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}

	deployOnce(t)
	deployOnce(t)

	got, err := store.Read("acme", "dev", stackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.History) != 2 {
		t.Errorf("history len = %d, want 2 (one append per redeploy)", len(got.History))
	}
	if len(got.Outputs) != 1 || len(got.Resources) != 1 || len(got.Parameters) != 1 {
		t.Errorf("merged fields duplicated on redeploy: outputs=%d resources=%d params=%d",
			len(got.Outputs), len(got.Resources), len(got.Parameters))
	}
}

// TestExecute_RecordHook_ErrorDoesNotFailDeploy asserts that a harvest miss
// is best-effort: the engine still returns the deploy script's exit status
// and never propagates the hook's error.
func TestExecute_RecordHook_ErrorDoesNotFailDeploy(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)

	var hookCalled atomic.Int32
	failingHook := func(_ context.Context, _ string, _ error) {
		hookCalled.Add(1)
		// Intentionally return without writing — the hook contract is
		// fire-and-forget. A panic here would still be the engine's
		// problem; recovery is the hook's responsibility (the real
		// implementation uses a logged error path).
		_ = errors.New("harvest exploded — engine must continue regardless")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := resource.Execute(
		ctx,
		albRecordManifest(t, dir),
		albRecordInputs(),
		awsx.NewForTest("acme-dev", "eu-west-1"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
		resource.WithRecordHook(failingHook),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range res.Events {
	}
	if err := res.Wait(); err != nil {
		t.Fatalf("Wait returned %v; the deploy must succeed even when the record hook fails", err)
	}
	if hookCalled.Load() == 0 {
		t.Errorf("record hook was not invoked")
	}
}
