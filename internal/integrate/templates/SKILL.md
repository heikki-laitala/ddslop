---
name: ddslop
description: Finds duplicated code (exact, renamed and near-miss copies, copied blocks, duplicated types and constants) and code to simplify (unused public helpers, pass-through wrappers, single-implementation traits), and proposes refactoring candidates. Use before starting a feature, to see the codebase's layers and the helpers it already shares; before finishing a code change; when asked to clean up, deduplicate or "deslop" code; and before writing a new helper, to reuse existing code instead of adding another copy.
{{- if .Claude}}
allowed-tools: Bash(ddslop *), Read, Grep, Glob
{{- end}}
---

# ddslop: find duplicated code to refactor

ddslop is a duplicate-code linter for Rust and Go (TypeScript is planned).
It reports refactoring candidates as JSON, with exact file paths and line
ranges, what differs between the copies, and a suggested action. It also
reports code to simplify: unused public helpers, wrappers that only
forward their arguments, and traits with a single implementation.

## When to run it

- Before starting a feature or a larger change: see how the codebase is
  organised and what it already offers (see "Before you write" below).
- Before finishing a change: check that you did not add another copy of
  existing code (see "Checking your change" below).
- When asked to clean up, deduplicate or "deslop" code.
- Before keeping a new helper: an existing function may already do it.
  Write the draft, then ask (see "Finding existing code" below).

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
`--min-similarity` (default 0.8) the threshold for near misses. A
repository may set them, and files to exclude, in `.ddslop.yaml`; flags
override it.

## Before you write

```sh
ddslop map --format json .
```

describes the codebase's conventions, one map per language. Its `scope`
says what the layers hold: Go `packages`; for a Rust workspace,
`crates`, with a summary of each crate's modules and layers; or one
crate's modules (`crate`), which a single-crate repository gets by
default. In a workspace, then look inside the crate you are changing:

```sh
ddslop map --format json --crate <name> .
```

- `layers`: the packages, crates or modules, top layer first, as the
  code's calls order them. Put new code in the layer its neighbours use,
  and call downwards.
- `helpers`: functions other modules already share, with their
  signature and the modules using them. Reuse one before writing your
  own; `ddslop similar` then checks a draft that remains.
- `breaks`: calls against the layers: `upward` (a call back up, often a
  dependency cycle) or `bypass` (a call past the layer the rest of the
  module goes through). Treat them as hints, not errors: some are
  deliberate, such as a composition root wiring everything together.
  Do not add to them; fix one only when asked.
- `features`: entry points that copy one pipeline through their own
  functions (a `parallel-flow` candidate in `check`): the `shape` (the
  most typical copy), its `steps` with the shared helpers each calls,
  and the other `members`. They show what not to do again: when your
  feature is of the same kind, extract the pipeline (the `pattern`)
  rather than copy a member, or ask whether to.
- `idioms`: how the code does each concern, such as checking and
  wrapping errors: the `convention` (the form most occurrences use, with
  its `share`), or none and the language's `idiomatic` form, and every
  form with an example. Write new code in the convention's form, or the
  idiomatic one where there is none, even where a nearby function uses
  another.
- `unplaced`: modules or crates with no counted calls to or from the
  others (often types-only crates, or test fakes run as their own
  binaries): nothing says which layer they belong to.

Only calls that name their target count (`f()`, `Type::f()`,
`module::f()`, calls on `self`), so a method reached through a field
(`x.store.get()`) is not seen: Go layers in particular stay shallow.
`--limit` (default 10) caps each list.

## Checking your change

```sh
ddslop check --format json --diff HEAD .         # uncommitted work
ddslop check --format json --diff origin/main .  # a whole branch
```

`--diff <ref>` still compares against the whole repository, but only
reports candidates touching lines changed since `<ref>`. Occurrences with
`"changed": true` are the copies your change added or edited; usually the
fix is to replace them with a call to an unchanged occurrence.

## Finding existing code

```sh
ddslop similar --format json src/orders.rs:42 .
```

lists existing functions like the one at that line, most similar first,
each with a `similarity` (1 for a renamed copy). If one already does what
your draft does, call it instead and delete the draft; if one nearly does,
consider extending it. `--min-similarity` (default 0.7) and `--limit`
(default 10) tune the list.

`ddslop explain --format json <id> .` returns one candidate with the code
of its occurrences, so you can read the copies without opening each file.
Pass the same flags as the check that listed it.

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
- `parameter-group` (`introduce-parameter-object`, pattern
  `parameter-object`): the occurrences are functions that all take the
  parameters in `values`. Group them in one struct and pass that; when one
  is a connection or context (`db`, `transaction`), a type holding it with
  the functions as methods is often better. Leave signatures a framework
  dictates (middleware, handlers) and small recursive helpers alone.
- `convention-drift` (`follow-convention`, the convention's form in
  `pattern`): the occurrence does a job named in `concern` (such as
  `error-checks`) in another form, given in `values`, than at least 90%
  of the codebase. Write it as the `target` does, the closest use of the
  convention, which `ddslop map` lists under `idioms`. When this place
  needs the other form (an outside library's error with no variant to
  match, say), leave it and say why in a `ddslop:ignore` comment.
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
  (Rust), or an exported function under `internal/` or in `package main`
  (Go), whose name appears nowhere else in the repository. Delete it, and
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
- When you skip a candidate because the copies are meant to differ, or a
  literal only happens to equal a constant, mark it so later runs do not
  report it again: `// ddslop:ignore <reason>` on the line above the
  function, struct, enum or constant, or after the code on a literal's
  line. The reason is required; say why the duplication is intended.
- Never edit generated files; fix their generator instead.
- Keep public APIs stable unless asked otherwise.
- After refactoring, run the project's tests and run ddslop again to confirm
  the candidate is gone.
