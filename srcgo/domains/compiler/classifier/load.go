package classifier

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Load reads every classification YAML from `dir` and builds a
// Classifier. `dir` must contain strategies.yaml + carrier.yaml +
// dbtype.yaml + constraint.yaml; any missing file returns an error.
//
// Per D30 the classifier is immutable after Load — no hot reload,
// no globals, no cache invalidation to worry about.
func Load(dir string) (*Classifier, error) {
	strat, err := loadStrategies(filepath.Join(dir, "strategies.yaml"))
	if err != nil {
		return nil, fmt.Errorf("classifier.Load: %w", err)
	}
	carrier, err := loadCarrier(filepath.Join(dir, "carrier.yaml"))
	if err != nil {
		return nil, fmt.Errorf("classifier.Load: %w", err)
	}
	dbtype, err := loadDbType(filepath.Join(dir, "dbtype.yaml"))
	if err != nil {
		return nil, fmt.Errorf("classifier.Load: %w", err)
	}
	constraint, err := loadConstraint(filepath.Join(dir, "constraint.yaml"))
	if err != nil {
		return nil, fmt.Errorf("classifier.Load: %w", err)
	}
	return &Classifier{
		strategyRank: strat,
		carrier:      carrier,
		dbtype:       dbtype,
		constraint:   constraint,
	}, nil
}

// --- YAML shapes -----------------------------------------------------------

type yamlStrategiesFile struct {
	Strategies []yamlStrategyEntry `yaml:"strategies"`
}

type yamlStrategyEntry struct {
	ID   string `yaml:"id"`
	Rank int32  `yaml:"rank"`
}

type yamlCarrierFile struct {
	Cells []yamlCell `yaml:"cells"`
}

type yamlDbTypeFile struct {
	Cells []yamlCell `yaml:"cells"`
}

type yamlConstraintFile struct {
	Cells      []yamlCell `yaml:"cells"`
	TableLevel []yamlCell `yaml:"table_level"`
}

type yamlCell struct {
	// Carrier / dbtype axes
	From   string `yaml:"from"`
	To     string `yaml:"to"`
	Family string `yaml:"family"`

	// Constraint axis
	Axis string `yaml:"axis"`
	Case string `yaml:"case"`

	// Shared
	Strategy  string `yaml:"strategy"`
	Using     string `yaml:"using"`
	CheckSQL  string `yaml:"check_sql"`
	Rationale string `yaml:"rationale"`
}

// --- File loaders ----------------------------------------------------------

func loadStrategies(path string) (map[planpb.Strategy]int32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f yamlStrategiesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[planpb.Strategy]int32, len(f.Strategies))
	for _, s := range f.Strategies {
		parsed, ok := parseStrategy(s.ID)
		if !ok {
			return nil, fmt.Errorf("%s: unknown strategy id %q", path, s.ID)
		}
		out[parsed] = s.Rank
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no strategies parsed", path)
	}
	return out, nil
}

func loadCarrier(path string) (map[carrierKey]Cell, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f yamlCarrierFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[carrierKey]Cell, len(f.Cells))
	for i, c := range f.Cells {
		if c.From == "" || c.To == "" {
			return nil, fmt.Errorf("%s: cell #%d missing from/to", path, i)
		}
		strat, ok := parseStrategy(c.Strategy)
		if !ok {
			return nil, fmt.Errorf("%s: cell %s→%s: unknown strategy %q", path, c.From, c.To, c.Strategy)
		}
		out[carrierKey{from: c.From, to: c.To}] = Cell{
			Strategy:  strat,
			Using:     c.Using,
			CheckSQL:  c.CheckSQL,
			Rationale: c.Rationale,
		}
	}
	return out, nil
}

func loadDbType(path string) (map[dbtypeKey]Cell, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f yamlDbTypeFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[dbtypeKey]Cell, len(f.Cells))
	for i, c := range f.Cells {
		if c.Family == "" || c.From == "" || c.To == "" {
			return nil, fmt.Errorf("%s: cell #%d missing family/from/to", path, i)
		}
		strat, ok := parseStrategy(c.Strategy)
		if !ok {
			return nil, fmt.Errorf("%s: cell %s %s→%s: unknown strategy %q", path, c.Family, c.From, c.To, c.Strategy)
		}
		out[dbtypeKey{family: c.Family, from: c.From, to: c.To}] = Cell{
			Strategy:  strat,
			Using:     c.Using,
			CheckSQL:  c.CheckSQL,
			Rationale: c.Rationale,
		}
	}
	return out, nil
}

func loadConstraint(path string) (map[constraintKey]Cell, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f yamlConstraintFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[constraintKey]Cell, len(f.Cells)+len(f.TableLevel))
	if err := ingestConstraintCells(path, f.Cells, out); err != nil {
		return nil, err
	}
	if err := ingestConstraintCells(path, f.TableLevel, out); err != nil {
		return nil, err
	}
	return out, nil
}

func ingestConstraintCells(path string, cells []yamlCell, out map[constraintKey]Cell) error {
	for i, c := range cells {
		if c.Axis == "" || c.Case == "" {
			return fmt.Errorf("%s: cell #%d missing axis/case", path, i)
		}
		strat, ok := parseStrategy(c.Strategy)
		if !ok {
			return fmt.Errorf("%s: cell %s.%s: unknown strategy %q", path, c.Axis, c.Case, c.Strategy)
		}
		key := constraintKey{axis: c.Axis, caseID: c.Case}
		if _, dup := out[key]; dup {
			return fmt.Errorf("%s: duplicate cell %s.%s", path, c.Axis, c.Case)
		}
		out[key] = Cell{
			Strategy:  strat,
			Using:     c.Using,
			CheckSQL:  c.CheckSQL,
			Rationale: c.Rationale,
		}
	}
	return nil
}

// parseStrategy maps a YAML strategy ID to the proto enum. Returns
// (value, true) on match, (0, false) otherwise.
func parseStrategy(s string) (planpb.Strategy, bool) {
	switch s {
	case "SAFE":
		return planpb.Strategy_SAFE, true
	case "LOSSLESS_USING":
		return planpb.Strategy_LOSSLESS_USING, true
	case "NEEDS_CONFIRM":
		return planpb.Strategy_NEEDS_CONFIRM, true
	case "DROP_AND_CREATE":
		return planpb.Strategy_DROP_AND_CREATE, true
	case "CUSTOM_MIGRATION":
		return planpb.Strategy_CUSTOM_MIGRATION, true
	}
	return planpb.Strategy_STRATEGY_UNSPECIFIED, false
}
