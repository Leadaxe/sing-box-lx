#!/usr/bin/env bash
# FEATURE 019 / SPEC 060 §6: loopback stand for lx.sniff_guard against a real
# SNI-sniffing Xray (destOverride http,tls,quic; routeOnly false).
#
#   lx-test/sniffguard/stand.sh gen               # cert + server/xray configs into $SG_WORK
#   lx-test/sniffguard/stand.sh up                # HTTP origin, sing-box servers, Xray
#   lx-test/sniffguard/stand.sh run NAME [N]      # client NAME: N curls per node, summary, stop
#   lx-test/sniffguard/stand.sh client NAME       # start client NAME (keep cache/log)
#   lx-test/sniffguard/stand.sh curl PORT [N] [URL]  # N curls through mixed PORT
#   lx-test/sniffguard/stand.sh client-down
#   lx-test/sniffguard/stand.sh check NAME        # sing-box check on client NAME
#   lx-test/sniffguard/stand.sh capture NAME [N]  # first N datagrams of client NAME -> capture.hex
#   lx-test/sniffguard/stand.sh list|status|down
#
# Everything listens on 127.0.0.1 (ports 28501-28580, clients from 28600), except
# the second HTTP origin for the WireGuard scenarios on SG_LAN_IP:28581 (the
# WireGuard server drops a loopback inner destination as martian). No TUN, no
# routes, no system proxy. Binaries: SG_SINGBOX (default bin/sing-box) and
# SG_XRAY (default bin/xray); work dir SG_WORK (default .stand/, gitignored).
set -u

HERE=$(cd "$(dirname "$0")" && pwd)
SINGBOX=${SG_SINGBOX:-$HERE/bin/sing-box}
XRAY=${SG_XRAY:-$HERE/bin/xray}
WORK=${SG_WORK:-$HERE/.stand}
TRIES_TIMEOUT=${SG_CURL_TIMEOUT:-12}
HTTP_PORT=28580
HTTP_LAN_PORT=28581
CAPTURE_PORT=28540
# first non-loopback IPv4: the WireGuard server's gVisor stack drops loopback inner destinations
SG_LAN_IP=${SG_LAN_IP:-$(ipconfig getifaddr en0 2>/dev/null || ip -4 -o addr show scope global 2>/dev/null | awk '{sub("/.*","",$4); print $4; exit}')}
export SG_LAN_IP
GEN="python3 -I $HERE/gen.py"

pidfile() { echo "$WORK/$1.pid"; }

start() { # name cmd...
	local name=$1
	shift
	"$@" >"$WORK/$name.log" 2>&1 &
	echo $! >"$(pidfile "$name")"
}

stop() {
	local f
	f=$(pidfile "$1")
	[ -f "$f" ] || return 0
	kill "$(cat "$f")" 2>/dev/null
	rm -f "$f"
}

gen() {
	mkdir -p "$WORK/www" || exit 1
	echo "sniffguard origin ok" >"$WORK/www/index.html"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
		-subj "/CN=srv.test" -addext "subjectAltName=DNS:srv.test" \
		-keyout "$WORK/key.pem" -out "$WORK/cert.pem" 2>/dev/null || exit 1
	"$SINGBOX" generate wg-keypair >"$WORK/wg-server.keys" || exit 1
	"$SINGBOX" generate wg-keypair >"$WORK/wg-client.keys" || exit 1
	$GEN server "$WORK" && $GEN xray "$WORK" || exit 1
	"$SINGBOX" check -c "$WORK/server.json" || exit 1
	"$XRAY" run -test -c "$WORK/xray.json" >/dev/null || exit 1
	echo "generated in $WORK"
}

up() {
	start http python3 -m http.server "$HTTP_PORT" --bind 127.0.0.1 --directory "$WORK/www"
	[ -n "$SG_LAN_IP" ] && start http-lan python3 -m http.server "$HTTP_LAN_PORT" --bind "$SG_LAN_IP" --directory "$WORK/www"
	start server "$SINGBOX" run -c "$WORK/server.json"
	start xray "$XRAY" run -c "$WORK/xray.json"
	sleep 1
	status
}

down() {
	client_down
	stop xray
	stop server
	stop http-lan
	stop http
}

status() {
	local n
	for n in http http-lan server xray client capture; do
		if [ -f "$(pidfile $n)" ] && kill -0 "$(cat "$(pidfile $n)")" 2>/dev/null; then
			echo "$n: running ($(cat "$(pidfile $n)"))"
		else
			echo "$n: stopped"
		fi
	done
}

client() {
	client_down
	$GEN client "$WORK" "$1" >"$WORK/$1.ports" || exit 1
	start client "$SINGBOX" run -c "$WORK/$1.json"
	sleep 1
	cat "$WORK/$1.ports"
}

client_down() {
	stop client
	sleep 0.3
}

curl_n() { # port n [url]
	local i out t0 t1
	for i in $(seq 1 "${2:-1}"); do
		t0=$(python3 -c 'import time;print(time.time())')
		out=$(curl -s -o /dev/null -w '%{http_code}' --max-time "$TRIES_TIMEOUT" \
			--socks5-hostname "127.0.0.1:$1" "${3:-http://127.0.0.1:$HTTP_PORT/}")
		t1=$(python3 -c 'import time;print(time.time())')
		printf 'port %s try %d: http=%s %.2fs\n' "$1" "$i" "$out" "$(python3 -c "print($t1-$t0)")"
	done
}

run() { # name n
	local tag port xstart
	xstart=$(wc -l <"$WORK/xray.log")
	client "$1" >/dev/null
	while read -r tag port url; do
		echo "== $tag"
		curl_n "$port" "${2:-3}" "$url"
	done <"$WORK/$1.ports"
	client_down
	echo "== sniff guard lines ($WORK/client.log)"
	grep -E "sniff guard|sniff decoy|self-heal" "$WORK/client.log" |
		sed -E 's/^[-+0-9]+ [0-9-]+ [0-9:]+ //; s/\[[0-9]+ [0-9a-z.]+\] //' | uniq -c
	cp "$WORK/client.log" "$WORK/$1.log"
	echo "== xray: sniffed / route / failure"
	tail -n +"$((xstart + 1))" "$WORK/xray.log" | grep -E "sniffed domain|default route|failed to open" |
		sed -E 's/^[0-9/]+ ([0-9:.]+) \[[A-Za-z]+\] \[[0-9]+\] /\1 /; s/ > common\/retry.*//'
}

capture() { # name n: a UDP sink on 127.0.0.1:CAPTURE_PORT records what client NAME sends first
	local tag port url
	start capture python3 -I "$HERE/gen.py" capture "$WORK" "$CAPTURE_PORT" "${2:-2}"
	sleep 0.5
	client "$1" >/dev/null
	while read -r tag port url; do
		curl -s -o /dev/null --max-time 3 --socks5-hostname "127.0.0.1:$port" "$url"
	done <"$WORK/$1.ports"
	client_down
	stop capture
	cat "$WORK/capture.log"
}

check() {
	$GEN client "$WORK" "$1" >/dev/null || exit 1
	"$SINGBOX" check -c "$WORK/$1.json"
}

mkdir -p "$WORK"
case "${1:-}" in
gen) gen ;;
up) up ;;
down) down ;;
status) status ;;
list) $GEN list ;;
client) client "$2" ;;
client-down) client_down ;;
curl) curl_n "$2" "${3:-1}" "${4:-}" ;;
run) run "$2" "${3:-3}" ;;
check) check "$2" ;;
capture) capture "$2" "${3:-2}" ;;
*) sed -n '2,20p' "$0" ;;
esac
