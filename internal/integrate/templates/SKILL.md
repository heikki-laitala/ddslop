---
name: ddslop
description: Finds duplicated code (exact, renamed and near-miss copies, copied blocks, duplicated types and constants) and code to simplify (unused public helpers, pass-through wrappers, single-implementation traits), and proposes refactoring candidates. Use before finishing a code change, when asked to clean up, deduplicate or "deslop" code, and before writing a new helper, to reuse existing code instead of adding another copy.
{{- if .Claude}}
allowed-tools: Bash(ddslop *), Read, Grep, Glob
{{- end}}
---

# ddslop: find duplicated code to refactor

ddslop is a duplicate-code linter for Rust (Go and TypeScript are planned).
It reports refactoring candidates as JSON, with exact file paths and line
ranges, what differs between the copies, and a suggested action. It also
reports code to simplify: unused public helpers, wrappers that only
forward their arguments, and traits with a single implementation.

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
- `target`, when present, is existing code to use instead of the
  occurrences.

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
- `parallel-flow` (`extract-pipeline`, pattern `generic-pipeline`): the occurrences are feature entry
  points (API handlers), and `steps` lines up the functions that play the
  same role in each feature, with their similarity to the first one's.
  Extract one pipeline (a generic handler or service) and pass the varying
  steps in, rather than deduplicating each step on its own. This is a
  larger refactor: propose it before doing it.
- `shared-preamble` (`extract-preamble`): the occurrences are the same
  opening check in many functions, which returns early (sign-in,
  permission, credential, readiness). `suggestion.pattern` names the idiom:
  an `extractor`. In request handlers, move the check into an extractor or
  middleware that hands the handler what the check produced (the signed-in
  user, the principal); elsewhere, into one guard function returning the
  checked value or the early result. Differences become its parameters.
- `setup-teardown` (`extract-scope`, pattern `closure-helper`): the
  functions open the same kind of value at their start (a transaction, a
  lease) and close it at their end (`commit`, `release`). Write one helper
  that opens the value, runs the work passed in as a closure and closes
  it, or an RAII guard that closes on drop, so no copy can forget the
  teardown. Mind early returns and commits in the middle: the helper must
  keep their behaviour.
- `string-kinds` (`introduce-enum`, pattern `enum`): the occurrences are
  matches on the same set of string literals in several functions:
  kinds, outcomes or states passed around as strings. Introduce an enum
  (or reuse one that already exists), parse the string into it once at the
  boundary (`FromStr` or `TryFrom`), give it an `as_str` for the way back,
  and match on the enum. Strings that belong to an external protocol stay
  strings at the boundary only.
- `duplicate-type`: structs with the same field names, or enums with the
  same variants (often one error enum per module). Keep one type and
  use it everywhere. Differences list field types that differ; when one
  struct is a wire format (serde) and the other a domain type, converting
  between them at the boundary can be deliberate: skip those.
- `duplicate-constant` (`reuse-existing`): the same constant declared again.
  Keep one declaration in a module every user can import, and delete the
  others.
- `inlined-constant` (`reuse-existing`): the first occurrence is a constant
  (also the `target`); the others are string literals repeating its value
  in the same crate. Replace the literals with the constant, widening its
  visibility (`pub(crate)`) if needed. Skip a literal that only happens to
  be spelled the same with another meaning.
- `dead-code` (`delete-dead`): a public function of an unpublished crate
  whose name appears nowhere else in the repository. Delete it, and
  anything only it used. Keep it if it is meant for code outside the
  repository.
- `pass-through` (`inline-wrapper`): the function only passes its
  parameters to the `target`. Call the target directly and delete the
  wrapper, or rename the target if the wrapper's name is better. Keep
  wrappers that fix a generic type of their target, or that are a public
  entry point to private code.
- `single-impl-trait` (`remove-trait`): the occurrences are the trait and
  its only impl block. Move the methods into an inherent impl of the type
  and replace `dyn Trait` and `impl Trait` uses with the type. Keep the
  trait if it is a seam for tests you are about to write, or a second
  implementation is planned.

## Rules

- Merge copies only when they change for the same reason. Copies that are
  meant to diverge (different business rules, different services' public
  contracts) are not duplication: skip them.
- Never edit generated files; fix their generator instead.
- Keep public APIs stable unless asked otherwise.
- After refactoring, run the project's tests and run ddslop again to confirm
  the candidate is gone.
