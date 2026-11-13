package cli

import "testing"

func TestCodeInOutOfLineTestModulesIsTestCode(t *testing.T) {
	// lib.rs declares helpers as a test module, ledger.rs declares
	// real_ledger; helpers/deep.rs is a submodule of helpers. Their copies
	// of load_user are test code (test cases, which only --tests checks).
	root := repo(t, map[string]string{
		"svc/src/lib.rs":                users + "\n#[cfg(test)]\nmod helpers;\n\npub mod ledger;\n",
		"svc/src/helpers.rs":            "#[test]\n" + orders,
		"svc/src/helpers/deep.rs":       "#[test]\n" + orders,
		"svc/src/ledger.rs":             "pub fn noop() {}\n\n#[cfg(test)]\nmod real_ledger;\n",
		"svc/src/ledger/real_ledger.rs": "#[test]\n" + orders,
	})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	r := decode(t, stdout)
	if code != 0 || len(r.Candidates) != 0 || r.Summary.TestUnits != 3 {
		t.Errorf("exit %d, %d candidates, %d test units; want 0, 0 and 3: the copies are test code", code, len(r.Candidates), r.Summary.TestUnits)
	}

	if code, _, _ := run("check", "--format", "json", "--min-nodes", "20", "--tests", root); code != 1 {
		t.Errorf("with --tests: exit %d, want 1", code)
	}
}
