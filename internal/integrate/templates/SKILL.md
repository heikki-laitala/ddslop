---
name: ddslop
description: Finds duplicated code (exact, renamed and near-miss copies, and copied blocks inside functions) and proposes refactoring candidates. Use before finishing a code change, when asked to clean up, deduplicate or "deslop" code, and before writing a new helper, to reuse existing code instead of adding another copy.
{{- if .Claude}}
allowed-tools: Bash(ddslop *), Read, Grep, Glob
{{- end}}
---

# ddslop: find duplicated code to refactor

ddslop is a duplicate-code linter for Rust (Go and TypeScript are planned).
It reports refactoring candidates as JSON, with exact file paths and line
ranges, what differs between the copies, and a suggested action.

## When to run it

- Before finishing a change: check that you did not add another copy of
  existing code (see "Checking your change" below).
- When asked to clean up, deduplicate or "deslop" code.
- Before writing a new helper: an existing function may already do it.

## How to run it

```sh
ddslop check --format json .
```

reports all duplication in the repository. Exit codes: 0 nothing found, 1
candidates found, 2 error. The first run
builds `.ddslop/index.db`; later runs only parse changed files. Generated
files and test code are skipped (`--tests` includes tests).

Useful flags: `--min-nodes` (default 50), `--min-block-nodes` (default
100) and `--min-near-nodes` (default 150) set the smallest code reported,
`--min-similarity` (default 0.8) the threshold for near misses.

## Checking your change

```sh
ddslop check --format json --diff HEAD .         # uncommitted work
ddslop check --format json --diff origin/main .  # a whole branch
```

`--diff <ref>` still compares against the whole repository, but only
reports candidates touching lines changed since `<ref>`. Occurrences with
`"changed": true` are the copies your change added or edited; usually the
fix is to replace them with a call to an unchanged occurrence.

## Reading the output

Candidates are ranked by `score.duplicated_nodes`, largest first. Each has
`occurrences` (file, function name, line range), `differences` (one value
per occurrence) and `suggestion`.

- `differences` with `"local": true` are names each copy declares itself;
  they need no parameter. Every other difference is a parameter of the
  shared code.
- `suggestion.parameters` counts those parameters.

What to do per `kind`:

- `exact-clone` (`reuse-existing`): keep one copy, usually the oldest or
  the one with the most callers, and call it from the other places.
- `renamed-clone` (`extract-common`): extract one function taking the
  non-local differences as parameters (types become generics).
- `near-miss` (`extract-common`): the first occurrence is the anchor; each
  other one has a `similarity` to it. `unmatched_lines` are code present in
  only some copies: make it optional (a parameter or closure) or keep it at
  the call site.
- `block-clone` (`extract-common`): the occurrence lines are a run of
  statements inside larger functions. Extract the run into a helper; names
  declared outside the run become its parameters.

## Rules

- Merge copies only when they change for the same reason. Copies that are
  meant to diverge (different business rules, different services' public
  contracts) are not duplication: skip them.
- Never edit generated files; fix their generator instead.
- Keep public APIs stable unless asked otherwise.
- After refactoring, run the project's tests and run ddslop again to confirm
  the candidate is gone.
