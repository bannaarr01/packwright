package delete

import (
	"errors"
	"fmt"
	"sort"
)

// ErrCyclicDependency is returned by Order when the supplied rows
// have a DependsOn cycle. Topo sort cannot proceed; the caller
// should refuse the batch and surface the cycle to the user.
var ErrCyclicDependency = errors.New("delete: rows have a cyclic dependency")

// Order returns rows in a dependency-safe deletion order. A row's
// DependsOn IDs must all appear before it in the result; ties are
// broken by (Kind, Identifier) so the order is stable across runs.
// IDs in DependsOn that do not correspond to a row in the input
// are ignored — they represent dependents outside the current
// batch and have no bearing on intra-batch ordering.
//
// ADR-0043's examples ("snapshots after AMIs", "target groups
// after listeners") describe ordering between rows the user
// staged together. External blocking dependents (an AMI that
// blocks a snapshot but is not itself in the batch) are surfaced
// by the dependency probe as Blocking dependents and reject the
// row at consent time — they never reach Order.
func Order(rows []Row) ([]Row, error) {
	n := len(rows)
	if n <= 1 {
		out := make([]Row, n)
		copy(out, rows)
		return out, nil
	}
	idx := make(map[string]int, n)
	for i, r := range rows {
		idx[r.ID] = i
	}
	indeg := make([]int, n)
	edges := make([][]int, n) // edges[i] = predecessor rows that depend on i? we want from→to
	for i, r := range rows {
		for _, dep := range r.DependsOn {
			j, ok := idx[dep]
			if !ok {
				continue
			}
			edges[j] = append(edges[j], i)
			indeg[i]++
		}
	}
	// Kahn's algorithm with deterministic tie-breaking on
	// (Kind, Identifier) so two equal-priority rows always emerge
	// in the same order.
	ready := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			ready = append(ready, i)
		}
	}
	out := make([]Row, 0, n)
	for len(ready) > 0 {
		sort.Slice(ready, func(a, b int) bool {
			ra, rb := rows[ready[a]], rows[ready[b]]
			if ra.Resource.Kind != rb.Resource.Kind {
				return ra.Resource.Kind < rb.Resource.Kind
			}
			return ra.Resource.Identifier < rb.Resource.Identifier
		})
		i := ready[0]
		ready = ready[1:]
		out = append(out, rows[i])
		for _, j := range edges[i] {
			indeg[j]--
			if indeg[j] == 0 {
				ready = append(ready, j)
			}
		}
	}
	if len(out) != n {
		return nil, fmt.Errorf("%w: %d of %d rows remain", ErrCyclicDependency, n-len(out), n)
	}
	return out, nil
}
