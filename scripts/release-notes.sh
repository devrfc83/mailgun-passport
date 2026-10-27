#!/bin/sh
# Prints the CHANGELOG.md section of a release, used as its GitHub release
# notes. A prerelease such as v1.0.0-rc1 falls back to the v1.0.0 section.
# Fails when the section is missing or empty, so a tag cannot be released
# without notes.
# Usage: scripts/release-notes.sh <vX.Y.Z> [changelog]
set -eu

tag=${1:?tag}
changelog=${2:-CHANGELOG.md}

# Lines after "## <tag>" up to the next "## " heading, without the blank
# lines around them.
section() {
    awk -v tag="$1" '
        /^## / { if (found) exit; found = ($2 == tag); next }
        found
    ' "$changelog" | sed '/./,$!d'
}

notes=$(section "$tag")
if [ -z "$notes" ] && [ "${tag%%-*}" != "$tag" ]; then
    notes=$(section "${tag%%-*}")
fi
if [ -z "$notes" ]; then
    echo "$changelog has no notes for $tag: add a \"## $tag\" section" >&2
    exit 1
fi
printf '%s\n' "$notes"
