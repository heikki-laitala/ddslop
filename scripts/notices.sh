#!/usr/bin/env bash
# Writes THIRD_PARTY_NOTICES.md: the licence texts of every module compiled
# into the ddslop binary, and of the Go standard library. MIT, BSD and
# Apache-2.0 require these notices when the binary is distributed.
#
# Usage: scripts/notices.sh > THIRD_PARTY_NOTICES.md
set -euo pipefail

cat <<'HEADER'
# Third-party notices

The ddslop binary includes the following software. Their licences follow.
HEADER

print_licence() {
    local name="$1" dir="$2" found=false
    for file in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING*; do
        [ -f "$file" ] || continue
        found=true
        printf '\n## %s\n\n```text\n' "$name"
        cat "$file"
        printf '```\n'
    done
    if [ "$found" = false ]; then
        echo "no licence file found for $name in $dir" >&2
        exit 1
    fi
}

print_licence "Go standard library" "$(go env GOROOT)"

go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}' . |
    sort -u |
    while read -r module dir; do
        print_licence "$module" "$dir"
    done

cat <<'SQLITE'

## SQLite

github.com/mattn/go-sqlite3 bundles the SQLite library, which is in the
public domain: <https://www.sqlite.org/copyright.html>.
SQLITE
