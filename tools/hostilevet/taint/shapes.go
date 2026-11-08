package taint

import (
	"go/ast"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

const (
	wirePath     = "github.com/ivy/almanac/wire"
	vocabPath    = "github.com/ivy/almanac/vocab"
	workflowPath = "github.com/ivy/almanac/workflow"
	agentPath    = "github.com/ivy/almanac/agent"
)

// shape is what a value must be able to hold: an observed value is clean or hostile, and a
// clean one holds no hostile value at all. Both mirror hostile.CheckObserved and
// hostile.CheckClean, which refuse the same types when a binary starts or a run reaches them.
type shape struct {
	// what names the value, followed by its type, such as "an observer's reading of type".
	what     string
	observed bool
}

// typeArgs are the generic functions whose type argument says what is read from outside or
// recorded, by the type argument's position. An event registered with vocab.Event is checked
// where its type is declared.
var typeArgs = map[string]struct {
	index int
	shape shape
}{
	vocabPath + ".Observer":      {1, shape{"an observer's reading of type", true}},
	vocabPath + ".Worker":        {0, shape{"a worker's answer of type", false}},
	workflowPath + ".Observe":    {0, shape{"an observed value of type", true}},
	workflowPath + ".RecordFact": {0, shape{"a recorded fact of type", false}},
	workflowPath + ".Act":        {0, shape{"a worker's answer of type", false}},
}

// inputs are the functions and methods whose argument is given to a worker, by its position.
var inputs = map[string]int{
	workflowPath + ".Act":            4,
	"(" + agentPath + ".Worker).Act": 1,
}

var (
	cleanInput = shape{"a worker's input of type", false}
	promptVar  = shape{"a prompt variable of type", false}
)

// checkShapes refuses, when the package is built, a type that hostile.CheckObserved or
// hostile.CheckClean would refuse when the binary starts or a run reaches it: an event type
// (one whose pointer is a wire.Event) that could hold free text, an observer's reading or a
// worker's answer of such a type, and a worker's input or a prompt variable that is not clean.
// A value whose static type is an interface or a type parameter is left to those checks.
// Tests may build such values, to show the run-time refusals.
func checkShapes(pass *analysis.Pass, root inspector.Cursor) {
	checkEvents(pass)
	for cursor := range root.Preorder((*ast.CallExpr)(nil), (*ast.CompositeLit)(nil)) {
		node := cursor.Node()
		if inTestFile(pass, node) {
			continue
		}
		switch node := node.(type) {
		case *ast.CallExpr:
			checkCallShapes(pass, node)
		case *ast.CompositeLit:
			if isNamed(pass.TypesInfo.TypeOf(node), agentPath, "Vars") {
				for _, element := range node.Elts {
					if pair, ok := element.(*ast.KeyValueExpr); ok {
						reportValueShape(pass, pair.Value, promptVar)
					}
				}
			}
		}
	}
}

// checkEvents refuses an event type declared in the package that could hold free text.
func checkEvents(pass *analysis.Pass) {
	event := wireEvent(pass.Pkg)
	if event == nil {
		return
	}
	scope := pass.Pkg.Scope()
	for _, name := range scope.Names() {
		object, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || object.IsAlias() || inTestFile(pass, &ast.Ident{NamePos: object.Pos()}) {
			continue
		}
		if !types.Implements(types.NewPointer(object.Type()), event) {
			continue
		}
		reportShape(pass, &ast.Ident{NamePos: object.Pos()}, object.Type(), shape{"the event type", true})
	}
}

// wireEvent is the interface wire.Event, if pkg can name it.
func wireEvent(pkg *types.Package) *types.Interface {
	for _, imported := range pkg.Imports() {
		if imported.Path() != wirePath {
			continue
		}
		if object, ok := imported.Scope().Lookup("Event").(*types.TypeName); ok {
			if event, ok := object.Type().Underlying().(*types.Interface); ok {
				return event
			}
		}
	}
	return nil
}

func checkCallShapes(pass *analysis.Pass, call *ast.CallExpr) {
	function := calledFunction(pass, call)
	if function == nil || function.Pkg() == nil {
		return
	}
	name := function.FullName()
	if want, ok := typeArgs[name]; ok {
		if instance, ok := pass.TypesInfo.Instances[calleeIdent(call)]; ok && instance.TypeArgs.Len() > want.index {
			reportShape(pass, call.Fun, instance.TypeArgs.At(want.index), want.shape)
		}
	}
	if index, ok := inputs[name]; ok && index < len(call.Args) {
		reportValueShape(pass, call.Args[index], cleanInput)
	}
}

// calledFunction is the function or method a call invokes, if it is a declared one.
func calledFunction(pass *analysis.Pass, call *ast.CallExpr) *types.Func {
	ident := calleeIdent(call)
	if ident == nil {
		return nil
	}
	function, _ := pass.TypesInfo.Uses[ident].(*types.Func)
	return function
}

func calleeIdent(call *ast.CallExpr) *ast.Ident {
	switch fun := unwrapInstance(call.Fun).(type) {
	case *ast.Ident:
		return fun
	case *ast.SelectorExpr:
		return fun.Sel
	}
	return nil
}

// reportValueShape reports a value that could carry free text it should not, unless it is nil
// or its static type is an interface: what it holds is then known only when a run reaches it.
func reportValueShape(pass *analysis.Pass, value ast.Expr, want shape) {
	t := pass.TypesInfo.TypeOf(value)
	if t == nil || types.IsInterface(t) || pass.TypesInfo.Types[value].IsNil() {
		return
	}
	reportShape(pass, value, t, want)
}

// reportShape reports at node why a value of type t could carry free text it should not.
func reportShape(pass *analysis.Pass, node ast.Node, t types.Type, want shape) {
	if t == nil {
		return
	}
	walk := shapeWalk{observed: want.observed, seen: map[types.Type]bool{}}
	path, holds, refused := walk.walk(t, "")
	if !refused {
		return
	}
	at := "it is"
	if path != "" {
		at = path + " is"
	}
	next := "make it a number, a bool, a validated type such as hostile.Slug, or a closed set"
	if want.observed {
		next = "make it a hostile.Of, a number, a bool, a validated type such as hostile.Slug, or a closed set"
	}
	pass.Reportf(node.Pos(), "%s %s could carry free text, because %s %s: %s", want.what, types.TypeString(t, types.RelativeTo(pass.Pkg)), at, holds, next)
}

// shapeWalk is hostile's checkShape over go/types: it finds where a type could hold text that
// is not hostile, or, for a clean type, any hostile value at all.
type shapeWalk struct {
	observed bool
	seen     map[types.Type]bool
}

func (s shapeWalk) walk(t types.Type, path string) (string, string, bool) {
	t = types.Unalias(t)
	if s.seen[t] {
		return "", "", false
	}
	s.seen[t] = true
	switch {
	case isTypeParam(t):
		return "", "", false
	case isHostileType(t, "Of"):
		if s.observed {
			return "", "", false
		}
		return path, "a hostile value", true
	case isNamed(t, "encoding/json", "RawMessage"):
		return path, "raw JSON", true
	case isHostileType(t, "Title"):
		return path, "a title, which has no stored form", true
	case isHostileType(t, "Slug"), isHostileType(t, "Sha"), isNamed(t, "time", "Time"), decodesItself(t):
		return "", "", false
	case isClosed(t):
		return path, "a closed set with no UnmarshalJSON to choose among its values", true
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&(types.IsBoolean|types.IsNumeric) != 0 && u.Info()&types.IsComplex == 0:
			return "", "", false
		case u.Info()&types.IsString != 0:
			return path, "a string", true
		}
		return path, "a " + u.Name() + ", which JSON cannot hold", true
	case *types.Pointer:
		return s.walk(u.Elem(), path)
	case *types.Array:
		return s.walk(u.Elem(), path)
	case *types.Slice:
		if element, ok := u.Elem().Underlying().(*types.Basic); ok && element.Kind() == types.Byte {
			return path, "a byte slice", true
		}
		return s.walk(u.Elem(), path+"[]")
	case *types.Map:
		if p, holds, refused := s.walk(u.Key(), path+"{key}"); refused {
			return p, holds, true
		}
		return s.walk(u.Elem(), path+"{}")
	case *types.Struct:
		for i := range u.NumFields() {
			field := u.Field(i)
			if !field.Exported() && !field.Embedded() || jsonSkipped(u.Tag(i)) {
				continue
			}
			if p, holds, refused := s.walk(field.Type(), path+"."+field.Name()); refused {
				return p, holds, true
			}
		}
		return "", "", false
	case *types.Interface:
		return path, "an interface, which can hold anything", true
	case *types.Chan:
		return path, "a chan, which JSON cannot hold", true
	case *types.Signature:
		return path, "a func, which JSON cannot hold", true
	}
	return path, "a " + t.String() + ", which JSON cannot hold", true
}

func isTypeParam(t types.Type) bool {
	_, ok := t.(*types.TypeParam)
	return ok
}

func isNamed(t types.Type, path, name string) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	object := named.Origin().Obj()
	return object.Pkg() != nil && object.Pkg().Path() == path && object.Name() == name
}

// decodesItself reports whether a value of t validates what it accepts when it is decoded.
func decodesItself(t types.Type) bool {
	methods := types.NewMethodSet(types.NewPointer(t))
	for _, name := range []string{"UnmarshalJSON", "UnmarshalText"} {
		if methods.Lookup(nil, name) != nil {
			return true
		}
	}
	return false
}

func jsonSkipped(tag string) bool {
	name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
	return name == "-"
}
