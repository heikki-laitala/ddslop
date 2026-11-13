# ddslop

**Find the duplicated and dead code AI agents leave behind, hand the
agent a fix it can apply, and show it the codebase's conventions before
it writes the next copy.**

> [!NOTE]
> **ddslop supports Rust and Go.** Every detector works for both, except
> setup-teardown, which Go does not need: its `defer` already is the
> idiom. TypeScript is planned. Files in other languages are not
> analysed.

Coding agents regenerate rather than reuse. Each session that cannot see
the helper written in the last one writes it again, a little differently:
a second `load_user`, a struct copied into another service, a constant
declared twice, a wrapper around a function that already existed, a trait
for a type that only ever has one implementation. Linters see none of it,
because each copy is valid code. Over a few hundred sessions it adds up to
a codebase that is larger, slower to change and harder for the next agent
to read.

ddslop finds that slop and turns it into refactoring candidates written
for the agent: exact file paths and line ranges, what differs between the
copies, which differences become parameters, and the action to take.
**No LLM, no embeddings, no API keys, no network calls.** It parses the
code with tree-sitter, keeps an index on disk, and runs locally in about
a second.

**Measured on a 733k-line Rust workspace** (cold run 9.7 s, warm run 1.5 s
on 12 cores), ddslop reported 713 candidates:

| What it found | Count | Example |
| --- | ---: | --- |
| Functions copied exactly or with renames | 226 | the same row loader in two services |
| Near-miss families (copies that drifted) | 189 | eight handlers with one statement more or less each |
| Blocks copied inside larger functions | 131 | a 46-node connection guard pasted into 27 methods |
| Features running the same pipeline | 8 | endpoints repeating decode → authorise → store → respond step by step |
| Structs with the same fields | 31 | one config type declared again in a second service |
| Constants declared twice or written inline | 104 | a field name written out 25 times next to its constant |
| Public functions nothing uses | 6 | helpers no file mentions anywhere else, checked with grep |
| Wrappers that only forward their arguments | 15 | `find_user(id)` whose whole body is `load_user(id)` |
| Traits with a single implementation | 3 | a store trait implemented by one session type, no test double |

Finding copies is half the job; the other half is not writing them.
`ddslop map` tells the agent, before it starts, how the codebase is
built: its layers, the helpers it already shares, the features that
copied one pipeline, and how it checks and wraps errors. Code that then
strays from those conventions is reported as `convention-drift`.

Works with **Claude Code**, **Codex** and **Copilot** through a skill that
`ddslop init` installs: the agent reads the map before starting a
feature, runs ddslop before it finishes a change, when you ask it to
clean up, and before it writes a new helper. Runs on Linux and macOS.

## Install

```sh
curl -sSfL https://raw.githubusercontent.com/heikki-laitala/ddslop/main/install.sh | bash
```

The script fetches a prebuilt binary of the latest
[release](https://github.com/heikki-laitala/ddslop/releases) for Linux or
macOS, x86_64 or aarch64, verifies its checksum, installs it to
`~/.local/bin` and, inside a Git repository, offers to set ddslop up for
your coding agent. Options: `--claude`, `--codex`, `--copilot` (set up
without asking), `--dir DIR`, `--version V` (a release tag such as
`v1.0.0`), `--no-interactive`.

### From source

Needs Go 1.25 and a C compiler, since tree-sitter and SQLite are C:

```sh
go install github.com/heikki-laitala/ddslop@latest
```

or clone the repository and run `make build` (binary in `bin/ddslop`).

### Upgrade

```sh
ddslop upgrade           # replace ddslop with the latest release
ddslop upgrade --check   # only say whether a newer release exists
```

It verifies the download against the release's checksums before
replacing the binary, and is the only command that uses the network.
`--version V` installs a particular release. A build from source is not
replaced unless you add `--force`.

### Uninstall

Remove ddslop from each repository you set it up in, then delete the
binary:

```sh
ddslop uninstall --purge path/to/repo   # skills, instructions, .gitignore entry and index
rm ~/.local/bin/ddslop                  # the installer's only file (or your --dir)
```

Without `--purge`, the index in `.ddslop/` is kept. `ddslop status` shows
what is installed in a repository.

## Quick start

```sh
cd your-project
ddslop init     # skill for Claude Code, CLAUDE.md section, .gitignore, index
ddslop check    # report duplicated code
```

Then ask your agent to clean up: the skill tells it how to read each kind
of candidate and when to leave copies alone.

`ddslop check` prints text in a terminal and JSON when piped. Exit codes:
0 nothing found, 1 candidates found, 2 error. It ends with the
repository's health, to track a cleanup over time:

```text
Duplication 7.6% (13502 of 177742 lines); 6 unused functions, 15 wrappers, 3 single-implementation traits, 8 parallel flows
```

The JSON `summary.health` has the same numbers. They always describe the
whole repository, also with `--diff` and `--baseline`. Each check records
them in the index, and `ddslop stats` shows the trend: one line per
commit or change, and how far duplication moved.

To check only what a change introduced, compare against a Git ref:

```sh
ddslop check --diff HEAD          # uncommitted work
ddslop check --diff origin/main   # everything on this branch
```

The whole repository is still analysed, so new code is compared against
all existing code, but only candidates touching changed lines are
reported, and the changed copies are marked `"changed": true`. This is
the check that stops an agent adding the next copy.

## What it finds

| Kind | What it is | Fix |
| --- | --- | --- |
| **Copied code** | | |
| `exact-clone`, `renamed-clone` | the same function, perhaps with other names, types or values | keep one; differences become parameters |
| `near-miss` | similar functions with statements added, removed or changed | extract the common code |
| `block-clone` | statements pasted into several larger functions | extract a helper |
| `duplicate-type`, `duplicate-constant` | the same struct, enum or constant declared again | keep one |
| `inlined-constant` | a string literal spelling out an existing constant | use the constant |
| **Repeated patterns** | | |
| `parallel-flow` | features running the same pipeline through their own functions | one generic pipeline |
| `shared-preamble` | the same early-return check (sign-in, permission) in 5+ functions | an extractor or middleware |
| `setup-teardown` | 5+ functions opening and closing the same thing (a transaction) | a closure helper or RAII guard |
| `string-kinds` | the same string literals matched in 3+ functions | an enum |
| `parameter-group` | the same 3+ parameters in 4+ functions | a parameter struct |
| **Drift** | | |
| `convention-drift` | a job done differently from 90%+ of the codebase: one `err.Error() == "..."` among dozens of `errors.Is` | follow the convention |
| **Code to simplify** | | |
| `dead-code` | a public function nothing in the repository uses | delete it |
| `pass-through` | a wrapper that only forwards its parameters | call the target |
| `single-impl-trait` | a trait with one implementation and no test double | drop the trait |

Each candidate carries everything an agent needs to fix it:

```json
{
  "id": "c1",
  "kind": "renamed-clone",
  "occurrences": [
    {"file": "src/orders.rs", "name": "load_order", "start_line": 1, "end_line": 9},
    {"file": "src/users.rs", "name": "load_user", "start_line": 1, "end_line": 9}
  ],
  "differences": [
    {"kind": "identifier", "values": ["order_id", "user_id"], "local": true},
    {"kind": "literal", "values": ["\"... orders ...\"", "\"... users ...\""], "local": false}
  ],
  "suggestion": {"action": "extract-common", "parameters": 1}
}
```

Non-local differences become parameters; a `target` names existing code
to use instead. Candidates are ranked by how much code the fix removes,
and `ddslop explain c1` shows the copies' code side by side.

**Few false alarms.** Generated files, test code (unless `--tests`),
small functions, copies that share only a shape, public API of published
crates and anything the compiler already reports are left out. Every
threshold was measured on real codebases
([docs/architecture.md](docs/architecture.md)).

## Know the codebase before writing

Agents duplicate code because they can't see what already exists.
`ddslop map` shows them, in a page that fits in a prompt. Here is
ddslop's map of itself:

```text
Layers, top first:
  0  .
  1  internal/cli
  2  internal/baseline, internal/codemap, internal/config, internal/index, ...
  3  internal/flow, internal/lang/syntax, internal/report

Calls breaking the layers:
  bypass  internal/cli -> internal/lang/syntax past internal/lang/rust: 1 call(s) against 9

Shared helpers:
  CrateOf (internal/flow/flow.go:250), used by internal/cli, internal/codemap
      func CrateOf(file string) string {

Idioms (use the convention; where there is none, the idiomatic form):
  error-checks: errors.Is/As in 7 of 7
  error-wrapping: %w in 10 of 10
```

It lists the layers and the calls that break them, the helpers modules
share, the features that copied one pipeline, and how the code checks and
wraps errors. Nothing to configure: it is read from the code.

Two more commands help before writing:

- `ddslop similar src/orders.rs:42` lists existing functions like a
  drafted helper, most similar first: reuse instead of adding a copy.
- `ddslop flows` prints each feature's entry point (a method of a
  generated API trait, such as OpenAPI) and the functions it reaches.

## Coding agents

`ddslop init` installs a skill telling the agent to read the `map`
before a feature, ask `similar` before keeping a helper, run
`check --diff` before finishing, and leave alone copies meant to diverge.
The agent decides when to run it; no hook forces it.

| Flag | Skill | Instructions |
| --- | --- | --- |
| `--claude` (default) | `.claude/skills/ddslop/SKILL.md` | `CLAUDE.md` |
| `--codex` | `.codex/skills/ddslop/SKILL.md` | `AGENTS.md` |
| `--copilot` | `.copilot/skills/ddslop/SKILL.md` | `.github/copilot-instructions.md` |

## In CI

Adopt it on an existing codebase by recording what is there, then gate
only on what is new:

```sh
ddslop baseline                                  # writes .ddslop-baseline.json
ddslop check --baseline .ddslop-baseline.json    # fails only on new candidates
```

The GitHub Action shows candidates as code scanning annotations on the
pull request, on the changed copy:

```yaml
permissions:
  contents: read
  security-events: write   # upload results to code scanning
steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
    with:
      fetch-depth: 0   # diff needs the base commit
  - uses: heikki-laitala/ddslop@v1   # the latest v1.x release
    with:
      diff: origin/${{ github.base_ref }}
      baseline: .ddslop-baseline.json
```

Pin an exact [release](https://github.com/heikki-laitala/ddslop/releases)
such as `@v1.0.0` to stay on one. Other inputs: `path`, `fail-over`
(fail only above this duplication percentage), `args` and `upload-sarif`.
Elsewhere, use `ddslop check --format sarif`.

Mark intended duplication in the code, with a reason:

```rust
// ddslop:ignore wire format, mirrors the Ledger's on purpose
pub struct LedgerRefusal { /* ... */ }
```

A marker on its own line covers the item below it; after code, it covers
that line's string literals.

## Reference

| Command | What it does |
| --- | --- |
| `check [path]` | report candidates (the default) |
| `map [path]` | describe layers, helpers, features and idioms (`--crate` for one Rust crate) |
| `similar FILE:LINE` | list existing functions like this one |
| `explain ID` | show one candidate's code |
| `flows [path]` | list features and the functions they reach |
| `baseline [path]` | record current candidates |
| `stats [path]` | index size and the duplication trend |
| `index`, `init`, `status`, `uninstall`, `upgrade`, `version` | housekeeping |

`ddslop COMMAND --help` lists every flag. The main `check` flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--format` | text on a terminal, else JSON | `text`, `json` or `sarif` |
| `--diff REF` | off | report only candidates touching lines changed since `REF` |
| `--baseline FILE` | off | skip candidates recorded in `FILE` |
| `--tests` | off | include test code |
| `--min-nodes` | 50 | smallest function, in syntax nodes |
| `--min-similarity` | 0.8 | threshold for near misses |

Repository settings go in `.ddslop.yaml`; command-line flags win:

```yaml
min-nodes: 60
min-similarity: 0.75
exclude:
  - "**/*_gen.go"
```

## How it works

tree-sitter parses each function into a sequence of syntax nodes with
names and values blanked out. Equal sequences are copies; MinHash, LSH
and a Myers diff find near misses; a suffix array finds copied blocks. A
call graph resolved by name gives the layers, features and helpers, and
each language adapter counts the forms of its idioms. The design and
measurements are in [docs/architecture.md](docs/architecture.md).

| Compared with | ddslop adds |
| --- | --- |
| `dupl`, jscpd, PMD CPD | renamed and near-miss copies, repeated patterns, fixes |
| depguard, go-arch-lint | layers and conventions read from the code, not written by hand |
| compiler warnings | unused public code, wrappers, copies |

## Development

```sh
make build   # bin/ddslop
make test    # tests with the race detector
make cover   # tests with an 80% coverage gate
make lint    # golangci-lint and markdownlint
```

Contributors and coding agents follow [AGENTS.md](AGENTS.md).

## Licence

MIT. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
