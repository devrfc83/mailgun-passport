#!/bin/sh
# Runs before removal and upgrade. rpm passes 0 on removal (1 on upgrade);
# deb passes "remove" on removal ("upgrade" on upgrade).
set -e

case "$1" in
    0|remove)
        if [ -d /run/systemd/system ]; then
            systemctl disable --now mailgun-passport.service >/dev/null 2>&1 || :
        fi
        ;;
esac
