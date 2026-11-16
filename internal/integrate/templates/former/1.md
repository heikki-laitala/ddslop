## ddslop

Before finishing a code change, run `ddslop check --format json .` and
refactor duplication you introduced: candidates whose occurrences include
files you changed. Prefer reusing existing code over adding another copy.
See the ddslop skill for how to read candidates and act on them.
