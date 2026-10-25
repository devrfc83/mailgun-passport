#!/bin/sh
# Runs after install and upgrade. rpm passes 1 (install) or 2 (upgrade);
# deb passes "configure" plus the previously installed version, if any.
set -e

if command -v systemd-sysusers >/dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/mailgun-passport.conf
elif ! getent passwd mailgun-passport >/dev/null 2>&1; then
    useradd --system --user-group --home-dir / --no-create-home \
        --shell /usr/sbin/nologin --comment "Mailgun Passport SMTP relay" \
        mailgun-passport
fi

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
    # Picks up the new binary on upgrade; does nothing if the service is stopped.
    systemctl try-restart mailgun-passport.service >/dev/null 2>&1 || :
fi

first_install=false
if [ "$1" = 1 ]; then
    first_install=true
elif [ "$1" = configure ] && [ -z "${2:-}" ]; then
    first_install=true
fi
if [ "$first_install" = true ]; then
    cat <<'EOF'
mailgun-passport is installed but not enabled. Set MAILGUN_API_KEY and
MAILGUN_DOMAIN, then start it:
  sudoedit /etc/mailgun-passport/mailgun-passport.env
  sudo systemctl enable --now mailgun-passport
EOF
fi
