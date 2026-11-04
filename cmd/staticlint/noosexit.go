package main

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// noOsExitAnalyzer reports direct calls to os.Exit in main of package main.
var noOsExitAnalyzer = &analysis.Analyzer{
	Name: "noosexit",
	Doc: `forbid direct calls to os.Exit in the main function of package main

os.Exit skips the deferred calls of main. Generated files and calls through
a variable are not reported.`,
	Run: runNoOsExit,
}

// runNoOsExit reports direct calls to os.Exit in main.
func runNoOsExit(pass *analysis.Pass) (any, error) {
	if pass.Pkg.Name() != "main" {
		return nil, nil
	}

	for _, file := range pass.Files {
		if ast.IsGenerated(file) {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "main" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if ok && isOsExit(pass.TypesInfo, call.Fun) {
					pass.Reportf(call.Pos(), "direct call to os.Exit in main function of package main")
				}
				return true
			})
		}
	}

	return nil, nil
}

// isOsExit reports whether fun names os.Exit.
func isOsExit(info *types.Info, fun ast.Expr) bool {
	var id *ast.Ident
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return false
	}

	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == "os" && fn.Name() == "Exit"
}
