# Contributing

Thank you for helping. ddslop is a linter, so the most valuable
contributions are reports of findings it gets wrong:

- **A wrong or missed finding:** open an issue with the "Wrong or missed
  finding" form. A small Rust snippet that reproduces it and the JSON
  candidate (`ddslop check --format json`) make it quick to fix.
- **A bug:** use the "Bug report" form.
- **A security issue:** do not open an issue; see [SECURITY.md](SECURITY.md).

ddslop supports Rust and Go; TypeScript is planned. Before starting
a larger change, such as a new detector or language, open an issue to
agree on the approach.

## Changing the code

[AGENTS.md](AGENTS.md) is the rule book for everyone, people and coding
agents alike. In short:

- work test-first: a failing test, then the code that makes it pass;
- keep changes small and focused, and every threshold backed by a
  measurement (see [docs/architecture.md](docs/architecture.md));
- run `make fmt`, `make lint`, `make test` and `make cover` (80% minimum)
  before committing;
- use conventional commit messages (`feat:`, `fix:`, `docs:`, ...).

Open a pull request against `main`. The Lint and Test checks must pass;
pull requests are merged by rebase.
