# Кто использует sing-box-lx

[English version](consumers.md)

Проекты, которые поставляют [sing-box-lx](https://github.com/Leadaxe/sing-box-lx), встраивают его или строятся на нём. sing-box-lx — клиентское ядро на базе sing-box с XHTTP, AmneziaWG, MASQUE / Cloudflare WARP, шифрованием VLESS, TrustTunnel, демоном `lxd` и наблюдаемостью.

## Собственные потребители

| Потребитель | Платформа | Что берёт отсюда |
|---|---|---|
| [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) | десктоп | бинарь `sing-box` (кладётся как `bin/sing-box`), опционально демон `lxd` |
| [LxBox](https://github.com/Leadaxe/LxBox) | Android | `libbox.aar` и расширения `CommandClient` |
| Роутеры OpenWrt | сборки mips / musl | бинарь как `lxd` вместе со скриптами установки |

## Проекты на базе sing-box-lx

Независимые проекты, которые поставляют это ядро, встраивают его или строятся на нём. Здесь они не сопровождаются; список составлен по их публичным репозиториям на 2026-11-23.

**Клиенты и приложения**

| Проект | Что это | Как использует sing-box-lx |
|---|---|---|
| [KeenProxy](https://github.com/ordo-systems/keen-proxy) | выборочная маршрутизация с веб-панелью для роутеров Keenetic / Netcraze (Entware) и OpenWrt | встраивает ядро как Go-модуль: `replace github.com/sagernet/sing-box => github.com/Leadaxe/sing-box-lx` на релизный тег, вместе с форками sing-tun, gVisor и uTLS |
| [Tunor](https://github.com/Vexorter42/Tunor) | GUI для Windows, macOS, Linux и Android с AmneziaWG 1.0–3.x | скачивает релизные бинари и Android-`libbox` из релизов этого репозитория |
| [NyameBox](https://github.com/qr243vbi/nekobox) (оригинальный NekoBox под новым именем) | кроссплатформенный десктоп-клиент на Qt | его ядро [qr243vbi/sing-box](https://github.com/qr243vbi/sing-box) — форк sing-box-lx, подготовленный для NekoBox 6; outbound TrustTunnel появился здесь по запросу этого проекта |
| [Hiddify](https://github.com/hiddify/hiddify-app) | мультиплатформенный клиент на собственном форке sing-box | [документация ядра Hiddify](https://github.com/hiddify/hiddify.com/blob/main/docs/core/features/index.md) называет sing-box-lx источником дополнений URL-теста, REALITY `key_share`, failover для MASQUE и режимов DNS-группы, а [utls-lx](https://github.com/Leadaxe/utls-lx) — источником браузерных TLS-отпечатков |
| [VPN Direct](https://github.com/TT450/vpn-direct-app) | нативный клиент для Apple (iOS / macOS / tvOS) | его ядро — тонкий форк, начатый от sing-box-lx (XHTTP, AWG, MASQUE, шифрование VLESS, idle suspend) |
| [CELERITY panel](https://github.com/ClickDevTech/CELERITY-panel) | self-hosted панель для серверов Hysteria 2 и Xray VLESS | её пробы узлов ставят и запускают sing-box-lx, собранный с `with_xhttp`, — то же ядро, что у клиентов панели |

**Скрипты для роутеров**

| Проект | Что это | Как использует sing-box-lx |
|---|---|---|
| [zirgozar](https://github.com/dreamboxone/zirgozar) | VPN-центр для OpenWrt | ставит sing-box-lx как одно из ядер; узлы AmneziaWG и naive работают на нём |
| [sing-box4asus](https://github.com/Dr4tez/sing-box4asus) | скрипт sing-box для роутеров Asus на прошивке Merlin | принимает sing-box-lx как выбираемое ядро |
| [SKeen](https://github.com/jinndi/SKeen) | прозрачный прокси для роутеров Keenetic / Netcraze | пример с XHTTP построен на sing-box-lx как ядре |

**Инструменты и совместимые реализации**

| Проект | Что это | Связь с sing-box-lx |
|---|---|---|
| [tachyon-core](https://github.com/Dushnilin/tachyon-core) | прокси-ядро на Rust для роутеров OpenWrt | заявляет совместимость с конфигом sing-box-lx и меряется с закреплённым релизом sing-box-lx в бенчмарках |
| [cfwarp-converter](https://github.com/juerson/cfwarp-converter) | конвертер профилей Cloudflare WARP (WireGuard и MASQUE) для сторонних ядер | имеет отдельный вывод под sing-box-lx |
| [F-Droid](https://github.com/f-droid/fdroiddata) | каталог приложений | собирает LxBox из исходников с sing-box-lx как исходной библиотекой |

Поставляете это ядро и вас нет в списке? Откройте issue со ссылкой на место, где закреплена зависимость.
