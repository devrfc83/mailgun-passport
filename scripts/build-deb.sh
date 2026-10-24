#!/bin/bash
set -e

DISTRO="$1"
DISTRO_VER="$2"
CODENAME="$3"
VERSION="$4"
NAME="$5"
PKG_DIR="$6"
DEB_DIR="$7"

echo "Building DEB for $DISTRO $DISTRO_VER ($CODENAME)..."

rm -rf "debian-build-${DISTRO}-${DISTRO_VER}"
mkdir -p "debian-build-${DISTRO}-${DISTRO_VER}"
tar -xzf "$PKG_DIR/${NAME}-${VERSION}.tar.gz" -C "debian-build-${DISTRO}-${DISTRO_VER}"

cd "debian-build-${DISTRO}-${DISTRO_VER}/${NAME}-${VERSION}"
mkdir -p debian

cat > debian/control << CTRL
Source: ${NAME}
Section: mail
Priority: optional
Maintainer: Mailgun Passport Team <maintainer@example.com>
Build-Depends: debhelper-compat (= 13), golang-go (>= 2:1.25)
Standards-Version: 4.6.0
Homepage: https://github.com/mailgun/mailgun-passport

Package: ${NAME}
Architecture: any
Depends: \${shlibs:Depends}, \${misc:Depends}, adduser, ca-certificates
Description: SMTP to Mailgun API relay
 Mailgun Passport is a service that accepts mail via SMTP on localhost
 and forwards it to Mailgun's API. It parses MIME messages and sends them
 using the Mailgun HTTP API.
CTRL

cat > debian/rules << 'RULES'
#!/usr/bin/make -f
%:
	dh $@

override_dh_auto_build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=$(DEB_VERSION)" -o mailgun-passport ./cmd/mailgun-passport

override_dh_auto_install:
	install -D -m 755 mailgun-passport debian/mailgun-passport/usr/bin/mailgun-passport
	install -D -m 644 contrib/systemd/mailgun-passport.service debian/mailgun-passport/lib/systemd/system/mailgun-passport.service
	install -D -m 600 .env.example debian/mailgun-passport/etc/mailgun-passport/mailgun-passport.env.example
RULES
chmod +x debian/rules

cat > debian/changelog << CHG
${NAME} (${VERSION}-1~${CODENAME}1) ${CODENAME}; urgency=medium

  * Automated build

 -- Mailgun Passport Team <maintainer@example.com>  $(date -R)
CHG

cat > debian/mailgun-passport.postinst << 'POSTINST'
#!/bin/sh
set -e
if ! getent group mailgun-passport >/dev/null; then
    addgroup --system mailgun-passport
fi
if ! getent passwd mailgun-passport >/dev/null; then
    adduser --system --ingroup mailgun-passport --home /opt/mailgun-passport --shell /usr/sbin/nologin mailgun-passport
fi
touch /var/log/mailgun-passport.log 2>/dev/null || true
chown mailgun-passport:mailgun-passport /var/log/mailgun-passport.log 2>/dev/null || true
chmod 640 /var/log/mailgun-passport.log 2>/dev/null || true
if [ -z "$2" ]; then
    if [ -f /etc/mailgun-passport/mailgun-passport.env.example ] && [ ! -f /etc/mailgun-passport.env ]; then
        cp /etc/mailgun-passport/mailgun-passport.env.example /etc/mailgun-passport.env
        chmod 600 /etc/mailgun-passport.env
        chown mailgun-passport:mailgun-passport /etc/mailgun-passport.env 2>/dev/null || true
    fi
fi
#DEBHELPER#
exit 0
POSTINST
chmod 755 debian/mailgun-passport.postinst

cat > debian/mailgun-passport.prerm << 'PRERM'
#!/bin/sh
set -e
#DEBHELPER#
exit 0
PRERM
chmod 755 debian/mailgun-passport.prerm

cat > debian/mailgun-passport.postrm << 'POSTRM'
#!/bin/sh
set -e
#DEBHELPER#
exit 0
POSTRM
chmod 755 debian/mailgun-passport.postrm

cat > debian/conffiles << 'CONF'
/etc/mailgun-passport.env
CONF

echo "/etc/mailgun-passport" >> debian/dirs
echo "/opt/mailgun-passport" >> debian/dirs

DEB_VERSION=${VERSION} dpkg-buildpackage -us -uc -b 2>&1 | tail -30

mkdir -p "$DEB_DIR/$DISTRO/$DISTRO_VER"
find .. -name "*.deb" -exec cp {} "$DEB_DIR/$DISTRO/$DISTRO_VER/" \; 2>/dev/null || true
find . -name "*.deb" -exec cp {} "$DEB_DIR/$DISTRO/$DISTRO_VER/" \; 2>/dev/null || true

echo "DEB built for $DISTRO $DISTRO_VER in $DEB_DIR/$DISTRO/$DISTRO_VER/"
