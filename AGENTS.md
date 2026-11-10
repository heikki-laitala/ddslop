# ddslop

A linter that finds duplicated code in Go, TypeScript and Rust and proposes
refactoring candidates. It targets AI-generated code: reinvented helpers,
near-identical functions from separate agent sessions, duplicated types and
constants. It never rewrites source files.

Output is for coding agents (Claude Code, Codex): machine-readable and
self-contained, with exact file paths and line ranges, what differs between
occurrences, and a concrete suggestion an agent can act on directly.

Go 1.25, module `github.com/heikki-laitala/ddslop`, MIT licensed.
Dependencies compiled into the binary must be MIT, BSD or Apache-2.0.
Design: [docs/architecture.md](docs/architecture.md); keep it in sync with
design changes.

## Commands

- `make build`: binary in `bin/ddslop`
- `make run ARGS="..."`
- `make test`
- `make cover`: tests with coverage, fails below 80% (`COVER_MIN=...` to override)
- `make notices`: regenerate `THIRD_PARTY_NOTICES.md` after changing dependencies
- `make lint`: Go and Markdown (`make lint-go`, `make lint-md`)
- `make fmt`
- `make help`: list all targets
- Releases: tag the commit `vYYYY.MM.DD_N` (N counts that day's releases
  from 0, e.g. `v2026.11.10_0`, `v2026.11.10_1`) and push the tag;
  `.github/workflows/release.yml` builds and publishes it. Use `_`, not
  `-`: a hyphen makes a pre-release that sorts before the plain date.

## Rules

These are requirements, not suggestions.

1. **Read before editing.** Understand the code, its callers, tests and
   shared utilities first. State assumptions; ask about ambiguous decisions
   that belong to the user instead of guessing.
2. **Match existing patterns.** If a convention is harmful or two conflict,
   say so and explain your choice; never silently introduce a competing one.
3. **Surgical changes.** Touch only what the task needs; no unrelated
   cleanups. Follow the engineering principles below.
4. **Deterministic work goes in code.** Models assist judgement; transforms,
   routing and accounting are code.
5. **Done means verified.** Define acceptance criteria and iterate until they
   are met. `make fmt` and `make lint` must be clean before committing.
6. **Commits.** Conventional commits (`feat:`, `fix:`, `chore:`,
   `refactor:`, `test:`, `ci:`, `docs:`). Commit each completed green slice.
   No `Co-Authored-By` trailer; no "Generated with Claude Code" footer in PRs.
7. **Layout.** Keep `main.go` thin; logic lives in packages under
   `internal/`. `CLAUDE.md` is a symlink: edit `AGENTS.md` only.

## Engineering principles

- **KISS (Keep It Simple, Stupid).** Prefer the simplest solution that
  works: plain functions over frameworks, the standard library over a
  dependency, a straightforward algorithm until profiling proves it too slow.
  Code should be obvious to the next reader without explanation.
- **YAGNI (You Aren't Gonna Need It).** Build only what the current task
  needs. No speculative features, flags, configuration, extension points or
  abstractions with a single user. The roadmap in `docs/architecture.md` is a
  plan, not a licence to build ahead of it.
- **DRY (Don't Repeat Yourself).** Every piece of knowledge (a rule, a
  constant, an algorithm) has one home. Search for an existing helper before
  writing a new one. DRY is about duplicated knowledge, not similar-looking
  code: extract on the third occurrence, and only when the copies change for
  the same reason; a wrong abstraction is worse than two copies. As a
  duplication linter, ddslop must stay clean under its own analysis (and
  `dupl` in `make lint` until it can).

## TDD (mandatory for features and behaviour fixes)

Work in small, independently verifiable slices. For each behaviour, or
family of closely related behaviours:

1. **Specify.** State the observable outcome and pick the cheapest test
   level that catches a regression (unit before integration before E2E).
   A bug fix starts by reproducing the bug.
2. **Red.** Write the test first and run it. It must fail because the
   behaviour is missing, not because of a setup or syntax error. A test that
   passes immediately is not red.
3. **Green.** Write the minimum code to pass, then run the affected tests.
   Never delete, skip or weaken a test to get green.
4. **Refactor on green only.** Keep refactoring and behaviour changes in
   separate steps, and re-run the tests after.
5. **Record evidence** before checking off a task: the red command and
   failure, the green command and result. Never invent a red run; if code was
   written first, say so and re-establish red against the old behaviour.

Tests must verify intent: they fail when the behaviour breaks. Mock only
boundaries we own, never the behaviour under test. Exploratory spikes inform
the design but are rebuilt test-first. Coverage stays at or above 80%,
without excluding files or weakening assertions to reach it; check with
`make cover`.
