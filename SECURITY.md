# Security Policy

[Русская версия ниже](#политика-безопасности)

## Reporting a vulnerability

Please **do not** open a public issue for a security problem.

Use GitHub's private reporting instead:
**[Report a vulnerability](https://github.com/Leadaxe/sing-box-lx/security/advisories/new)**.
The report is visible only to you and the maintainer until a fix is released.

Include what you can:

- affected version (`sing-box version`, e.g. `v1.14.2-lx.3`) and platform;
- the feature involved (XHTTP, AWG, MASQUE, VLESS encryption, `lxd` daemon, gRPC/REST API, …);
- steps to reproduce, a minimal config, or a proof of concept;
- impact as you understand it.

You will get a reply within a few days. Fixes ship as a hotfix tag on the current
`v1.14.x-lx.N` line; the advisory is published after the fix, with credit to the reporter
unless you ask otherwise.

## Scope

This repository is a downstream of [SagerNet/sing-box](https://github.com/SagerNet/sing-box).
Report here for anything in the `-lx` additions: transports (XHTTP, MASQUE), AWG/WireGuard
endpoints, VLESS encryption, the `lxd` daemon and its admin API, DNS group, chain outbound,
the launcher/LxBox integration surface.

If the problem is in upstream sing-box code, you may still report it here; we will confirm,
patch our line and tell you how to reach upstream. We cannot file upstream issues on your behalf.

## Supported versions

Only the latest stable `-lx` tag (see [Releases](https://github.com/Leadaxe/sing-box-lx/releases))
receives security fixes. Older tags and pre-releases are not patched.

---

# Политика безопасности

## Как сообщить об уязвимости

**Не открывайте** публичный issue для проблемы безопасности.

Используйте приватную форму GitHub:
**[Report a vulnerability](https://github.com/Leadaxe/sing-box-lx/security/advisories/new)**.
Отчёт виден только вам и мейнтейнеру до выхода исправления.

Укажите, что сможете:

- затронутую версию (`sing-box version`, например `v1.14.2-lx.3`) и платформу;
- фичу (XHTTP, AWG, MASQUE, VLESS-шифрование, демон `lxd`, gRPC/REST API, …);
- шаги воспроизведения, минимальный конфиг или PoC;
- последствия, как вы их понимаете.

Ответ — в течение нескольких дней. Исправление выходит хотфикс-тегом в текущей линии
`v1.14.x-lx.N`; advisory публикуется после фикса с упоминанием репортёра, если вы не против.

## Область

Репозиторий — downstream [SagerNet/sing-box](https://github.com/SagerNet/sing-box).
Сюда — всё, что относится к `-lx`-добавкам: транспорты (XHTTP, MASQUE), AWG/WireGuard-эндпоинты,
VLESS-шифрование, демон `lxd` и его admin-API, DNS-группа, chain outbound, поверхность
интеграции с лаунчером/LxBox.

Если проблема в апстримном коде sing-box, всё равно можно сообщить сюда: мы подтвердим,
исправим свою линию и подскажем, как связаться с апстримом. Заводить issue в апстриме за вас
мы не можем.

## Поддерживаемые версии

Исправления безопасности получает только последний stable-тег `-lx`
(см. [Releases](https://github.com/Leadaxe/sing-box-lx/releases)). Старые теги и пререлизы не патчатся.
