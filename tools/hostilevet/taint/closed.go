package taint

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// checkClosed keeps a closed type's values to the set its package declares. hostile.Classify
// returns a closed value, so a closed value built from the text would carry the text out.
func checkClosed(pass *analysis.Pass, root inspector.Cursor) {
	for cursor := range root.Preorder((*ast.TypeSpec)(nil), (*ast.CompositeLit)(nil), (*ast.AssignStmt)(nil), (*ast.IncDecStmt)(nil)) {
		switch node := cursor.Node().(type) {
		case *ast.TypeSpec:
			checkClosedFields(pass, node)
		case *ast.CompositeLit:
			t := pass.TypesInfo.TypeOf(node)
			if len(node.Elts) > 0 && isClosed(t) && !declaredAtPackageLevel(cursor) {
				pass.Reportf(node.Pos(), "a value of closed type %s is declared once, as a package-level variable; choose one of those", types.TypeString(t, types.RelativeTo(pass.Pkg)))
			}
		case *ast.AssignStmt:
			for _, target := range node.Lhs {
				checkClosedField(pass, target)
			}
		case *ast.IncDecStmt:
			checkClosedField(pass, node.X)
		}
	}
}

// checkClosedFields refuses an exported field in a closed type, which JSON or another package
// could set to anything.
func checkClosedFields(pass *analysis.Pass, spec *ast.TypeSpec) {
	structure, ok := spec.Type.(*ast.StructType)
	if !ok || !isClosed(pass.TypesInfo.TypeOf(spec.Name)) {
		return
	}
	for _, field := range structure.Fields.List {
		for _, name := range field.Names {
			if name.IsExported() {
				pass.Reportf(name.Pos(), "closed type %s has exported field %s; a closed type's fields are unexported, so only its own package sets them", spec.Name.Name, name.Name)
			}
		}
	}
}

// declaredAtPackageLevel reports whether the literal at cursor initialises a package-level
// variable directly, rather than in a function.
func declaredAtPackageLevel(cursor inspector.Cursor) bool {
	for enclosing := range cursor.Enclosing((*ast.FuncLit)(nil), (*ast.FuncDecl)(nil), (*ast.GenDecl)(nil)) {
		declaration, ok := enclosing.Node().(*ast.GenDecl)
		if !ok {
			return false
		}
		_, atFile := enclosing.Parent().Node().(*ast.File)
		return atFile && declaration.Tok == token.VAR
	}
	return false
}

// checkClosedField refuses assigning to a field of a closed value.
func checkClosedField(pass *analysis.Pass, target ast.Expr) {
	selector, ok := ast.Unparen(target).(*ast.SelectorExpr)
	if !ok {
		return
	}
	selection := pass.TypesInfo.Selections[selector]
	if selection == nil || selection.Kind() != types.FieldVal {
		return
	}
	owner := selection.Recv()
	if pointer, ok := owner.Underlying().(*types.Pointer); ok {
		owner = pointer.Elem()
	}
	if isClosed(owner) {
		pass.Reportf(selector.Pos(), "a field of closed type %s is never assigned; assign one of its declared values instead", types.TypeString(owner, types.RelativeTo(pass.Pkg)))
	}
}
