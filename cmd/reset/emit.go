package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"unicode"
)

// maxResetDepth bounds the resetExpr/resetNamed recursion while unwrapping
// named types defined through pointers or other named types. It guards
// against recursive definitions such as `type P *P`, which would otherwise
// recurse forever; it is not a tuned value. Past the limit the field falls
// back to resetUnknown, which is still correct, so the number can be
// changed freely.
const maxResetDepth = 8

var basicZero = map[string]string{
	"bool": "false", "string": `""`,
	"int": "0", "int8": "0", "int16": "0", "int32": "0", "int64": "0",
	"uint": "0", "uint8": "0", "uint16": "0", "uint32": "0", "uint64": "0", "uintptr": "0",
	"byte": "0", "rune": "0",
	"float32": "0", "float64": "0",
	"complex64": "0", "complex128": "0",
}

func generate(fset *token.FileSet, info *pkgInfo) ([]byte, error) {
	sort.Slice(info.targets, func(i, j int) bool {
		return info.targets[i].spec.Name.Name < info.targets[j].spec.Name.Name
	})

	var body bytes.Buffer
	for _, t := range info.targets {
		g := &generator{fset: fset, info: info, file: t.file, buf: &body}
		g.writeMethod(t.spec)
		if g.err != nil {
			return nil, fmt.Errorf("%s: %w", t.spec.Name.Name, g.err)
		}
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "%s\n\npackage %s\n\n", genHeader, info.name)
	if len(info.imports) > 0 {
		names := make([]string, 0, len(info.imports))
		for n := range info.imports {
			names = append(names, n)
		}
		sort.Strings(names)
		out.WriteString("import (\n")
		for _, n := range names {
			fmt.Fprintf(&out, "\t%s\n", info.imports[n])
		}
		out.WriteString(")\n\n")
	}
	out.Write(body.Bytes())

	src, err := format.Source(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated code: %w\n%s", err, out.String())
	}
	return src, nil
}

type generator struct {
	fset *token.FileSet
	info *pkgInfo
	file *ast.File
	buf  *bytes.Buffer
	recv string
	err  error
}

func (g *generator) printf(format string, args ...any) {
	fmt.Fprintf(g.buf, format, args...)
}

// fail records the first error; generate checks it after each method so a
// broken file never reaches the disk.
func (g *generator) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

func (g *generator) writeMethod(ts *ast.TypeSpec) {
	g.recv = receiverName(ts.Name.Name)

	typeName := ts.Name.Name
	if ts.TypeParams != nil {
		var params []string
		for _, f := range ts.TypeParams.List {
			for _, n := range f.Names {
				params = append(params, n.Name)
			}
		}
		typeName += "[" + strings.Join(params, ", ") + "]"
	}

	g.printf("// Reset resets %s to its initial state.\n", ts.Name.Name)
	g.printf("func (%s *%s) Reset() {\n", g.recv, typeName)
	g.printf("if %s == nil {\nreturn\n}\n\n", g.recv)

	st := ts.Type.(*ast.StructType)
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			if name := embeddedName(field.Type); name != "" {
				g.resetExpr(g.recv+"."+name, field.Type, 0)
			}
			continue
		}
		for _, n := range field.Names {
			if n.Name == "_" {
				continue
			}
			g.resetExpr(g.recv+"."+n.Name, field.Type, 0)
		}
	}
	g.printf("}\n\n")
}

func (g *generator) resetExpr(expr string, typ ast.Expr, depth int) {
	switch t := typ.(type) {
	case *ast.ParenExpr:
		g.resetExpr(expr, t.X, depth)

	case *ast.Ident:
		if zero, ok := basicZero[t.Name]; ok && g.info.types[t.Name] == nil {
			g.printf("%s = %s\n", expr, zero)
			return
		}
		if t.Name == "error" || t.Name == "any" {
			g.printf("%s = nil\n", expr)
			return
		}
		g.resetNamed(expr, t, t.Name, depth)

	case *ast.IndexExpr, *ast.IndexListExpr:
		g.resetNamed(expr, t, recvTypeName(t), depth)

	case *ast.SelectorExpr:
		g.resetExternal(expr, t)

	case *ast.StarExpr:
		ptr := expr
		if depth > 0 || strings.HasPrefix(expr, "*") {
			ptr = "(" + expr + ")"
		}
		g.printf("if %s != nil {\n", ptr)
		g.resetExpr("*"+ptr, t.X, depth+1)
		g.printf("}\n")

	case *ast.ArrayType:
		if t.Len == nil {
			g.printf("%s = %s[:0]\n", expr, expr)
		} else {
			g.printf("%s = %s{}\n", expr, g.typeString(t))
		}

	case *ast.MapType:
		g.printf("clear(%s)\n", expr)

	case *ast.StructType:
		g.printf("%s = %s{}\n", expr, g.typeString(t))

	default:
		g.printf("%s = nil\n", expr)
	}
}

func (g *generator) resetNamed(expr string, typ ast.Expr, name string, depth int) {
	ts, ok := g.info.types[name]
	if !ok {
		g.resetUnknown(expr, typ)
		return
	}
	if g.info.hasReset[name] {
		g.printf("%s.Reset()\n", methodTarget(expr))
		return
	}
	if ts.Assign.IsValid() || ts.TypeParams != nil {
		g.resetUnknown(expr, typ)
		return
	}
	switch u := ts.Type.(type) {
	case *ast.StructType:
		g.printf("%s = %s{}\n", expr, g.typeString(typ))
	case *ast.Ident, *ast.ArrayType, *ast.MapType, *ast.StarExpr:
		if depth > maxResetDepth {
			g.resetUnknown(expr, typ)
			return
		}
		if id, ok := u.(*ast.Ident); ok && basicZero[id.Name] != "" {
			g.printf("%s = %s(%s)\n", expr, name, basicZero[id.Name])
			return
		}
		g.resetExpr(expr, u, depth+1)
	default:
		g.printf("%s = nil\n", expr)
	}
}

func (g *generator) resetExternal(expr string, sel *ast.SelectorExpr) {
	named, err := g.lookupExternal(sel)
	if err != nil {
		g.fail(err)
		return
	}
	if named == nil {
		g.resetUnknown(expr, sel)
		return
	}
	if obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(named), true, nil, "Reset"); obj != nil {
		if fn, ok := obj.(*types.Func); ok && fn.Signature().Params().Len() == 0 && fn.Signature().Results().Len() == 0 {
			g.printf("%s.Reset()\n", methodTarget(expr))
			return
		}
	}
	switch named.Underlying().(type) {
	case *types.Map:
		g.printf("clear(%s)\n", expr)
	case *types.Slice:
		g.printf("%s = %s[:0]\n", expr, expr)
	default:
		g.printf("%s = *new(%s)\n", expr, g.typeString(sel))
	}
}

func (g *generator) resetUnknown(expr string, typ ast.Expr) {
	addr := "&" + expr
	if ptr, ok := strings.CutPrefix(expr, "*"); ok {
		addr = ptr
	}
	g.printf("if resetter, ok := any(%s).(interface{ Reset() }); ok {\n", addr)
	g.printf("resetter.Reset()\n")
	g.printf("} else {\n")
	g.printf("%s = *new(%s)\n", expr, g.typeString(typ))
	g.printf("}\n")
}

func (g *generator) typeString(typ ast.Expr) string {
	ast.Inspect(typ, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			g.addImport(id.Name)
		}
		return false
	})

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, g.fset, typ); err != nil {
		g.fail(fmt.Errorf("print type: %w", err))
		return ""
	}
	return buf.String()
}

func methodTarget(expr string) string {
	if ptr, ok := strings.CutPrefix(expr, "*"); ok {
		return ptr
	}
	return expr
}

func embeddedName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	}
	return ""
}

func receiverName(typeName string) string {
	for _, r := range typeName {
		return string(unicode.ToLower(r))
	}
	return "r"
}
