package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*/reset.gen.go.golden")

const goldenName = genFileName + ".golden"

// copyFixture copies the .go files of testdata/<name> into a fresh directory,
// so processDir writes its output there and never touches testdata.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestGenerateGolden(t *testing.T) {
	for _, name := range []string{"fields", "generic"} {
		t.Run(name, func(t *testing.T) {
			dir := copyFixture(t, name)
			if err := processDir(dir, newPkgLoader()); err != nil {
				t.Fatalf("processDir: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, genFileName))
			if err != nil {
				t.Fatal(err)
			}

			golden := filepath.Join("testdata", name, goldenName)
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -update to create it)", err)
			}
			// Git on Windows may check the golden file out with CRLF endings.
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Errorf("generated code differs from %s (run go test -update to accept):\n--- got\n%s\n--- want\n%s", golden, got, want)
			}

			typeCheck(t, dir)
		})
	}
}

// typeCheck makes sure the generated file compiles together with the fixture:
// a golden file only proves the output is stable, not that it is valid Go.
func typeCheck(t *testing.T, dir string) {
	t.Helper()
	fset := token.NewFileSet()
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, m, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check(files[0].Name.Name, fset, files, nil); err != nil {
		t.Errorf("generated code does not type-check: %v", err)
	}
}

func TestProcessDirRemovesStale(t *testing.T) {
	dir := copyFixture(t, "nomarker")
	if err := processDir(dir, newPkgLoader()); err != nil {
		t.Fatalf("processDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, genFileName)); !os.IsNotExist(err) {
		t.Errorf("stale %s was not removed: %v", genFileName, err)
	}
}

func TestProcessDirKeepsForeignFile(t *testing.T) {
	dir := copyFixture(t, "foreign")
	path := filepath.Join(dir, genFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := processDir(dir, newPkgLoader()); err != nil {
		t.Fatalf("processDir: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hand-written %s was removed: %v", genFileName, err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("hand-written %s was modified", genFileName)
	}
}

func TestCollectDirsSkipsSpecialDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"pkg/sub", ".git", "_tools", "vendor/x", "testdata/x"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := collectDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{root, filepath.Join(root, "pkg"), filepath.Join(root, "pkg", "sub")}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Errorf("collectDirs = %v, want %v", dirs, want)
	}
}

// fakeLoader serves hand-built packages and records every load, so generate
// can be exercised without the filesystem or the go command.
type fakeLoader struct {
	pkgs  map[string]*types.Package
	loads []string
}

func (l *fakeLoader) load(path, _ string) (*types.Package, error) {
	l.loads = append(l.loads, path)
	pkg, ok := l.pkgs[path]
	if !ok {
		return nil, fmt.Errorf("load %s: not found", path)
	}
	return pkg, nil
}

// newExtPackage builds example.com/ext with Resettable (has Reset), Set
// (a map) and Opaque (a struct without Reset).
func newExtPackage() *types.Package {
	pkg := types.NewPackage("example.com/ext", "ext")
	newType := func(name string, underlying types.Type) *types.Named {
		obj := types.NewTypeName(token.NoPos, pkg, name, nil)
		pkg.Scope().Insert(obj)
		return types.NewNamed(obj, underlying, nil)
	}

	resettable := newType("Resettable", types.NewStruct(nil, nil))
	recv := types.NewVar(token.NoPos, pkg, "r", types.NewPointer(resettable))
	sig := types.NewSignatureType(recv, nil, nil, nil, nil, false)
	resettable.AddMethod(types.NewFunc(token.NoPos, pkg, "Reset", sig))

	newType("Set", types.NewMap(types.Typ[types.String], types.Typ[types.Bool]))
	newType("Opaque", types.NewStruct(nil, nil))
	pkg.MarkComplete()
	return pkg
}

// generateSrc runs generate on a single in-memory file.
func generateSrc(t *testing.T, src string, loader packageLoader) ([]byte, error) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "demo.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	info := newPkgInfo("unused", loader)
	info.name = f.Name.Name
	collectDecls(f, info)
	return generate(fset, info)
}

func newFakeLoader() *fakeLoader {
	return &fakeLoader{pkgs: map[string]*types.Package{"example.com/ext": newExtPackage()}}
}

func TestGenerateWithFakeLoader(t *testing.T) {
	const src = `package demo

import (
	"example.com/ext"
	"example.com/yaml.v3"
)

// generate:reset
type Demo struct {
	r ext.Resettable
	s ext.Set
	o ext.Opaque
	y yaml.Node
}
`
	loader := newFakeLoader()
	out, err := generateSrc(t, src, loader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"d.r.Reset()",
		"clear(d.s)",
		"d.o = *new(ext.Opaque)",
		// yaml does not match any import by name, so it is never loaded and
		// falls back to the runtime check.
		"any(&d.y).(interface{ Reset() })",
		`"example.com/ext"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated code lacks %q:\n%s", want, got)
		}
	}
	for _, path := range loader.loads {
		if path != "example.com/ext" {
			t.Errorf("unexpected load of %q", path)
		}
	}
}

func TestGenerateFailsOnUnresolvedExternal(t *testing.T) {
	for name, tc := range map[string]struct{ src, wantErr string }{
		"load error": {
			src: `package demo

import "example.com/broken"

// generate:reset
type Demo struct{ b broken.T }
`,
			wantErr: "Demo: load example.com/broken: not found",
		},
		"missing type": {
			src: `package demo

import "example.com/ext"

// generate:reset
type Demo struct{ m ext.Missing }
`,
			wantErr: "Demo: type Missing not found in example.com/ext",
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := generateSrc(t, tc.src, newFakeLoader())
			if err == nil {
				t.Fatalf("generate succeeded, want error %q:\n%s", tc.wantErr, out)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPkgLoaderDoesNotCacheFailures(t *testing.T) {
	l := newPkgLoader()
	const path = "example.invalid/does/not/exist"
	if _, err := l.load(path, t.TempDir()); err == nil {
		t.Fatalf("load(%q) succeeded, want error", path)
	}
	if _, ok := l.cache[path]; ok {
		t.Errorf("failed load of %q was cached", path)
	}
}
