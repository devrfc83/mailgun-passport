#!/bin/bash
set -e

RUNTIME="$1"
VERSION="$2"
NAME="$3"
PKG_DIR="$4"
DEB_DIR="$5"

if [ "$RUNTIME" = "none" ]; then
    echo "No container runtime (docker/podman) found."
    exit 1
fi

echo "Building DEB packages in $RUNTIME..."

mkdir -p "$DEB_DIR"
WORKDIR="/build"

$RUNTIME run --rm -v "$(pwd):$WORKDIR" -w "$WORKDIR" debian:trixie bash -c "
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y build-essential debhelper devscripts golang-go ca-certificates git
export VERSION=$VERSION NAME=$NAME PKG_DIR=$PKG_DIR DEB_DIR=$DEB_DIR
bash scripts/build-deb.sh debian 13 trixie $VERSION $NAME $PKG_DIR $DEB_DIR
" 2>&1 | tail -60

echo "DEB packages built via $RUNTIME in $DEB_DIR/"
