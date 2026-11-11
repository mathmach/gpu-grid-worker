package arch_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCyclomaticAndCognitiveComplexityCeilings(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	for _, tree := range []string{"cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !isProductionGoFile(path) {
				return walkErr
			}
			astFile, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			for _, decl := range astFile.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				cyc := computeCyclomatic(fn.Body)
				cog := computeCognitive(fn.Body, 0)
				if cyc > 15 || cog > 15 {
					pos := fset.Position(fn.Pos())
					t.Errorf("%s:%d %s exceeds complexity ceiling <=15 (cyclomatic=%d cognitive=%d)", path, pos.Line, fn.Name.Name, cyc, cog)
				}
			}
			ast.Inspect(astFile, func(n ast.Node) bool {
				lit, ok := n.(*ast.FuncLit)
				if !ok || lit.Body == nil {
					return true
				}
				cyc := computeCyclomatic(lit.Body)
				cog := computeCognitive(lit.Body, 0)
				if cyc > 15 || cog > 15 {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d func literal exceeds complexity ceiling <=15 (cyclomatic=%d cognitive=%d)", path, pos.Line, cyc, cog)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}
}

type parsedIfaceEntry struct {
	path    string
	pkgKey  string
	imports map[string]string
	spec    *ast.TypeSpec
	fields  []*ast.Field
}

func TestISPRoleInterfaceMethodCeiling(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	var all []parsedIfaceEntry
	byKey := map[string]parsedIfaceEntry{}

	for _, tree := range []string{"cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !isProductionGoFile(path) {
				return walkErr
			}
			astFile, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			pkgKey := filepath.ToSlash(filepath.Dir(path))
			impMap := buildRepoImportAliasMap(root, astFile)
			ast.Inspect(astFile, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok || iface.Methods == nil {
					return true
				}
				entry := parsedIfaceEntry{path: path, pkgKey: pkgKey, imports: impMap, spec: ts, fields: iface.Methods.List}
				all = append(all, entry)
				byKey[pkgKey+"."+ts.Name.Name] = entry
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}

	for _, item := range all {
		methods := countResolvedInterfaceMethods(item, byKey, map[string]bool{})
		if methods > 5 {
			pos := fset.Position(item.spec.Pos())
			t.Errorf("%s:%d interface %s has %d resolved methods, exceeding ISP ceiling of 5", item.path, pos.Line, item.spec.Name.Name, methods)
		}
	}
}

func buildRepoImportAliasMap(root string, astFile *ast.File) map[string]string {
	out := make(map[string]string, len(astFile.Imports))
	for _, imp := range astFile.Imports {
		raw := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasPrefix(raw, workerModulePrefix) {
			continue
		}
		rel := strings.TrimPrefix(raw, workerModulePrefix)
		targetPkgKey := filepath.ToSlash(filepath.Join(root, filepath.FromSlash(rel)))
		alias := filepath.Base(raw)
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" && imp.Name.Name != "." {
			alias = imp.Name.Name
		}
		out[alias] = targetPkgKey
	}
	return out
}

func countResolvedInterfaceMethods(item parsedIfaceEntry, byKey map[string]parsedIfaceEntry, seen map[string]bool) int {
	total := 0
	for _, f := range item.fields {
		if len(f.Names) > 0 {
			total += len(f.Names)
			continue
		}
		switch emb := f.Type.(type) {
		case *ast.Ident:
			key := item.pkgKey + "." + emb.Name
			if target, exists := byKey[key]; exists && !seen[key] {
				seen[key] = true
				total += countResolvedInterfaceMethods(target, byKey, seen)
				continue
			}
		case *ast.SelectorExpr:
			if pkgIdent, ok := emb.X.(*ast.Ident); ok && emb.Sel != nil {
				if targetPkg, mapped := item.imports[pkgIdent.Name]; mapped {
					key := targetPkg + "." + emb.Sel.Name
					if target, exists := byKey[key]; exists && !seen[key] {
						seen[key] = true
						total += countResolvedInterfaceMethods(target, byKey, seen)
						continue
					}
				}
			}
		}
		total++
	}
	return total
}

func TestZeroInlineProseCommentsInGoFiles(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	for _, tree := range []string{"cmd", "pkg", "tests"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return walkErr
			}
			astFile, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			for _, cg := range astFile.Comments {
				for _, c := range cg.List {
					txt := strings.TrimSpace(c.Text)
					if strings.HasPrefix(txt, "//go:build") || strings.HasPrefix(txt, "//go:embed") || strings.HasPrefix(txt, "// +build") {
						continue
					}
					pos := fset.Position(c.Pos())
					t.Errorf("prose comment prohibited in %s:%d: %s", path, pos.Line, txt)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}
}

func TestZeroSilentBlankErrorDiscardsAndUnusedParams(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	type fileEntry struct {
		path string
		file *ast.File
	}
	var files []fileEntry
	errReturningFns := map[string]bool{
		"Marshal": true, "MarshalIndent": true, "Unmarshal": true,
		"Atoi": true, "ParseInt": true, "ParseUint": true, "ParseFloat": true, "ParseBool": true,
		"ReadAll": true, "ReadFile": true, "WriteFile": true, "Stat": true, "Open": true, "Create": true,
		"SplitHostPort": true, "ResolveTCPAddr": true, "Listen": true, "Do": true,
		"DecodeString": true, "Rel": true, "Abs": true, "EvalSymlinks": true,
	}
	nonErrDecls := map[string]bool{}
	singleErrDecls := map[string]bool{
		"Unmarshal": true, "WriteFile": true, "MkdirAll": true,
		"Remove": true, "RemoveAll": true, "Rename": true, "Chmod": true,
		"WalkDir": true, "Encode": true, "Decode": true,
		"WriteJSON": true, "WriteMessage": true, "WriteControl": true,
	}

	for _, tree := range []string{"cmd", "pkg", "tests"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return walkErr
			}
			astFile, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			if !strings.HasSuffix(path, "_test.go") {
				recordFunctionReturnShapes(astFile, errReturningFns, singleErrDecls, nonErrDecls)
				assertZeroUnusedFuncDeclParams(t, fset, astFile)
			}
			files = append(files, fileEntry{path: path, file: astFile})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}

	unambiguousSingleErr := map[string]bool{}
	for name := range singleErrDecls {
		if !nonErrDecls[name] {
			unambiguousSingleErr[name] = true
		}
	}
	for _, fe := range files {
		assertZeroSilentErrorDiscardsInFile(t, fset, fe.path, fe.file, errReturningFns, unambiguousSingleErr)
	}
}

func recordFunctionReturnShapes(file *ast.File, errReturning, singleErr, nonErr map[string]bool) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Type == nil {
			continue
		}
		if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
			nonErr[fn.Name.Name] = true
			continue
		}
		list := fn.Type.Results.List
		if id, ok := list[len(list)-1].Type.(*ast.Ident); ok && id.Name == "error" {
			errReturning[fn.Name.Name] = true
			if len(list) == 1 && len(list[0].Names) <= 1 {
				singleErr[fn.Name.Name] = true
			}
			continue
		}
		nonErr[fn.Name.Name] = true
	}
}

func assertZeroSilentErrorDiscardsInFile(
	t *testing.T,
	fset *token.FileSet,
	path string,
	file *ast.File,
	errReturningFns map[string]bool,
	unambiguousSingleErr map[string]bool,
) {
	t.Helper()
	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if len(stmt.Rhs) != 1 {
				return true
			}
			call, isCall := stmt.Rhs[0].(*ast.CallExpr)
			if !isCall {
				return true
			}
			if allLHSBlank(stmt.Lhs) {
				pos := fset.Position(stmt.Pos())
				t.Errorf("silent blank-identifier return discard (_ = call()) prohibited in %s:%d", path, pos.Line)
				return true
			}
			if len(stmt.Lhs) >= 2 && lastLHSBlank(stmt.Lhs) && isKnownErrorCall(path, call, errReturningFns) {
				pos := fset.Position(stmt.Pos())
				t.Errorf("silent multi-return error discard (x, _ := call()) prohibited in %s:%d", path, pos.Line)
			}
		case *ast.ExprStmt:
			call, isCall := stmt.X.(*ast.CallExpr)
			if !isCall || isCloseMethodCall(call) {
				return true
			}
			if isKnownErrorCall(path, call, unambiguousSingleErr) {
				pos := fset.Position(stmt.Pos())
				t.Errorf("silent bare error-returning call statement prohibited in %s:%d", path, pos.Line)
			}
		}
		return true
	})
}

func lastLHSBlank(exprs []ast.Expr) bool {
	if len(exprs) == 0 {
		return false
	}
	id, ok := exprs[len(exprs)-1].(*ast.Ident)
	return ok && id.Name == "_"
}

func isKnownErrorCall(path string, call *ast.CallExpr, errReturningFns map[string]bool) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return errReturningFns[fn.Name]
	case *ast.SelectorExpr:
		if fn.Sel == nil || (fn.Sel.Name == "Call" && strings.HasSuffix(path, "_windows.go")) {
			return false
		}
		return errReturningFns[fn.Sel.Name]
	default:
		return false
	}
}

func allLHSBlank(exprs []ast.Expr) bool {
	if len(exprs) == 0 {
		return false
	}
	for _, e := range exprs {
		id, ok := e.(*ast.Ident)
		if !ok || id.Name != "_" {
			return false
		}
	}
	return true
}

func isCloseMethodCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "Close"
}

func assertZeroUnusedFuncDeclParams(t *testing.T, fset *token.FileSet, f *ast.File) {
	t.Helper()
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || len(fn.Body.List) == 0 || fn.Type.Params == nil {
			continue
		}
		used := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if id, isID := n.(*ast.Ident); isID {
				used[id.Name] = true
			}
			return true
		})
		for _, fld := range fn.Type.Params.List {
			for _, pn := range fld.Names {
				if pn.Name != "_" && !used[pn.Name] {
					pos := fset.Position(pn.Pos())
					t.Errorf("unused parameter %q in function %s at %s:%d", pn.Name, fn.Name.Name, pos.Filename, pos.Line)
				}
			}
		}
	}
}

func TestWorkerErrorCodesHaveCompleteBilingualI18nCatalogParity(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	derrFile, err := parser.ParseFile(fset, filepath.Join(root, "pkg", "domain", "derr", "errors.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse derr/errors.go: %v", err)
	}
	definedCodes := map[string]bool{}
	for _, decl := range derrFile.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, isVS := spec.(*ast.ValueSpec)
			if !isVS {
				continue
			}
			for _, name := range vs.Names {
				if strings.HasPrefix(name.Name, "Code") {
					definedCodes[name.Name] = true
				}
			}
		}
	}

	locFile, err := parser.ParseFile(fset, filepath.Join(root, "pkg", "presentation", "workeri18n", "localizer.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse workeri18n/localizer.go: %v", err)
	}
	catalogCodes := map[string]bool{}
	ast.Inspect(locFile, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if sel, isSel := kv.Key.(*ast.SelectorExpr); isSel && sel.Sel != nil && strings.HasPrefix(sel.Sel.Name, "Code") {
			catalogCodes[sel.Sel.Name] = true
		}
		return true
	})

	for code := range definedCodes {
		if !catalogCodes[code] {
			t.Errorf("derr.%s is missing from workeri18n errorCatalog", code)
		}
	}
	for code := range catalogCodes {
		if !definedCodes[code] {
			t.Errorf("workeri18n errorCatalog references undefined derr.%s", code)
		}
	}
}

func computeCyclomatic(body *ast.BlockStmt) int {
	c := 1
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			c++
		case *ast.CaseClause:
			if s.List != nil {
				c++
			}
		case *ast.CommClause:
			if s.Comm != nil {
				c++
			}
		case *ast.BinaryExpr:
			if s.Op == token.LAND || s.Op == token.LOR {
				c++
			}
		}
		return true
	})
	return c
}

func computeCognitive(node ast.Node, nesting int) int {
	if node == nil {
		return 0
	}
	switch s := node.(type) {
	case *ast.BlockStmt:
		return sumBlockCognitive(s.List, nesting)
	case *ast.LabeledStmt:
		return computeCognitive(s.Stmt, nesting)
	case *ast.DeclStmt:
		return computeGenDeclCognitive(s.Decl, nesting)
	case *ast.IfStmt:
		return computeIfCognitive(s, nesting, false)
	case *ast.ForStmt:
		return 1 + nesting + computeCognitive(s.Init, nesting) + computeBoolSeqs(s.Cond) + computeCognitive(s.Cond, nesting) + computeCognitive(s.Post, nesting) + computeCognitive(s.Body, nesting+1)
	case *ast.RangeStmt:
		return 1 + nesting + computeCognitive(s.X, nesting) + computeCognitive(s.Body, nesting+1)
	case *ast.SwitchStmt:
		return 1 + nesting + computeCognitive(s.Init, nesting) + computeBoolSeqs(s.Tag) + computeCognitive(s.Tag, nesting) + computeSwitchClausesCognitive(s.Body, nesting+1)
	case *ast.TypeSwitchStmt:
		return 1 + nesting + computeCognitive(s.Init, nesting) + computeCognitive(s.Assign, nesting) + computeSwitchClausesCognitive(s.Body, nesting+1)
	case *ast.SelectStmt:
		return 1 + nesting + computeSelectClausesCognitive(s.Body, nesting+1)
	case *ast.FuncLit:
		return computeCognitive(s.Body, nesting+1)
	default:
		return computeLeafStmtCognitive(node, nesting)
	}
}

func computeGenDeclCognitive(decl ast.Decl, nesting int) int {
	gd, ok := decl.(*ast.GenDecl)
	if !ok {
		return 0
	}
	total := 0
	for _, sp := range gd.Specs {
		if vs, isVS := sp.(*ast.ValueSpec); isVS {
			total += sumExprListCognitive(vs.Values, nesting, true)
		}
	}
	return total
}

func sumBlockCognitive(stmts []ast.Stmt, nesting int) int {
	total := 0
	for _, st := range stmts {
		total += computeCognitive(st, nesting)
	}
	return total
}

func computeSwitchClausesCognitive(body *ast.BlockStmt, nextNesting int) int {
	if body == nil {
		return 0
	}
	total := 0
	for _, st := range body.List {
		if cc, ok := st.(*ast.CaseClause); ok {
			total += sumExprListCognitive(cc.List, nextNesting, true)
			total += sumBlockCognitive(cc.Body, nextNesting)
		}
	}
	return total
}

func computeSelectClausesCognitive(body *ast.BlockStmt, nextNesting int) int {
	if body == nil {
		return 0
	}
	total := 0
	for _, st := range body.List {
		if cc, ok := st.(*ast.CommClause); ok {
			total += computeCognitive(cc.Comm, nextNesting)
			total += sumBlockCognitive(cc.Body, nextNesting)
		}
	}
	return total
}

func computeLeafStmtCognitive(node ast.Node, nesting int) int {
	switch s := node.(type) {
	case *ast.AssignStmt:
		return sumExprListCognitive(s.Rhs, nesting, true)
	case *ast.ReturnStmt:
		return sumExprListCognitive(s.Results, nesting, true)
	case *ast.SendStmt:
		return computeBoolSeqs(s.Value) + computeCognitive(s.Chan, nesting) + computeCognitive(s.Value, nesting)
	case *ast.ExprStmt:
		return computeCognitive(s.X, nesting)
	case *ast.DeferStmt:
		return computeCognitive(s.Call, nesting)
	case *ast.GoStmt:
		return computeCognitive(s.Call, nesting)
	case *ast.CallExpr:
		return computeCognitive(s.Fun, nesting) + sumExprListCognitive(s.Args, nesting, true)
	case *ast.CompositeLit:
		return sumExprListCognitive(s.Elts, nesting, false)
	case *ast.KeyValueExpr:
		return computeBoolSeqs(s.Value) + computeCognitive(s.Value, nesting)
	case *ast.UnaryExpr:
		return computeCognitive(s.X, nesting)
	case *ast.ParenExpr:
		return computeCognitive(s.X, nesting)
	case *ast.BinaryExpr:
		return computeCognitive(s.X, nesting) + computeCognitive(s.Y, nesting)
	case *ast.BranchStmt:
		if s.Label != nil {
			return 1
		}
	}
	return 0
}

func sumExprListCognitive(exprs []ast.Expr, nesting int, includeBool bool) int {
	total := 0
	for _, e := range exprs {
		if includeBool {
			total += computeBoolSeqs(e)
		}
		total += computeCognitive(e, nesting)
	}
	return total
}

func computeIfCognitive(s *ast.IfStmt, nesting int, isElseIf bool) int {
	score := 1 + computeCognitive(s.Init, nesting) + computeBoolSeqs(s.Cond) + computeCognitive(s.Cond, nesting) + computeCognitive(s.Body, nesting+1)
	if !isElseIf {
		score += nesting
	}
	if s.Else == nil {
		return score
	}
	if nextIf, ok := s.Else.(*ast.IfStmt); ok {
		return score + computeIfCognitive(nextIf, nesting, true)
	}
	return score + 1 + computeCognitive(s.Else, nesting+1)
}

func computeBoolSeqs(e ast.Expr) int {
	ops := collectLogicalOps(e)
	if len(ops) == 0 {
		return 0
	}
	seqs := 1
	for i := 1; i < len(ops); i++ {
		if ops[i] != ops[i-1] {
			seqs++
		}
	}
	return seqs
}

func collectLogicalOps(e ast.Expr) []token.Token {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return collectLogicalOps(x.X)
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			return collectLogicalOps(x.X)
		}
	case *ast.BinaryExpr:
		if x.Op == token.LAND || x.Op == token.LOR {
			out := append(collectLogicalOps(x.X), x.Op)
			return append(out, collectLogicalOps(x.Y)...)
		}
	}
	return nil
}
