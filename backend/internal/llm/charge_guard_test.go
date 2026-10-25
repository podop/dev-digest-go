package llm_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const llmImportPath = "dev-digest-go/backend/internal/llm"

// TestProductionRequestsCarryACharge is a guard for the cost ledger (security L3, FR1): an LLM
// call without a Charge is answered and paid for but never recorded, and it only shows up as one
// WARN line at run time. Every llm.Request that production code under internal/ builds must
// therefore set Charge, either in the literal or by assigning the field in the same function (the
// reviewer's llmcall adapter copies it from its config that way). A new call site that forgets it
// fails here, whether or not any end-to-end test happens to exercise it. A literal "Charge: nil"
// does not count.
//
// It is syntactic on purpose: it cannot see a Charge that is set but nil at run time, nor a Request
// built through a variable declaration. The run-time half is TestLedgerCalls_EveryKindRecordsOneEntryPerAnsweredCall
// in cmd/server, which runs one call of each kind through the real wiring and fails on a missing
// entry and on the "no usable spend charge" warning.
func TestProductionRequestsCarryACharge(t *testing.T) {
	internal, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Abs(..) = %v", err)
	}
	fset := token.NewFileSet()
	sites := 0
	err = filepath.WalkDir(internal, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		alias := llmAlias(file)
		if alias == "" {
			return nil
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			assigned := assignsCharge(fn.Body)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isSelector(lit.Type, alias, "Request") {
					return true
				}
				sites++
				pos := fset.Position(lit.Pos())
				switch keyed, isNil := chargeKey(lit); {
				case keyed && isNil:
					t.Errorf("%s: llm.Request sets Charge to nil", pos)
				case !keyed && !assigned:
					t.Errorf("%s: llm.Request built without a Charge (set Charge in the literal or assign req.Charge in %s); its usage would never reach the cost ledger", pos, fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", internal, err)
	}
	// llmcall (reviews and evals), conventions, brief, onboarding and intent. A lower count means the
	// scan lost its way, not that a call site went away unnoticed.
	if sites < 5 {
		t.Errorf("found %d llm.Request literals in production code, want at least 5: the guard no longer sees the call sites", sites)
	}
}

// llmAlias returns the name the file imports internal/llm under, or "".
func llmAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != llmImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "llm"
	}
	return ""
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// chargeKey reports whether the literal has a Charge field and whether its value is the literal nil.
func chargeKey(lit *ast.CompositeLit) (keyed, isNil bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Charge" {
			v, ok := kv.Value.(*ast.Ident)
			return true, ok && v.Name == "nil"
		}
	}
	return false, false
}

// assignsCharge reports whether the block assigns some value's Charge field.
func assignsCharge(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Charge" {
				found = true
			}
		}
		return !found
	})
	return found
}
