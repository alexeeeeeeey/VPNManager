# VPN Manager

Веб-панель для управления Xray, Zapret2, DNS и маршрутами устройств на OpenWrt.

## Установка

Под `root` в Bash:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/alexeeeeeeey/VPNManager/main/install.sh)
```

На чистом OpenWrt без Bash и curl:

```sh
wget -O /tmp/vpn-manager-install.sh https://raw.githubusercontent.com/alexeeeeeeey/VPNManager/main/install.sh && sh /tmp/vpn-manager-install.sh
```

Готовый пакет OpenWrt можно передать установщику напрямую, например `sh install.sh ./vpn-manager.ipk`. Для сборки пакета используйте workflow [OpenWrt package](.github/workflows/openwrt.yml).

## Вход

На OpenWrt откройте панель по LAN-адресу роутера, например [http://192.168.1.1:9080](http://192.168.1.1:9080). Логин и пароль выводятся при первой установке.

## Возможности

- Импорт VPN-подписок, ссылок и JSON Xray, проверка задержки и обновление подписок.
- Общие правила и пресеты для устройств с прямым выходом, VPN, Zapret2 или блокировкой.
- Запуск Xray без VPN-профиля и подбор TLS-стратегии Zapret2 на заданных доменах.
- Настраиваемая DNS-цепочка через Xray, DoH и обычные DNS-серверы.
- Графики CPU, памяти и трафика, устройства LAN и журнал.
- Смена пароля, порта и темы, настройка автообновления.

## Удаление

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/alexeeeeeeey/VPNManager/main/uninstall.sh)
```

На OpenWrt без Bash и curl:

```sh
wget -O /tmp/vpn-manager-uninstall.sh https://raw.githubusercontent.com/alexeeeeeeey/VPNManager/main/uninstall.sh && sh /tmp/vpn-manager-uninstall.sh
```

Данные панели сохраняются в `/etc/vpn-manager/`. Для удаления данных передайте скрипту `--purge`.
