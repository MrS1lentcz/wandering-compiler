// Package memory provides in-memory implementations of engine.Sink
// and engine.ResolutionSource. Their primary use is unit testing —
// the Plan() pipeline can be exercised end-to-end without touching
// the filesystem or any external service.
//
// Both types are safe to use without initialisation (zero-value
// friendly) and expose their captured state as plain fields for
// test assertions.
package memory

import (
	"sync"

	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Source is an engine.ResolutionSource backed by a map. Add
// resolutions via Add or by assigning the exported fields directly.
// Zero value is usable.
type Source struct {
	mu sync.RWMutex
	// byID indexes resolutions by finding_id. Separate from the
	// ordered slice so Lookup is O(1) while All preserves insertion
	// order.
	byID    map[string]*planpb.Resolution
	ordered []*planpb.Resolution
}

// NewSource returns a Source seeded with the given resolutions.
// Nil-safe.
func NewSource(resolutions ...*planpb.Resolution) *Source {
	s := &Source{}
	for _, r := range resolutions {
		s.Add(r)
	}
	return s
}

// Add registers a resolution. Later Adds for the same finding_id
// overwrite earlier ones — callers that want "first write wins"
// should check Lookup before Add.
func (s *Source) Add(r *planpb.Resolution) {
	if r == nil || r.GetFindingId() == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = make(map[string]*planpb.Resolution)
	}
	if _, existed := s.byID[r.GetFindingId()]; !existed {
		s.ordered = append(s.ordered, r)
	} else {
		// Replace in-place while keeping ordered-slice stable.
		for i, existing := range s.ordered {
			if existing.GetFindingId() == r.GetFindingId() {
				s.ordered[i] = r
				break
			}
		}
	}
	s.byID[r.GetFindingId()] = r
}

// Lookup implements engine.ResolutionSource.
func (s *Source) Lookup(findingID string) (*planpb.Resolution, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byID[findingID]
	return r, ok
}

// All implements engine.ResolutionSource.
func (s *Source) All() []*planpb.Resolution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*planpb.Resolution, len(s.ordered))
	copy(out, s.ordered)
	return out
}

// Sink is an engine.Sink that captures the last plan it was given.
// Useful for test assertions on what the engine produced without
// touching a filesystem. Zero value is usable.
type Sink struct {
	mu    sync.Mutex
	Plans []*planpb.Plan
}

// Write implements engine.Sink. Stores the plan; returns nil.
func (s *Sink) Write(plan *planpb.Plan) error {
	if plan == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Plans = append(s.Plans, plan)
	return nil
}

// Last returns the most recently written plan, or nil if Write was
// never called. Convenience for single-run tests.
func (s *Sink) Last() *planpb.Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Plans) == 0 {
		return nil
	}
	return s.Plans[len(s.Plans)-1]
}

// Count returns how many plans have been written.
func (s *Sink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Plans)
}
