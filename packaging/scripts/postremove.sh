#!/bin/sh
# Runs after removal and upgrade. rpm passes 0 on removal;
# deb passes "remove" or "purge". The system user is kept on purpose.
set -e

case "$1" in
    0|remove|purge)
        if [ -d /run/systemd/system ]; then
            systemctl daemon-reload >/dev/null 2>&1 || :
        fi
        ;;
esac
