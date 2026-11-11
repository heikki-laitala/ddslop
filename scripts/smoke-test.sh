#!/usr/bin/env bash
# Exercises a ddslop binary end to end on a small sample project:
# version, init, check (expects a candidate), status, uninstall --purge.
#
# Usage: scripts/smoke-test.sh path/to/ddslop
set -euo pipefail

ddslop="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
project="$(mktemp -d)"
trap 'rm -rf "$project"' EXIT

fail() {
    echo "smoke test failed: $*" >&2
    exit 1
}

"$ddslop" version | grep -q '^ddslop ' || fail "version"

mkdir -p "$project/src"
for name in users orders; do
    cat > "$project/src/$name.rs" <<RUST
pub fn load_${name}(db: &Db, id: u64) -> Result<Row, Error> {
    let row = db.query("SELECT * FROM ${name} WHERE id = \$1", &[&id])?;
    let name: String = row.get(0);
    let email: String = row.get(1);
    if name.is_empty() {
        return Err(Error::NotFound(id));
    }
    Ok(Row { name, email })
}
RUST
done

"$ddslop" init "$project" >/dev/null || fail "init"
test -f "$project/.claude/skills/ddslop/SKILL.md" || fail "init did not install the Claude skill"
grep -q '^## ddslop$' "$project/CLAUDE.md" || fail "init did not add the CLAUDE.md section"
grep -qx '.ddslop/' "$project/.gitignore" || fail "init did not add .ddslop/ to .gitignore"
test -f "$project/.ddslop/index.db" || fail "init did not build the index"

set +e
"$ddslop" check --format json --min-nodes 20 "$project" > "$project/report.json"
code=$?
set -e
test "$code" -eq 1 || fail "check exited $code, want 1 (a renamed copy)"
grep -q '"kind": "renamed-clone"' "$project/report.json" || fail "check did not report the renamed copy"

# Capture first: grep -q exits at the first match, and status writing its
# remaining lines into the closed pipe would fail the pipeline.
status="$("$ddslop" status "$project")" || fail "status exited non-zero"
grep -q '^claude: skill, instructions$' <<<"$status" || fail "status"

"$ddslop" uninstall --purge "$project" >/dev/null || fail "uninstall"
test ! -e "$project/.claude/skills/ddslop" || fail "uninstall left the skill"
test ! -e "$project/.ddslop" || fail "uninstall --purge left the index"

echo "smoke test passed"
