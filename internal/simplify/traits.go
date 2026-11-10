package simplify

import (
	"github.com/heikki-laitala/ddslop/internal/unit"
)

// SingleImplTrait is a trait implemented exactly once.
type SingleImplTrait struct {
	Trait unit.Trait
	Impl  unit.Impl
}

// SingleImpl returns the non-test traits, declared once in decls (the
// declarations of every analysed file), that have exactly one
// implementation, for a type the code declares itself. Such a trait is an
// abstraction over one type, and its methods could be the type's own.
//
// Left out: traits a test double also implements (the second
// implementation is why the trait exists), extension traits adding
// methods to foreign types, blanket implementations, and public traits of
// published crates (internal reports whether a file's crate is
// unpublished), which code outside may implement.
func SingleImpl(decls unit.Decls, internal func(file string) bool) []SingleImplTrait {
	declared := map[string]int{}
	for _, t := range decls.Traits {
		declared[t.Name]++
	}
	impls := map[string][]unit.Impl{}
	for _, im := range decls.Impls {
		impls[im.Trait] = append(impls[im.Trait], im)
	}
	types := map[string]bool{}
	for _, name := range decls.TypeNames {
		types[name] = true
	}
	var out []SingleImplTrait
	for _, t := range decls.Traits {
		if t.Test || declared[t.Name] != 1 || (t.Public && !internal(t.File)) {
			continue
		}
		if ims := impls[t.Name]; len(ims) == 1 {
			im := ims[0]
			if !im.Test && !im.Blanket && types[im.Type] {
				out = append(out, SingleImplTrait{Trait: t, Impl: im})
			}
		}
	}
	return out
}
