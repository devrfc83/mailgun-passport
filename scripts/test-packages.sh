#!/bin/sh
# Installs, checks and removes the amd64 packages from DIST in a container per
# supported distribution. Usage: scripts/test-packages.sh <podman|docker> <dist-dir>
set -eu

runtime=${1:?container runtime}
dist=$(cd "${2:?dist directory}" && pwd)

rpm_images="
docker.io/library/fedora:43
docker.io/library/fedora:44
docker.io/library/almalinux:8
docker.io/library/almalinux:9
docker.io/library/almalinux:10
docker.io/rockylinux/rockylinux:9
docker.io/rockylinux/rockylinux:10
registry.access.redhat.com/ubi8/ubi
registry.access.redhat.com/ubi9/ubi
registry.access.redhat.com/ubi10/ubi
"
deb_images="
docker.io/library/debian:bookworm
docker.io/library/debian:trixie
docker.io/library/ubuntu:24.04
docker.io/library/ubuntu:26.04
docker.io/library/ubuntu:26.10
"

rpm=$(ls "$dist"/mailgun-passport-*.x86_64.rpm 2>/dev/null | tail -n 1)
deb=$(ls "$dist"/mailgun-passport_*_amd64.deb 2>/dev/null | tail -n 1)
[ -n "$rpm" ] && [ -n "$deb" ] || { echo "no packages in $dist; run make packages" >&2; exit 1; }

# Shared by every distribution once the package is installed.
checks='
set -eu
mailgun-passport -version
getent passwd mailgun-passport >/dev/null
test -f /usr/lib/systemd/system/mailgun-passport.service
test "$(stat -c %a:%U /etc/mailgun-passport/mailgun-passport.env)" = 600:root
out=$(mailgun-passport 2>&1) && status=0 || status=$?
test "$status" = 2
echo "$out" | grep -q "MAILGUN_API_KEY is required"
'

failed=""
run() {
    image=$1
    script=$2
    echo "=== $image"
    if ! "$runtime" run --rm --pull=newer -v "$dist:/pkgs:ro,Z" "$image" sh -c "$script"; then
        failed="$failed $image"
    fi
}

for image in $rpm_images; do
    run "$image" "
        set -eu
        pkg=/pkgs/$(basename "$rpm")
        if command -v dnf >/dev/null; then dnf -q -y install \"\$pkg\"; else microdnf -y install \"\$pkg\"; fi
        $checks
        rpm -e mailgun-passport
        ! test -e /usr/bin/mailgun-passport
        echo OK"
done

for image in $deb_images; do
    run "$image" "
        set -eu
        export DEBIAN_FRONTEND=noninteractive
        apt-get -qq update
        apt-get -qq install -y /pkgs/$(basename "$deb") >/dev/null
        $checks
        apt-get -qq purge -y mailgun-passport >/dev/null
        ! test -e /usr/bin/mailgun-passport
        ! test -e /etc/mailgun-passport/mailgun-passport.env
        echo OK"
done

if [ -n "$failed" ]; then
    echo "FAILED:$failed" >&2
    exit 1
fi
echo "all package tests passed"
