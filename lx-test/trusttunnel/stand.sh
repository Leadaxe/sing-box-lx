#!/usr/bin/env bash
# FEATURE 018 / SPEC 133 §6, SPEC 134 §6: loopback stand with the official
# TrustTunnel endpoint and the official CLI client, both macOS/Linux release
# binaries, GPG-verified. Everything listens on 127.0.0.1 only; the client runs
# in SOCKS5 mode (no TUN, no routes, no kill switch, no system DNS changes).
#
#   lx-test/trusttunnel/stand.sh install     # download + verify binaries into bin/
#   lx-test/trusttunnel/stand.sh gen         # self-signed cert + endpoint/client configs
#   lx-test/trusttunnel/stand.sh up          # start the endpoint (background)
#   lx-test/trusttunnel/stand.sh client-up   # start the official client as SOCKS5 proxy
#   lx-test/trusttunnel/stand.sh check       # curl through the official client
#   lx-test/trusttunnel/stand.sh status|logs|down
#
# Our sing-box (built with LX_TAGS, which carry with_trusttunnel, into bin/sing-box):
#   lx-test/trusttunnel/stand.sh build       # LX_TAGS -> bin/sing-box
#   lx-test/trusttunnel/stand.sh sb-out      # our trusttunnel outbound, mixed on TT_SB_OUT_PORT
#   lx-test/trusttunnel/stand.sh check-out   # curl through it: private origin + example.com
#   lx-test/trusttunnel/stand.sh sb-in       # our trusttunnel inbound + official client (client-sb.toml)
#   lx-test/trusttunnel/stand.sh check-in    # curl through the official client into our inbound
#   lx-test/trusttunnel/stand.sh sb-down     # stop sb-out, sb-in and the client-sb client
#
# Ports (override with env): TT_PORT endpoint (TCP h2 + UDP h3), TT_SOCKS_PORT
# official client SOCKS5, TT_HTTP_PORT a loopback HTTP origin for private-net
# checks (the endpoint runs with allow_private_network_connections = true),
# TT_SB_OUT_PORT our mixed inbound in front of the outbound, TT_SB_IN_PORT our
# trusttunnel inbound (TCP h2 + UDP h3), TT_SB_SOCKS_PORT the official client
# connected to our inbound. TT_OUT_TARGET=endpoint|inbound picks the server of
# our outbound, TT_OUT_QUIC=1 switches it to HTTP/3, TT_OUT_PASS and
# TT_CLIENT_PASS override the password (negative checks).
set -u

HERE=$(cd "$(dirname "$0")" && pwd)
BIN=$HERE/bin
WORK=$HERE/.stand
TT_PORT=${TT_PORT:-24441}
TT_SOCKS_PORT=${TT_SOCKS_PORT:-24442}
TT_HTTP_PORT=${TT_HTTP_PORT:-24443}
TT_SB_OUT_PORT=${TT_SB_OUT_PORT:-24444}
TT_SB_IN_PORT=${TT_SB_IN_PORT:-24445}
TT_SB_SOCKS_PORT=${TT_SB_SOCKS_PORT:-24446}
TT_OUT_TARGET=${TT_OUT_TARGET:-endpoint}
TT_OUT_QUIC=${TT_OUT_QUIC:-0}
TT_SNI=${TT_SNI:-tt.test}
TT_USER=${TT_USER:-lx}
TT_PASS=${TT_PASS:-lx-stand-secret}
ENDPOINT_VER=${ENDPOINT_VER:-v1.1.0}
CLIENT_VER=${CLIENT_VER:-v1.1.11}
SIGNING_KEY=28645AC9776EC4C00BCE2AFC0FE641E7235E2EC6

case "$(uname -s)-$(uname -m)" in
Darwin-*) EP_ASSET="trusttunnel-$ENDPOINT_VER-macos-universal" CL_ASSET="trusttunnel_client-$CLIENT_VER-macos-universal" ;;
Linux-x86_64) EP_ASSET="trusttunnel-$ENDPOINT_VER-linux-x86_64" CL_ASSET="trusttunnel_client-$CLIENT_VER-linux-x86_64" ;;
Linux-aarch64) EP_ASSET="trusttunnel-$ENDPOINT_VER-linux-aarch64" CL_ASSET="trusttunnel_client-$CLIENT_VER-linux-aarch64" ;;
*) echo "unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac
ENDPOINT=$BIN/$EP_ASSET/trusttunnel_endpoint
CLIENT=$BIN/$CL_ASSET/trusttunnel_client
SINGBOX=$BIN/sing-box
ROOT=$(cd "$HERE/../.." && pwd)

install() {
	mkdir -p "$BIN" && cd "$BIN" || exit 1
	gh release download "$ENDPOINT_VER" --repo TrustTunnel/TrustTunnel -p "$EP_ASSET.tar.gz" --clobber || exit 1
	gh release download "$CLIENT_VER" --repo TrustTunnel/TrustTunnelClient -p "$CL_ASSET.tar.gz" --clobber || exit 1
	tar xzf "$EP_ASSET.tar.gz" && tar xzf "$CL_ASSET.tar.gz" || exit 1
	chmod +x "$ENDPOINT" "$CLIENT"
	if ! gpg --batch --list-keys "$SIGNING_KEY" >/dev/null 2>&1; then
		curl -fsS --max-time 30 "https://keys.openpgp.org/vks/v1/by-fingerprint/$SIGNING_KEY" | gpg --batch --import || exit 1
	fi
	for f in "$ENDPOINT" "$CLIENT"; do
		LC_ALL=C gpg --batch --verify "$f.sig" "$f" 2>&1 | grep -q "Good signature from \"AdGuard" || { echo "signature check failed: $f" >&2; exit 1; }
		echo "verified: $f"
	done
	"$ENDPOINT" --version && "$CLIENT" --version
}

gen() {
	mkdir -p "$WORK" && cd "$WORK" || exit 1
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
		-subj "/CN=$TT_SNI" -addext "subjectAltName=DNS:$TT_SNI" \
		-keyout key.pem -out cert.pem >/dev/null 2>&1 || exit 1
	cat >vpn.toml <<TOML
listen_address = "127.0.0.1:$TT_PORT"
ipv6_available = false
allow_private_network_connections = true
tls_handshake_timeout_secs = 10
client_listener_timeout_secs = 600
connection_establishment_timeout_secs = 30
tcp_connections_timeout_secs = 604800
udp_connections_timeout_secs = 300
credentials_file = "$WORK/credentials.toml"

[listen_protocols]

[listen_protocols.http2]
max_concurrent_streams = 1000

[listen_protocols.quic]
initial_max_streams_bidi = 4096
disable_active_migration = true
enable_early_data = true

[forward_protocol]
direct = {}
TOML
	cat >hosts.toml <<TOML
[[main_hosts]]
hostname = "$TT_SNI"
cert_chain_path = "$WORK/cert.pem"
private_key_path = "$WORK/key.pem"
TOML
	cat >credentials.toml <<TOML
[[client]]
username = "$TT_USER"
password = "$TT_PASS"
TOML
	cat >client.toml <<TOML
loglevel = "debug"
vpn_mode = "general"
killswitch_enabled = false
exclusions = []

[endpoint]
hostname = "$TT_SNI"
addresses = ["127.0.0.1:$TT_PORT"]
has_ipv6 = false
username = "$TT_USER"
password = "$TT_PASS"
skip_verification = false
certificate = '''
$(cat cert.pem)
'''
upstream_protocol = "${TT_CLIENT_PROTO:-http2}"
tls_profile = "chrome"
anti_dpi = false

[listener.socks]
address = "127.0.0.1:$TT_SOCKS_PORT"
TOML
	mkdir -p www && echo "trusttunnel-stand-ok" >www/index.html
	echo "generated in $WORK (sni $TT_SNI, endpoint 127.0.0.1:$TT_PORT, socks 127.0.0.1:$TT_SOCKS_PORT)"
}

up() {
	cd "$WORK" || exit 1
	nice -n 10 "$ENDPOINT" vpn.toml hosts.toml -l debug --logfile endpoint.log >endpoint.out 2>&1 &
	echo $! >endpoint.pid
	nice -n 10 python3 -m http.server "$TT_HTTP_PORT" --bind 127.0.0.1 --directory www >http.log 2>&1 &
	echo $! >http.pid
	for _ in $(seq 50); do
		lsof -nP -iTCP@127.0.0.1:"$TT_PORT" -sTCP:LISTEN >/dev/null 2>&1 && break
		sleep 0.2
	done
	status
}

client_up() {
	cd "$WORK" || exit 1
	nice -n 10 "$CLIENT" -c client.toml -l debug >client.log 2>&1 &
	echo $! >client.pid
	for _ in $(seq 50); do
		lsof -nP -iTCP@127.0.0.1:"$TT_SOCKS_PORT" -sTCP:LISTEN >/dev/null 2>&1 && break
		sleep 0.2
	done
	status
}

check() {
	echo "-- private origin through the official client:"
	curl -sS --max-time 15 --socks5-hostname "127.0.0.1:$TT_SOCKS_PORT" "http://127.0.0.1:$TT_HTTP_PORT/" || echo "FAIL"
	echo "-- internet through the official client:"
	curl -sS --max-time 20 -o /dev/null -w '%{http_code} %{remote_ip}\n' --socks5-hostname "127.0.0.1:$TT_SOCKS_PORT" https://example.com/ || echo "FAIL"
}

# start <name> <port> <proto tcp|udp> <command...>: background process under
# nice 10, pid in .stand/<name>.pid, output in .stand/<name>.log.
start() {
	local name=$1 port=$2
	shift 2
	cd "$WORK" || exit 1
	nice -n 10 "$@" >"$name.log" 2>&1 &
	echo $! >"$name.pid"
	for _ in $(seq 50); do
		lsof -nP -iTCP@127.0.0.1:"$port" -sTCP:LISTEN >/dev/null 2>&1 && return 0
		kill -0 "$(cat "$name.pid")" 2>/dev/null || break
		sleep 0.2
	done
	echo "$name did not come up, see $WORK/$name.log" >&2
	tail -n 20 "$name.log" >&2
	return 1
}

stop() {
	for p in "$@"; do
		[ -f "$WORK/$p.pid" ] && { kill "$(cat "$WORK/$p.pid")" 2>/dev/null; rm -f "$WORK/$p.pid"; }
	done
}

build() {
	cd "$ROOT" || exit 1
	mkdir -p "$BIN" "$WORK"
	local tags
	tags="$(make -s -f Makefile.lx lx-print-tags)"
	case ",$tags," in
	*,with_trusttunnel,*) ;;
	*) tags="$tags,with_trusttunnel" ;;
	esac
	echo "tags: $tags"
	GOTOOLCHAIN=${GOTOOLCHAIN:-$(tr -d '[:space:]' <go.version)} GOFLAGS=-p=4 \
		nice -n 15 make -f Makefile.lx lx-build LX_TAGS="$tags" LX_OUTPUT="$SINGBOX" >"$WORK/build.log" 2>&1 ||
		{ tail -n 30 "$WORK/build.log" >&2; exit 1; }
	"$SINGBOX" version
}

sb_out_config() {
	local port=$TT_PORT quic=false
	[ "$TT_OUT_TARGET" = inbound ] && port=$TT_SB_IN_PORT
	[ "$TT_OUT_QUIC" = 1 ] && quic=true
	cat >"$WORK/sb-out.json" <<JSON
{
  "log": { "level": "debug", "timestamp": true },
  "dns": {
    "servers": [ { "type": "udp", "tag": "dns-tt", "server": "1.1.1.1", "detour": "tt-out" } ],
    "final": "dns-tt"
  },
  "inbounds": [ { "type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": $TT_SB_OUT_PORT } ],
  "outbounds": [
    {
      "type": "trusttunnel",
      "tag": "tt-out",
      "server": "127.0.0.1",
      "server_port": $port,
      "username": "$TT_USER",
      "password": "${TT_OUT_PASS:-$TT_PASS}",
      "quic": $quic,
      "tls": { "enabled": true, "server_name": "$TT_SNI", "certificate_path": "$WORK/cert.pem" }
    },
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "rules": [ { "action": "resolve", "strategy": "ipv4_only" } ],
    "final": "tt-out"
  }
}
JSON
}

sb_out() {
	[ -x "$SINGBOX" ] || { echo "run build first" >&2; exit 1; }
	stop sb-out
	sb_out_config
	"$SINGBOX" check -c "$WORK/sb-out.json" || exit 1
	start sb-out "$TT_SB_OUT_PORT" "$SINGBOX" run -c "$WORK/sb-out.json" || exit 1
	echo "sb-out: mixed 127.0.0.1:$TT_SB_OUT_PORT -> trusttunnel $TT_OUT_TARGET (quic=$TT_OUT_QUIC)"
}

sb_in_config() {
	cat >"$WORK/sb-in.json" <<JSON
{
  "log": { "level": "debug", "timestamp": true },
  "inbounds": [
    {
      "type": "trusttunnel",
      "tag": "tt-in",
      "listen": "127.0.0.1",
      "listen_port": $TT_SB_IN_PORT,
      "users": [ { "username": "$TT_USER", "password": "$TT_PASS" } ],
      "tls": {
        "enabled": true,
        "server_name": "$TT_SNI",
        "certificate_path": "$WORK/cert.pem",
        "key_path": "$WORK/key.pem"
      }
    }
  ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ]
}
JSON
	sed -e "s|^addresses = .*|addresses = [\"127.0.0.1:$TT_SB_IN_PORT\"]|" \
		-e "s|^password = .*|password = \"${TT_CLIENT_PASS:-$TT_PASS}\"|" \
		-e "s|^upstream_protocol = .*|upstream_protocol = \"${TT_CLIENT_PROTO:-http2}\"|" \
		-e "s|^address = \"127.0.0.1:$TT_SOCKS_PORT\"|address = \"127.0.0.1:$TT_SB_SOCKS_PORT\"|" \
		"$WORK/client.toml" >"$WORK/client-sb.toml"
}

sb_in() {
	[ -x "$SINGBOX" ] || { echo "run build first" >&2; exit 1; }
	stop client-sb sb-in
	sb_in_config
	"$SINGBOX" check -c "$WORK/sb-in.json" || exit 1
	start sb-in "$TT_SB_IN_PORT" "$SINGBOX" run -c "$WORK/sb-in.json" || exit 1
	start client-sb "$TT_SB_SOCKS_PORT" "$CLIENT" -c "$WORK/client-sb.toml" -l debug || exit 1
	echo "sb-in: trusttunnel 127.0.0.1:$TT_SB_IN_PORT; official client (${TT_CLIENT_PROTO:-http2}) socks 127.0.0.1:$TT_SB_SOCKS_PORT"
}

# curl_pair <socks port> <origin host> <label>
curl_pair() {
	local port=$1 origin=$2 what=$3
	echo "-- private origin ($origin) through $what:"
	curl -sS --max-time 15 --socks5-hostname "127.0.0.1:$port" "http://$origin:$TT_HTTP_PORT/" || echo "FAIL"
	echo "-- internet through $what:"
	curl -sS --max-time 20 -o /dev/null -w '%{http_code} %{remote_ip}\n' --socks5-hostname "127.0.0.1:$port" https://example.com/ || echo "FAIL"
}

check_out() { curl_pair "$TT_SB_OUT_PORT" 127.0.0.1 "our outbound"; }

# The official client connects loopback destinations directly, bypassing the
# tunnel; a domain goes into the CONNECT and is resolved by our inbound.
check_in() { curl_pair "$TT_SB_SOCKS_PORT" localhost "the official client into our inbound"; }

sb_down() {
	stop client-sb sb-in sb-out
	status
}

status() {
	for p in endpoint http client sb-out sb-in client-sb; do
		if [ -f "$WORK/$p.pid" ] && kill -0 "$(cat "$WORK/$p.pid")" 2>/dev/null; then echo "$p: running pid $(cat "$WORK/$p.pid")"; else echo "$p: stopped"; fi
	done
	lsof -nP -i@127.0.0.1:"$TT_PORT" 2>/dev/null | tail -n +2
}

logs() { tail -n "${1:-40}" "$WORK/endpoint.log" "$WORK/client.log" "$WORK/sb-out.log" "$WORK/sb-in.log" "$WORK/client-sb.log" 2>/dev/null; }

down() {
	for p in client http endpoint; do
		[ -f "$WORK/$p.pid" ] && { kill "$(cat "$WORK/$p.pid")" 2>/dev/null; rm -f "$WORK/$p.pid"; }
	done
	status
}

case "${1:-}" in
install) install ;;
gen) gen ;;
up) up ;;
client-up) client_up ;;
check) check ;;
status) status ;;
logs) logs "${2:-40}" ;;
down) down ;;
build) build ;;
sb-out) sb_out ;;
sb-in) sb_in ;;
check-out) check_out ;;
check-in) check_in ;;
sb-down) sb_down ;;
*) sed -n '2,33p' "$0"; exit 1 ;;
esac
