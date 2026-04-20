package ir

// Check is a column-level CHECK constraint in tagged-union form. Emitters
// type-switch on the variant and render dialect-specific SQL.
//
// Variants cover the validation surface from (w17.field) minus the facts
// that resolve into column type/modifiers (pk, null, unique, max_len on
// CHAR/SLUG, precision/scale on DECIMAL).
type Check interface {
	isCheck()
}

// LengthCheck — char_length bounds on string carriers whose column type
// doesn't already enforce them. For CHAR/SLUG the VARCHAR(N) sizing covers
// the upper bound and no LengthCheck is added; other string types use a
// CHECK. MinLen always renders as a CHECK when present.
type LengthCheck struct {
	Min    int32
	Max    int32
	HasMin bool
	HasMax bool
}

// BlankCheck — the implicit `CHECK col <> ''` on string carriers. Absent
// when the author opted in to (w17.field).blank = true.
type BlankCheck struct{}

// RangeCheck — numeric bounds. Pointers distinguish 0 from unset since 0 is
// a valid bound.
type RangeCheck struct {
	Gt, Gte, Lt, Lte *float64
}

// RegexCheck — author-supplied pattern OR the default pattern implied by
// type (SLUG, UUID, EMAIL, URL). Emitters render PG-flavoured regex (`col ~
// 'pat'`) and must escape the pattern per dialect.
type RegexCheck struct {
	Pattern string
	// Source records whether this regex is an author override or a
	// type-defaulted pattern. Emitters treat them identically; this is
	// kept so --verbose can label the CHECK clearly.
	Source RegexSource
}

type RegexSource int

const (
	RegexFromType RegexSource = iota
	RegexFromPattern
)

// ChoicesCheck — resolved proto enum values for (w17.field).choices.
// EnumFQN is kept alongside for error messages; emitters use Values.
type ChoicesCheck struct {
	EnumFQN string
	Values  []string
}

func (LengthCheck) isCheck()  {}
func (BlankCheck) isCheck()   {}
func (RangeCheck) isCheck()   {}
func (RegexCheck) isCheck()   {}
func (ChoicesCheck) isCheck() {}
