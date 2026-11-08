package taint

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// checkTitles refuses a value hostile.Titlef or hostile.Format.Title would panic on: anything
// but a number, a bool, validated text, a title, a closed value, or a pointer to one.
func checkTitles(pass *analysis.Pass, root inspector.Cursor) {
	for cursor := range root.Preorder((*ast.CallExpr)(nil)) {
		call := cursor.Node().(*ast.CallExpr)
		name, values := titleValues(pass, call)
		for _, arg := range values {
			if t := pass.TypesInfo.TypeOf(arg); !cleanForTitle(t) {
				pass.Reportf(arg.Pos(), "%s takes only clean values, and this is a %s; put fixed text in the format", name, types.TypeString(t, types.RelativeTo(pass.Pkg)))
			}
		}
	}
}

// titleValues returns the values a call fills a title with, if it makes one.
func titleValues(pass *analysis.Pass, call *ast.CallExpr) (string, []ast.Expr) {
	function := calleeIn(pass, call, hostilePath)
	if function == nil {
		return "", nil
	}
	switch receiver := function.Signature().Recv(); {
	case receiver == nil && function.Name() == "Titlef" && len(call.Args) > 0:
		return "hostile.Titlef", call.Args[1:]
	case receiver != nil && isHostileType(receiver.Type(), "Format") && function.Name() == "Title":
		return "hostile.Format.Title", call.Args
	}
	return "", nil
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
