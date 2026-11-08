package taint

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// formatters are the packages whose functions turn their arguments into text.
var formatters = []string{"fmt", "log"}

// checkFormatting refuses a hostile value, or a value holding one, as an argument to fmt or
// log. hostile.Of prints a placeholder, but a struct reached through an unexported field is
// printed by reflection, and nothing should need to rely on that.
func checkFormatting(pass *analysis.Pass, root inspector.Cursor) {
	for cursor := range root.Preorder((*ast.CallExpr)(nil)) {
		call := cursor.Node().(*ast.CallExpr)
		formatter := formatterOf(pass, call)
		if formatter == "" {
			continue
		}
		for _, arg := range call.Args {
			if holdsHostile(pass.TypesInfo.TypeOf(arg)) {
				pass.Reportf(arg.Pos(), "%s formats a hostile value: narrow it with hostile.Narrow or hostile.Classify, or keep it hostile with hostile.Map", formatter)
			}
		}
	}
}

// formatterOf names the fmt or log function a call invokes, or returns "".
func formatterOf(pass *analysis.Pass, call *ast.CallExpr) string {
	for _, path := range formatters {
		if function := calleeIn(pass, call, path); function != nil {
			return path + "." + function.Name()
		}
	}
	return ""
}
