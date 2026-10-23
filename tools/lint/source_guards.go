package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sourceScopeDirs lists top-level directories scanned by the source guards.
// Other directories (agile/, docs/, deploy/, web/, schemas/, etc.) are not Go
// code and never enter the scan.
var sourceScopeDirs = []string{"cmd", "internal", "pkg", "test", "tools"}

// packageDocAllowlist names package directories that are exempt from the
// package-doc guard because they belong to feature lanes outside the S01
// foundations scope and were never given a canonical package doc comment.
// Each entry is a slash-joined path relative to the repository root.
var packageDocAllowlist = map[string]bool{
	"internal/logs":       true,
	"test/drills/harness": true,
	"test/contract":       true,
	"test/integration":    true,
}

// goSourceFile is a parsed Go file plus its repo-relative path for guard
// reporting. Files that fail to parse are reported as errors by the loader.
type goSourceFile struct {
	relPath string
	fset    *token.FileSet
	file    *ast.File
	pkgName string
	isTest  bool
	pkgDir  string // directory containing the file, repo-relative slash form
}

// loadGoSources walks the source-scope directories under root and returns a
// deterministic slice of parsed Go source files. Vendor directories and
// directories whose name starts with a dot are skipped to avoid spurious
// failures on tooling state.
func loadGoSources(root string) ([]goSourceFile, error) {
	var files []goSourceFile
	for _, top := range sourceScopeDirs {
		topAbs := filepath.Join(root, top)
		info, err := os.Stat(topAbs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", top, err)
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.Walk(topAbs, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "node_modules" || name == "testdata" || (strings.HasPrefix(name, ".") && name != ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(info.Name(), ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			fset := token.NewFileSet()
			parsed, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			files = append(files, goSourceFile{
				relPath: rel,
				fset:    fset,
				file:    parsed,
				pkgName: parsed.Name.Name,
				isTest:  strings.HasSuffix(info.Name(), "_test.go"),
				pkgDir:  filepath.ToSlash(filepath.Dir(rel)),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relPath < files[j].relPath })
	return files, nil
}

// checkForbidPanic forbids the use of panic() outside cmd/<binary>/main.go
// entry points unless the call has an adjacent lint:allow-panic annotation.
// The annotation is reserved for deliberate fail-fast construction boundaries
// where the surrounding public API cannot return an error. Test files are
// exempt because they may legitimately panic to validate recovery semantics.
func checkForbidPanic(files []goSourceFile) []error {
	var errs []error
	for _, f := range files {
		if f.isTest {
			continue
		}
		if isCmdMainFile(f.relPath) {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name != "panic" {
				return true
			}
			pos := f.fset.Position(call.Pos())
			if hasAdjacentComment(f, pos.Line, "lint:allow-panic") {
				return true
			}
			errs = append(errs, fmt.Errorf("forbidden panic() at %s:%d (allowed only in cmd/*/main.go or with an adjacent lint:allow-panic rationale)", f.relPath, pos.Line))
			return true
		})
	}
	return errs
}

func hasAdjacentComment(f goSourceFile, line int, marker string) bool {
	for _, group := range f.file.Comments {
		start := f.fset.Position(group.Pos()).Line
		end := f.fset.Position(group.End()).Line
		if (end == line || end == line-1 || start == line) && strings.Contains(group.Text(), marker) {
			return true
		}
	}
	return false
}

// checkForbidOSExit forbids os.Exit calls outside cmd/<binary>/main.go entry
// points and the tools/ subtree. Tools may call os.Exit directly because they
// are not part of the long-running daemons. Test files are exempt because Go
// tests sometimes use os.Exit in TestMain.
func checkForbidOSExit(files []goSourceFile) []error {
	var errs []error
	for _, f := range files {
		if f.isTest {
			continue
		}
		if isCmdMainFile(f.relPath) || isUnderToolsDir(f.relPath) {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "os" && sel.Sel.Name == "Exit" {
				pos := f.fset.Position(call.Pos())
				errs = append(errs, fmt.Errorf("forbidden os.Exit at %s:%d (allowed only in cmd/*/main.go and tools/)", f.relPath, pos.Line))
			}
			return true
		})
	}
	return errs
}

// checkRequirePackageDoc requires that every non-main package in internal/,
// pkg/, and tools/ (excluding the explicit allowlist) declares the canonical
// "// Package <name>" doc comment on at least one of its package clauses. The
// allowlist exists for packages owned by feature lanes that S01 cannot edit.
func checkRequirePackageDoc(files []goSourceFile) []error {
	var errs []error
	pkgsByDir := make(map[string][]goSourceFile)
	for _, f := range files {
		if f.isTest {
			continue
		}
		if f.pkgName == "main" {
			continue
		}
		top, _, _ := strings.Cut(f.pkgDir, "/")
		switch top {
		case "internal", "pkg", "tools":
			// in scope
		default:
			continue
		}
		if packageDocAllowlist[f.pkgDir] {
			continue
		}
		pkgsByDir[f.pkgDir] = append(pkgsByDir[f.pkgDir], f)
	}
	dirs := make([]string, 0, len(pkgsByDir))
	for dir := range pkgsByDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		pkgFiles := pkgsByDir[dir]
		ok := false
		var pkgName string
		for _, f := range pkgFiles {
			pkgName = f.pkgName
			if hasCanonicalPackageDoc(f) {
				ok = true
				break
			}
		}
		if !ok {
			errs = append(errs, fmt.Errorf("package %s missing canonical \"// Package %s\" doc comment in any non-test file", dir, pkgName))
		}
	}
	return errs
}

// hasCanonicalPackageDoc returns true when the file's package clause carries
// a doc comment whose first line begins with "Package <pkgName>". The Go style
// guide and gofmt convention both expect this exact form, and tooling like
// `go doc` and `pkg.go.dev` rely on it.
func hasCanonicalPackageDoc(f goSourceFile) bool {
	if f.file.Doc == nil {
		return false
	}
	text := strings.TrimSpace(f.file.Doc.Text())
	if text == "" {
		return false
	}
	line, _, _ := strings.Cut(text, "\n")
	return strings.HasPrefix(strings.TrimSpace(line), "Package "+f.pkgName)
}

// isCmdMainFile returns true when the path points at a top-level cmd/<binary>/
// main.go entry-point file. Subdirectories under cmd/<binary>/ such as
// cmd/midnight-agent/internal/<pkg>/ are not entry points and are not exempt.
func isCmdMainFile(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 {
		return false
	}
	return parts[0] == "cmd" && parts[2] == "main.go"
}

// isUnderToolsDir returns true when the path lives anywhere under tools/.
func isUnderToolsDir(rel string) bool {
	return strings.HasPrefix(rel, "tools/")
}
