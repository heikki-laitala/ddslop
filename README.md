# ddslop

**Find the duplicated and dead code AI agents leave behind, and hand the
agent a fix it can apply.**

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

Works with **Claude Code**, **Codex** and **Copilot** through a skill that
`ddslop init` installs: the agent runs ddslop before it finishes a change,
when you ask it to clean up, and before it writes a new helper. Runs on
Linux and macOS.

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

Duplication:

| Kind | Meaning | Suggestion |
| --- | --- | --- |
| `exact-clone` | functions identical apart from formatting | `reuse-existing`: keep one, call it |
| `renamed-clone` | same structure, different names, types or values | `extract-common` with the differences as parameters |
| `near-miss` | similar functions with statements added, removed or changed, as a family around an anchor | `extract-common`; `unmatched_lines` show code only some copies have |
| `block-clone` | a run of statements repeated inside larger functions | `extract-common` into a helper |
| `setup-teardown` | 5 or more functions opening the same kind of value at their start and closing it at their end (begin and commit a transaction) | `extract-scope`: a closure-taking helper or an RAII guard (`suggestion.pattern`; GoF: Template Method) |
| `string-kinds` | the same set of 3 or more string literals matched in 3 or more functions: kinds or states passed around as strings | `introduce-enum`: an enum parsed once at the boundary (`suggestion.pattern`) |
| `parameter-group` | the same 3 or more parameters (names and types) taken by 4 or more functions | `introduce-parameter-object`: one struct passed instead (`suggestion.pattern`; listed in `values`) |
| `shared-preamble` | the same opening check that returns early (sign-in, permission, readiness) in 5 or more functions | `extract-preamble`: an extractor or middleware (`suggestion.pattern`; GoF: Chain of Responsibility) |
| `parallel-flow` | features whose call graphs run the same pipeline through different functions, step by step | `extract-pipeline`: one generic handler or service with the varying steps as parameters (`suggestion.pattern`; GoF: Template Method) |
| `duplicate-type` | structs declaring the same field names (three or more), or enums declaring the same variants | `reuse-existing`: keep one type; `extract-common` when field types differ, listed as differences |
| `duplicate-constant` | a constant declared again with the same name and value | `reuse-existing`: keep one declaration and import it |
| `inlined-constant` | a string literal repeating the value of a constant in the same crate | `reuse-existing`: use the constant (the `target`) |

Code to simplify:

| Kind | Meaning | Suggestion |
| --- | --- | --- |
| `dead-code` | a public function of an unpublished crate (Rust) or an exported function under `internal/` or in `package main` (Go) whose name appears nowhere else | `delete-dead` |
| `pass-through` | a function whose body only passes its parameters on to another workspace function | `inline-wrapper`: call the `target` directly |
| `single-impl-trait` | a trait implemented once, for one of the code's own types, with no test double | `remove-trait`: make its methods the type's own |

Every candidate is self-contained, so an agent can act on it without
another search:

```json
{
  "id": "c1",
  "kind": "renamed-clone",
  "similarity": 1,
  "occurrences": [
    {"file": "src/orders.rs", "name": "load_order", "start_line": 1, "end_line": 9},
    {"file": "src/users.rs", "name": "load_user", "start_line": 1, "end_line": 9}
  ],
  "differences": [
    {"kind": "identifier", "values": ["order_id", "user_id"], "lines": [[1, 2, 6], [1, 2, 6]], "local": true},
    {"kind": "literal", "values": ["\"... orders ...\"", "\"... users ...\""], "lines": [[2], [2]], "local": false}
  ],
  "suggestion": {"action": "extract-common", "parameters": 1},
  "score": {"nodes": 120, "lines": 9, "occurrences": 2, "duplicated_nodes": 120}
}
```

A difference marked `"local": true` is a name each copy declares itself
and needs no parameter; every other difference is a parameter of the
shared code. A `target` is existing code to use instead of the
occurrences. A `parallel-flow` candidate also lists its `steps`: for each
role in the pipeline, the function that plays it in every feature.
Candidates are ranked by how much code the fix removes.

### Built to be trusted

A linter that cries wolf gets ignored, by people and by agents. ddslop
leaves out:

- generated files (marked `@generated`, "do not edit" or "Generated by" in
  their header comments);
- test code: test functions, `#[cfg(test)]` modules (inline or in their
  own file), `tests/`, `benches/` and `tests.rs` (`--tests` includes it);
- small functions and blocks (see the size flags below), unless a small
  block is copied often enough to add up (25 nodes and 3 lines or more,
  400 duplicated nodes in all); one- and two-line idioms never count;
- near misses whose names and values mostly differ, and candidates that
  would be mostly parameters: code that shares a shape, not logic;
- public items of published crates, which are API, and anything the
  compiler already reports;
- wrappers around library functions, extension traits on foreign types,
  and traits a test double implements.

Every threshold was measured on a real codebase before it was set; the
measurements are in [docs/architecture.md](docs/architecture.md).

## Coding agents

`ddslop init` installs a skill that tells the agent when to run ddslop
(`map` before starting a feature, to follow the codebase's layers and
reuse its shared helpers; `check` before finishing a change, when asked
to clean up and before writing a new helper), how to act on each kind of
candidate, and to leave copies that are meant to diverge alone:

| Flag | Skill | Instructions |
| --- | --- | --- |
| `--claude` (default) | `.claude/skills/ddslop/SKILL.md` | `## ddslop` section in `CLAUDE.md` |
| `--codex` | `.codex/skills/ddslop/SKILL.md` | `## ddslop` section in `AGENTS.md` |
| `--copilot` | `.copilot/skills/ddslop/SKILL.md` | `## ddslop` section in `.github/copilot-instructions.md` |

The agent decides when to run it; no hook forces it to stop or continue.
Running `init` again only updates what changed. When `CLAUDE.md` and
`AGENTS.md` are one file through a symlink, in either direction, the
section is written once, into the real file, and the symlink stays. Add the agent flags
together to install for several agents.

## Commands

| Command | What it does |
| --- | --- |
| `ddslop check [path]` | report duplicated code (the default command) |
| `ddslop baseline [path]` | record current duplication in `.ddslop-baseline.json`, so `check --baseline` reports only new duplication |
| `ddslop explain ID [path]` | show one candidate in full, with each occurrence's code and its differences marked |
| `ddslop similar FILE:LINE [path]` | list existing functions like the one at that line, most similar first: ask before keeping a new helper |
| `ddslop flows [path]` | list feature entry points (methods implementing generated API traits) and the functions each reaches |
| `ddslop map [path]` | describe the layers the code forms (Go packages, a Rust workspace's crates, or one crate's modules with `--crate`), the calls breaking them and the helpers other modules share |
| `ddslop index [path]` | build or refresh the index at `.ddslop/index.db` |
| `ddslop init [path]` | set ddslop up for coding agents |
| `ddslop stats [path]` | describe the index, and the duplication trend over past checks |
| `ddslop status [path]` | show what ddslop has installed in a project |
| `ddslop uninstall [path]` | remove the skills, sections and `.gitignore` entry; `--purge` also the index |
| `ddslop upgrade` | replace ddslop with the latest release (`--check` only reports) |
| `ddslop version` | print the version (also `--version`) |

`check` flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--format` | text on a terminal, JSON otherwise | `text`, `json` or `sarif` (SARIF 2.1.0 for code scanning) |
| `--min-nodes` | 50 | ignore functions smaller than this many syntax nodes |
| `--min-block-nodes` | 100 | ignore copied blocks smaller than this (never below `--min-nodes`) |
| `--min-near-nodes` | 150 | ignore near misses of functions smaller than this (never below `--min-nodes`) |
| `--min-similarity` | 0.8 | threshold for near misses |
| `--tests` | off | include test code |
| `--diff` | off | only report candidates touching lines changed since this Git ref |
| `--baseline` | off | do not report duplication recorded in this baseline file |
| `--fail-over` | off | exit 1 only when the repository's duplication percentage is above this, still reporting every candidate |

Settings for a repository go in `.ddslop.yaml` at its root:

```yaml
min-nodes: 60
min-similarity: 0.75
tests: false
exclude:
  - "**/*_gen.go"
  - "crates/fake-*/**"
```

It sets the five analysis flags above (a flag given on the command line
still wins) and `exclude`, glob patterns of files to leave out, matched
against paths from the root: `*` within one directory, `**` across any
number. An unknown key is an error, so a misspelt setting does not go
unnoticed.

The index keeps parsed code between runs, so only changed files are
parsed again. Files are parsed on half the CPU cores; set
`DDSLOP_INDEX_THREADS` to change that.

## Feature flows

`ddslop flows` maps the features of a codebase: each entry point and the
functions it reaches, printed as a call tree (`--format json` for agents,
`--depth` to limit it).

```text
OrdersApi::create_order (services/orders/src/api.rs:40-71)
  validate (services/orders/src/api.rs:120-141)
    caller (services/orders/src/auth.rs:12-21)
  Orders::create (services/orders/src/orders.rs:88-150)
    authorize (services/orders/src/orders.rs:30-36)
    order_fingerprint (services/orders/src/orders.rs:200-213)
```

Entry points are methods implementing a trait declared in generated code,
such as OpenAPI server traits. `check` uses the same map to find
`parallel-flow` candidates (features that rebuild the same pipeline step
by step instead of sharing it) and the targets of wrappers.

Calls are resolved by name, without type information, and conservatively:
a call that could mean several functions is not followed. Bare calls
leave their crate only through `use` imports, calls into another crate
need that crate to be a real (non-test) dependency, and common
standard-library method names are not guessed. Calls through traits,
closures and macros are not followed.

## In CI

`check` exits 1 when it finds candidates, so it can gate a pipeline. On an
existing codebase, record what is already there and commit the file:

```sh
ddslop baseline     # writes .ddslop-baseline.json
```

`check --baseline .ddslop-baseline.json` then reports only duplication that
is not in the baseline. Occurrences are recorded by file and function, not
by line, so unrelated edits do not invalidate it; a known family stays
known while you clean it up, and a new copy of it is reported. Re-run
`ddslop baseline` after cleaning up to shrink the file.

To keep intended duplication out of every run, mark it in the code with
a reason:

```rust
// ddslop:ignore wire format, mirrors the Ledger's on purpose
pub struct LedgerRefusal { /* ... */ }

.field("request_timeout", &self.timeout) // ddslop:ignore Debug field name
```

A marker on a line of its own applies to the function, method, struct,
enum or constant below it (doc comments and attributes may sit between);
a marker after code applies to that line's string literals, such as a
value that only happens to equal a constant. Marked code is left out of
the analysis, and the summary counts it. A marker without a reason is an
error.

The ddslop GitHub Action installs ddslop, runs `check`, and shows the
results as annotations on pull requests through GitHub code scanning. In
diff mode the annotation sits on the changed copy:

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

`@v1` follows each v1.x release; pin an exact
[release](https://github.com/heikki-laitala/ddslop/releases) such as
`@v1.0.0` to stay on one. The tag you pin is the ddslop version the action
installs. Other
inputs: `path` (default `.`), `fail-over`, `args` (further `check`
arguments) and `upload-sarif` (`false` to skip code scanning). Outside
GitHub, run `ddslop check --format sarif` the same way.

Either `--baseline` or `--diff` alone also works: the baseline gates the
whole codebase on new duplication, the diff limits a pull request to what
it changed. For a softer gate, `--fail-over 5` fails only while the whole
repository's duplication is above 5%, and still reports every candidate.

## How it works

Files are parsed with tree-sitter. Each function becomes a structure
sequence with names and values blanked out:

- equal sequences are exact or renamed copies;
- near misses are found with MinHash and LSH, verified with a bounded
  Myers diff, kept only when most names and values agree, and grouped into
  families;
- copied blocks are maximal repeats of statements, found with a suffix
  array;
- parallel flows compare features' call-graph slices step by step,
  ignoring code they share and code most features reach;
- types are compared by field names, constants by name and value, and
  dead code is a public name that no source file mentions anywhere else,
  comments and macros included.

See [docs/architecture.md](docs/architecture.md) for the design and the
measurements behind the thresholds.

## How ddslop differs

| Tool | Approach | What ddslop adds |
| --- | --- | --- |
| `dupl` (golangci-lint) | suffix tree over Go syntax trees | values and renames, near misses, suggestions (ddslop covers Rust today; Go is planned) |
| jscpd, PMD CPD | token windows, many languages | near misses and drift, matches on whole syntax units, refactoring candidates |
| `mizchi/similarity` | tree-sitter and tree edit distance | refactoring candidates, agent output, feature flows |
| Compiler warnings | unused private code | unused public code across a workspace, copies and wrappers |

## Development

```sh
make build   # bin/ddslop
make test    # tests with the race detector
make cover   # tests with an 80% coverage gate
make lint    # golangci-lint and markdownlint
make help    # all targets
```

Contributors and coding agents follow [AGENTS.md](AGENTS.md).

## Licence

MIT. See [LICENSE](LICENSE) and, for the software compiled into the binary,
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
