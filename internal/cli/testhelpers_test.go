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

// Copies of a helper within one test file are its own business: only
// helpers copied between test files are reported.
func TestCheckLeavesHelperCopiesInOneFileAlone(t *testing.T) {
	files := testHelperCrate()
	files["src/orders.rs"] = "pub fn place() {}\n"
	files["src/accounts.rs"] = "pub fn open() {}\n#[cfg(test)]\nmod tests {\n    use super::*;\n\n    fn fixture() -> Store " + helperBody +
		"\n\n    fn seeded() -> Store " + helperBody + "\n}\n"
	code, stdout, _ := run("check", "--format", "json", repo(t, files))
	if r := decode(t, stdout); code != 0 || len(r.Candidates) != 0 {
		t.Errorf("exit %d, candidates %+v; want none: both copies are in one file", code, r.Candidates)
	}
}

// The helpers compared count as analysed code, so the duplication share
// measures the code it was found in.
func TestHelpersCountInTheAnalysedLines(t *testing.T) {
	_, stdout, _ := run("check", "--format", "json", repo(t, testHelperCrate()))
	h := decodeHealth(t, stdout).Summary.Health
	if h.DuplicatedLines == 0 || h.AnalysedLines < 2*h.DuplicatedLines || h.DuplicationPercent > 50 {
		t.Errorf("health = %+v, want the two helpers' lines among the analysed ones", h)
	}
}

// Go's testdata holds fixture programs, not helpers: copies there are not
// reported. Helpers compared count as analysed units, test cases as
// skipped.
func TestTestdataIsNoHelperAndHelpersCountAsAnalysed(t *testing.T) {
	fixture := "package fixture\n\nfunc build() []int {\n\tvar out []int\n\tfor i := 0; i < 10; i++ {\n\t\tif i%2 == 0 {\n\t\t\tout = append(out, i*i)\n\t\t} else {\n\t\t\tout = append(out, i+1)\n\t\t}\n\t}\n\tfor j := range out {\n\t\tout[j] += len(out)\n\t}\n\treturn out\n}\n"
	files := testHelperCrate()
	files["go.mod"] = "module example.com/app\n"
	files["fix/testdata/a/a.go"] = fixture
	files["fix/testdata/b/b.go"] = fixture
	_, stdout, _ := run("check", "--format", "json", repo(t, files))
	r := decode(t, stdout)
	if len(r.Candidates) != 1 || r.Candidates[0].Occurrences[0].Name != "fixture" {
		t.Errorf("candidates = %+v, want the Rust helpers alone, not the testdata fixtures", r.Candidates)
	}
	// Production: open, place. Helpers compared: fixture, seeded. Skipped
	// test code: the two saves and the two testdata fixtures.
	if r.Summary.Units != 4 || r.Summary.TestUnits != 4 {
		t.Errorf("summary = %+v, want 4 units analysed (2 production, 2 helpers) and 4 test units skipped", r.Summary)
	}
}
