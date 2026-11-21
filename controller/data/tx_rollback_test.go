package data

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestTxBeginHasRollbackOnEveryReturn walks the controller sources and fails
// when a function opens a transaction with Begin() and can return without
// Commit or Rollback on that transaction. A leaked transaction keeps its pool
// connection for good, and the controller pool is only five connections per
// process (CORE-22).
//
// A function passes when it has `defer tx.Rollback()` (safe after Commit in
// pgx), or when every return after Begin (other than the Begin error check)
// follows a statement that commits or rolls back, commits or rolls back in
// the return itself, or hands the transaction to the caller.
func TestTxBeginHasRollbackOnEveryReturn(t *testing.T) {
	var problems []string
	root := ".." // controller/
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		problems = append(problems, txLeaksInFile(t, path)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Errorf("transaction may leak: %s", p)
	}
}

// TestTxLeakCheckerFindsLeaks makes sure the checker itself flags the shapes
// it is meant to catch and accepts the safe ones.
func TestTxLeakCheckerFindsLeaks(t *testing.T) {
	src := `package x
func leakEarlyReturn(db DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if bad() {
		return errBad
	}
	return tx.Commit()
}
func leakSwitchDefault(db DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	switch k {
	case 1:
		err = use(tx)
	default:
		return errBad
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
func leakFallOff(db DB) {
	tx, _ := db.Begin()
	use(tx)
}
func okDefer(db DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if bad() {
		return errBad
	}
	return tx.Commit()
}
func okExplicit(db DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := use(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
func okHandOff(db DB) (*Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	return tx, nil
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range txLeaksInAST(fset, f) {
		got[p[strings.LastIndex(p, " ")+1:]] = true
	}
	for _, name := range []string{"leakEarlyReturn", "leakSwitchDefault", "leakFallOff"} {
		if !got[name] {
			t.Errorf("checker missed leak in %s (got %v)", name, got)
		}
	}
	for _, name := range []string{"okDefer", "okExplicit", "okHandOff"} {
		if got[name] {
			t.Errorf("checker flagged safe function %s", name)
		}
	}
}

func txLeaksInFile(t *testing.T, path string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %s", path, err)
	}
	return txLeaksInAST(fset, f)
}

func txLeaksInAST(fset *token.FileSet, f *ast.File) []string {
	var out []string
	check := func(name string, body *ast.BlockStmt) {
		if body == nil {
			return
		}
		for _, b := range findBegins(body) {
			for _, pos := range unclosedReturns(body, b) {
				out = append(out, fset.Position(pos).String()+" "+name)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			check(fn.Name.Name, fn.Body)
		case *ast.FuncLit:
			check("func literal", fn.Body)
		}
		return true
	})
	return out
}

type beginSite struct {
	tx    string
	stmt  ast.Stmt
	block *ast.BlockStmt
	index int
}

// findBegins returns `tx, err := x.Begin()` statements directly in body or its
// nested blocks, not inside nested function literals.
func findBegins(body *ast.BlockStmt) []beginSite {
	var out []beginSite
	var walkBlock func(*ast.BlockStmt)
	walkBlock = func(b *ast.BlockStmt) {
		for i, s := range b.List {
			if as, ok := s.(*ast.AssignStmt); ok && len(as.Rhs) == 1 && len(as.Lhs) >= 1 {
				if call, ok := as.Rhs[0].(*ast.CallExpr); ok && len(call.Args) == 0 {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Begin" {
						if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
							out = append(out, beginSite{tx: id.Name, stmt: s, block: b, index: i})
						}
					}
				}
			}
		}
		for _, s := range b.List {
			for _, nb := range childBlocks(s) {
				walkBlock(nb)
			}
		}
	}
	walkBlock(body)
	return out
}

// childBlocks returns the blocks directly nested in s (if/else, for, range,
// switch cases, select cases, bare blocks), not inside function literals.
func childBlocks(s ast.Stmt) []*ast.BlockStmt {
	switch st := s.(type) {
	case *ast.BlockStmt:
		return []*ast.BlockStmt{st}
	case *ast.IfStmt:
		out := []*ast.BlockStmt{st.Body}
		if st.Else != nil {
			out = append(out, childBlocks(st.Else)...)
		}
		return out
	case *ast.ForStmt:
		return []*ast.BlockStmt{st.Body}
	case *ast.RangeStmt:
		return []*ast.BlockStmt{st.Body}
	case *ast.SwitchStmt:
		return caseBlocks(st.Body)
	case *ast.TypeSwitchStmt:
		return caseBlocks(st.Body)
	case *ast.SelectStmt:
		return caseBlocks(st.Body)
	case *ast.LabeledStmt:
		return childBlocks(st.Stmt)
	}
	return nil
}

func caseBlocks(b *ast.BlockStmt) []*ast.BlockStmt {
	var out []*ast.BlockStmt
	for _, s := range b.List {
		switch c := s.(type) {
		case *ast.CaseClause:
			out = append(out, &ast.BlockStmt{Lbrace: c.Colon, List: c.Body, Rbrace: c.End()})
		case *ast.CommClause:
			out = append(out, &ast.BlockStmt{Lbrace: c.Colon, List: c.Body, Rbrace: c.End()})
		}
	}
	return out
}

func inspectNoFuncLit(n ast.Node, fn func(ast.Node)) {
	ast.Inspect(n, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if n != nil {
			fn(n)
		}
		return true
	})
}

// closesTx reports whether n calls tx.Commit() or tx.Rollback() (outside
// function literals).
func closesTx(n ast.Node, tx string) bool {
	found := false
	inspectNoFuncLit(n, func(n ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Commit" && sel.Sel.Name != "Rollback") {
			return
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == tx {
			found = true
		}
	})
	return found
}

// closingStmt reports whether a statement, once executed, leaves tx closed on
// every path that continues past it: a bare Commit/Rollback call or
// assignment, or an if/switch whose init does it.
func closingStmt(s ast.Stmt, tx string) bool {
	switch st := s.(type) {
	case *ast.ExprStmt, *ast.AssignStmt, *ast.DeferStmt:
		if d, ok := st.(*ast.DeferStmt); ok {
			return closesTx(d.Call, tx)
		}
		return closesTx(st, tx)
	case *ast.IfStmt:
		return st.Init != nil && closesTx(st.Init, tx)
	case *ast.SwitchStmt:
		return st.Init != nil && closesTx(st.Init, tx)
	}
	return false
}

func mentionsIdent(n ast.Node, name string) bool {
	found := false
	inspectNoFuncLit(n, func(n ast.Node) {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
	})
	return found
}

// unclosedReturns walks every statement after the Begin site and reports
// return statements (and falling off the end of the enclosing block) reached
// without the transaction having been closed.
func unclosedReturns(body *ast.BlockStmt, b beginSite) []token.Pos {
	var out []token.Pos
	// The statement right after Begin is usually the error check; returns
	// inside it are fine because no transaction was opened.
	var errCheck ast.Stmt
	if b.index+1 < len(b.block.List) {
		if is, ok := b.block.List[b.index+1].(*ast.IfStmt); ok && mentionsIdent(is.Cond, "err") {
			errCheck = is
		}
	}
	// walk returns true when the list ends with tx closed or control never
	// falls through (terminated by return/panic).
	var walk func(list []ast.Stmt, closed bool) (fallsThroughOpen bool)
	walk = func(list []ast.Stmt, closed bool) bool {
		for _, s := range list {
			if s == errCheck {
				continue
			}
			if closed {
				return false
			}
			if d, ok := s.(*ast.DeferStmt); ok && closesTx(d.Call, b.tx) {
				return false
			}
			if r, ok := s.(*ast.ReturnStmt); ok {
				if !closesTx(r, b.tx) && !mentionsIdent(r, b.tx) {
					out = append(out, r.Pos())
				}
				return false
			}
			if ifs, ok := s.(*ast.IfStmt); ok && ifs.Init != nil && closesTx(ifs.Init, b.tx) {
				return false
			}
			if closingStmt(s, b.tx) {
				closed = true
				continue
			}
			for _, nb := range childBlocks(s) {
				walk(nb.List, false)
			}
		}
		return !closed
	}
	// Statements after Begin in its own block.
	open := walk(b.block.List[b.index+1:], false)
	if open && b.block == body {
		if len(body.List) == 0 || !isReturn(body.List[len(body.List)-1]) {
			out = append(out, body.Rbrace)
		}
	}
	return out
}

func isReturn(s ast.Stmt) bool {
	_, ok := s.(*ast.ReturnStmt)
	return ok
}
