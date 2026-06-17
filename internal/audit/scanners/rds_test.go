package scanners

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

type fakeRDS struct {
	dbs       []*rds.DescribeDBInstancesOutput
	snapshots []*rds.DescribeDBSnapshotsOutput

	dbCalls, snapCalls int
}

func (f *fakeRDS) DescribeDBInstances(_ context.Context, _ *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if len(f.dbs) == 0 {
		return &rds.DescribeDBInstancesOutput{}, nil
	}
	f.dbCalls++
	out := f.dbs[0]
	f.dbs = f.dbs[1:]
	return out, nil
}

func (f *fakeRDS) DescribeDBSnapshots(_ context.Context, _ *rds.DescribeDBSnapshotsInput, _ ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error) {
	if len(f.snapshots) == 0 {
		return &rds.DescribeDBSnapshotsOutput{}, nil
	}
	f.snapCalls++
	out := f.snapshots[0]
	f.snapshots = f.snapshots[1:]
	return out, nil
}

func TestRDSInstanceScannerSurfacesTagsAndStatus(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeRDS{
		dbs: []*rds.DescribeDBInstancesOutput{
			{
				DBInstances: []rdstypes.DBInstance{{
					DBInstanceArn:        aws.String("arn:aws:rds:us-east-1:123:db:prod"),
					DBInstanceIdentifier: aws.String("prod"),
					DBInstanceStatus:     aws.String("available"),
					InstanceCreateTime:   &when,
					TagList:              []rdstypes.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
				}},
				Marker: aws.String("more"),
			},
			{
				DBInstances: []rdstypes.DBInstance{{
					DBInstanceArn:        aws.String("arn:aws:rds:us-east-1:123:db:staging"),
					DBInstanceIdentifier: aws.String("staging"),
					DBInstanceStatus:     aws.String("stopped"),
				}},
			},
		},
	}
	c := audit.NewForTest(audit.WithRDS(fake))
	got, err := RDSDBInstance{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d resources, want 2", len(got))
	}
	if got[0].Tags["env"] != "prod" || got[0].State != "available" {
		t.Errorf("first instance = %+v, want env=prod state=available", got[0])
	}
	if fake.dbCalls != 2 {
		t.Errorf("DescribeDBInstances calls = %d, want 2", fake.dbCalls)
	}
}

func TestRDSSnapshotScannerSurfacesCreateTime(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeRDS{
		snapshots: []*rds.DescribeDBSnapshotsOutput{
			{DBSnapshots: []rdstypes.DBSnapshot{{
				DBSnapshotArn:        aws.String("arn:snap-1"),
				DBSnapshotIdentifier: aws.String("snap-1"),
				Status:               aws.String("available"),
				SnapshotCreateTime:   &when,
			}}},
		},
	}
	c := audit.NewForTest(audit.WithRDS(fake))
	got, err := RDSDBSnapshot{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || !got[0].CreatedAt.Equal(when) {
		t.Errorf("got %+v, want one snapshot at %v", got, when)
	}
}
