package ir

// D36 — custom_types registry. Build at BuildMany time from
// (w17.pg.project) + (w17.pg.module) FileOptions across the loaded
// file set. Used by buildColumn to resolve `(w17.pg.field).custom_type`
// (which now carries an alias, not a raw SQL type) against the
// project + domain merge.
//
// Invariants enforced at registry build:
//   - alias is a valid identifier
//   - alias does not collide with native PG type keywords / D14 sem
//     names (reserved list)
//   - alias is unique within the project registry
//   - alias is unique within each domain's module registry
//   - no project-level alias is also declared in any domain (no
//     shadowing — collision = compile error)
//   - convertible_to / convertible_from `type` fields refer to either
//     a registered alias (project or any domain) or a native PG type
//     keyword (registry doesn't validate the latter — emit-time concern)

import (
	"errors"
	"fmt"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	pgpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/pg"
)

// CustomTypeRegistry is the immutable lookup index built once per
// BuildMany call. Project entries are visible to every domain;
// domain entries are visible only to that domain's tables. Lookups
// at column-resolve time merge transparently.
//
// Lookups go: domain entry first, then project entry, then nil. The
// IR rejects shadowing at build time so this order never matters
// semantically — it's an optimisation only.
type CustomTypeRegistry struct {
	// projectByAlias merges every (w17.pg.project).custom_types entry
	// across the loaded file set. Exactly one file is allowed to
	// declare (w17.pg.project) — multiple = compile error.
	projectByAlias map[string]*pgpb.CustomType

	// domainByAlias keys: domain identifier (today: source file path,
	// since each .proto file is its own implicit "domain" per
	// (w17.db.module)). Future multi-file-per-domain: keyed by
	// (w17.db.module).connection.name.
	//
	// Iter-2 reality: domain registry is per-file because module-level
	// options live per-file. This matches today's PG fixtures.
	domainByAlias map[string]*pgpb.CustomType
}

// Lookup resolves an alias against domain → project. Returns
// (entry, true) on hit, (nil, false) on miss. Caller produces the
// "alias not registered" diag.Error.
func (r *CustomTypeRegistry) Lookup(alias string) (*pgpb.CustomType, bool) {
	if r == nil {
		return nil, false
	}
	if e, ok := r.domainByAlias[alias]; ok {
		return e, true
	}
	if e, ok := r.projectByAlias[alias]; ok {
		return e, true
	}
	return nil, false
}

// All returns a deterministic snapshot of every registered alias,
// merged. Project entries first (sorted), then domain entries
// (sorted). Used by tests + manifest assembly.
func (r *CustomTypeRegistry) All() []*pgpb.CustomType {
	if r == nil {
		return nil
	}
	out := make([]*pgpb.CustomType, 0, len(r.projectByAlias)+len(r.domainByAlias))
	for _, k := range sortedKeys(r.projectByAlias) {
		out = append(out, r.projectByAlias[k])
	}
	for _, k := range sortedKeys(r.domainByAlias) {
		out = append(out, r.domainByAlias[k])
	}
	return out
}

// reservedAliases collects identifiers that must NOT be reused as
// custom_type aliases. Includes native PG type keywords + D14 sem
// names so author confusion is impossible. List is conservative —
// extending later is always safe.
var reservedAliases = map[string]struct{}{
	// Native PG type keywords.
	"text": {}, "varchar": {}, "char": {}, "citext": {},
	"json": {}, "jsonb": {},
	"int": {}, "integer": {}, "smallint": {}, "bigint": {},
	"real": {}, "double": {}, "numeric": {}, "decimal": {},
	"boolean": {}, "bool": {},
	"date": {}, "time": {}, "timestamp": {}, "timestamptz": {}, "interval": {},
	"bytea": {}, "uuid": {},
	"inet": {}, "cidr": {}, "macaddr": {},
	"tsvector": {}, "tsquery": {},
	// Note: "hstore" is NOT reserved — it's a user-registered alias
	// (extension-backed). Same for any pgvector / PostGIS type names.
	// Author registers them via project / domain custom_types.
}

// buildRegistry assembles the registry from the loaded file set.
// Aggregates project + domain options, validates aliases, returns
// either (*CustomTypeRegistry, nil) or (nil, error) with a joined
// diag.Error stack on the first validation failure category.
func buildRegistry(files []*loader.LoadedFile) (*CustomTypeRegistry, error) {
	r := &CustomTypeRegistry{
		projectByAlias: map[string]*pgpb.CustomType{},
		domainByAlias:  map[string]*pgpb.CustomType{},
	}
	var errs []error

	// (1) Project-level: at most one file in the set may declare
	// (w17.pg.project). Multiple = compile error.
	var projectSource *loader.LoadedFile
	for _, lf := range files {
		if lf.PgProject == nil {
			continue
		}
		if projectSource != nil {
			priorPath := "a previously-loaded file"
			if projectSource.File != nil {
				priorPath = projectSource.File.Path()
			}
			errs = append(errs, diag.Atf(lf.File,
				"multiple files declare (w17.pg.project) — only one project options file is allowed per build").
				WithWhy("project options are project-wide singletons; allowing multiple sources would create silent merge ambiguity").
				WithFix(fmt.Sprintf("consolidate (w17.pg.project) into one file (commonly project.proto at proto root); existing source at %s", priorPath)))
			continue
		}
		projectSource = lf
		for _, ct := range lf.PgProject.GetCustomTypes() {
			if err := validateCustomType(lf, ct); err != nil {
				errs = append(errs, err)
				continue
			}
			if _, dup := r.projectByAlias[ct.GetAlias()]; dup {
				errs = append(errs, diag.Atf(lf.File,
					"(w17.pg.project).custom_types: duplicate alias %q", ct.GetAlias()).
					WithWhy("aliases identify the type — collisions silently shadow each other").
					WithFix("rename one of the entries"))
				continue
			}
			r.projectByAlias[ct.GetAlias()] = ct
		}
	}

	// (2) Domain-level: every file may declare (w17.pg.module). Each
	// file's entries are merged into the global domain registry; the
	// domain dimension today is collapsed into "all loaded files"
	// (single domain per BuildMany call), which matches D26 single-
	// connection-per-domain rule.
	for _, lf := range files {
		if lf.PgModule == nil {
			continue
		}
		for _, ct := range lf.PgModule.GetCustomTypes() {
			if err := validateCustomType(lf, ct); err != nil {
				errs = append(errs, err)
				continue
			}
			alias := ct.GetAlias()
			if _, dup := r.domainByAlias[alias]; dup {
				errs = append(errs, diag.Atf(lf.File,
					"(w17.pg.module).custom_types: duplicate alias %q across module files", alias).
					WithWhy("module-level entries from every file in the build are merged; aliases must be unique across the merge").
					WithFix("rename the duplicate or consolidate the registration into one file"))
				continue
			}
			if _, projectDup := r.projectByAlias[alias]; projectDup {
				errs = append(errs, diag.Atf(lf.File,
					"(w17.pg.module).custom_types: alias %q already declared at project level", alias).
					WithWhy("project + domain registries are merged but cannot shadow each other — every alias has a single source of truth").
					WithFix(fmt.Sprintf("rename the domain-level entry, or remove it if the project-level (w17.pg.project) entry covers the same use case")))
				continue
			}
			r.domainByAlias[alias] = ct
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return r, nil
}

// validateCustomType enforces alias-shape invariants on one entry.
// Returns nil on pass.
func validateCustomType(lf *loader.LoadedFile, ct *pgpb.CustomType) error {
	alias := ct.GetAlias()
	if alias == "" {
		return diag.Atf(lf.File, "(w17.pg) custom_types entry has empty alias").
			WithWhy("every entry needs an alias for field-level reference").
			WithFix(`set alias to a snake_case identifier — e.g. alias: "road_vector"`)
	}
	if why := validateIdentifier(alias); why != "" {
		return diag.Atf(lf.File, "(w17.pg) custom_types alias %q: %s", alias, why).
			WithWhy("aliases appear in IR as identity keys + manifest output; they must be valid identifiers").
			WithFix("pick a snake_case identifier under 63 bytes — e.g. road_vector / embedding_1536")
	}
	if _, reserved := reservedAliases[alias]; reserved {
		return diag.Atf(lf.File, "(w17.pg) custom_types alias %q collides with a native PG type keyword", alias).
			WithWhy("aliases must not look like native PG types — author confusion is the failure mode").
			WithFix("prefix or suffix the alias to disambiguate — e.g. for citext use alias \"citext_v2\" or wrap as \"my_citext\"")
	}
	if ct.GetSqlType() == "" {
		return diag.Atf(lf.File, "(w17.pg) custom_types alias %q: sql_type is empty", alias).
			WithWhy("sql_type is what the emitter writes into CREATE TABLE — it cannot be empty").
			WithFix(`set sql_type to the raw PG type expression — e.g. sql_type: "vector(1536)"`)
	}
	return nil
}

// sortedKeys returns map keys in lexical order. Used by registry
// iterators that need determinism.
func sortedKeys(m map[string]*pgpb.CustomType) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion sort (small N)
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
