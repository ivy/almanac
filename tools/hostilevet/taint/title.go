package taint

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// checkTitles refuses a value hostile.Titlef would panic on: anything but a number, a bool,
// validated text, a title, a closed value, or a pointer to one.
func checkTitles(pass *analysis.Pass, root inspector.Cursor) {
	for cursor := range root.Preorder((*ast.CallExpr)(nil)) {
		call := cursor.Node().(*ast.CallExpr)
		function := calleeIn(pass, call, hostilePath)
		if function == nil || function.Name() != "Titlef" || len(call.Args) == 0 {
			continue
		}
		for _, arg := range call.Args[1:] {
			if t := pass.TypesInfo.TypeOf(arg); !cleanForTitle(t) {
				pass.Reportf(arg.Pos(), "hostile.Titlef takes only clean values, and this is a %s; put fixed text in the format", types.TypeString(t, types.RelativeTo(pass.Pkg)))
			}
		}
	}
}

func cleanForTitle(t types.Type) bool {
	if pointer, ok := types.Unalias(t).(*types.Pointer); ok {
		t = pointer.Elem()
	}
	if isHostileType(t, "Slug") || isHostileType(t, "Sha") || isHostileType(t, "Title") || isClosed(t) {
		return true
	}
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Info()&(types.IsBoolean|types.IsNumeric) != 0
}
