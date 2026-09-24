#!/usr/bin/env bash
# SPEC 104 §11 item 7: XHTTP over HTTP/3 against a live Xray, one run per mode.
#
# Xray: VLESS inbound, xhttp, security tls, alpn ["h3"] (QUIC only), self-signed
# certificate, UDP on 127.0.0.1. Client: the issue #25 shape — uTLS chrome,
# certificate_public_key_sha256, alpn ["h3"]. Each run curls a loopback HTTP
# server through a mixed inbound and checks the core log for "HTTP version 3".
#
#   XRAY=/path/to/xray SING_BOX=/path/to/sing-box lx-test/xhttp_h3/run.sh
#
# Everything listens on 127.0.0.1 only. A launcher tun that captures UDP can
# still eat the QUIC flow; the Xray log line "accepted" per run shows it arrived.
set -u

XRAY=${XRAY:?set XRAY to an Xray-core binary}
SING_BOX=${SING_BOX:?set SING_BOX to a sing-box-lx binary}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/xhttp_h3.XXXXXX")
XRAY_PORT=${XRAY_PORT:-24431}
MIXED_PORT=${MIXED_PORT:-24432}
HTTP_PORT=${HTTP_PORT:-24433}
UUID=b831381d-6324-4d53-ad4f-8cda48b30811
PIDS=()
cleanup() {
	for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null; done
	wait 2>/dev/null
}
trap cleanup EXIT

cd "$WORK" || exit 1
"$SING_BOX" generate tls-keypair localhost >keypair.pem || exit 1
awk '/BEGIN PRIVATE KEY/,/END PRIVATE KEY/' keypair.pem >key.pem
awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/' keypair.pem >cert.pem
PIN=$(openssl x509 -in cert.pem -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64)

cat >xray.json <<JSON
{
  "log": {"loglevel": "debug"},
  "inbounds": [{
    "listen": "127.0.0.1", "port": $XRAY_PORT, "protocol": "vless",
    "settings": {"clients": [{"id": "$UUID"}], "decryption": "none"},
    "streamSettings": {
      "network": "xhttp",
      "xhttpSettings": {"path": "/xh", "mode": "auto"},
      "security": "tls",
      "tlsSettings": {"alpn": ["h3"], "certificates": [{"certificateFile": "$WORK/cert.pem", "keyFile": "$WORK/key.pem"}]}
    }
  }],
  "outbounds": [{"protocol": "freedom", "settings": {"finalRules": [{"action": "allow", "ip": ["127.0.0.1/32"]}]}}]
}
JSON

mkdir -p www && echo "xhttp-h3-ok" >www/index.html
python3 -m http.server "$HTTP_PORT" --bind 127.0.0.1 --directory www >http.log 2>&1 &
PIDS+=($!)
"$XRAY" run -c xray.json >xray.log 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do
	lsof -nP -iUDP@127.0.0.1:"$XRAY_PORT" >/dev/null 2>&1 && break
	sleep 0.2
done
if ! lsof -nP -iUDP@127.0.0.1:"$XRAY_PORT" >/dev/null 2>&1; then
	echo "xray does not listen on UDP 127.0.0.1:$XRAY_PORT"
	cat xray.log
	exit 1
fi

status=0
for mode in packet-up stream-up stream-one; do
	cat >"sb-$mode.json" <<JSON
{
  "log": {"level": "debug"},
  "inbounds": [{"type": "mixed", "listen": "127.0.0.1", "listen_port": $MIXED_PORT}],
  "outbounds": [{
    "type": "vless", "tag": "xhttp-h3", "server": "127.0.0.1", "server_port": $XRAY_PORT, "uuid": "$UUID",
    "tls": {
      "enabled": true, "server_name": "localhost", "alpn": ["h3"],
      "utls": {"enabled": true, "fingerprint": "chrome"},
      "certificate_public_key_sha256": ["$PIN"]
    },
    "transport": {"type": "xhttp", "mode": "$mode", "path": "/xh"}
  }]
}
JSON
	xray_before=$(wc -l <xray.log)
	"$SING_BOX" run -c "sb-$mode.json" >"sb-$mode.log" 2>&1 &
	sb=$!
	for _ in $(seq 50); do
		lsof -nP -iTCP@127.0.0.1:"$MIXED_PORT" -sTCP:LISTEN >/dev/null 2>&1 && break
		sleep 0.2
	done
	body=$(curl -sS --max-time 10 -x "socks5h://127.0.0.1:$MIXED_PORT" "http://127.0.0.1:$HTTP_PORT/" 2>&1)
	kill "$sb" 2>/dev/null
	wait "$sb" 2>/dev/null
	version=$(grep -o 'HTTP version [0-9.]*' "sb-$mode.log" | head -1)
	accepted=$(tail -n +"$((xray_before + 1))" xray.log | grep -c "accepted")
	if [ "$body" = "xhttp-h3-ok" ] && [ "$version" = "HTTP version 3" ] && [ "$accepted" -gt 0 ]; then
		echo "$mode: OK ($version, xray accepted $accepted)"
	else
		echo "$mode: FAIL body=[$body] version=[$version] xray accepted=$accepted"
		grep -iE "warn|error" "sb-$mode.log" | head -5
		status=1
	fi
done
echo "logs: $WORK"
grep -iE "quic|h3" xray.log | head -3
exit $status
