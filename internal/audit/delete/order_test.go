package delete

import (
	"errors"
	"testing"
)

func TestOrder_NoDeps(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{ID: "b", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}},
		{ID: "a", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}},
	}
	out, err := Order(rows)
	if err != nil {
		t.Fatal(err)
	}
	// Stable tie-break is by (Kind, Identifier): vol-a < vol-b.
	if out[0].ID != "a" || out[1].ID != "b" {
		t.Errorf("Order without deps = %v, want [a b] (sorted by identifier)",
			[]string{out[0].ID, out[1].ID})
	}
}

func TestOrder_RespectsDependsOn(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{ID: "snap", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-1"}, DependsOn: []string{"ami"}},
		{ID: "ami", Resource: Resource{Kind: KindEC2Volume, Identifier: "ami-fake"}},
	}
	out, err := Order(rows)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, r := range out {
		pos[r.ID] = i
	}
	if pos["ami"] >= pos["snap"] {
		t.Errorf("ami must come before snap: ami@%d snap@%d", pos["ami"], pos["snap"])
	}
}

func TestOrder_IgnoresExternalDeps(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{ID: "a", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}, DependsOn: []string{"not-in-batch"}},
	}
	out, err := Order(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "a" {
		t.Errorf("Order = %+v, want one row 'a'", out)
	}
}

func TestOrder_DetectsCycle(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{ID: "a", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-a"}, DependsOn: []string{"b"}},
		{ID: "b", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-b"}, DependsOn: []string{"a"}},
	}
	_, err := Order(rows)
	if !errors.Is(err, ErrCyclicDependency) {
		t.Errorf("Order(cycle) = %v, want ErrCyclicDependency", err)
	}
}

func TestOrder_StableAcrossInputOrder(t *testing.T) {
	t.Parallel()
	// Same set, different input order; result must match.
	a := []Row{
		{ID: "x", Resource: Resource{Kind: KindEC2Volume, Identifier: "vol-x"}},
		{ID: "y", Resource: Resource{Kind: KindEC2Snapshot, Identifier: "snap-y"}},
		{ID: "z", Resource: Resource{Kind: KindEC2EIP, Identifier: "eipalloc-z"}},
	}
	b := []Row{a[2], a[0], a[1]}
	oa, _ := Order(a)
	ob, _ := Order(b)
	for i := range oa {
		if oa[i].ID != ob[i].ID {
			t.Errorf("Order not stable: %d: %s vs %s", i, oa[i].ID, ob[i].ID)
		}
	}
}
