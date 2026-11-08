package taint

import (
	"go/ast"
	"go/constant"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ast/inspector"
)

// boundaries are the packages that may import hostile/reveal, because they hand hostile text
// to something outside Almanac. Each is here for a reason, and adding one is a reviewed change.
var boundaries = []string{
	// Checks an agent's patch against the patch policy, which reads paths and contents.
	"github.com/ivy/almanac/patch",
	// Writes to GitHub: an issue body quoted as inert text, and a checked patch pushed.
	"github.com/ivy/almanac/github",
}

// mayReveal reports whether the package at path may import hostile/reveal.
func mayReveal(path string) bool {
	return slices.Contains(boundaries, strings.TrimSuffix(path, "_test"))
}

// tag is the JSON key package hostile stores a hostile value under.
const tag = "$untrusted"

// checkBoundaries refuses reading hostile text through its stored form, and importing
// hostile/reveal outside the boundaries. Tests may do both, to assert what a boundary sends.
func checkBoundaries(pass *analysis.Pass, root inspector.Cursor) {
	for cursor := range root.Preorder((*ast.ImportSpec)(nil), (*ast.BasicLit)(nil), (*ast.BinaryExpr)(nil), (*ast.Field)(nil)) {
		node := cursor.Node()
		if inTestFile(pass, node) {
			continue
		}
		switch node := node.(type) {
		case *ast.ImportSpec:
			path, err := strconv.Unquote(node.Path.Value)
			if err == nil && path == revealPath && !mayReveal(pass.Pkg.Path()) {
				pass.Reportf(node.Pos(), "only the boundary packages tools/hostilevet lists may import hostile/reveal: narrow the value with hostile.Narrow or hostile.Classify instead")
			}
		case *ast.BasicLit, *ast.BinaryExpr:
			if value := pass.TypesInfo.Types[node.(ast.Expr)].Value; mentionsTag(value) && !partOfTagged(pass, cursor) {
				pass.Reportf(node.Pos(), "only package hostile reads the %s tag: narrow the value with hostile.Narrow or hostile.Classify instead", tag)
			}
		case *ast.Field:
			if node.Tag != nil && taggedField(node.Tag) {
				pass.Reportf(node.Tag.Pos(), "only package hostile decodes the %s tag: keep the field a hostile.Of", tag)
			}
		}
	}
}

func mentionsTag(value constant.Value) bool {
	return value != nil && value.Kind() == constant.String && strings.Contains(constant.StringVal(value), tag)
}

// partOfTagged reports whether the constant at cursor is an operand of a larger constant that
// mentions the tag, so that "$untr" + "usted" is reported once, at the whole.
func partOfTagged(pass *analysis.Pass, cursor inspector.Cursor) bool {
	parent, ok := cursor.Parent().Node().(*ast.BinaryExpr)
	return ok && mentionsTag(pass.TypesInfo.Types[parent].Value)
}

func taggedField(literal *ast.BasicLit) bool {
	tags, err := strconv.Unquote(literal.Value)
	if err != nil {
		return false
	}
	name, _, _ := strings.Cut(reflect.StructTag(tags).Get("json"), ",")
	return name == tag
}
