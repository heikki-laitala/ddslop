package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Two functions with the same structure: names, a literal and a called
// method differ, and the parameter names are local renames.
const users = `pub fn load_user(db: &Db, user_id: u64) -> Result<User, Error> {
    let row = db.query("SELECT * FROM users WHERE id = $1", &[&user_id])?;
    let name: String = row.get(0);
    let email: String = row.get(1);
    if name.is_empty() {
        return Err(Error::NotFound(user_id));
    }
    Ok(User { name, email })
}
`

const orders = `pub fn load_order(db: &Db, order_id: u64) -> Result<User, Error> {
    let row = db.query("SELECT * FROM orders WHERE id = $1", &[&order_id])?;
    let name: String = row.get(0);
    let email: String = row.get(1);
    if name.is_empty() {
        return Err(Error::NotFound(order_id));
    }
    Ok(User { name, email })
}
`

const unrelated = `pub fn add(a: u32, b: u32) -> u32 {
    a + b
}
`

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

type jsonReport struct {
	Candidates []struct {
		ID          string  `json:"id"`
		Fingerprint string  `json:"fingerprint"`
		Language    string  `json:"language"`
		Kind        string  `json:"kind"`
		Similarity  float64 `json:"similarity"`
		Occurrences []struct {
			File           string   `json:"file"`
			Name           string   `json:"name"`
			StartLine      int      `json:"start_line"`
			EndLine        int      `json:"end_line"`
			Similarity     float64  `json:"similarity"`
			UnmatchedLines [][2]int `json:"unmatched_lines"`
			Changed        bool     `json:"changed"`
		} `json:"occurrences"`
		Differences []struct {
			Kind   string   `json:"kind"`
			Values []string `json:"values"`
			Lines  [][]int  `json:"lines"`
			Local  bool     `json:"local"`
		} `json:"differences"`
		Suggestion struct {
			Action     string `json:"action"`
			Parameters int    `json:"parameters"`
		} `json:"suggestion"`
		Score struct {
			Nodes           int `json:"nodes"`
			Lines           int `json:"lines"`
			Occurrences     int `json:"occurrences"`
			DuplicatedNodes int `json:"duplicated_nodes"`
		} `json:"score"`
	} `json:"candidates"`
	Summary struct {
		Files          int `json:"files"`
		GeneratedFiles int `json:"generated_files"`
		Units          int `json:"units"`
		TestUnits      int `json:"test_units"`
		ParsedFiles    int `json:"parsed_files"`
		UnchangedFiles int `json:"unchanged_files"`
		Baselined      int `json:"baselined"`
		Candidates     int `json:"candidates"`
	} `json:"summary"`
}

func decode(t *testing.T, stdout string) jsonReport {
	t.Helper()
	var r jsonReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	return r
}

func TestCheckReportsRenamedCloneAsJSON(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs":  users,
		"src/orders.rs": orders,
		"src/math.rs":   unrelated,
	})

	code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (candidates found); stderr: %s", code, stderr)
	}
	r := decode(t, stdout)
	if r.Summary.Files != 3 || r.Summary.Units != 3 || r.Summary.Candidates != 1 {
		t.Errorf("summary = %+v, want 3 files, 3 units, 1 candidate", r.Summary)
	}
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(r.Candidates))
	}
	c := r.Candidates[0]
	if c.ID != "c1" || c.Language != "rust" || c.Kind != "renamed-clone" {
		t.Errorf("candidate = %s/%s/%s, want c1/rust/renamed-clone", c.ID, c.Language, c.Kind)
	}
	if !strings.HasPrefix(c.Fingerprint, "rust:") {
		t.Errorf("fingerprint = %q, want rust:<hash>", c.Fingerprint)
	}
	if len(c.Occurrences) != 2 {
		t.Fatalf("occurrences = %+v, want 2", c.Occurrences)
	}
	o := c.Occurrences[0]
	if o.File != filepath.Join("src", "orders.rs") || o.Name != "load_order" || o.StartLine != 1 || o.EndLine != 9 {
		t.Errorf("first occurrence = %+v, want src/orders.rs load_order 1-9", o)
	}

	// The literal differs: a real parameter. The parameter names and the
	// function names are local renames.
	var params, locals int
	for _, d := range c.Differences {
		if d.Local {
			locals++
			continue
		}
		params++
		if d.Kind != "literal" || !strings.Contains(d.Values[0], "orders") || !strings.Contains(d.Values[1], "users") {
			t.Errorf("unexpected parameter difference %+v", d)
		}
		if len(d.Lines) != 2 || d.Lines[0][0] != 2 {
			t.Errorf("difference lines = %v, want line 2 in each copy", d.Lines)
		}
	}
	if params != 1 || locals != 2 {
		t.Errorf("got %d parameter and %d local differences, want 1 and 2", params, locals)
	}
	if c.Suggestion.Action != "extract-common" || c.Suggestion.Parameters != 1 {
		t.Errorf("suggestion = %+v, want extract-common with 1 parameter", c.Suggestion)
	}
	if c.Score.Occurrences != 2 || c.Score.Lines != 9 || c.Score.Nodes == 0 || c.Score.DuplicatedNodes != c.Score.Nodes {
		t.Errorf("score = %+v", c.Score)
	}
}

func TestCheckSuggestsReuseForExactCopies(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": users})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	c := decode(t, stdout).Candidates[0]
	if c.Kind != "exact-clone" || c.Suggestion.Action != "reuse-existing" || c.Suggestion.Parameters != 0 {
		t.Errorf("candidate = %s, %+v; want exact-clone with reuse-existing", c.Kind, c.Suggestion)
	}
	if len(c.Differences) != 0 {
		t.Errorf("exact copies have differences: %+v", c.Differences)
	}
}

func TestCheckWithoutDuplicatesExitsZero(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": unrelated})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if r := decode(t, stdout); len(r.Candidates) != 0 || r.Summary.Units != 2 {
		t.Errorf("report = %+v, want no candidates and 2 units", r)
	}
}

func TestCheckRespectsMinNodes(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	code, _, _ := run("check", "--format", "json", "--min-nodes", "100000", root)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 when every unit is below --min-nodes", code)
	}
}

func TestCheckTextFormat(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	code, stdout, _ := run("check", "--format", "text", "--min-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"c1 renamed-clone",
		"a.rs:1-9 load_user",
		"b.rs:1-9 load_order",
		"extract-common",
		`"SELECT * FROM users WHERE id = $1" | "SELECT * FROM orders WHERE id = $1"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestCheckDefaultsToCurrentDirectoryAndCommand(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})
	t.Chdir(root)

	code, stdout, _ := run("--format", "json", "--min-nodes", "20")

	if code != 1 || len(decode(t, stdout).Candidates) != 1 {
		t.Errorf("exit code = %d, want 1 candidate when check and the path are omitted", code)
	}
}

func TestCheckErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":   {"check", "--bogus"},
		"bad format":     {"check", "--format", "xml", "."},
		"missing path":   {"check", filepath.Join(os.TempDir(), "ddslop-does-not-exist")},
		"too many paths": {"check", ".", "."},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := run(args...)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if stderr == "" {
				t.Error("no error message on stderr")
			}
		})
	}
}

func TestCheckFailsOnUnreadableFile(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users})
	if err := os.Chmod(filepath.Join(root, "a.rs"), 0o000); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := run("check", root)

	if code != 2 || !strings.Contains(stderr, "a.rs") {
		t.Errorf("exit code = %d, stderr = %q; want 2 and the file name", code, stderr)
	}
}

func TestCheckSkipsGeneratedFilesAndCountsThem(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs":            users,
		"src/generated/orders.rs": "//! Generated by openapi-to-rust. Do not edit.\n" + orders,
		"src/generated/more.rs":   "// @generated\n" + users,
	})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	if code != 0 {
		t.Errorf("exit code = %d, want 0: duplicates only involve generated files", code)
	}
	r := decode(t, stdout)
	if r.Summary.Files != 1 || r.Summary.GeneratedFiles != 2 || r.Summary.Units != 1 {
		t.Errorf("summary = %+v, want 1 analysed file, 2 generated files, 1 unit", r.Summary)
	}
}

func TestCheckSkipsTestsUnlessAsked(t *testing.T) {
	root := repo(t, map[string]string{
		"src/users.rs": users,
		"src/lib.rs":   "#[cfg(test)]\nmod tests {\n" + orders + "}\n",
		"tests/it.rs":  users,
	})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)
	if code != 0 {
		t.Errorf("exit code = %d, want 0: the only copies are in tests", code)
	}
	r := decode(t, stdout)
	if r.Summary.Units != 1 || r.Summary.TestUnits != 2 {
		t.Errorf("summary = %+v, want 1 unit analysed and 2 test units skipped", r.Summary)
	}

	code, stdout, _ = run("check", "--format", "json", "--min-nodes", "20", "--tests", root)
	if code != 1 {
		t.Fatalf("exit code with --tests = %d, want 1", code)
	}
	r = decode(t, stdout)
	if r.Summary.Units != 3 || r.Summary.TestUnits != 0 || len(r.Candidates) != 1 || len(r.Candidates[0].Occurrences) != 3 {
		t.Errorf("with --tests: summary = %+v, candidates = %d; want all 3 units in one candidate", r.Summary, len(r.Candidates))
	}
}

func TestCheckReusesTheIndexOnTheSecondRun(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})

	_, first, _ := run("check", "--format", "json", "--min-nodes", "20", root)
	code, second, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	if _, err := os.Stat(filepath.Join(root, ".ddslop", "index.db")); err != nil {
		t.Errorf("index database not created: %v", err)
	}
	r1, r2 := decode(t, first), decode(t, second)
	if r1.Summary.ParsedFiles != 2 || r1.Summary.UnchangedFiles != 0 {
		t.Errorf("first run summary = %+v, want 2 parsed files", r1.Summary)
	}
	if r2.Summary.ParsedFiles != 0 || r2.Summary.UnchangedFiles != 2 {
		t.Errorf("second run summary = %+v, want 2 unchanged files", r2.Summary)
	}
	if code != 1 || len(r2.Candidates) != 1 || len(r2.Candidates[0].Differences) != len(r1.Candidates[0].Differences) {
		t.Errorf("second run found %d candidates (exit %d), want the same single candidate", len(r2.Candidates), code)
	}
}

func TestCheckSeesEditsAfterIndexing(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders})
	if code, _, _ := run("check", "--format", "json", "--min-nodes", "20", root); code != 1 {
		t.Fatalf("first run exit code = %d, want 1", code)
	}

	p := filepath.Join(root, "b.rs")
	if err := os.WriteFile(p, []byte(unrelated), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)
	r := decode(t, stdout)
	if code != 0 || len(r.Candidates) != 0 || r.Summary.ParsedFiles != 1 {
		t.Errorf("after edit: exit %d, %d candidates, summary %+v; want 0, none, 1 file parsed", code, len(r.Candidates), r.Summary)
	}
}

// usersLogged is users with an extra logging statement on line 3 and a
// different column index on line 5.
const usersLogged = `pub fn load_logged(db: &Db, user_id: u64) -> Result<User, Error> {
    let row = db.query("SELECT * FROM users WHERE id = $1", &[&user_id])?;
    tracing::debug!(user_id, "loaded user row");
    let name: String = row.get(0);
    let email: String = row.get(2);
    if name.is_empty() {
        return Err(Error::NotFound(user_id));
    }
    Ok(User { name, email })
}
`

func TestCheckReportsNearMissCopies(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": usersLogged})

	code, stdout, stderr := run("check", "--format", "json", "--min-nodes", "20", "--min-near-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(r.Candidates))
	}
	c := r.Candidates[0]
	if c.Kind != "near-miss" || c.Similarity < 0.8 || c.Similarity >= 1 {
		t.Errorf("candidate = %s with similarity %v, want a near-miss in [0.8, 1)", c.Kind, c.Similarity)
	}
	if len(c.Occurrences) != 2 || c.Occurrences[0].File != "a.rs" || c.Occurrences[1].File != "b.rs" {
		t.Fatalf("occurrences = %+v, want a.rs and b.rs", c.Occurrences)
	}
	if len(c.Occurrences[0].UnmatchedLines) != 0 {
		t.Errorf("a.rs unmatched lines = %v, want none", c.Occurrences[0].UnmatchedLines)
	}
	if got := c.Occurrences[1].UnmatchedLines; len(got) != 1 || got[0] != [2]int{3, 3} {
		t.Errorf("b.rs unmatched lines = %v, want [[3 3]] (the logging statement)", got)
	}
	var column bool
	for _, d := range c.Differences {
		if d.Kind == "literal" && d.Values[0] == "1" && d.Values[1] == "2" && !d.Local {
			column = true
		}
	}
	if !column {
		t.Errorf("differences = %+v, want the column literal 1 | 2", c.Differences)
	}
	if c.Suggestion.Action != "extract-common" || c.Suggestion.Parameters == 0 {
		t.Errorf("suggestion = %+v, want extract-common with parameters", c.Suggestion)
	}
}

func TestCheckExactCopiesHaveSimilarityOneAndNoNearMissDuplicate(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": orders, "c.rs": usersLogged})

	_, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--min-near-nodes", "20", root)

	r := decode(t, stdout)
	kinds := map[string]int{}
	for _, c := range r.Candidates {
		kinds[c.Kind]++
		if c.Kind == "renamed-clone" && c.Similarity != 1 {
			t.Errorf("renamed clone similarity = %v, want 1", c.Similarity)
		}
	}
	// a.rs and b.rs are one shape: one renamed-clone group, and one
	// near-miss between that shape and c.rs, not one per copy.
	if kinds["renamed-clone"] != 1 || kinds["near-miss"] != 1 || len(r.Candidates) != 2 {
		t.Errorf("candidate kinds = %v, want one renamed-clone and one near-miss", kinds)
	}
}

func TestCheckMinSimilarity(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": usersLogged})

	_, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--min-near-nodes", "20", "--min-similarity", "1", root)
	for _, c := range decode(t, stdout).Candidates {
		if c.Kind == "near-miss" {
			t.Errorf("near miss reported with --min-similarity 1: similarity %v", c.Similarity)
		}
	}
	for _, bad := range []string{"0", "1.5", "-0.2"} {
		if code, _, stderr := run("check", "--min-similarity", bad, root); code != 2 || stderr == "" {
			t.Errorf("--min-similarity %s: exit %d, stderr %q; want 2 with an error", bad, code, stderr)
		}
	}
}

func TestCheckTextFormatShowsNearMiss(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": usersLogged})

	_, stdout, _ := run("check", "--format", "text", "--min-nodes", "20", "--min-near-nodes", "20", root)

	for _, want := range []string{"c1 near-miss", "% similar", "b.rs:1-10 load_logged (unmatched: 3)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text output is missing %q:\n%s", want, stdout)
		}
	}
}

// usersChecked is users with an extra check on line 5.
const usersChecked = `pub fn load_checked(db: &Db, user_id: u64) -> Result<User, Error> {
    let row = db.query("SELECT * FROM users WHERE id = $1", &[&user_id])?;
    let name: String = row.get(0);
    let email: String = row.get(1);
    ensure_valid_email(&email)?;
    if name.is_empty() {
        return Err(Error::NotFound(user_id));
    }
    Ok(User { name, email })
}
`

func TestCheckReportsNearMissFamilyAsOneCandidate(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": users, "b.rs": usersLogged, "c.rs": usersChecked})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--min-near-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want one family", len(r.Candidates))
	}
	c := r.Candidates[0]
	var files []string
	for _, o := range c.Occurrences {
		files = append(files, o.File)
	}
	if strings.Join(files, " ") != "a.rs b.rs c.rs" || c.Score.Occurrences != 3 {
		t.Fatalf("occurrences = %v (score %d), want anchor a.rs then b.rs, c.rs", files, c.Score.Occurrences)
	}
	if c.Occurrences[0].Similarity != 1 || c.Occurrences[1].Similarity >= 1 || c.Occurrences[2].Similarity >= 1 {
		t.Errorf("occurrence similarities = %v, %v, %v; want 1 for the anchor, below 1 for members",
			c.Occurrences[0].Similarity, c.Occurrences[1].Similarity, c.Occurrences[2].Similarity)
	}
	if c.Similarity != min(c.Occurrences[1].Similarity, c.Occurrences[2].Similarity) {
		t.Errorf("candidate similarity = %v, want the lowest member similarity", c.Similarity)
	}
	if fmt.Sprint(c.Occurrences[1].UnmatchedLines) != "[[3 3]]" || fmt.Sprint(c.Occurrences[2].UnmatchedLines) != "[[5 5]]" {
		t.Errorf("unmatched lines = %v and %v, want [[3 3]] and [[5 5]]", c.Occurrences[1].UnmatchedLines, c.Occurrences[2].UnmatchedLines)
	}
}

const syncUsers = `fn sync_users(client: &Client, users: &[User]) -> Result<(), Error> {
    let started = Instant::now();
    for user in users {
        client.push(user)?;
    }
    let mut attempt = 0;
    let body = client.render(users)?;
    while attempt < 3 {
        attempt += 1;
        client.send(&body)?;
    }
    metrics::record("users", started.elapsed());
    Ok(())
}
`

const syncOrders = `fn sync_orders(client: &Client, orders: &[Order], extra: u32) -> Result<(), Error> {
    if orders.is_empty() {
        return Ok(());
    }
    let mut tries = 0;
    let body = client.render(orders)?;
    while tries < 5 {
        tries += 1;
        client.send(&body)?;
    }
    audit(extra);
    Ok(())
}
`

func TestCheckReportsCopiedBlocksInsideFunctions(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": syncUsers, "b.rs": syncOrders})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", "--min-block-nodes", "20", root)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	r := decode(t, stdout)
	if len(r.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(r.Candidates))
	}
	c := r.Candidates[0]
	if c.Kind != "block-clone" || c.Similarity != 1 {
		t.Errorf("candidate = %s, similarity %v; want block-clone, 1", c.Kind, c.Similarity)
	}
	var occ []string
	for _, o := range c.Occurrences {
		occ = append(occ, fmt.Sprintf("%s:%d-%d %s", o.File, o.StartLine, o.EndLine, o.Name))
	}
	if fmt.Sprint(occ) != "[a.rs:6-11 sync_users b.rs:5-10 sync_orders]" {
		t.Errorf("occurrences = %v", occ)
	}
	if c.Suggestion.Action != "extract-common" || c.Suggestion.Parameters != 2 {
		t.Errorf("suggestion = %+v, want extract-common with 2 parameters (3 | 5, users | orders)", c.Suggestion)
	}
	if c.Score.Lines != 6 || c.Score.Occurrences != 2 || c.Score.DuplicatedNodes != c.Score.Nodes {
		t.Errorf("score = %+v, want 6 lines, 2 occurrences", c.Score)
	}
}

func TestCheckTextFormatShowsBlockClone(t *testing.T) {
	root := repo(t, map[string]string{"a.rs": syncUsers, "b.rs": syncOrders})

	_, stdout, _ := run("check", "--format", "text", "--min-nodes", "20", "--min-block-nodes", "20", root)

	for _, want := range []string{"c1 block-clone: 2 copies of 6 lines", "a.rs:6-11 sync_users", "b.rs:5-10 sync_orders"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestCheckBlockClonesNeedMoreNodesByDefault(t *testing.T) {
	// The copied run is about 50 nodes: enough for a function clone, below
	// the default 100 a block needs to be worth a helper.
	root := repo(t, map[string]string{"a.rs": syncUsers, "b.rs": syncOrders})

	code, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	if code != 0 || len(decode(t, stdout).Candidates) != 0 {
		t.Errorf("exit code = %d with candidates, want no block clone below --min-block-nodes 100", code)
	}
}

func TestCheckNearMissesNeedMoreNodesByDefault(t *testing.T) {
	// users and usersLogged are near misses of about 135 nodes: enough for a
	// function clone, below the default 150 a near miss needs, since small
	// functions often share Rust's usual shape without being copies.
	root := repo(t, map[string]string{"a.rs": users, "b.rs": usersLogged})

	_, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	for _, c := range decode(t, stdout).Candidates {
		if c.Kind == "near-miss" {
			t.Errorf("near miss of %d nodes reported below the default --min-near-nodes", c.Score.Nodes)
		}
	}
}

const debugResponses = `impl fmt::Debug for ResponsesTurn {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("ResponsesTurn")
            .field("items", &self.output.len())
            .field("incomplete", &self.incomplete)
            .finish()
    }
}
`

const debugMessages = `impl fmt::Debug for MessagesTurn {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("MessagesTurn")
            .field("blocks", &self.content.len())
            .field("stop_reason", &self.stop_reason)
            .finish()
    }
}
`

func TestCheckDropsCandidatesThatAreMostlyParameters(t *testing.T) {
	// The two Debug impls share a shape but differ in 5 places within 88
	// nodes: extracting them would turn most of the body into arguments.
	// users and orders differ in one literal.
	root := repo(t, map[string]string{
		"a.rs": debugResponses, "b.rs": debugMessages,
		"c.rs": users, "d.rs": orders,
	})

	_, stdout, _ := run("check", "--format", "json", "--min-nodes", "20", root)

	r := decode(t, stdout)
	if len(r.Candidates) != 1 || r.Candidates[0].Occurrences[0].File != "c.rs" {
		var found []string
		for _, c := range r.Candidates {
			found = append(found, c.Occurrences[0].File)
		}
		t.Errorf("candidates start at %v, want only the users/orders pair", found)
	}
}
