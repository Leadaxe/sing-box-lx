#!/usr/bin/env bash
# LX_WG_NO_GRO A/B — measure download throughput + bufsArrs heap, GRO-off/batch-8 vs stock.
#
# Hypothesis (SPEC 020): forcing GRO off + a reduced recv batch (8) shrinks the
# recv-worker bufsArrs (the RAM/GC holder) — does it keep download throughput on a
# real cellular/WARP link? If yes, this is a simpler global alternative to Down/Up.
#
# PREREQUISITES on the phone (CPH2411 or any rooted Android):
#   - LxBox built from branch lx-1.14-nogro-exp (AAR carries the LX_WG_NO_GRO switch).
#   - A WARP (or any WG) endpoint set as the active/final outbound, tunnel UP.
#   - Root (su) to set the wrap.<pkg> prop and read /proc/<pid>/environ.
#   - A static arm64 curl in /data/local/tmp (see RESEARCH.md §throughput method).
#   - The app's pprof/diag endpoint reachable (adb forward), as in ANDROID_RESEARCH.
#
# The core reads LX_WG_NO_GRO once via os.Getenv at tunnel start, and the app does
# not forward env — so we inject it with the Zygote wrap.<pkg> prop and force-stop
# the app so it restarts with the new env. (Same mechanism as LX_CONN_TRACE, see
# memory lx-conn-trace-probe-and-env.)

set -euo pipefail
DEV="${DEV:-$(adb devices | awk 'NR==2{print $1}')}"
PKG="${PKG:-com.leadaxe.lxbox}"
CURL="${CURL:-/data/local/tmp/curl}"
# Cloudflare speed endpoint (works through WARP); --resolve avoids in-tunnel DNS.
DL_HOST="speed.cloudflare.com"
DL_PATH="/__down?bytes=52428800"   # 50 MB, as in RESEARCH.md
PPROF="${PPROF:-http://127.0.0.1:9269/diag/pprof}"   # adb forward tcp:9269 first
RUNS="${RUNS:-5}"

say(){ printf '\n=== %s ===\n' "$*"; }
adbsh(){ adb -s "$DEV" shell "$@"; }

set_nogro(){ # $1 = 0|1
  say "set LX_WG_NO_GRO=$1 and restart $PKG"
  adbsh "su -c 'setprop wrap.$PKG \"LX_WG_NO_GRO=$1\"'"
  adbsh "su -c 'am force-stop $PKG'"
  echo "  now start the tunnel again in the app (or via its command), wait for handshake"
  echo "  press Enter once the tunnel is UP..."; read -r _
  local pid; pid="$(adbsh "su -c 'pidof $PKG'" | tr -d '\r')"
  echo "  verifying env in /proc/$pid/environ:"
  adbsh "su -c 'cat /proc/$pid/environ'" | tr '\0' '\n' | grep -i LX_WG_NO_GRO || echo "  ⚠️ env NOT set — check root/prop"
}

download_ab(){ # $1 = label
  say "download × $RUNS ($1)"
  local ip; ip="$(adbsh "$CURL -s --resolve $DL_HOST:443:1.1.1.1 -o /dev/null -w '%{remote_ip}' https://$DL_HOST/ 2>/dev/null" || true)"
  for i in $(seq 1 "$RUNS"); do
    # -k: glibc curl finds no CA store on Android; --resolve: DNS bypass (RESEARCH.md).
    adbsh "$CURL -sk --resolve $DL_HOST:443:104.16.123.96 -o /dev/null \
      -w '  run $i: %{speed_download} B/s  (%{size_download} bytes, %{time_total}s)\n' \
      https://$DL_HOST$DL_PATH" || echo "  run $i: FAILED"
  done
}

heap_snapshot(){ # $1 = label
  say "heap snapshot ($1) — bufsArrs holder"
  local out="heap-$1.pb"
  curl -s "$PPROF?profile=heap&query=gc=1" -o "$out" && echo "  saved $out"
  go tool pprof -top -inuse_space "$out" 2>/dev/null | \
    grep -iE "PopulatePools|RoutineReceiveIncoming|bufsArrs" | head -4 || true
  curl -s "$PPROF?profile=goroutine&query=debug=2" 2>/dev/null | \
    grep -c RoutineReceiveIncoming | xargs echo "  RoutineReceiveIncoming goroutines:"
}

say "PHASE A — STOCK (LX_WG_NO_GRO=0): full GRO, batch=128"
set_nogro 0
heap_snapshot before-stock
download_ab stock

say "PHASE B — EXPERIMENT (LX_WG_NO_GRO=1): GRO off, batch=8"
set_nogro 1
heap_snapshot after-nogro
download_ab nogro

say "DONE — compare"
echo "Expect: bufsArrs/PopulatePools much smaller under nogro (128→8 = ~16× less per worker),"
echo "recv-worker goroutine count unchanged. The QUESTION is download speed:"
echo "  - if nogro speed ≈ stock speed → GRO not needed on this link → simpler global fix viable"
echo "  - if nogro speed << stock speed → GRO is load-bearing → keep Down/Up (rc.19)"
