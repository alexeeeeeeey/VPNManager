#!/bin/sh
set -eu

[ "$(id -u)" -eq 0 ] || { printf '%s\n' "Запустите удаление от root." >&2; exit 1; }
case "${1:-}" in ""|--purge) ;; *) printf '%s\n' "Использование: $0 [--purge]" >&2; exit 1 ;; esac
[ "$#" -le 1 ] || exit 1

if [ -f /etc/openwrt_release ]; then
    if [ -x /etc/init.d/vpn-manager ]; then
        /etc/init.d/vpn-manager stop
        /etc/init.d/vpn-manager disable
    fi
    if command -v opkg >/dev/null && opkg status vpn-manager | grep -q '^Status: .* installed'; then
        opkg remove vpn-manager
    elif command -v apk >/dev/null && apk info -e vpn-manager >/dev/null 2>&1; then
        apk del vpn-manager
    fi
    rm -f /etc/init.d/vpn-manager
elif [ "$(uname -s)" = Linux ] && command -v systemctl >/dev/null; then
    if [ -f /etc/systemd/system/vpn-manager.service ]; then
        systemctl disable --now vpn-manager.service
        rm -f /etc/systemd/system/vpn-manager.service
        systemctl daemon-reload
    fi
else
    printf '%s\n' "Автоудаление поддерживает OpenWrt и Linux с systemd." >&2
    exit 1
fi

rm -f /usr/bin/vpn-manager /usr/bin/vpn-manager.previous
if [ "${1:-}" = --purge ]; then
    rm -rf /etc/vpn-manager
else
    printf '%s\n' "Данные сохранены в /etc/vpn-manager."
fi
