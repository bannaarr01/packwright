package resource_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/awsx"
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
