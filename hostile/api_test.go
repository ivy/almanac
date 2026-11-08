package hostile_test

import (
	"go/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/tools/go/packages"
)

// No exported function or method in package hostile hands back a hostile value as text. The
// one way out is hostile/reveal, which hostilevet keeps to the boundary packages.
var _ = Describe("The exported API", func() {
	It("returns nothing that could be the text, from anything that takes a hostile value", func() {
		pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedTypes}, "github.com/ivy/almanac/hostile")
		Expect(err).NotTo(HaveOccurred())
		Expect(pkgs).To(HaveLen(1))
		Expect(pkgs[0].Errors).To(BeEmpty())
		scope := pkgs[0].Types.Scope()

		var checked, leaking []string
		check := func(name string, function *types.Func) {
			checked = append(checked, name)
			if leaks(function) {
				leaking = append(leaking, name)
			}
		}
		for _, name := range scope.Names() {
			switch object := scope.Lookup(name).(type) {
			case *types.Func:
				check(name, object)
			case *types.TypeName:
				for method := range types.NewMethodSet(types.NewPointer(object.Type())).Methods() {
					if method.Obj().Exported() {
						check(name+"."+method.Obj().Name(), method.Obj().(*types.Func))
					}
				}
			}
		}
		Expect(checked).To(ContainElements("New", "Map", "Narrow", "Classify", "Zip", "Join", "Of.Format", "Of.MarshalJSON"))
		Expect(leaking).To(BeEmpty())
	})
})

// leaks reports whether f takes a hostile value and returns something that could be its text:
// a string, an interface, or a type parameter a string could instantiate.
func leaks(f *types.Func) bool {
	signature := f.Signature()
	takesHostile := signature.Recv() != nil && isOf(signature.Recv().Type())
	for param := range signature.Params().Variables() {
		takesHostile = takesHostile || isOf(param.Type())
	}
	if !takesHostile {
		return false
	}
	for result := range signature.Results().Variables() {
		if couldBeText(result.Type()) {
			return true
		}
	}
	return false
}

func isOf(t types.Type) bool {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg().Path() == "github.com/ivy/almanac/hostile" && named.Obj().Name() == "Of"
}

func couldBeText(t types.Type) bool {
	switch t := t.(type) {
	case *types.TypeParam:
		return types.Satisfies(types.Typ[types.String], t.Constraint().Underlying().(*types.Interface))
	case *types.Basic:
		return t.Info()&types.IsString != 0
	}
	switch t.Underlying().(type) {
	case *types.Interface:
		return t.String() != "error"
	case *types.Basic:
		return couldBeText(t.Underlying())
	}
	return false
}
