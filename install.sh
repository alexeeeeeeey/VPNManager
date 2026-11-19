#!/bin/sh
set -eu

repo=alexeeeeeeey/VPNManager
xray_repo=XTLS/Xray-core
config=/etc/vpn-manager/config.json

fail() { printf '%s\n' "$*" >&2; exit 1; }
download() { curl --proto '=https' --proto-redir '=https' -fsSL --retry 2 --connect-timeout 10 --max-time 180 "$1" -o "$2"; }
latest_tag() {
    link=$(curl --proto '=https' --proto-redir '=https' -fsSL --connect-timeout 10 --max-time 30 \
        -o /dev/null -w '%{url_effective}' "https://github.com/$1/releases/latest") || return 1
    case "$link" in
        "https://github.com/$1/releases/tag/"*) printf '%s\n' "${link##*/}" ;;
        *) return 1 ;;
    esac
}
verify_panel() {
    expected=$(awk -v name="${artifact##*/}" '$2 == name || $2 == "*" name {print $1; exit}' "$checksums")
    actual=$(sha256sum "$artifact" | awk '{print $1}')
    [ -n "$expected" ] && [ "$actual" = "$expected" ] || fail "SHA-256 архива панели не совпадает."
}

install_dependencies() {
    if [ "$platform" = openwrt ]; then
        set -- ip-full kmod-nft-tproxy kmod-nft-queue conntrack ca-bundle curl unzip
        if ! command -v nft >/dev/null; then set -- "$@" nftables-json; fi
        if [ "$package_manager" = opkg ]; then
            opkg update
            opkg install "$@"
        else
            apk update
            apk add "$@"
        fi
    elif command -v apt-get >/dev/null; then
        apt-get update
        apt-get install -y ca-certificates curl unzip iproute2 nftables conntrack
    elif command -v dnf >/dev/null; then
        dnf install -y ca-certificates curl unzip iproute nftables conntrack-tools
    elif command -v pacman >/dev/null; then
        pacman -S --needed --noconfirm ca-certificates curl unzip iproute2 nftables conntrack-tools
    fi
    for tool in curl unzip sha256sum tar ip nft conntrack; do
        command -v "$tool" >/dev/null || fail "Системная команда $tool недоступна."
    done
}

install_xray() {
    if command -v xray >/dev/null && [ -s /usr/share/xray/geoip.dat ] && [ -s /usr/share/xray/geosite.dat ]; then
        return
    fi
    tag=$(latest_tag "$xray_repo") || fail "Не удалось получить стабильный релиз Xray-core."
    base="https://github.com/$xray_repo/releases/download/$tag"
    zip="$work/$xray_asset"
    download "$base/$xray_asset" "$zip"
    download "$base/$xray_asset.dgst" "$zip.dgst"
    actual=$(sha256sum "$zip" | awk '{print $1}')
    expected=$(awk '$1 == "SHA2-256=" {print tolower($2); exit}' "$zip.dgst")
    [ -n "$expected" ] && [ "$actual" = "$expected" ] || fail "SHA-256 архива Xray-core не совпадает."

    if ! command -v xray >/dev/null; then
        unzip -p "$zip" xray > "$stage/xray"
        chmod 755 "$stage/xray"
        "$stage/xray" version >/dev/null
        mv "$stage/xray" /usr/bin/xray
    fi
    mkdir -p /usr/share/xray
    for asset in geoip.dat geosite.dat; do
        if [ ! -s "/usr/share/xray/$asset" ]; then
            asset_tmp=$(mktemp "/usr/share/xray/.$asset.XXXXXX")
            unzip -p "$zip" "$asset" > "$asset_tmp"
            chmod 644 "$asset_tmp"
            mv "$asset_tmp" "/usr/share/xray/$asset"
            asset_tmp=
        fi
    done
    rm -f "$zip" "$zip.dgst"
}

start_panel() {
    if [ "$platform" = openwrt ]; then
        /etc/init.d/vpn-manager enable
        /etc/init.d/vpn-manager restart
    else
        systemctl daemon-reload
        systemctl enable vpn-manager.service
        systemctl restart vpn-manager.service
    fi
}

print_login() {
    tries=0
    while [ "$tries" -lt 20 ]; do
        if [ -s "$config" ]; then
            username=$(sed -n 's/^[[:space:]]*"username":[[:space:]]*"\([^"]*\)".*/\1/p' "$config")
            password=$(sed -n 's/^[[:space:]]*"password":[[:space:]]*"\([^"]*\)".*/\1/p' "$config")
            if [ -n "$username" ] && [ -n "$password" ]; then
                printf 'Login: %s\nPassword: %s\n' "$username" "$password"
                return
            fi
        fi
        tries=$((tries + 1))
        sleep 1
    done
    fail "Панель ещё не создала пароль. Проверьте запуск службы."
}

[ "$(id -u)" -eq 0 ] || fail "Запустите установку от root."
[ "$(uname -s)" = Linux ] || fail "Нужен OpenWrt или Linux с systemd."
[ "$#" -le 1 ] || fail "Использование: $0 [пакет, архив или бинарник vpn-manager]"
case "$(uname -m)" in
    x86_64) arch=amd64; xray_asset=Xray-linux-64.zip ;;
    aarch64|arm64) arch=arm64; xray_asset=Xray-linux-arm64-v8a.zip ;;
    *) fail "Автоустановка поддерживает x86_64 и ARM64." ;;
esac

platform=systemd
package_manager=
unit=packaging/systemd/vpn-manager.service
unit_destination=/etc/systemd/system/vpn-manager.service
if [ -f /etc/openwrt_release ]; then
    platform=openwrt
    unit=packaging/openwrt/vpn-manager.init
    unit_destination=/etc/init.d/vpn-manager
    if command -v opkg >/dev/null; then
        package_manager=opkg
    elif command -v apk >/dev/null; then
        package_manager=apk
    else
        fail "В OpenWrt не найден opkg или apk."
    fi
else
    command -v systemctl >/dev/null || fail "На Linux нужна служба systemd."
fi

artifact=${1:-}
if [ -n "$artifact" ]; then
    [ -f "$artifact" ] || fail "Файл установки не найден."
    case "$artifact" in /*) ;; *) artifact="$PWD/$artifact" ;; esac
    case "$artifact" in
        *.ipk) [ "$package_manager" = opkg ] || fail "Для .ipk нужен OpenWrt с opkg." ;;
        *.apk) [ "$package_manager" = apk ] || fail "Для .apk нужен OpenWrt с apk." ;;
        *.tar.gz|*/vpn-manager) ;;
        *) fail "Ожидается пакет .ipk/.apk, архив .tar.gz или бинарник vpn-manager." ;;
    esac
fi
case "$artifact" in
    *.ipk|*.apk) ;;
    *)
        if [ "$package_manager" = opkg ] && opkg status vpn-manager | grep -q '^Status: .* installed'; then
            fail "Панель установлена пакетом. Для обновления передайте новый .ipk."
        elif [ "$package_manager" = apk ] && apk info -e vpn-manager >/dev/null 2>&1; then
            fail "Панель установлена пакетом. Для обновления передайте новый .apk."
        fi
        ;;
esac

first_install=0
[ -e "$config" ] || first_install=1
install_dependencies
work=$(mktemp -d)
stage=
asset_tmp=
cleanup() {
    rm -rf "$work"
    if [ -n "$stage" ]; then rm -rf "$stage"; fi
    if [ -n "$asset_tmp" ]; then rm -f "$asset_tmp"; fi
}
trap cleanup EXIT
stage=$(mktemp -d /usr/bin/.vpn-manager-install.XXXXXX)

if [ -z "$artifact" ]; then
    tag=$(latest_tag "$repo") || fail "Нет опубликованного стабильного релиза $repo."
    printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail "Неверный тег релиза панели."
    artifact="$work/vpn-manager_${tag#v}_linux_$arch.tar.gz"
    base="https://github.com/$repo/releases/download/$tag"
    download "$base/${artifact##*/}" "$artifact"
    download "$base/checksums.txt" "$work/checksums.txt"
fi

case "$artifact" in
    *.ipk|*.apk) ;;
    *)
        checksums="${artifact%/*}/checksums.txt"
        [ -f "$checksums" ] || fail "Положите checksums.txt рядом с файлом установки."
        verify_panel
        case "$artifact" in
            *.tar.gz)
                tar -xOzf "$artifact" vpn-manager > "$stage/vpn-manager"
                tar -xOzf "$artifact" "$unit" > "$work/service"
                ;;
            *)
                cp "$artifact" "$stage/vpn-manager"
                project=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
                cp "$project/$unit" "$work/service"
                ;;
        esac
        chmod 755 "$stage/vpn-manager"
        "$stage/vpn-manager" -version >/dev/null || fail "Бинарник не подходит для этой системы."
        if [ "${artifact%/*}" = "$work" ]; then rm -f "$artifact"; fi
        ;;
esac

install_xray
case "$artifact" in
    *.ipk) opkg install "$artifact" ;;
    *.apk) apk add --allow-untrusted "$artifact" ;;
    *)
        cp "$work/service" "$unit_destination"
        if [ "$platform" = openwrt ]; then chmod 755 "$unit_destination"; else chmod 644 "$unit_destination"; fi
        mv "$stage/vpn-manager" /usr/bin/vpn-manager
        ;;
esac
start_panel
printf '%s\n' "VPN Manager установлен. Конфигурация: $config"
if [ "$first_install" -eq 1 ]; then print_login; fi
