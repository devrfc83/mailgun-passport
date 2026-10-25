#!/bin/sh
# Prints the package version derived from the latest vX.Y.Z git tag:
#   on the tag, clean tree   -> 1.2.3
#   commits after the tag    -> 1.2.3+4.gabc1234
#   uncommitted changes      -> 1.2.3+4.gabc1234.dirty
# Without any tag the base is 0.0.0. The "+" suffix sorts after the release
# in both rpm and dpkg, so development builds upgrade cleanly.
set -eu

if ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo 0.0.0
    exit 0
fi

dirty=""
if ! git diff --quiet HEAD -- 2>/dev/null; then
    dirty=".dirty"
fi
hash=$(git rev-parse --short HEAD)

if tag=$(git describe --tags --match 'v[0-9]*' --abbrev=0 2>/dev/null); then
    base=${tag#v}
    count=$(git rev-list --count "$tag..HEAD")
else
    base=0.0.0
    count=$(git rev-list --count HEAD)
fi

if [ "$count" = 0 ] && [ -z "$dirty" ]; then
    echo "$base"
else
    echo "$base+$count.g$hash$dirty"
fi
