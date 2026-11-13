package cli

import (
	"strings"
	"testing"
)

// helperBody is a test helper big enough to count: about 15 lines.
const helperBody = `{
        let mut store = Store::default();
        store.insert("a", 1);
        store.insert("b", 2);
        store.insert("c", 3);
        if store.len() > 2 {
            store.remove("c");
        }
        for key in ["a", "b"] {
            store.touch(key);
        }
        store.flush();
        store.reopen();
        store
    }`

// testHelperCrate has the same helper in two modules' tests, and the same
// test case in both: the helper is a copy to share, the cases are not.
func testHelperCrate() map[string]string {
	tests := func(helper string) string {
		return "#[cfg(test)]\nmod tests {\n    use super::*;\n\n    fn " + helper + "() -> Store " + helperBody +
			"\n\n    #[test]\n    fn saves() {\n        let store = " + helper + "();\n        assert_eq!(store.len(), 2);\n        assert!(store.contains(\"a\"));\n    }\n}\n"
	}
	return map[string]string{
		"Cargo.toml":      "[package]\nname = \"app\"\n",
		"src/lib.rs":      "pub mod accounts;\npub mod orders;\n",
		"src/accounts.rs": "pub fn open() {}\n" + tests("fixture"),
		"src/orders.rs":   "pub fn place() {}\n" + tests("seeded"),
	}
}

func TestCheckReportsCopiedTestHelpers(t *testing.T) {
	code, stdout, stderr := run("check", "--format", "json", repo(t, testHelperCrate()))
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %s", code, stderr)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the copied helper alone", r.Candidates)
	}
	var names []string
	for _, o := range r.Candidates[0].Occurrences {
		names = append(names, o.Name)
	}
	if got := strings.Join(names, " "); got != "fixture seeded" {
		t.Errorf("occurrences = %s, want the two helpers, not the test cases", got)
	}
}
