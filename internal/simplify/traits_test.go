package simplify

import (
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

func TestSingleImplFindsTraitsWithOneImplementationOfAWorkspaceType(t *testing.T) {
	trait := func(name string, public bool, file string) unit.Trait {
		return unit.Trait{File: file, Name: name, StartLine: 1, EndLine: 5, Public: public}
	}
	impl := func(tr, ty string) unit.Impl { return unit.Impl{File: "x/src/impls.rs", Trait: tr, Type: ty} }
	decls := unit.Decls{
		Traits: []unit.Trait{
			trait("DirectoryStore", false, "x/src/store.rs"), // reported
			trait("Mocked", false, "x/src/a.rs"),             // a test double implements it too
			trait("Twice", false, "x/src/a.rs"),
			trait("Ext", false, "x/src/a.rs"),     // extends a foreign type
			trait("Blanket", false, "x/src/a.rs"), // impl<T> for T
			trait("Unused", false, "x/src/a.rs"),  // no impl: not this detector's concern
			trait("LibApi", true, "lib/src/a.rs"), // public trait of a published crate
			trait("Internal", true, "x/src/b.rs"), // public, but the crate is unpublished
			trait("Dup", false, "x/src/a.rs"),     // declared twice: ambiguous
			trait("Dup", false, "x/src/b.rs"),
		},
		Impls: []unit.Impl{
			impl("DirectoryStore", "DirectorySession"),
			impl("Mocked", "Real"), {Trait: "Mocked", Type: "Fake", Test: true},
			impl("Twice", "Real"), impl("Twice", "Other"),
			impl("Ext", "String"),
			{Trait: "Blanket", Type: "T", Blanket: true},
			impl("LibApi", "Real"),
			impl("Internal", "Real"),
			impl("Dup", "Real"),
		},
		TypeNames: []string{"DirectorySession", "Real", "Other", "Fake"},
	}
	internal := func(file string) bool { return strings.HasPrefix(file, "x/") }

	var got []string
	for _, s := range SingleImpl(decls, internal) {
		got = append(got, s.Trait.Name+" for "+s.Impl.Type)
	}
	if want := "DirectoryStore for DirectorySession\nInternal for Real"; strings.Join(got, "\n") != want {
		t.Errorf("single-implementation traits:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}
