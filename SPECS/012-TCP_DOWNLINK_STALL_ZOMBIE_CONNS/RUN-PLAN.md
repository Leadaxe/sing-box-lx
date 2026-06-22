# 012 — RUN-PLAN: прогон зонда `LX_CONN_TRACE` на устройстве

План для **нашей** стороны (LxBox/диагностика) после того, как команда ядра отдаст
lx-ядро, собранное с зондом из [PROBE.md](PROBE.md). Цель прогона — снять развилку:
`read=0` (дефект выше copy, в proxy-расшифровке) vs `read>0, write=0` (застряла
запись в gVisor tun) vs `read>0, write>0` (двигалось, смотреть err/лаг).

## Предусловия

- ✅ **Тестовая сборка готова (22.06.2026):** APK с зондом собран —
  `app/build/app/outputs/flutter-apk/app-arm64-v8a-release.apk` (29.3 МБ).
  Зонд подтверждён в `lib/arm64-v8a/libbox.so` (строки `LX_CONN_TRACE`,
  `conn_trace_lx.go`). AAR-зонд подменён в `app/android/app/libs/libbox.aar`
  (sha `221c0e35…`), version-метка сбита на `lx-conn-trace-probe`, собрано в обход
  `fetch-libbox.sh` (иначе перекачал бы релиз). **Бэкап релиза:**
  `/tmp/libbox-release-backup.aar` (sha `c50786a6…`, =`v1.13.13-lx.14`) +
  `/tmp/.libbox.version.backup` — для отката после прогона.
- ⚠️ **Доставка env `LX_CONN_TRACE=1` в процесс ядра — НЕ решена кодом.** `Libbox` API
  не имеет `Setenv`; `os.Getenv` в Go читает env процесса. Кандидат для рут-устройства:
  Android wrap-property — `adb shell su -c 'setprop wrap.com.leadaxe.lxbox "LX_CONN_TRACE=1 "'`
  затем перезапуск приложения (Zygote стартует процесс с этой env). **Проверить на
  железе, что зонд активировался** (в core-логе при висящем зомби должны пойти
  `lx-trace download tick#k`). Если wrap-prop не сработает — запросить у команды ядра
  правку Kotlin (`os.Setenv`/JNI перед `Libbox.setup`) или чтение флага из конфига.
- CPH2411: USB нестабилен при 100% заряда; wifi-adb IP плавает (.181/.219) — сверять,
  `ensure-wifi-adb.sh`. На момент готовности APK устройство было отключено — переткнуть кабель.
- Debug API жив: `adb forward tcp:9269 tcp:9269`, token из памяти `project_dev_endpoints`.

## Установка сборки

```
adb install -r app/build/app/outputs/flutter-apk/app-arm64-v8a-release.apk
adb shell am start -n com.leadaxe.lxbox/.MainActivity   # стартануть, чтобы Debug API слушал
```
versionCode авто-согласован (§125, vc=2xxx) → встанет поверх релиза без downgrade.

## Шаги

1. **Поднять стрим core-лога В ФАЙЛ заранее** (не через `timeout` — на macOS его нет,
   это сломало прошлый прогон, см. SPEC уточнение №2):
   ```
   curl -s -N -H "Authorization: Bearer $TOKEN" "http://localhost:9269/logs?source=core" \
     | tee /tmp/lx_trace_run.log
   ```
   (фоном; перенаправление в файл, без `timeout`-обёртки.)

2. **Параллельно — синхронный двойной pcap** (как в успешном прогоне): tun0 (WhatsApp-эджи
   :443) + wlan0 (порт сервера выхода), ОДНОЙ adb-командой (единые часы), от момента SYN.

3. **Спровоцировать зомби** (пользователь — живой WhatsApp/Telegram; либо быстрый
   триггер `PUT /proxies/<selector>` сменой ноды, помня что в проде баг «не на переключение»).

4. **Поймать зомби** 1-Hz поллингом `/connections` — зафиксировать `destinationIP` + `sourcePort`.

5. **Снять лог зонда.** Зонд логирует ДВЕ формы (PROBE №3 + раздел «Гейт»):
   - `lx-trace download tick#k: read=… write=…` — периодически (дефолт 5s), **пока зомби
     висит**. Для висящего зомби это и есть прямой снимок: серия `tick#1 write=0`,
     `tick#2 write=0`… показывает застревание в реальном времени, разрывать НЕ нужно.
   - `lx-trace download final: read=… write=… err=…` — один раз, при отвисании
     (↓0→↓2820) или разрыве (`DELETE /connections/<id>`).
   Искать в `/tmp/lx_trace_run.log` строки `lx-trace download` для нужного conn (сверить
   с `destinationIP`/`sourcePort` из шага 4).

6. **Сопоставить** найденную строку с pcap-потоком по времени/объёму (тот же conn).

## Чтение результата (развилка)

| Снимок | Вывод | Следующий шаг расследования |
|---|---|---|
| `read=0 write=0` | proxy (reality/vless) не отдал plaintext, хотя pcap видел зашифрованные 777B | копать **выше** `connectionCopy`: расшифровка/фрейминг vless/reality на download |
| `read>0 write=0` | байты прочитаны из upstream, не записаны в tun | подтверждение гипотезы SPEC; копать запись в gVisor tun-сокет (флаш/блокировка) |
| `read>0 write>0` | copy двигался | смотреть `err`/`timeout`; причина в завершении/лаге, не в чистом stuck |

## Период тика

Тик уже встроен. Период задаётся значением `LX_CONN_TRACE`: `1`/`true` → 5s (дефолт);
`3s`/`1s`/`500ms` → этот период. Для висящего зомби 5s достаточно; если нужен более
плотный снимок прогресса — поставить `LX_CONN_TRACE=1s`. Помнить: чем чаще тик, тем
больше строк в core-логе.

## Не забыть

- `LX_CONN_TRACE` — только на время диагностики (теряется zero-copy splice, half-close
  деградирует в Close). После прогона — выключить / поставить релизное ядро обратно.
- Метод, который сработал и который НЕ менять: синхронный tun0+wlan0 ОДНОЙ командой
  (единые часы) + 1-Hz поллинг. Точечные несинхронные срезы врут (рассинхрон по времени).
