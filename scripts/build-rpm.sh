#!/bin/bash
set -e

DISTRO="$1"
DISTRO_VER="$2"
VERSION="$3"
NAME="$4"
PKG_DIR="$5"
RPM_DIR="$6"

echo "Building RPM for $DISTRO $DISTRO_VER..."

# rpmbuild cannot handle spaces in the build tree paths, so use a temp workdir.
WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/${NAME}-rpm-${DISTRO}-${DISTRO_VER}.XXXXXX")"
cleanup() { rm -rf "$WORK_ROOT"; }
trap cleanup EXIT

cp "$PKG_DIR/${NAME}-${VERSION}.tar.gz" "$WORK_ROOT/"
mkdir -p "$WORK_ROOT"/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
cp "$WORK_ROOT/${NAME}-${VERSION}.tar.gz" "$WORK_ROOT/SOURCES/"

cat > "$WORK_ROOT/SPECS/${NAME}.spec" << 'SPEC'
Name:           mailgun-passport
Version:        VERSION_PLACEHOLDER
Release:        1%{?dist}
Summary:        SMTP to Mailgun API relay
License:        MIT
URL:            https://github.com/mailgun/mailgun-passport
Source0:        %{name}-%{version}.tar.gz
BuildRequires:  golang >= 1.25
Requires:       ca-certificates
Requires(pre):  shadow-utils
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

%description
Mailgun Passport is a service that accepts mail via SMTP on localhost and forwards it to Mailgun's API. It parses MIME messages and sends them using the Mailgun HTTP API.

%global debug_package %{nil}

%prep
%setup -q

%build
export CGO_ENABLED=0
go build -trimpath -ldflags "-s -w -X main.Version=%{version}" -o %{name} ./cmd/mailgun-passport

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}%{_bindir}
mkdir -p %{buildroot}/etc
mkdir -p %{buildroot}%{_unitdir}
install -m 755 %{name} "%{buildroot}%{_bindir}/%{name}"
install -m 644 contrib/systemd/%{name}.service "%{buildroot}%{_unitdir}/%{name}.service"
install -m 600 .env.example "%{buildroot}/etc/%{name}.env.example"

%pre
getent group %{name} >/dev/null || groupadd -r %{name}
getent passwd %{name} >/dev/null || useradd -r -g %{name} -d /opt/%{name} -s /sbin/nologin %{name}

%post
%systemd_post %{name}.service
touch /var/log/%{name}.log 2>/dev/null || true
chown %{name}:%{name} /var/log/%{name}.log 2>/dev/null || true
chmod 640 /var/log/%{name}.log 2>/dev/null || true

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun_with_restart %{name}.service

%files
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%config(noreplace) /etc/%{name}.env.example
%doc README.md

%changelog
* Sat Oct 24 2026 Build Bot <build@example.com> - VERSION_PLACEHOLDER-1
- Automated build
SPEC

sed -i "s/VERSION_PLACEHOLDER/${VERSION}/" "$WORK_ROOT/SPECS/${NAME}.spec"

rpmbuild --define "_topdir $WORK_ROOT" --define "dist .${DISTRO}${DISTRO_VER}" -bb "$WORK_ROOT/SPECS/${NAME}.spec"

mkdir -p "$RPM_DIR/$DISTRO/$DISTRO_VER"
find "$WORK_ROOT/RPMS" -name "*.rpm" -exec cp {} "$RPM_DIR/$DISTRO/$DISTRO_VER/" \;

echo "RPM built for $DISTRO $DISTRO_VER in $RPM_DIR/$DISTRO/$DISTRO_VER/"
