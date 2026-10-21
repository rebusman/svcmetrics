package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// packageLoader resolves an import path to its type information. dir is the
// directory the import is resolved from, so module-relative paths work.
type packageLoader interface {
	load(path, dir string) (*types.Package, error)
}

// pkgLoader loads packages with go/packages and caches them by import path.
// Only successful loads are cached: a transient failure must not turn into a
// permanent one for the rest of the run.
type pkgLoader struct {
	cache map[string]*types.Package
}

func newPkgLoader() *pkgLoader {
	return &pkgLoader{cache: map[string]*types.Package{}}
}

func (l *pkgLoader) load(path, dir string) (*types.Package, error) {
	if pkg, ok := l.cache[path]; ok {
		return pkg, nil
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes, Dir: dir}
	pkgs, err := packages.Load(cfg, path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("load %s: got %d packages, want 1", path, len(pkgs))
	}
	if len(pkgs[0].Errors) > 0 {
		errs := make([]error, len(pkgs[0].Errors))
		for i, e := range pkgs[0].Errors {
			errs[i] = e
		}
		return nil, fmt.Errorf("load %s: %w", path, errors.Join(errs...))
	}
	l.cache[path] = pkgs[0].Types
	return pkgs[0].Types, nil
}

// lookupExternal returns the type sel refers to. It returns nil, nil when sel
// does not name an import of the current file, leaving the caller to fall back
// to a runtime check; a package that fails to load or lacks the type is an
// error, since guessing there would silently change the generated code.
func (g *generator) lookupExternal(sel *ast.SelectorExpr) (types.Type, error) {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, nil
	}
	path := g.importPath(id.Name)
	if path == "" {
		return nil, nil
	}
	pkg, err := g.info.loader.load(path, g.info.dir)
	if err != nil {
		return nil, err
	}
	obj, ok := pkg.Scope().Lookup(sel.Sel.Name).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("type %s not found in %s", sel.Sel.Name, path)
	}
	return obj.Type(), nil
}

func (g *generator) importPath(name string) string {
	for _, imp := range g.file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		local := filepath.Base(path)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == name {
			return path
		}
	}
	return ""
}

func (g *generator) addImport(name string) {
	if _, ok := g.info.imports[name]; ok {
		return
	}
	for _, imp := range g.file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		local := filepath.Base(path)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local != name {
			continue
		}
		if imp.Name != nil || local != filepath.Base(path) {
			g.info.imports[name] = name + " " + imp.Path.Value
		} else {
			g.info.imports[name] = imp.Path.Value
		}
		return
	}
	for _, imp := range g.file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if imp.Name == nil && strings.Contains(filepath.Base(path), name) {
			g.info.imports[name] = name + " " + imp.Path.Value
			return
		}
	}
}
