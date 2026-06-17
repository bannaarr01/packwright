package scanners

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

// fakeEC2 is the counting EC2 fake every EC2-scanner test in this file
// shares. Each Describe* method walks the matching slice of canned
// responses in order so a test can prove "this scanner exhausted N
// pages" without time-dependent mocks. failNext applies to the very
// next call across any method.
type fakeEC2 struct {
	instances []*ec2.DescribeInstancesOutput
	volumes   []*ec2.DescribeVolumesOutput
	snapshots []*ec2.DescribeSnapshotsOutput
	addresses *ec2.DescribeAddressesOutput
	natgws    []*ec2.DescribeNatGatewaysOutput

	instCalls, volCalls, snapCalls, addrCalls, natCalls int

	snapshotInputs []*ec2.DescribeSnapshotsInput
	failNext       error
}

func (f *fakeEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if err := f.consumeFail(); err != nil {
		return nil, err
	}
	if len(f.instances) == 0 {
		return &ec2.DescribeInstancesOutput{}, nil
	}
	f.instCalls++
	out := f.instances[0]
	f.instances = f.instances[1:]
	return out, nil
}

func (f *fakeEC2) DescribeVolumes(_ context.Context, _ *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	if err := f.consumeFail(); err != nil {
		return nil, err
	}
	if len(f.volumes) == 0 {
		return &ec2.DescribeVolumesOutput{}, nil
	}
	f.volCalls++
	out := f.volumes[0]
	f.volumes = f.volumes[1:]
	return out, nil
}

func (f *fakeEC2) DescribeSnapshots(_ context.Context, in *ec2.DescribeSnapshotsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error) {
	f.snapshotInputs = append(f.snapshotInputs, in)
	if err := f.consumeFail(); err != nil {
		return nil, err
	}
	if len(f.snapshots) == 0 {
		return &ec2.DescribeSnapshotsOutput{}, nil
	}
	f.snapCalls++
	out := f.snapshots[0]
	f.snapshots = f.snapshots[1:]
	return out, nil
}

func (f *fakeEC2) DescribeAddresses(_ context.Context, _ *ec2.DescribeAddressesInput, _ ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	if err := f.consumeFail(); err != nil {
		return nil, err
	}
	f.addrCalls++
	if f.addresses == nil {
		return &ec2.DescribeAddressesOutput{}, nil
	}
	return f.addresses, nil
}

func (f *fakeEC2) DescribeNatGateways(_ context.Context, _ *ec2.DescribeNatGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	if err := f.consumeFail(); err != nil {
		return nil, err
	}
	if len(f.natgws) == 0 {
		return &ec2.DescribeNatGatewaysOutput{}, nil
	}
	f.natCalls++
	out := f.natgws[0]
	f.natgws = f.natgws[1:]
	return out, nil
}

func (f *fakeEC2) consumeFail() error {
	if f.failNext == nil {
		return nil
	}
	err := f.failNext
	f.failNext = nil
	return err
}

// TestEC2InstanceScannerPaginates feeds two pages and asserts the
// scanner walks both and emits a Progress event per page.
func TestEC2InstanceScannerPaginates(t *testing.T) {
	launch := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeEC2{
		instances: []*ec2.DescribeInstancesOutput{
			{
				Reservations: []ec2types.Reservation{
					{Instances: []ec2types.Instance{{
						InstanceId: aws.String("i-1"),
						LaunchTime: &launch,
						State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
						Tags:       []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("prod-web")}},
					}}},
				},
				NextToken: aws.String("more"),
			},
			{
				Reservations: []ec2types.Reservation{
					{Instances: []ec2types.Instance{{
						InstanceId: aws.String("i-2"),
						State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped},
					}}},
				},
			},
		},
	}
	c := audit.NewForTest(audit.WithEC2(fake), audit.WithRegion("eu-west-1"), audit.WithAccount("123"))
	emit := &audit.RecordingEmitter{}

	got, err := EC2Instance{}.Scan(context.Background(), c, emit)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d resources, want 2", len(got))
	}
	if got[0].ID != "i-1" || got[0].Name != "prod-web" || got[0].State != "running" {
		t.Errorf("first instance = %+v, want id=i-1 name=prod-web state=running", got[0])
	}
	if got[0].Region != "eu-west-1" || got[0].Account != "123" {
		t.Errorf("region/account not threaded onto resource: %+v", got[0])
	}
	if !got[0].CreatedAt.Equal(launch) {
		t.Errorf("CreatedAt = %v, want %v", got[0].CreatedAt, launch)
	}
	if fake.instCalls != 2 {
		t.Errorf("DescribeInstances calls = %d, want 2 (pagination)", fake.instCalls)
	}
	if len(emit.Counts) != 2 || emit.Counts[1] != 2 {
		t.Errorf("Progress events = %v, want two events ending at 2", emit.Counts)
	}
}

// TestEC2VolumeScannerEmitsState ensures volume state is surfaced.
func TestEC2VolumeScannerEmitsState(t *testing.T) {
	fake := &fakeEC2{
		volumes: []*ec2.DescribeVolumesOutput{
			{Volumes: []ec2types.Volume{
				{VolumeId: aws.String("vol-1"), State: ec2types.VolumeStateAvailable},
				{VolumeId: aws.String("vol-2"), State: ec2types.VolumeStateInUse},
			}},
		},
	}
	c := audit.NewForTest(audit.WithEC2(fake))
	got, err := EC2Volume{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].State != "available" || got[1].State != "in-use" {
		t.Errorf("got %+v, want available + in-use", got)
	}
}

// TestEC2SnapshotScannerFiltersToSelfOwner pins down the "OwnerIds=self"
// invariant from ADR-0040 so a future refactor that drops the filter
// trips a test rather than scanning every public snapshot in the
// region.
func TestEC2SnapshotScannerFiltersToSelfOwner(t *testing.T) {
	fake := &fakeEC2{
		snapshots: []*ec2.DescribeSnapshotsOutput{
			{Snapshots: []ec2types.Snapshot{{SnapshotId: aws.String("snap-1"), State: ec2types.SnapshotStateCompleted}}},
		},
	}
	c := audit.NewForTest(audit.WithEC2(fake))
	_, err := EC2Snapshot{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(fake.snapshotInputs) == 0 {
		t.Fatal("DescribeSnapshots was not called")
	}
	owners := fake.snapshotInputs[0].OwnerIds
	if len(owners) != 1 || owners[0] != "self" {
		t.Errorf("OwnerIds = %v, want [self]", owners)
	}
}

// TestEC2EIPScannerLabelsState exercises the associated/unassociated
// branch the scanner derives from the AssociationId pointer.
func TestEC2EIPScannerLabelsState(t *testing.T) {
	fake := &fakeEC2{addresses: &ec2.DescribeAddressesOutput{
		Addresses: []ec2types.Address{
			{AllocationId: aws.String("eipalloc-1"), AssociationId: aws.String("eipassoc-1")},
			{AllocationId: aws.String("eipalloc-2")},
		},
	}}
	c := audit.NewForTest(audit.WithEC2(fake))
	got, err := EC2EIP{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].State != "in-use" || got[1].State != "unassociated" {
		t.Errorf("got %+v, want in-use + unassociated", got)
	}
}

// TestEC2NATGatewayScannerCapturesCreateTime asserts CreateTime is
// surfaced on the resource so ADR-0041's idleness probe has somewhere
// to anchor "first seen".
func TestEC2NATGatewayScannerCapturesCreateTime(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeEC2{
		natgws: []*ec2.DescribeNatGatewaysOutput{
			{NatGateways: []ec2types.NatGateway{
				{NatGatewayId: aws.String("nat-1"), State: ec2types.NatGatewayStateAvailable, CreateTime: &when},
			}},
		},
	}
	c := audit.NewForTest(audit.WithEC2(fake))
	got, err := EC2NATGateway{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || !got[0].CreatedAt.Equal(when) {
		t.Errorf("CreatedAt = %v, want %v", got[0].CreatedAt, when)
	}
}

// TestEC2ScannersBubbleSDKErrors confirms a transient SDK error is
// wrapped with the scanner's kind prefix so the surface log line
// identifies the culprit.
func TestEC2ScannersBubbleSDKErrors(t *testing.T) {
	boom := errors.New("boom")
	fake := &fakeEC2{failNext: boom}
	c := audit.NewForTest(audit.WithEC2(fake))
	_, err := EC2Instance{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if !errors.Is(err, boom) {
		t.Errorf("Scan err = %v, want one wrapping boom", err)
	}
}

// TestEC2ScannersRejectMissingClient guards the "no EC2 client wired"
// branch so a misconfigured Client surfaces a friendly error instead
// of a nil-pointer panic in the SDK paginator.
func TestEC2ScannersRejectMissingClient(t *testing.T) {
	c := audit.NewForTest()
	_, instErr := EC2Instance{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if instErr == nil {
		t.Error("EC2Instance.Scan with nil EC2 client returned nil error")
	}
	_, volErr := EC2Volume{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if volErr == nil {
		t.Error("EC2Volume.Scan with nil EC2 client returned nil error")
	}
}
