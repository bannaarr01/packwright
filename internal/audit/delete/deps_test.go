package delete

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
)

func TestProbe_Volume_AttachedIsBlocking(t *testing.T) {
	t.Parallel()
	ec2c := &fakeEC2{
		descVolumes: &ec2.DescribeVolumesOutput{
			Volumes: []ec2types.Volume{{
				VolumeId: aws.String("vol-1"),
				Attachments: []ec2types.VolumeAttachment{{
					InstanceId: aws.String("i-abc"),
					Device:     aws.String("/dev/sda1"),
				}},
			}},
		},
	}
	row := Row{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}}
	rd := ProbeRow(context.Background(), &Clients{EC2: ec2c}, row)
	if rd.Err != nil {
		t.Fatalf("probe error: %v", rd.Err)
	}
	if !rd.Blocked {
		t.Errorf("attached volume should be Blocked")
	}
	if len(rd.Dependents) != 1 || rd.Dependents[0].Identifier != "i-abc" {
		t.Errorf("dependents = %+v", rd.Dependents)
	}
}

func TestProbe_Snapshot_BackingAMIIsBlocking(t *testing.T) {
	t.Parallel()
	ec2c := &fakeEC2{
		descImages: &ec2.DescribeImagesOutput{
			Images: []ec2types.Image{{ImageId: aws.String("ami-1")}},
		},
		descInstances: &ec2.DescribeInstancesOutput{
			Reservations: []ec2types.Reservation{{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-1")}, {InstanceId: aws.String("i-2")}},
			}},
		},
	}
	row := Row{ID: "r1", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-1"}}
	rd := ProbeRow(context.Background(), &Clients{EC2: ec2c}, row)
	if rd.Err != nil {
		t.Fatalf("probe error: %v", rd.Err)
	}
	if !rd.Blocked {
		t.Errorf("snapshot referenced by an AMI should be Blocked")
	}
	if len(rd.Dependents) != 1 || rd.Dependents[0].Identifier != "ami-1" {
		t.Fatalf("dependents = %+v", rd.Dependents)
	}
	if !strings.Contains(rd.Dependents[0].Detail, "2 EC2") {
		t.Errorf("detail = %q, want to mention '2 EC2'", rd.Dependents[0].Detail)
	}
}

func TestProbe_Snapshot_NoAMINoDeps(t *testing.T) {
	t.Parallel()
	ec2c := &fakeEC2{} // empty DescribeImages
	row := Row{ID: "r1", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-1"}}
	rd := ProbeRow(context.Background(), &Clients{EC2: ec2c}, row)
	if rd.Err != nil {
		t.Fatalf("probe: %v", rd.Err)
	}
	if rd.Blocked {
		t.Errorf("unreferenced snapshot should not be Blocked")
	}
	if len(rd.Dependents) != 0 {
		t.Errorf("dependents = %+v, want empty", rd.Dependents)
	}
}

func TestProbe_TargetGroup_ListenerForwardIsBlocking(t *testing.T) {
	t.Parallel()
	tgARN := "arn:aws:elasticloadbalancing:tg/test"
	lbARN := "arn:aws:elasticloadbalancing:lb/test"
	elbv2c := &fakeELBv2{
		descTargetGroup: &elasticloadbalancingv2.DescribeTargetGroupsOutput{
			TargetGroups: []elbv2types.TargetGroup{{
				TargetGroupArn:   aws.String(tgARN),
				LoadBalancerArns: []string{lbARN},
			}},
		},
		descListeners: &elasticloadbalancingv2.DescribeListenersOutput{
			Listeners: []elbv2types.Listener{{
				ListenerArn: aws.String("arn:aws:elasticloadbalancing:listener/test"),
				DefaultActions: []elbv2types.Action{{
					TargetGroupArn: aws.String(tgARN),
				}},
			}},
		},
	}
	row := Row{ID: "r1", Resource: Resource{Kind: KindELBv2TargetGroup, Identifier: tgARN}}
	rd := ProbeRow(context.Background(), &Clients{ELBv2: elbv2c}, row)
	if rd.Err != nil {
		t.Fatalf("probe: %v", rd.Err)
	}
	if !rd.Blocked {
		t.Errorf("target group with active listener should be Blocked; got deps=%+v", rd.Dependents)
	}
}

func TestProbe_LogGroup_LambdaPrefixIsInformational(t *testing.T) {
	t.Parallel()
	logc := &fakeLogs{
		descOut: &cloudwatchlogs.DescribeLogGroupsOutput{
			LogGroups: []cwltypes.LogGroup{{LogGroupName: aws.String("/aws/lambda/old-fn")}},
		},
	}
	row := Row{ID: "r1", Resource: Resource{Kind: KindLogsLogGroup, Identifier: "/aws/lambda/old-fn"}}
	rd := ProbeRow(context.Background(), &Clients{Logs: logc}, row)
	if rd.Err != nil {
		t.Fatalf("probe: %v", rd.Err)
	}
	if rd.Blocked {
		t.Errorf("log group should never be Blocked")
	}
	if len(rd.Dependents) != 1 || rd.Dependents[0].Kind != "lambda/function" {
		t.Errorf("dependents = %+v, want a lambda/function hint", rd.Dependents)
	}
}

func TestProbe_RDSSnapshot_DeletedSourceIsInformational(t *testing.T) {
	t.Parallel()
	rdsc := &fakeRDS{
		descSnapshots: &rds.DescribeDBSnapshotsOutput{
			DBSnapshots: []rdstypes.DBSnapshot{{
				DBSnapshotIdentifier: aws.String("rds:db-stale-2025"),
				DBInstanceIdentifier: aws.String("db-stale"),
			}},
		},
		// Source DB does not exist any more — DescribeDBInstances
		// returns the documented not-found error.
		describeInstErr: &dbInstanceNotFound{},
	}
	row := Row{ID: "r1", Resource: Resource{Kind: KindRDSDBSnapshot, Identifier: "rds:db-stale-2025"}}
	rd := ProbeRow(context.Background(), &Clients{RDS: rdsc}, row)
	if rd.Err != nil {
		t.Fatalf("probe: %v", rd.Err)
	}
	if rd.Blocked {
		t.Errorf("rds snapshot with deleted source should not be Blocked")
	}
	if len(rd.Dependents) != 1 {
		t.Fatalf("dependents = %+v, want 1 informational entry", rd.Dependents)
	}
	if !strings.Contains(rd.Dependents[0].Detail, "deleted") {
		t.Errorf("detail = %q, want it to mention 'deleted'", rd.Dependents[0].Detail)
	}
}

// dbInstanceNotFound emulates the AWS SDK's DBInstanceNotFound API
// error well enough for our errors.As switch to take the right branch.
type dbInstanceNotFound struct{}

func (*dbInstanceNotFound) Error() string     { return "DBInstanceNotFound" }
func (*dbInstanceNotFound) ErrorCode() string { return "DBInstanceNotFound" }

func TestProbe_ECRImage_TaggedDigestIsInformational(t *testing.T) {
	t.Parallel()
	ecrc := &fakeECR{
		descOut: &ecr.DescribeImagesOutput{
			ImageDetails: []ecrtypes.ImageDetail{{
				ImageDigest: aws.String("sha256:abc"),
				ImageTags:   []string{"v1", "latest"},
			}},
		},
	}
	row := Row{ID: "r1", Resource: Resource{
		Kind:       KindECRImage,
		Identifier: "sha256:abc",
		Extra:      map[string]string{"repository_name": "myapp"},
	}}
	rd := ProbeRow(context.Background(), &Clients{ECR: ecrc}, row)
	if rd.Err != nil {
		t.Fatalf("probe: %v", rd.Err)
	}
	if rd.Blocked {
		t.Errorf("tagged digest should not be Blocked")
	}
	if len(rd.Dependents) != 2 {
		t.Errorf("dependents = %+v, want 2 tag entries", rd.Dependents)
	}
}

func TestProbe_UnknownKindReturnsEmpty(t *testing.T) {
	t.Parallel()
	row := Row{ID: "r1", Resource: Resource{Kind: "what/is-this", Identifier: "x"}}
	rd := ProbeRow(context.Background(), &Clients{}, row)
	if rd.Err != nil {
		t.Errorf("Err = %v, want nil", rd.Err)
	}
	if rd.Blocked || len(rd.Dependents) != 0 {
		t.Errorf("unknown kind should return empty rd, got %+v", rd)
	}
}

func TestProbe_ServiceUnavailableSurfacesError(t *testing.T) {
	t.Parallel()
	row := Row{ID: "r1", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-1"}}
	rd := ProbeRow(context.Background(), &Clients{}, row)
	if rd.Err == nil {
		t.Fatal("probe with nil EC2 client should return err")
	}
}
