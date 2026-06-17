package scanners

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

type fakeEFS struct {
	pages   []*efs.DescribeFileSystemsOutput
	calls   int
	markers []*string
}

func (f *fakeEFS) DescribeFileSystems(_ context.Context, in *efs.DescribeFileSystemsInput, _ ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error) {
	f.markers = append(f.markers, in.Marker)
	if len(f.pages) == 0 {
		return &efs.DescribeFileSystemsOutput{}, nil
	}
	f.calls++
	out := f.pages[0]
	f.pages = f.pages[1:]
	return out, nil
}

func TestEFSScannerWalksMarker(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeEFS{
		pages: []*efs.DescribeFileSystemsOutput{
			{
				FileSystems: []efstypes.FileSystemDescription{{
					FileSystemArn:  aws.String("arn:efs-1"),
					Name:           aws.String("logs"),
					LifeCycleState: efstypes.LifeCycleStateAvailable,
					CreationTime:   &when,
				}},
				NextMarker: aws.String("page-2"),
			},
			{
				FileSystems: []efstypes.FileSystemDescription{{
					FileSystemArn:  aws.String("arn:efs-2"),
					Tags:           []efstypes.Tag{{Key: aws.String("Name"), Value: aws.String("backups")}},
					LifeCycleState: efstypes.LifeCycleStateAvailable,
				}},
			},
		},
	}
	c := audit.NewForTest(audit.WithEFS(fake))
	got, err := EFSFileSystem{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].Name != "logs" || got[1].Name != "backups" {
		t.Errorf("got %+v, want logs + backups", got)
	}
	if fake.calls != 2 {
		t.Errorf("DescribeFileSystems calls = %d, want 2 (marker pagination)", fake.calls)
	}
	if len(fake.markers) < 2 || aws.ToString(fake.markers[1]) != "page-2" {
		t.Errorf("second call marker = %v, want page-2", fake.markers)
	}
}
