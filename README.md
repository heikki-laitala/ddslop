# ddslop

ddslop finds duplicated code and proposes refactoring candidates. It is
built for code written by AI agents, which regenerate rather than copy:
helpers re-implemented instead of reused, near-identical functions from
separate sessions, blocks of statements copied into larger functions,
types and constants declared again. It also finds code to simplify:
unused public helpers, wrappers that only forward their arguments, and
traits with a single implementation.

Its output is meant for coding agents (Claude Code, Codex, Copilot): JSON
with exact file paths and line ranges, what differs between the copies,
and a concrete suggestion. ddslop never edits source files.

Rust is supported today; Go and TypeScript are planned. ddslop runs on
Linux and macOS.

## Install

From source (needs Go 1.25 and a C compiler, since tree-sitter and SQLite
are C):

```sh
go install github.com/heikki-laitala/ddslop@latest
```

or clone the repository and run `make build` (binary in `bin/ddslop`).

Once releases are published (none yet), the install script fetches a
prebuilt binary for Linux or macOS, x86_64 or aarch64:

```sh
curl -sSfL https://raw.githubusercontent.com/heikki-laitala/ddslop/main/install.sh | bash
```

It installs to `~/.local/bin`, verifies the release checksum and, inside a
Git repository, offers to set ddslop up for your coding agent. Options:
`--claude`, `--codex`, `--copilot` (set up without asking), `--dir DIR`,
`--version V`, `--no-interactive`.

## Quick start

```sh
cd your-project
ddslop init     # skill for Claude Code, CLAUDE.md section, .gitignore, index
ddslop check    # report duplicated code
```

`ddslop check` prints text in a terminal and JSON when piped. Exit codes:
0 nothing found, 1 candidates found, 2 error.

To check only what a change introduced, compare against a Git ref:

```sh
ddslop check --diff HEAD          # uncommitted work
ddslop check --diff origin/main   # everything on this branch
```

The whole repository is still analysed, so new code is compared against
all existing code, but only candidates touching changed lines are
reported, and the changed copies are marked `"changed": true`.

## What it finds

| Kind | Meaning | Suggestion |
| --- | --- | --- |
| `exact-clone` | functions identical apart from formatting | `reuse-existing`: keep one, call it |
| `renamed-clone` | same structure, different names, types or values | `extract-common` with the differences as parameters |
| `near-miss` | similar functions with statements added, removed or changed, as a family around an anchor | `extract-common`; `unmatched_lines` show code only some copies have |
| `block-clone` | a run of statements repeated inside larger functions | `extract-common` into a helper |
| `parallel-flow` | features whose call graphs run the same pipeline through different functions, step by step | `extract-pipeline`: one generic handler or service with the varying steps as parameters |
| `duplicate-type` | structs declaring the same field names (three or more) | `reuse-existing`: keep one type; `extract-common` when field types differ, listed as differences |
| `duplicate-constant` | a constant declared again with the same name and value | `reuse-existing`: keep one declaration and import it |
| `inlined-constant` | a string literal repeating the value of a constant in the same crate | `reuse-existing`: use the constant (the `target`) |
| `dead-code` | a public function of an unpublished crate whose name appears nowhere else | `delete-dead` |
| `pass-through` | a function whose body only passes its parameters on to another workspace function | `inline-wrapper`: call the `target` directly |
| `single-impl-trait` | a trait implemented once, for one of the code's own types, with no test double | `remove-trait`: make its methods the type's own |

Each candidate lists its occurrences and its differences. A difference
marked `"local": true` is a name each copy declares itself and needs no
parameter; every other difference is a parameter of the shared code. A
`parallel-flow` candidate also lists its `steps`: for each role in the
pipeline, the function that plays it in every feature. A `target` is
existing code to use instead of the occurrences.

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

To keep the list worth acting on, ddslop leaves out:

- generated files (marked `@generated`, "do not edit" or "Generated by" in
  their header comments);
- test code: test functions, `#[cfg(test)]` modules (inline or in their
  own file), `tests/`, `benches/` and `tests.rs` (`--tests` includes it);
- small functions and blocks (see the size flags below), unless a small
  block is copied often enough to add up (25 nodes or more, 400
  duplicated nodes in all);
- near misses whose names and values mostly differ, and candidates that
  would be mostly parameters: code that shares a shape, not logic.

## Commands

| Command | What it does |
| --- | --- |
| `ddslop check [path]` | report duplicated code (the default command) |
| `ddslop baseline [path]` | record current duplication in `.ddslop-baseline.json`, so `check --baseline` reports only new duplication |
| `ddslop flows [path]` | list feature entry points (methods implementing generated API traits) and the functions each reaches |
| `ddslop index [path]` | build or refresh the index at `.ddslop/index.db` |
| `ddslop init [path]` | set ddslop up for coding agents |
| `ddslop stats [path]` | describe the index without updating it |
| `ddslop status [path]` | show what ddslop has installed in a project |
| `ddslop uninstall [path]` | remove the skills, sections and `.gitignore` entry; `--purge` also the index |
| `ddslop version` | print the version |

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

The index keeps parsed functions between runs, so only changed files are
parsed again: on a 730k-line Rust codebase the first run takes about 9
seconds and later runs about 1.5 seconds. Files are parsed on half the CPU
cores; set `DDSLOP_INDEX_THREADS` to change that.

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
`parallel-flow` candidates: features that rebuild the same pipeline step
by step instead of sharing it.

Calls are resolved by name, without type information, and conservatively:
a call that could mean several functions is not followed. Bare calls
leave their crate only through `use` imports, calls into another crate
need that crate to be a real (non-test) dependency, and common
standard-library method names are not guessed. Calls through traits,
closures and macros are not followed.

## Coding agents

`ddslop init` installs a skill that tells the agent when to run ddslop
(before finishing a change, when asked to clean up, before writing a new
helper), how to read each kind of candidate, and to leave copies that are
meant to diverge alone:

| Flag | Skill | Instructions |
| --- | --- | --- |
| `--claude` (default) | `.claude/skills/ddslop/SKILL.md` | `## ddslop` section in `CLAUDE.md` |
| `--codex` | `.codex/skills/ddslop/SKILL.md` | `## ddslop` section in `AGENTS.md` |
| `--copilot` | `.copilot/skills/ddslop/SKILL.md` | `## ddslop` section in `.github/copilot-instructions.md` |

Running `init` again only updates what changed. When `CLAUDE.md` is a
symlink to `AGENTS.md`, the section is written once. Add the agent flags
together to install for several agents.

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

With `--format sarif`, results show as annotations on pull requests in
GitHub code scanning. In diff mode the annotation sits on the changed copy:

```yaml
permissions:
  contents: read
  security-events: write   # upload-sarif
steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
    with:
      fetch-depth: 0   # --diff needs the base commit
  - run: curl -sSfL https://raw.githubusercontent.com/heikki-laitala/ddslop/main/install.sh | bash -s -- --no-interactive
  - run: >
      ~/.local/bin/ddslop check --format sarif --baseline .ddslop-baseline.json
      --diff origin/${{ github.base_ref }} . > ddslop.sarif
  - uses: github/codeql-action/upload-sarif@33d7b85da1a04171e211befe2c739303b9a4c066 # v4.39.3
    if: always()
    with:
      sarif_file: ddslop.sarif
```

Either `--baseline` or `--diff` alone also works: the baseline gates the
whole codebase on new duplication, the diff limits a pull request to what
it changed.

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
