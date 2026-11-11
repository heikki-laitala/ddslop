# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately, not in a public issue: open the
repository's **Security** tab and choose **Report a vulnerability**
(GitHub's private vulnerability reporting). Include what you found, how to
reproduce it, and the ddslop version (`ddslop version`).

You will get a reply in the advisory. Once a fix is released, the advisory
is published with credit to you, unless you prefer otherwise.

## What is in scope

ddslop reads source files and writes only its own index (`.ddslop/`) and
the files `ddslop init` and `ddslop baseline` create. It never runs the
code it analyses. Only `ddslop upgrade` uses the network: it downloads a
release from this repository, verifies it against the release's checksums
and replaces the running binary. Relevant reports include:

- ddslop writing, deleting or reading outside the repository it is given;
- the install script (`install.sh`) or `ddslop upgrade` installing
  something other than a verified release binary;
- release binaries or checksums that do not match the source and workflow
  they were built from;
- a crafted source file that makes ddslop crash or hang in a way that
  breaks CI for others.

## Supported versions

Only the latest release receives fixes. Releases are tagged
`vYYYY.MM.DD_N`; see the
[releases](https://github.com/heikki-laitala/ddslop/releases).
