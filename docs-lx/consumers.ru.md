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
| [ZDT-D](https://github.com/GAME-OVER-op/ZDT-D) | модуль обхода ограничений для Android с root | запускает sing-box-lx отдельным компонентом со своим разделом API, включая аккаунт WARP / MASQUE |
| [justray](https://github.com/luynrs/justray) | VPN-клиент в терминале | встраивает ядро как Go-модуль через `replace`, запинен на коммит ветки `lx` |
| [JiajiaBox](https://github.com/weilai090624-code/JiajiaBox) | Android-клиент линии NekoBox | в уведомлениях называет sing-box-lx источником клиента VLESS Encryption |
| [MCRF](https://github.com/vnenapravo7-source/mcrf) | клиент для Windows с портами под Android | поставляет собственную сборку sing-box-lx, запиненную на релизный тег |
| [LAZEYKA / INCY](https://github.com/reb0oornalexey/LAZEYKA) | десктоп-клиент на Electron | использует ядро на Windows и macOS; на macOS ставит демон `lxd` как LaunchDaemon для режима TUN |
| [TarnVPN](https://github.com/Amesu-afk/TarnVPN) | Android-клиент (VLESS / REALITY, XHTTP) | построен на sing-box-lx из собственного форка ядра |
| [sora](https://github.com/levvs-one/sora) | клиент для Windows, Linux и Android | пинит релиз sing-box-lx вместе с checksum |
| [VPNRouter](https://github.com/PavelLizunov/VPNRouter) | split-tunnel по приложениям на Avalonia | собирает ядро из запиненного коммита sing-box-lx |
| [cloakwire](https://github.com/markwhite7881-cpu/cloakwire) | клиент для Windows, macOS и Android | поставляет sing-box-lx рядом с Xray |
| [GhostBox](https://github.com/Krakva1337/GhostBox) | клиент на Flutter | собирает Android-`libbox` из запиненного релиза sing-box-lx |
| [vladossVPN](https://github.com/valdissor1990-ui/vladossVPN) | Android-клиент (VLESS + XHTTP + REALITY) | пинит релиз sing-box-lx |
| [Brod](https://github.com/dqzaexxpess/brod) | бесплатный VPN-клиент для Linux, Windows и Android: собирает бесплатные подписки и проверяет узлы в сети пользователя | встраивает ядро как Go-модуль через `replace` на скачанное дерево исходников sing-box-lx, со своими патчами поверх |

**Скрипты для роутеров**

| Проект | Что это | Как использует sing-box-lx |
|---|---|---|
| [zirgozar](https://github.com/dreamboxone/zirgozar) | VPN-центр для OpenWrt | ставит sing-box-lx как одно из ядер; узлы AmneziaWG и naive работают на нём |
| [sing-box4asus](https://github.com/Dr4tez/sing-box4asus) | скрипт sing-box для роутеров Asus на прошивке Merlin | принимает sing-box-lx как выбираемое ядро |
| [SKeen](https://github.com/jinndi/SKeen) | прозрачный прокси для роутеров Keenetic / Netcraze | пример с XHTTP построен на sing-box-lx как ядре |
| [mawg](https://github.com/MarkinAlexander/mawg) | менеджер WireGuard / AmneziaWG для Keenetic / Netcraze и OpenWrt | ставит sing-box-lx из своей панели как движок для прокси-узлов |
| [cheburnet](https://github.com/ph4n70m1984/cheburnet) | прозрачный прокси и демон маршрутизации | предлагает sing-box-lx как ядро в установщике и собирает пакеты из релизного тега |
| [singA](https://github.com/infinjest/singA) | прозрачный шлюз для OpenWrt с веб-интерфейсом | sing-box-lx — его ядро; установщик берёт последний релиз |
| [Forkop](https://github.com/ushan0v/forkop) | маршрутизация трафика по правилам для OpenWrt | его ядро [sing-box-forkop](https://github.com/ushan0v/sing-box-forkop) содержит реализацию TrustTunnel из этого репозитория — 19 из 26 файлов TrustTunnel совпадают со здешними байт в байт — без указания источника |

**Инструменты и совместимые реализации**

| Проект | Что это | Связь с sing-box-lx |
|---|---|---|
| [tachyon-core](https://github.com/Dushnilin/tachyon-core) | прокси-ядро на Rust для роутеров OpenWrt | заявляет совместимость с конфигом sing-box-lx и меряется с запиненным релизом sing-box-lx в бенчмарках |
| [cfwarp-converter](https://github.com/juerson/cfwarp-converter) | конвертер профилей Cloudflare WARP (WireGuard и MASQUE) для сторонних ядер | имеет отдельный вывод под sing-box-lx |
| [F-Droid](https://github.com/f-droid/fdroiddata) | каталог приложений | собирает LxBox из исходников с sing-box-lx как исходной библиотекой |
| [GUI.for.Cores Plugin-Hub](https://github.com/GUI-for-Cores/Plugin-Hub) | каталог плагинов для GUI.for.SingBox | содержит плагин, который ставит, проверяет и обновляет sing-box-lx из релизов этого репозитория |
| [ALT Linux](https://github.com/altlinux/specs) | дистрибутив Linux | его пакет `sing-box` в Sisyphus собирается с возможностями sing-box-lx `with_xhttp` и `with_awg` |
| [ScarletDevil](https://github.com/Areral/ScarletDevil) | агрегатор и валидатор списков прокси-узлов | в инструкции по клиентам ссылается на [singbox-launcher](https://github.com/Leadaxe/singbox-launcher) — десктоп-клиент на этом ядре |

Поставляете это ядро и вас нет в списке? Откройте issue со ссылкой на место, где запинена зависимость.
