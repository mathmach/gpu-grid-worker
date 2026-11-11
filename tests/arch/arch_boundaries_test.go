package arch_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const workerModulePrefix = "github.com/mathmach/gpu-grid-worker/"

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if st, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repository root containing go.mod not found from %s", dir)
		}
		dir = parent
	}
}

func TestDomainAndProtocolZeroForbiddenImports(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	for _, rel := range []string{filepath.Join("pkg", "domain"), filepath.Join("pkg", "protocol")} {
		err := filepath.WalkDir(filepath.Join(root, rel), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !isProductionGoFile(path) {
				return walkErr
			}
			astFile, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			for _, imp := range astFile.Imports {
				pkgPath := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(pkgPath, workerModulePrefix+"pkg/domain/") {
					continue
				}
				if isForbiddenDomainImport(pkgPath) {
					t.Errorf("forbidden import %q in domain/protocol file %s", pkgPath, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", rel, err)
		}
	}
}

func TestApplicationLayerStrictHexagonalAllowlist(t *testing.T) {
	appRoot := filepath.Join(findRepoRoot(t), "pkg", "application")
	fset := token.NewFileSet()
	err := filepath.WalkDir(appRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !isProductionGoFile(path) {
			return walkErr
		}
		astFile, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, imp := range astFile.Imports {
			pkgPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(pkgPath, workerModulePrefix+"pkg/domain/") || pkgPath == workerModulePrefix+"pkg/protocol" {
				continue
			}
			if isForbiddenDomainImport(pkgPath) {
				t.Errorf("forbidden import %q in application file %s", pkgPath, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk pkg/application: %v", err)
	}
}

func TestPresentationInfrastructureAndWSConnConcentricBoundaries(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()

	assertTreeForbiddenImportPrefixes(t, fset, filepath.Join(root, "pkg", "presentation"), []string{
		workerModulePrefix + "pkg/infrastructure",
		workerModulePrefix + "cmd",
		"database/sql",
		"os/exec",
	}, true)

	assertTreeForbiddenImportPrefixes(t, fset, filepath.Join(root, "pkg", "infrastructure"), []string{
		workerModulePrefix + "pkg/application",
		workerModulePrefix + "pkg/presentation",
		workerModulePrefix + "cmd",
	}, true)

	assertTreeForbiddenImportPrefixes(t, fset, filepath.Join(root, "pkg", "wsconn"), []string{
		workerModulePrefix + "pkg/application",
		workerModulePrefix + "pkg/infrastructure",
		workerModulePrefix + "pkg/presentation",
		workerModulePrefix + "cmd",
	}, true)

	assertTreeForbiddenImportPrefixes(t, fset, filepath.Join(root, "tests", "unit", "domain"), []string{
		workerModulePrefix + "pkg/application",
		workerModulePrefix + "pkg/infrastructure",
		workerModulePrefix + "pkg/presentation",
		workerModulePrefix + "cmd",
	}, false)

	assertTreeForbiddenImportPrefixes(t, fset, filepath.Join(root, "tests", "unit", "application"), []string{
		workerModulePrefix + "pkg/infrastructure",
		workerModulePrefix + "pkg/presentation",
		workerModulePrefix + "cmd",
	}, false)

	assertTreeForbiddenImportPrefixes(t, fset, filepath.Join(root, "tests", "unit", "presentation"), []string{
		workerModulePrefix + "pkg/infrastructure",
		workerModulePrefix + "cmd",
	}, false)
}

func assertTreeForbiddenImportPrefixes(t *testing.T, fset *token.FileSet, dir string, forbidden []string, prodOnly bool) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}
		if prodOnly && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		astFile, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, imp := range astFile.Imports {
			pkgPath := strings.Trim(imp.Path.Value, `"`)
			if matchesAnyPkgPrefix(pkgPath, forbidden) {
				t.Errorf("file %s imports forbidden layer %q", path, pkgPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func matchesAnyPkgPrefix(pkgPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if pkgPath == prefix || strings.HasPrefix(pkgPath, prefix+"/") {
			return true
		}
	}
	return false
}

func isForbiddenDomainImport(pkgPath string) bool {
	firstSeg, _, _ := strings.Cut(pkgPath, "/")
	if strings.Contains(firstSeg, ".") {
		return true
	}
	switch {
	case pkgPath == "C" || pkgPath == "os" || strings.HasPrefix(pkgPath, "os/"):
		return true
	case pkgPath == "net" || pkgPath == "net/http" || strings.HasPrefix(pkgPath, "net/http/"):
		return true
	case pkgPath == "syscall" || strings.HasPrefix(pkgPath, "syscall/") || pkgPath == "unsafe":
		return true
	case pkgPath == "database/sql" || strings.HasPrefix(pkgPath, "archive/"):
		return true
	default:
		return false
	}
}

func TestZeroCGOAndLOCCeilingsAndConfigIsolation(t *testing.T) {
	root := findRepoRoot(t)
	fset := token.NewFileSet()
	for _, tree := range []string{"cmd", "pkg", "tests"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return walkErr
			}
			astFile, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Fatalf("parse %s: %v", path, parseErr)
			}
			for _, imp := range astFile.Imports {
				if strings.Trim(imp.Path.Value, `"`) == "C" {
					t.Errorf("cgo import \"C\" prohibited under CGO_ENABLED=0 invariant in %s", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}

	assertDirFilesWithinLOCCeiling(t, filepath.Join(root, "cmd"), 200, isProductionGoFile)
	assertDirFilesWithinLOCCeiling(t, filepath.Join(root, "pkg", "presentation"), 300, isProductionGoFile)

	allowedDir := filepath.Clean(filepath.Join(root, "pkg", "infrastructure", "workerconfig"))
	for _, tree := range []string{"cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !isProductionGoFile(path) {
				return walkErr
			}
			if filepath.Clean(filepath.Dir(path)) == allowedDir {
				return nil
			}
			assertNoOsEnvCalls(t, fset, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}
}

func TestAllGoTestsResideUnderTestsAndUnitTestsArePure(t *testing.T) {
	root := findRepoRoot(t)
	for _, prodTree := range []string{"cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, prodTree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr == nil && !d.IsDir() && strings.HasSuffix(path, "_test.go") {
				t.Errorf("test file %s must reside under root tests/ directory", path)
			}
			return walkErr
		})
		if err != nil {
			t.Fatalf("walk %s: %v", prodTree, err)
		}
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "tests"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}
		verifySingleTestFileGovernance(t, fset, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk tests/: %v", err)
	}

	err = filepath.WalkDir(filepath.Join(root, "tests", "unit"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}
		assertZeroUnitIOPatterns(t, fset, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk tests/unit: %v", err)
	}
}

func verifySingleTestFileGovernance(t *testing.T, fset *token.FileSet, path string) {
	t.Helper()
	if strings.Contains(strings.ToLower(filepath.Base(path)), "phase") {
		t.Errorf("test file %s must be named after a domain capability, never a phase number", path)
	}
	astFile, parseErr := parser.ParseFile(fset, path, nil, 0)
	if parseErr != nil {
		t.Fatalf("parse %s: %v", path, parseErr)
	}
	slashPath := filepath.ToSlash(path)
	if strings.Contains(slashPath, "/tests/integration/testkit/") && !strings.HasSuffix(slashPath, "_test.go") {
		if astFile.Name.Name != "testkit" {
			t.Errorf("shared testkit file %s must declare package testkit, got %q", path, astFile.Name.Name)
		}
		return
	}
	if !strings.HasSuffix(slashPath, "_test.go") || !strings.HasSuffix(astFile.Name.Name, "_test") {
		t.Errorf("test file %s must use *_test.go and external *_test package, got %q", path, astFile.Name.Name)
	}
	for _, decl := range astFile.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && strings.HasPrefix(strings.ToLower(fn.Name.Name), "testphase") {
			t.Errorf("test function %s in %s must be named after a domain capability", fn.Name.Name, path)
		}
	}
}

func assertZeroUnitIOPatterns(t *testing.T, fset *token.FileSet, path string) {
	t.Helper()
	astFile, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	aliasToImport := map[string]string{}
	for _, imp := range astFile.Imports {
		pkgPath := strings.Trim(imp.Path.Value, `"`)
		if pkgPath == "database/sql" || pkgPath == "os/exec" {
			t.Errorf("forbidden I/O package import %q in unit test %s", pkgPath, path)
		}
		alias := filepath.Base(pkgPath)
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" && imp.Name.Name != "." {
			alias = imp.Name.Name
		}
		aliasToImport[alias] = pkgPath
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		if sel.Sel.Name == "TempDir" {
			t.Errorf("disk TempDir in unit test %s must move to tests/integration", path)
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && isForbiddenUnitPkgCall(pkg.Name, aliasToImport[pkg.Name], sel.Sel.Name) {
			t.Errorf("forbidden I/O or sleep call %s.%s in unit test %s", pkg.Name, sel.Sel.Name, path)
		}
		return true
	})
}

func isForbiddenUnitPkgCall(alias, impPath, fn string) bool {
	switch {
	case impPath == "os" || alias == "os":
		switch fn {
		case "WriteFile", "ReadFile", "Mkdir", "MkdirAll", "MkdirTemp", "Open", "OpenFile", "Create", "CreateTemp", "Remove", "RemoveAll", "Rename", "Stat", "Lstat", "Executable", "Chdir", "Setenv", "Unsetenv":
			return true
		}
	case impPath == "net/http/httptest" || alias == "httptest":
		return fn == "NewServer" || fn == "NewTLSServer" || fn == "NewUnstartedServer"
	case impPath == "net" || alias == "net":
		return strings.HasPrefix(fn, "Listen") || strings.HasPrefix(fn, "Dial")
	case impPath == "time" || alias == "time":
		return fn == "Sleep"
	}
	return false
}

func isProductionGoFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

func assertDirFilesWithinLOCCeiling(t *testing.T, dirPath string, ceiling int, match func(string) bool) {
	t.Helper()
	err := filepath.WalkDir(dirPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !match(path) {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		lines := countFileLines(raw)
		if lines > ceiling {
			t.Errorf("file %s has %d lines, exceeding ceiling of %d LOC", path, lines, ceiling)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dirPath, err)
	}
}

func countFileLines(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	trimmed := bytes.TrimRight(raw, "\r\n")
	if len(trimmed) == 0 {
		return 1
	}
	return bytes.Count(trimmed, []byte{'\n'}) + 1
}

func assertNoOsEnvCalls(t *testing.T, fset *token.FileSet, filePath string) {
	t.Helper()
	astFile, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filePath, err)
	}
	envAliases := map[string]bool{}
	for _, imp := range astFile.Imports {
		raw := strings.Trim(imp.Path.Value, `"`)
		if raw != "os" && raw != "syscall" {
			continue
		}
		if imp.Name == nil {
			envAliases[raw] = true
		} else if imp.Name.Name != "_" && imp.Name.Name != "." {
			envAliases[imp.Name.Name] = true
		}
	}
	if len(envAliases) == 0 {
		return
	}
	forbiddenFns := map[string]bool{"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true, "Setenv": true, "Unsetenv": true, "Clearenv": true}
	ast.Inspect(astFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel && sel.Sel != nil && forbiddenFns[sel.Sel.Name] {
			if pkg, isIdent := sel.X.(*ast.Ident); isIdent && envAliases[pkg.Name] {
				t.Errorf("forbidden %s.%s call outside workerconfig package in %s", pkg.Name, sel.Sel.Name, filePath)
			}
		}
		return true
	})
}
