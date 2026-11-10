package rust

import (
	"fmt"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

const callsSrc = `impl LedgerApi for Ledger {
    async fn create(&self, x: u32) -> u32 {
        let a = helper(x);
        let b = Accounts::create(a);
        let c = self.store.get(b);
        Self::finish(c).await;
        utils::log::<u32>(c);
        self.audit(c);
        let v = vec![missed(c)];
        v.len()
    }
}

impl Ledger {
    fn finish(c: u32) {}
}


fn helper(x: u32) -> u32 {
    x
}

pub trait Local {
    fn f(&self);
}

trait Other {}
`

func callStrings(u unit.Unit) []string {
	kinds := map[unit.CallKind]string{unit.FreeCall: "free", unit.PathCall: "path", unit.MethodCall: "method"}
	var out []string
	for _, c := range u.Calls {
		name := c.Name
		if c.Qualifier != "" {
			name = c.Qualifier + "::" + c.Name
		}
		out = append(out, fmt.Sprintf("%s %s @%d", kinds[c.Kind], name, c.Line))
	}
	return out
}

func TestCallsAreRecordedInSourceOrder(t *testing.T) {
	create := byName(t, mustUnits(t, callsSrc), "Ledger::create")

	got := fmt.Sprint(callStrings(create))
	// self.audit records self as its receiver; self.store.get does not.
	want := "[free helper @3 path Accounts::create @4 method get @5 path Self::finish @6 path utils::log @7 method self::audit @8 method len @10]"
	if got != want {
		t.Errorf("calls =\n  %s\nwant\n  %s\n(calls inside vec![...] are not visible)", got, want)
	}
}

func TestTraitOfEnclosingImpl(t *testing.T) {
	units := mustUnits(t, callsSrc)
	if got := byName(t, units, "Ledger::create").Trait; got != "LedgerApi" {
		t.Errorf("Ledger::create trait = %q, want LedgerApi", got)
	}
	if got := byName(t, units, "Ledger::finish").Trait; got != "" {
		t.Errorf("inherent method trait = %q, want none", got)
	}
	if got := byName(t, units, "helper").Trait; got != "" {
		t.Errorf("free function trait = %q, want none", got)
	}
}

func TestTraitsListsDeclaredTraits(t *testing.T) {
	if got := fmt.Sprint(Traits([]byte(callsSrc))); got != "[Local Other]" {
		t.Errorf("Traits = %s, want [Local Other]", got)
	}
}
