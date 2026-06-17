package pricing

import "testing"

// TestEmbeddedSnapshotsLoad verifies the embedded snapshot files parse
// cleanly at startup and cover the two regions ADR-0042 requires.
func TestEmbeddedSnapshotsLoad(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatalf("LoadError = %v", err)
	}
	required := []Region{"us-east-1", "ap-northeast-1"}
	for _, r := range required {
		if _, ok := Lookup(string(r)); !ok {
			t.Errorf("Lookup(%q) missing", r)
		}
	}
}

// TestSnapshotHasEC2Pricing spot-checks that the snapshot carries the
// instance-type table — a missing table here means every EC2 cost
// estimate would silently fall to Unavailable.
func TestSnapshotHasEC2Pricing(t *testing.T) {
	snap, ok := Lookup("us-east-1")
	if !ok {
		t.Fatal("us-east-1 snapshot missing")
	}
	if _, ok := snap.EC2Instance["t3.medium"]; !ok {
		t.Errorf("us-east-1 missing t3.medium pricing")
	}
	if snap.EBSSnapshot == nil || snap.EBSSnapshot.PerGBMonth <= 0 {
		t.Errorf("us-east-1 missing EBS snapshot pricing")
	}
	if snap.NATGateway == nil || snap.NATGateway.PerHour <= 0 {
		t.Errorf("us-east-1 missing NAT gateway pricing")
	}
}
