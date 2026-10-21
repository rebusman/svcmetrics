package main

import (
	"go/ast"
	"go/token"
	"strings"
)

type pkgInfo struct {
	dir      string
	name     string
	types    map[string]*ast.TypeSpec
	hasReset map[string]bool
	targets  []target
	imports  map[string]string
	loader   packageLoader
}

type target struct {
	spec *ast.TypeSpec
	file *ast.File
}

func newPkgInfo(dir string, loader packageLoader) *pkgInfo {
	return &pkgInfo{
		dir:      dir,
		types:    map[string]*ast.TypeSpec{},
		hasReset: map[string]bool{},
		imports:  map[string]string{},
		loader:   loader,
	}
}

func collectDecls(f *ast.File, info *pkgInfo) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && d.Name.Name == "Reset" && len(d.Recv.List) == 1 {
				if name := recvTypeName(d.Recv.List[0].Type); name != "" {
					info.hasReset[name] = true
				}
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, s := range d.Specs {
				ts := s.(*ast.TypeSpec)
				info.types[ts.Name.Name] = ts
				if _, ok := ts.Type.(*ast.StructType); !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(d.Specs) == 1 {
					doc = d.Doc
				}
				if hasMarker(doc) {
					info.targets = append(info.targets, target{spec: ts, file: f})
					info.hasReset[ts.Name.Name] = true
				}
			}
		}
	}
}

func hasMarker(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if text == marker {
			return true
		}
	}
	return false
}

func recvTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch t := expr.(type) {
	case *ast.IndexExpr:
		expr = t.X
	case *ast.IndexListExpr:
		expr = t.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
