package postgres

import (
	"fmt"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// emitAddForeignKey renders ALTER TABLE … ADD CONSTRAINT <name>
// FOREIGN KEY (col) REFERENCES <tgt>(<tgt_col>) [ON DELETE <action>];
// down drops the constraint.
func (e Emitter) emitAddForeignKey(afk *planpb.AddForeignKey, usage *emit.Usage) (string, string, error) {
	fk := afk.GetFk()
	if fk == nil {
		return "", "", fmt.Errorf("postgres: AddForeignKey with nil ForeignKey")
	}
	tbl := tableShellFromCtx(afk.GetCtx(), nil)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
	recordFKActionCap(usage, fk.GetOnDelete())
	qual := qualifiedTable(tbl)
	srcCol, err := resolveSqlColName(afk.GetColumns(), fk.GetColumn())
	if err != nil {
		return "", "", fmt.Errorf("postgres: AddForeignKey: %w", err)
	}
	up := renderAddFK(qual, afk.GetConstraintName(), srcCol, fk, tbl)
	down := renderDropFK(qual, afk.GetConstraintName())
	return up, down, nil
}

// recordFKActionCap tags the catalog cap for a non-default ON DELETE
// action. CASCADE / SET NULL are universal and need no cap. RESTRICT
// and SET DEFAULT get explicit caps so the manifest reflects them.
func recordFKActionCap(usage *emit.Usage, a irpb.FKAction) {
	switch a {
	case irpb.FKAction_FK_ACTION_RESTRICT:
		usage.Use(emit.CapOnDeleteRestrict)
	case irpb.FKAction_FK_ACTION_SET_DEFAULT:
		usage.Use(emit.CapOnDeleteSetDefault)
	}
}

// emitDropForeignKey renders ALTER TABLE … DROP CONSTRAINT <name>;
// down recreates via the carried prev-side ForeignKey.
func (e Emitter) emitDropForeignKey(dfk *planpb.DropForeignKey) (string, string, error) {
	fk := dfk.GetFk()
	if fk == nil {
		return "", "", fmt.Errorf("postgres: DropForeignKey with nil ForeignKey")
	}
	tbl := tableShellFromCtx(dfk.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	srcCol, err := resolveSqlColName(dfk.GetColumns(), fk.GetColumn())
	if err != nil {
		return "", "", fmt.Errorf("postgres: DropForeignKey: %w", err)
	}
	up := renderDropFK(qual, dfk.GetConstraintName())
	down := renderAddFK(qual, dfk.GetConstraintName(), srcCol, fk, tbl)
	return up, down, nil
}

// emitReplaceForeignKey emits drop+add on both sides — PG can ALTER
// CONSTRAINT only for DEFERRABLE (out of scope iter-2). Any change
// to columns / target / on_delete forces drop+recreate.
func (e Emitter) emitReplaceForeignKey(rfk *planpb.ReplaceForeignKey, usage *emit.Usage) (string, string, error) {
	from, to := rfk.GetFrom(), rfk.GetTo()
	if from == nil || to == nil {
		return "", "", fmt.Errorf("postgres: ReplaceForeignKey missing from/to")
	}
	tbl := tableShellFromCtx(rfk.GetCtx(), nil)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
	recordFKActionCap(usage, to.GetOnDelete())
	recordFKActionCap(usage, from.GetOnDelete())
	qual := qualifiedTable(tbl)
	fromCol, err := resolveSqlColName(rfk.GetColumns(), from.GetColumn())
	if err != nil {
		return "", "", fmt.Errorf("postgres: ReplaceForeignKey from: %w", err)
	}
	toCol, err := resolveSqlColName(rfk.GetColumns(), to.GetColumn())
	if err != nil {
		return "", "", fmt.Errorf("postgres: ReplaceForeignKey to: %w", err)
	}
	up := renderDropFK(qual, rfk.GetConstraintName()) + "\n" +
		renderAddFK(qual, rfk.GetConstraintName(), toCol, to, tbl)
	down := renderDropFK(qual, rfk.GetConstraintName()) + "\n" +
		renderAddFK(qual, rfk.GetConstraintName(), fromCol, from, tbl)
	return up, down, nil
}

// renderAddFK builds one ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY …
// REFERENCES … [ON DELETE …]; statement.
func renderAddFK(qualTable, name, srcCol string, fk *irpb.ForeignKey, tbl *irpb.Table) string {
	tgt := qualifiedIdentifier(tbl, fk.GetTargetTable())
	core := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s)",
		qualTable, name, srcCol, tgt, fk.GetTargetColumn())
	if action := fkActionSQL(fk.GetOnDelete()); action != "" {
		return fmt.Sprintf("%s ON DELETE %s;", core, action)
	}
	return core + ";"
}

// renderDropFK builds one ALTER TABLE … DROP CONSTRAINT IF EXISTS <name>;
// IF EXISTS keeps re-runs idempotent in face of partial-failure repair.
func renderDropFK(qualTable, name string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", qualTable, name)
}

// resolveSqlColName looks up the SQL column name for a proto-name
// reference using the carried column snapshot. Defends the IR-builder
// invariant that every FK's column points at a real column.
func resolveSqlColName(cols []*irpb.Column, protoName string) (string, error) {
	for _, c := range cols {
		if c.GetProtoName() == protoName {
			return c.GetName(), nil
		}
	}
	return "", fmt.Errorf("proto column %q not found in carried snapshot (builder invariant violated)", protoName)
}

// emitAddCheck / emitDropCheck / emitReplaceCheck render the three
// structured-CHECK alter ops. Identity is the constraint name from
// renderCheck (`<table>_<col>_<suffix>`). Up: ALTER TABLE … ADD
// CONSTRAINT <line>; Down: DROP CONSTRAINT.
func (e Emitter) emitAddCheck(ac *planpb.AddCheck) (string, string, error) {
	tbl := tableShellFromCtx(ac.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	col := ac.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: AddCheck with nil column")
	}
	line, err := renderCheck(tbl.GetName(), col, ac.GetCheck())
	if err != nil {
		return "", "", fmt.Errorf("postgres: AddCheck: %w", err)
	}
	if line == "" {
		return "", "", fmt.Errorf("postgres: AddCheck rendered empty SQL (check has no body)")
	}
	name := checkConstraintName(tbl.GetName(), col, ac.GetCheck())
	up := fmt.Sprintf("ALTER TABLE %s ADD %s;", qual, line)
	down := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", qual, name)
	return up, down, nil
}

func (e Emitter) emitDropCheck(dc *planpb.DropCheck) (string, string, error) {
	tbl := tableShellFromCtx(dc.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	col := dc.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: DropCheck with nil column")
	}
	name := checkConstraintName(tbl.GetName(), col, dc.GetCheck())
	up := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", qual, name)

	addUp, _, err := e.emitAddCheck(&planpb.AddCheck{
		Ctx: dc.GetCtx(), Column: col, Check: dc.GetCheck(),
	})
	if err != nil {
		return "", "", err
	}
	return up, addUp, nil
}

func (e Emitter) emitReplaceCheck(rc *planpb.ReplaceCheck) (string, string, error) {
	tbl := tableShellFromCtx(rc.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	col := rc.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: ReplaceCheck with nil column")
	}
	fromName := checkConstraintName(tbl.GetName(), col, rc.GetFrom())
	toName := checkConstraintName(tbl.GetName(), col, rc.GetTo())
	toLine, err := renderCheck(tbl.GetName(), col, rc.GetTo())
	if err != nil {
		return "", "", fmt.Errorf("postgres: ReplaceCheck to: %w", err)
	}
	fromLine, err := renderCheck(tbl.GetName(), col, rc.GetFrom())
	if err != nil {
		return "", "", fmt.Errorf("postgres: ReplaceCheck from: %w", err)
	}
	up := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;\nALTER TABLE %s ADD %s;",
		qual, fromName, qual, toLine)
	down := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;\nALTER TABLE %s ADD %s;",
		qual, toName, qual, fromLine)
	return up, down, nil
}

// emitAddRawCheck / emitDropRawCheck / emitReplaceRawCheck handle the
// D11 escape-hatch CHECK constraints. Body is opaque; identity is the
// constraint name on the RawCheck (validated by ir.Build).
func (e Emitter) emitAddRawCheck(arc *planpb.AddRawCheck) (string, string, error) {
	tbl := tableShellFromCtx(arc.GetCtx(), nil)
	rc := arc.GetCheck()
	if rc == nil || rc.GetName() == "" {
		return "", "", fmt.Errorf("postgres: AddRawCheck missing name")
	}
	qual := qualifiedTable(tbl)
	up := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s);", qual, rc.GetName(), rc.GetExpr())
	down := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", qual, rc.GetName())
	return up, down, nil
}

func (e Emitter) emitDropRawCheck(drc *planpb.DropRawCheck) (string, string, error) {
	tbl := tableShellFromCtx(drc.GetCtx(), nil)
	rc := drc.GetCheck()
	if rc == nil || rc.GetName() == "" {
		return "", "", fmt.Errorf("postgres: DropRawCheck missing name")
	}
	qual := qualifiedTable(tbl)
	up := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", qual, rc.GetName())
	down := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s);", qual, rc.GetName(), rc.GetExpr())
	return up, down, nil
}

func (e Emitter) emitReplaceRawCheck(rrc *planpb.ReplaceRawCheck) (string, string, error) {
	tbl := tableShellFromCtx(rrc.GetCtx(), nil)
	from, to := rrc.GetFrom(), rrc.GetTo()
	if from == nil || to == nil {
		return "", "", fmt.Errorf("postgres: ReplaceRawCheck missing from/to")
	}
	qual := qualifiedTable(tbl)
	up := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;\nALTER TABLE %s ADD CONSTRAINT %s CHECK (%s);",
		qual, from.GetName(), qual, to.GetName(), to.GetExpr())
	down := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;\nALTER TABLE %s ADD CONSTRAINT %s CHECK (%s);",
		qual, to.GetName(), qual, from.GetName(), from.GetExpr())
	return up, down, nil
}

// checkConstraintName re-derives the constraint name renderCheck
// produces. Mirrors `<table>_<col>_<suffix>` with suffix from
// renderCheckBody. Empty body → "" (no constraint emitted at all).
func checkConstraintName(table string, col *irpb.Column, ck *irpb.Check) string {
	suffix, body, _ := renderCheckBody(col, ck)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("%s_%s_%s", table, col.GetName(), suffix)
}
