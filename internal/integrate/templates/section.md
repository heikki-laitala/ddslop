## ddslop

Before finishing a code change, run `ddslop check --format json --diff HEAD .`
(or `--diff` against the base branch) and refactor the duplication it
reports: occurrences marked `"changed": true` are copies your change added
or edited. Prefer reusing existing code over adding another copy. See the
ddslop skill for how to read candidates and act on them.
