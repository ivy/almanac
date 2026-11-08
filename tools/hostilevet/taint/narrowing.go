package taint

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// checkNarrowing checks each call to hostile.Map, Narrow or Classify: the function it is
// given, and, for Narrow and Classify, what that function does with the text.
func checkNarrowing(pass *analysis.Pass, root inspector.Cursor) {
	declarations := map[*types.Func]*ast.FuncDecl{}
	for cursor := range root.Preorder((*ast.FuncDecl)(nil)) {
		declaration := cursor.Node().(*ast.FuncDecl)
		if function, ok := pass.TypesInfo.Defs[declaration.Name].(*types.Func); ok {
			declarations[function] = declaration
		}
	}

	checked := map[*types.Func]bool{}
	for cursor := range root.Preorder((*ast.CallExpr)(nil)) {
		call := cursor.Node().(*ast.CallExpr)
		function := calleeIn(pass, call, hostilePath)
		if function == nil || len(call.Args) != 2 {
			continue
		}
		switch function.Name() {
		case "Map":
			declaredFunction(pass, call.Args[1], function.Name())
		case "Narrow", "Classify":
			narrower := declaredFunction(pass, call.Args[1], function.Name())
			if declaration := declarations[narrower]; declaration != nil && !checked[narrower] {
				checked[narrower] = true
				checkNarrower(pass, declaration, function.Name())
			}
		}
	}
}

// declaredFunction returns the declared function arg names, or reports why arg is not one.
func declaredFunction(pass *analysis.Pass, arg ast.Expr, takes string) *types.Func {
	switch expr := unwrapInstance(arg).(type) {
	case *ast.FuncLit:
		pass.Reportf(arg.Pos(), "hostile.%s takes a declared function, not a function literal, which could capture a variable and carry the text out", takes)
		return nil
	case *ast.SelectorExpr:
		if selection := pass.TypesInfo.Selections[expr]; selection != nil && selection.Kind() == types.MethodVal {
			pass.Reportf(arg.Pos(), "hostile.%s takes a declared function, not a bound method, whose receiver could carry the text out", takes)
			return nil
		}
		if function, ok := pass.TypesInfo.Uses[expr.Sel].(*types.Func); ok {
			return function.Origin()
		}
	case *ast.Ident:
		if function, ok := pass.TypesInfo.Uses[expr].(*types.Func); ok {
			return function.Origin()
		}
	}
	pass.Reportf(arg.Pos(), "hostile.%s takes a declared function, not a function value, which could come from anywhere", takes)
	return nil
}

// checkNarrower refuses a narrowing function formatting the raw text it reads, or storing it
// outside itself. Its parameters, and the locals it fills from them, hold raw text.
func checkNarrower(pass *analysis.Pass, declaration *ast.FuncDecl, by string) {
	if declaration.Body == nil {
		return
	}
	n := &narrower{pass: pass, name: declaration.Name.Name, by: by, raw: map[types.Object]bool{}}
	for _, list := range []*ast.FieldList{declaration.Recv, declaration.Type.Params} {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			for _, name := range field.Names {
				n.raw[pass.TypesInfo.Defs[name]] = true
			}
		}
	}
	n.spread(declaration.Body)
	ast.Inspect(declaration.Body, n.check)
}

type narrower struct {
	pass *analysis.Pass
	name string
	by   string
	raw  map[types.Object]bool
}

// spread marks the locals that are filled from raw text, until nothing more changes.
func (n *narrower) spread(body *ast.BlockStmt) {
	for changed := true; changed; {
		changed = false
		mark := func(target ast.Expr) {
			ident, ok := rootOf(target).(*ast.Ident)
			if !ok {
				return
			}
			if object := n.pass.TypesInfo.ObjectOf(ident); object != nil && !n.raw[object] && holdsText(object.Type()) {
				n.raw[object] = true
				changed = true
			}
		}
		ast.Inspect(body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				for i, target := range node.Lhs {
					if n.isRaw(source(node.Rhs, len(node.Lhs), i)) {
						mark(target)
					}
				}
			case *ast.ValueSpec:
				for i, name := range node.Names {
					if n.isRaw(source(node.Values, len(node.Names), i)) {
						mark(name)
					}
				}
			case *ast.RangeStmt:
				if n.isRaw(node.X) {
					if node.Key != nil {
						mark(node.Key)
					}
					if node.Value != nil {
						mark(node.Value)
					}
				}
			}
			return true
		})
	}
}

// source returns the expression the i-th of count targets is assigned from: its own value, or
// the one call that returns them all. A declaration without values has none.
func source(values []ast.Expr, count, i int) ast.Expr {
	switch len(values) {
	case count:
		return values[i]
	case 1:
		return values[0]
	}
	return nil
}

// isRaw reports whether expr is text taken from the raw text: text, built only through text
// from a raw variable. A length, an index or a comparison is not text, so it ends the chain.
func (n *narrower) isRaw(expr ast.Expr) bool {
	if expr == nil || !holdsText(n.pass.TypesInfo.TypeOf(expr)) {
		return false
	}
	switch expr := expr.(type) {
	case *ast.Ident:
		return n.raw[n.pass.TypesInfo.ObjectOf(expr)]
	case *ast.ParenExpr:
		return n.isRaw(expr.X)
	case *ast.SelectorExpr:
		return n.isRaw(expr.X)
	case *ast.IndexExpr:
		return n.isRaw(expr.X)
	case *ast.SliceExpr:
		return n.isRaw(expr.X)
	case *ast.StarExpr:
		return n.isRaw(expr.X)
	case *ast.UnaryExpr:
		return n.isRaw(expr.X)
	case *ast.TypeAssertExpr:
		return n.isRaw(expr.X)
	case *ast.BinaryExpr:
		return n.isRaw(expr.X) || n.isRaw(expr.Y)
	case *ast.CompositeLit:
		for _, element := range expr.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				element = pair.Value
			}
			if n.isRaw(element) {
				return true
			}
		}
	case *ast.CallExpr:
		if selector, ok := expr.Fun.(*ast.SelectorExpr); ok && n.isRaw(selector.X) {
			return true
		}
		for _, arg := range expr.Args {
			if n.isRaw(arg) {
				return true
			}
		}
	}
	return false
}

// check reports each route by which raw text leaves as text.
func (n *narrower) check(node ast.Node) bool {
	switch node := node.(type) {
	case *ast.CallExpr:
		n.checkCall(node)
	case *ast.BinaryExpr:
		if node.Op == token.ADD && isText(n.pass.TypesInfo.TypeOf(node)) && (n.isRaw(node.X) || n.isRaw(node.Y)) {
			n.report(node, "concatenates it with +")
		}
	case *ast.AssignStmt:
		n.checkAssign(node)
	case *ast.SendStmt:
		if n.isRaw(node.Value) {
			n.report(node, "sends it on a channel")
		}
	}
	return true
}

func (n *narrower) checkCall(call *ast.CallExpr) {
	anyRaw := false
	for _, arg := range call.Args {
		anyRaw = anyRaw || n.isRaw(arg)
	}
	if !anyRaw {
		return
	}
	if conversion := n.pass.TypesInfo.Types[call.Fun]; conversion.IsType() {
		if isText(conversion.Type) {
			n.report(call, "converts it with "+types.ExprString(call.Fun)+"(...)")
		}
		return
	}
	if formatter := formatterOf(n.pass, call); formatter != "" {
		n.report(call, "formats it with "+formatter)
		return
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok && isBuffer(n.pass.TypesInfo.TypeOf(selector.X)) {
		n.report(call, "writes it to a "+types.TypeString(n.pass.TypesInfo.TypeOf(selector.X), nil))
	}
}

func (n *narrower) checkAssign(assign *ast.AssignStmt) {
	for i, target := range assign.Lhs {
		if !n.isRaw(source(assign.Rhs, len(assign.Lhs), i)) {
			continue
		}
		if assign.Tok == token.ADD_ASSIGN {
			n.report(assign, "concatenates it with +=")
		} else if n.outside(target) {
			n.report(assign, "stores it outside the function")
		}
	}
}

// outside reports whether an assignment to target reaches past the function: a package-level
// variable, or anything reached through one or through a pointer. A local map or slice may
// alias one outside too; that is a gap, and review stands behind it.
func (n *narrower) outside(target ast.Expr) bool {
	switch target := ast.Unparen(target).(type) {
	case *ast.Ident:
		object, ok := n.pass.TypesInfo.ObjectOf(target).(*types.Var)
		return ok && object.Parent() == object.Pkg().Scope()
	case *ast.SelectorExpr:
		if _, ok := n.pass.TypesInfo.TypeOf(target.X).Underlying().(*types.Pointer); ok {
			return true
		}
		return n.outside(target.X)
	case *ast.IndexExpr:
		return n.outside(target.X)
	case *ast.StarExpr:
		return true
	}
	return false
}

func (n *narrower) report(node ast.Node, route string) {
	n.pass.Reportf(node.Pos(), "%s reads raw text for hostile.%s and %s: a narrowing function answers a question and does not carry the text", n.name, n.by, route)
}

// isBuffer reports whether t is a strings.Builder or a bytes.Buffer, or a pointer to one.
func isBuffer(t types.Type) bool {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	name := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	return name == "strings.Builder" || name == "bytes.Buffer"
}

// rootOf returns the variable an assignment target is part of: local for local.text[0].
func rootOf(target ast.Expr) ast.Expr {
	for {
		switch expr := ast.Unparen(target).(type) {
		case *ast.SelectorExpr:
			target = expr.X
		case *ast.IndexExpr:
			target = expr.X
		case *ast.StarExpr:
			target = expr.X
		default:
			return expr
		}
	}
}
