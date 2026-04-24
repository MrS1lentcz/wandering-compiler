package emit

import "sort"

// Usage is the per-migration capability-usage collector. Emitters call
// Use(capID) at every dispatch site that references a D16 capability;
// the engine reads Sorted() after emission to populate
// Migration.Manifest.Capabilities.
//
// Nil-safe: a nil *Usage drops every Use call silently. This lets
// callers that don't care about tracking (ad-hoc fixtures, low-level
// unit tests) call EmitOp with usage=nil without boilerplate.
//
// Sorted() output is deterministic — idempotence (iter-1 AC #4 / D30)
// requires identical plans to produce identical manifests.
type Usage struct {
	set map[string]struct{}
}

// NewUsage returns a fresh, empty collector.
func NewUsage() *Usage {
	return &Usage{set: map[string]struct{}{}}
}

// Use records a capability ID. Empty cap IDs and nil receivers are
// no-ops; lazy-init of the backing map keeps zero-value literals
// (`&emit.Usage{}`) usable.
func (u *Usage) Use(cap string) {
	if u == nil || cap == "" {
		return
	}
	if u.set == nil {
		u.set = map[string]struct{}{}
	}
	u.set[cap] = struct{}{}
}

// Sorted returns the recorded capability IDs in lexical order,
// deduped. Returns nil (not an empty slice) when nothing was
// recorded so callers can test `len(u.Sorted()) == 0` with the
// same result as `usage == nil`.
func (u *Usage) Sorted() []string {
	if u == nil || len(u.set) == 0 {
		return nil
	}
	out := make([]string, 0, len(u.set))
	for cap := range u.set {
		out = append(out, cap)
	}
	sort.Strings(out)
	return out
}
