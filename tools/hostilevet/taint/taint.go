// Package taint is the analysis behind hostilevet. It enforces what the Go type system
// cannot about hostile text (principle 3 in docs/core-principles.md):
//
//   - A hostile value is not formatted by fmt or log. (Concatenating one with +, converting
//     one with string(...), or writing one to a strings.Builder does not compile.)
//   - hostile.Map, Narrow and Classify take a declared function, never a function literal,
//     a function variable, or a bound method, any of which could carry the text out.
//   - A function given to Narrow or Classify reads raw text and answers a question. It does
//     not format that text with fmt, +, string(...), a strings.Builder or a bytes.Buffer,
//     or store it outside itself.
//   - Only package hostile reads the "$untrusted" tag, and only the boundary packages listed
//     in boundaries import hostile/reveal.
//   - A closed type has only unexported fields, its values are declared as package-level
//     variables, and no field of one is assigned.
//   - hostile.Titlef takes only clean values.
//   - An event type, an observer's reading, a worker's input or answer, a recorded fact and a
//     prompt variable have the shape hostile.CheckObserved or hostile.CheckClean requires, so
//     what start-up or a run would refuse is refused when the package is built.
//
// Package hostile, the packages under it, and this analyzer are exempt: they implement the
// rules.
package taint

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const (
	hostilePath = "github.com/ivy/almanac/hostile"
	revealPath  = hostilePath + "/reveal"

	analyzerPath = "github.com/ivy/almanac/tools/hostilevet"
)

// Analyzer reports hostile text reaching a string, and closed sets that are not closed.
var Analyzer = &analysis.Analyzer{
	Name:     "hostilevet",
	Doc:      "refuses formatting hostile text, and the other ways out of package hostile",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	if implementsTaint(pass.Pkg.Path()) {
		return nil, nil
	}
	root := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector).Root()
	checkFormatting(pass, root)
	checkNarrowing(pass, root)
	checkBoundaries(pass, root)
	checkClosed(pass, root)
	checkTitles(pass, root)
	checkShapes(pass, root)
	return nil, nil
}

// implementsTaint reports whether path is package hostile, a package under it, this
// analyzer, or the external test package of one.
func implementsTaint(path string) bool {
	path = strings.TrimSuffix(path, "_test")
	for _, implementation := range []string{hostilePath, analyzerPath} {
		if path == implementation || strings.HasPrefix(path, implementation+"/") {
			return true
		}
	}
	return false
}

func inTestFile(pass *analysis.Pass, node ast.Node) bool {
	return strings.HasSuffix(pass.Fset.File(node.Pos()).Name(), "_test.go")
}

// calleeIn returns the function or method a call invokes, if it is declared in package path.
func calleeIn(pass *analysis.Pass, call *ast.CallExpr, path string) *types.Func {
	var ident *ast.Ident
	switch fun := unwrapInstance(call.Fun).(type) {
	case *ast.Ident:
		ident = fun
	case *ast.SelectorExpr:
		ident = fun.Sel
	default:
		return nil
	}
	function, ok := pass.TypesInfo.Uses[ident].(*types.Func)
	if !ok || function.Pkg() == nil || function.Pkg().Path() != path {
		return nil
	}
	return function
}

// unwrapInstance strips parentheses and explicit type arguments from a function expression.
func unwrapInstance(expr ast.Expr) ast.Expr {
	expr = ast.Unparen(expr)
	switch e := expr.(type) {
	case *ast.IndexExpr:
		return ast.Unparen(e.X)
	case *ast.IndexListExpr:
		return ast.Unparen(e.X)
	}
	return expr
}

func isHostileType(t types.Type, name string) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	object := named.Origin().Obj()
	return object.Pkg() != nil && object.Pkg().Path() == hostilePath && object.Name() == name
}

// holdsHostile reports whether a value of type t is or holds a hostile.Of.
func holdsHostile(t types.Type) bool {
	return reaches(t, func(t types.Type) bool { return isHostileType(t, "Of") }, map[types.Type]bool{})
}

// holdsText reports whether a value of type t is or holds text: a string, or a byte or rune
// slice.
func holdsText(t types.Type) bool {
	return reaches(t, isText, map[types.Type]bool{})
}

func isText(t types.Type) bool {
	switch t := t.Underlying().(type) {
	case *types.Basic:
		return t.Info()&types.IsString != 0
	case *types.Slice:
		element, ok := t.Elem().Underlying().(*types.Basic)
		return ok && (element.Kind() == types.Byte || element.Kind() == types.Rune)
	}
	return false
}

// reaches reports whether match holds for t or for any type a value of t holds.
func reaches(t types.Type, match func(types.Type) bool, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	if match(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return reaches(u.Elem(), match, seen)
	case *types.Slice:
		return reaches(u.Elem(), match, seen)
	case *types.Array:
		return reaches(u.Elem(), match, seen)
	case *types.Chan:
		return reaches(u.Elem(), match, seen)
	case *types.Map:
		return reaches(u.Key(), match, seen) || reaches(u.Elem(), match, seen)
	case *types.Struct:
		for field := range u.Fields() {
			if reaches(field.Type(), match, seen) {
				return true
			}
		}
	case *types.Tuple:
		for variable := range u.Variables() {
			if reaches(variable.Type(), match, seen) {
				return true
			}
		}
	}
	return false
}

// isClosed reports whether t is a struct that embeds hostile.Closed.
func isClosed(t types.Type) bool {
	structure, ok := types.Unalias(t).Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for field := range structure.Fields() {
		if field.Embedded() && (isHostileType(field.Type(), "Closed") || isClosed(field.Type())) {
			return true
		}
	}
	return false
}
