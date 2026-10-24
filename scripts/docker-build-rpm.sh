#!/bin/bash
set -e

RUNTIME="$1"
VERSION="$2"
NAME="$3"
PKG_DIR="$4"
RPM_DIR="$5"

if [ "$RUNTIME" = "none" ]; then
    echo "No container runtime (docker/podman) found."
    exit 1
fi

echo "Building RPM packages in $RUNTIME..."

mkdir -p "$RPM_DIR"
WORKDIR="/build"

$RUNTIME run --rm -v "$(pwd):$WORKDIR" -w "$WORKDIR" fedora:43 bash -c "
set -e
dnf install -y rpm-build golang systemd-rpm-macros ca-certificates git shadow-utils tar
export VERSION=$VERSION NAME=$NAME PKG_DIR=$PKG_DIR RPM_DIR=$RPM_DIR
bash scripts/build-rpm.sh fedora 43 $VERSION $NAME $PKG_DIR $RPM_DIR
bash scripts/build-rpm.sh fedora 44 $VERSION $NAME $PKG_DIR $RPM_DIR
bash scripts/build-rpm.sh rocky 9 $VERSION $NAME $PKG_DIR $RPM_DIR 2>/dev/null || true
" 2>&1 | tail -60

echo "RPM packages built via $RUNTIME in $RPM_DIR/"
