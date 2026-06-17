package delete

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/bannaarr01/packwright/internal/stream"
)

// fakeEC2 records every Delete*/Describe* call so tests can assert
// dispatch order and counts. The default zero value is "every call
// succeeds with an empty response"; per-test setters override
// individual responses or inject errors.
type fakeEC2 struct {
	mu sync.Mutex
	// calls is the ordered list of method names the executor
	// invoked, in dispatch order.
	calls []string
	// errs maps method name -> error to return on the NEXT call.
	errs map[string]error

	descVolumes     *ec2.DescribeVolumesOutput
	descSnapshots   *ec2.DescribeSnapshotsOutput
	descImages      *ec2.DescribeImagesOutput
	descInstances   *ec2.DescribeInstancesOutput
	descAddresses   *ec2.DescribeAddressesOutput
	descNatGateways *ec2.DescribeNatGatewaysOutput
	descRouteTables *ec2.DescribeRouteTablesOutput
}

func (f *fakeEC2) record(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		delete(f.errs, name)
		return err
	}
	return nil
}

func (f *fakeEC2) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeEC2) DeleteVolume(context.Context, *ec2.DeleteVolumeInput, ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	if err := f.record("DeleteVolume"); err != nil {
		return nil, err
	}
	return &ec2.DeleteVolumeOutput{}, nil
}
func (f *fakeEC2) DeleteSnapshot(context.Context, *ec2.DeleteSnapshotInput, ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	if err := f.record("DeleteSnapshot"); err != nil {
		return nil, err
	}
	return &ec2.DeleteSnapshotOutput{}, nil
}
func (f *fakeEC2) ReleaseAddress(context.Context, *ec2.ReleaseAddressInput, ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
	if err := f.record("ReleaseAddress"); err != nil {
		return nil, err
	}
	return &ec2.ReleaseAddressOutput{}, nil
}
func (f *fakeEC2) DeleteNatGateway(context.Context, *ec2.DeleteNatGatewayInput, ...func(*ec2.Options)) (*ec2.DeleteNatGatewayOutput, error) {
	if err := f.record("DeleteNatGateway"); err != nil {
		return nil, err
	}
	return &ec2.DeleteNatGatewayOutput{}, nil
}
func (f *fakeEC2) DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if err := f.record("DescribeVolumes"); err != nil {
		return nil, err
	}
	if f.descVolumes != nil {
		return f.descVolumes, nil
	}
	return &ec2.DescribeVolumesOutput{}, nil
}
func (f *fakeEC2) DescribeSnapshots(context.Context, *ec2.DescribeSnapshotsInput, ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	if err := f.record("DescribeSnapshots"); err != nil {
		return nil, err
	}
	if f.descSnapshots != nil {
		return f.descSnapshots, nil
	}
	return &ec2.DescribeSnapshotsOutput{}, nil
}
func (f *fakeEC2) DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	if err := f.record("DescribeImages"); err != nil {
		return nil, err
	}
	if f.descImages != nil {
		return f.descImages, nil
	}
	return &ec2.DescribeImagesOutput{}, nil
}
func (f *fakeEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if err := f.record("DescribeInstances"); err != nil {
		return nil, err
	}
	if f.descInstances != nil {
		return f.descInstances, nil
	}
	return &ec2.DescribeInstancesOutput{}, nil
}
func (f *fakeEC2) DescribeAddresses(context.Context, *ec2.DescribeAddressesInput, ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	if err := f.record("DescribeAddresses"); err != nil {
		return nil, err
	}
	if f.descAddresses != nil {
		return f.descAddresses, nil
	}
	return &ec2.DescribeAddressesOutput{}, nil
}
func (f *fakeEC2) DescribeNatGateways(context.Context, *ec2.DescribeNatGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	if err := f.record("DescribeNatGateways"); err != nil {
		return nil, err
	}
	if f.descNatGateways != nil {
		return f.descNatGateways, nil
	}
	return &ec2.DescribeNatGatewaysOutput{}, nil
}
func (f *fakeEC2) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	if err := f.record("DescribeRouteTables"); err != nil {
		return nil, err
	}
	if f.descRouteTables != nil {
		return f.descRouteTables, nil
	}
	return &ec2.DescribeRouteTablesOutput{}, nil
}

type fakeELBv2 struct {
	mu              sync.Mutex
	calls           []string
	descTargetGroup *elasticloadbalancingv2.DescribeTargetGroupsOutput
	descListeners   *elasticloadbalancingv2.DescribeListenersOutput
	descRules       *elasticloadbalancingv2.DescribeRulesOutput
	deleteErr       error
}

func (f *fakeELBv2) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}
func (f *fakeELBv2) DeleteTargetGroup(context.Context, *elasticloadbalancingv2.DeleteTargetGroupInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DeleteTargetGroup")
	err := f.deleteErr
	f.mu.Unlock()
	return &elasticloadbalancingv2.DeleteTargetGroupOutput{}, err
}
func (f *fakeELBv2) DescribeTargetGroups(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeTargetGroups")
	out := f.descTargetGroup
	f.mu.Unlock()
	if out == nil {
		out = &elasticloadbalancingv2.DescribeTargetGroupsOutput{}
	}
	return out, nil
}
func (f *fakeELBv2) DescribeListeners(context.Context, *elasticloadbalancingv2.DescribeListenersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenersOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeListeners")
	out := f.descListeners
	f.mu.Unlock()
	if out == nil {
		out = &elasticloadbalancingv2.DescribeListenersOutput{}
	}
	return out, nil
}
func (f *fakeELBv2) DescribeRules(context.Context, *elasticloadbalancingv2.DescribeRulesInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeRules")
	out := f.descRules
	f.mu.Unlock()
	if out == nil {
		out = &elasticloadbalancingv2.DescribeRulesOutput{}
	}
	return out, nil
}

type fakeLogs struct {
	mu        sync.Mutex
	calls     []string
	descOut   *cloudwatchlogs.DescribeLogGroupsOutput
	deleteErr error
}

func (f *fakeLogs) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}
func (f *fakeLogs) DeleteLogGroup(context.Context, *cloudwatchlogs.DeleteLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DeleteLogGroup")
	err := f.deleteErr
	f.mu.Unlock()
	return &cloudwatchlogs.DeleteLogGroupOutput{}, err
}
func (f *fakeLogs) DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeLogGroups")
	out := f.descOut
	f.mu.Unlock()
	if out == nil {
		out = &cloudwatchlogs.DescribeLogGroupsOutput{}
	}
	return out, nil
}

type fakeRDS struct {
	mu              sync.Mutex
	calls           []string
	descSnapshots   *rds.DescribeDBSnapshotsOutput
	descInstances   *rds.DescribeDBInstancesOutput
	describeInstErr error
	deleteErr       error
}

func (f *fakeRDS) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}
func (f *fakeRDS) DeleteDBSnapshot(context.Context, *rds.DeleteDBSnapshotInput, ...func(*rds.Options)) (*rds.DeleteDBSnapshotOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DeleteDBSnapshot")
	err := f.deleteErr
	f.mu.Unlock()
	return &rds.DeleteDBSnapshotOutput{}, err
}
func (f *fakeRDS) DescribeDBSnapshots(context.Context, *rds.DescribeDBSnapshotsInput, ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeDBSnapshots")
	out := f.descSnapshots
	f.mu.Unlock()
	if out == nil {
		out = &rds.DescribeDBSnapshotsOutput{}
	}
	return out, nil
}
func (f *fakeRDS) DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeDBInstances")
	err := f.describeInstErr
	out := f.descInstances
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = &rds.DescribeDBInstancesOutput{}
	}
	return out, nil
}

type fakeECR struct {
	mu        sync.Mutex
	calls     []string
	descOut   *ecr.DescribeImagesOutput
	delOut    *ecr.BatchDeleteImageOutput
	deleteErr error
}

func (f *fakeECR) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}
func (f *fakeECR) BatchDeleteImage(context.Context, *ecr.BatchDeleteImageInput, ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "BatchDeleteImage")
	err := f.deleteErr
	out := f.delOut
	f.mu.Unlock()
	if out == nil {
		out = &ecr.BatchDeleteImageOutput{}
	}
	return out, err
}
func (f *fakeECR) DescribeImages(context.Context, *ecr.DescribeImagesInput, ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "DescribeImages")
	out := f.descOut
	f.mu.Unlock()
	if out == nil {
		out = &ecr.DescribeImagesOutput{}
	}
	return out, nil
}

// captureBus records every Publish so tests can assert which events
// the Executor emitted in which order. The implementation mirrors
// the captureBus pattern used elsewhere in the codebase
// (internal/ai/cost/meter_test.go).
type captureBus struct {
	mu     sync.Mutex
	events []stream.Event
}

func (b *captureBus) Publish(_ string, ev stream.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *captureBus) Snapshot() []stream.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]stream.Event, len(b.events))
	copy(out, b.events)
	return out
}

// bufferLog is a LogWriter that captures every entry in a buffer.
type bufferLog struct {
	WriterLog
	buf bytes.Buffer
}

func newBufferLog() *bufferLog {
	bl := &bufferLog{}
	bl.W = &bl.buf
	return bl
}

// staticErr is a small helper for tests that want a sentinel error.
func staticErr(s string) error { return errors.New(s) }
