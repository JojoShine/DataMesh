package workflow

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
	"github.com/pingcap/tidb/pkg/parser/model"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

type sandboxSQLTable struct {
	Database string
	Table    string
	Fields   []Field
}

// Compilation always restores a validated tree; the submitted text never reaches Doris.
func compileSandboxSQL(source string, tables map[string]sandboxSQLTable) (string, error) {
	if len(source) == 0 || len(source) > 32768 {
		return "", fmt.Errorf("SQL length exceeds sandbox bounds")
	}
	// MySQL executable comments can contain syntax invisible to other SQL engines.
	if strings.Contains(source, "/*!") || strings.Contains(source, "/*+") {
		return "", fmt.Errorf("executable comments and hints are unsupported")
	}
	p := parser.New()
	nodes, _, err := p.Parse(source, "", "")
	if err != nil || len(nodes) != 1 {
		return "", fmt.Errorf("expected one SELECT statement")
	}
	switch nodes[0].(type) {
	case *ast.SelectStmt, *ast.SetOprStmt:
	default:
		return "", fmt.Errorf("only SELECT is allowed")
	}
	v := &sandboxSQLVisitor{tables: tables}
	_, ok := nodes[0].Accept(v)
	if !ok || v.err != nil {
		return "", v.err
	}
	var out strings.Builder
	if err = nodes[0].Restore(format.NewRestoreCtx(format.DefaultRestoreFlags|format.RestoreStringWithoutCharset, &out)); err != nil {
		return "", err
	}
	return out.String(), nil
}

type sandboxSQLVisitor struct {
	tables       map[string]sandboxSQLTable
	err          error
	depth, nodes int
	scopes       []map[string]bool
}

func (v *sandboxSQLVisitor) fail(s string) (ast.Node, bool) {
	v.err = fmt.Errorf("sandbox SQL: %s", s)
	return nil, true
}
func (v *sandboxSQLVisitor) Enter(n ast.Node) (ast.Node, bool) {
	v.depth++
	v.nodes++
	if v.depth > 128 || v.nodes > 8192 {
		return v.fail("query is too complex")
	}
	name := reflect.TypeOf(n).Elem().Name()
	switch name {
	case "WithClause", "CommonTableExpression", "SelectStmt", "SetOprStmt", "SetOprSelectList", "TableRefsClause", "Join", "OnCondition", "TableSource", "TableName", "FieldList", "SelectField", "WildCardField", "ColumnNameExpr", "ColumnName", "BinaryOperationExpr", "UnaryOperationExpr", "ParenthesesExpr", "SubqueryExpr", "ExistsSubqueryExpr", "CompareSubqueryExpr", "PatternInExpr", "PatternLikeOrIlikeExpr", "PatternRegexpExpr", "BetweenExpr", "IsNullExpr", "IsTruthExpr", "RowExpr", "CaseExpr", "WhenClause", "FuncCallExpr", "AggregateFuncExpr", "FuncCastExpr", "GroupByClause", "HavingClause", "OrderByClause", "ByItem", "Limit", "ValueExpr":
	default:
		return v.fail("unsupported syntax " + name)
	}
	switch x := n.(type) {
	case *ast.SelectStmt:
		if err := v.scope(x.With); err != nil {
			return v.fail(err.Error())
		}
		if x.LockInfo != nil || x.SelectIntoOpt != nil || len(x.TableHints) > 0 || len(x.WindowSpecs) > 0 {
			return v.fail("locks, INTO, hints and windows are forbidden")
		}
	case *ast.SetOprStmt:
		if err := v.scope(x.With); err != nil {
			return v.fail(err.Error())
		}
	case *ast.WildCardField:
		if x.Schema.O != "" {
			return v.fail("schema qualifiers are forbidden")
		}
	case *ast.ColumnName:
		if x.Schema.O != "" {
			return v.fail("schema qualifiers are forbidden")
		}
	case *ast.FuncCallExpr:
		if x.Schema.O != "" || !sandboxPureFunction(x.FnName.L) {
			return v.fail("function is not allowed")
		}
	case *ast.AggregateFuncExpr:
		if !sandboxPureFunction(strings.ToLower(x.F)) {
			return v.fail("aggregate is not allowed")
		}
	case *ast.TableSource:
		t, ok := x.Source.(*ast.TableName)
		if !ok {
			break
		}
		if t.Schema.O != "" || len(t.IndexHints) > 0 || len(t.PartitionNames) > 0 || t.TableSample != nil || t.AsOf != nil {
			return v.fail("qualified tables and index hints are forbidden")
		}
		if v.cte(t.Name.L) {
			break
		}
		physical, ok := v.tables[t.Name.O]
		if !ok {
			return v.fail("table is outside this sandbox")
		}
		if len(physical.Fields) == 0 {
			return v.fail("table has no fields")
		}
		cols := make([]string, len(physical.Fields))
		for i, f := range physical.Fields {
			cols[i] = dorisIdentifier(f.Name)
		}
		query := "SELECT " + strings.Join(cols, ",") + " FROM " + dorisIdentifier(physical.Database) + "." + dorisIdentifier(physical.Table)
		derived, _, err := parser.New().Parse(query, "", "")
		if err != nil {
			return v.fail("invalid physical table mapping")
		}
		x.Source = derived[0].(*ast.SelectStmt)
		if x.AsName.O == "" {
			x.AsName = model.NewCIStr(t.Name.O)
		}
		return x, true
	}
	return n, false
}
func (v *sandboxSQLVisitor) Leave(n ast.Node) (ast.Node, bool) {
	switch x := n.(type) {
	case *ast.CommonTableExpression:
		v.scopes[len(v.scopes)-1][x.Name.L] = true
	case *ast.SelectStmt, *ast.SetOprStmt:
		v.scopes = v.scopes[:len(v.scopes)-1]
	}
	v.depth--
	return n, v.err == nil
}
func (v *sandboxSQLVisitor) cte(name string) bool {
	for i := len(v.scopes) - 1; i >= 0; i-- {
		if v.scopes[i][name] {
			return true
		}
	}
	return false
}
func (v *sandboxSQLVisitor) scope(w *ast.WithClause) error {
	scope := map[string]bool{}
	v.scopes = append(v.scopes, scope)
	if w == nil {
		return nil
	}
	if w.IsRecursive {
		return fmt.Errorf("recursive WITH is forbidden")
	}
	seen := map[string]bool{}
	for _, c := range w.CTEs {
		if seen[c.Name.L] || v.cte(c.Name.L) {
			return fmt.Errorf("ambiguous CTE shadowing")
		}
		for k := range v.tables {
			if strings.EqualFold(k, c.Name.O) {
				return fmt.Errorf("CTE shadows a table")
			}
		}
		seen[c.Name.L] = true
	}
	return nil
}
func sandboxPureFunction(s string) bool {
	switch s {
	case "abs", "ceil", "ceiling", "floor", "round", "truncate", "mod", "power", "sqrt", "lower", "upper", "trim", "ltrim", "rtrim", "concat", "concat_ws", "substring", "substr", "left", "right", "replace", "length", "char_length", "coalesce", "ifnull", "nullif", "if", "greatest", "least", "date", "year", "month", "day", "dayofmonth", "date_format", "str_to_date", "datediff", "date_add", "date_sub", "count", "sum", "avg", "min", "max", "stddev", "variance":
		return true
	}
	return false
}
