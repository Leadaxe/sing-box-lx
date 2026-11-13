#!/usr/bin/env python3
# FEATURE 019 / SPEC 060 §6: config generator for the lx.sniff_guard loopback stand.
#
#   gen.py server WORK          sing-box server: trojan+TLS, hysteria2, tuic on 127.0.0.1
#   gen.py xray WORK            Xray: sniffing vless relay + sniffing dokodemo-door middlebox
#                               + plain WireGuard server (no sniffing) for the AWG scenarios
#   gen.py client WORK NAME     client config WORK/NAME.json; prints "tag port url" per node
#   gen.py capture WORK PORT N  write the first N datagrams sent to 127.0.0.1:PORT
#                               into WORK/capture.hex (one hex line each)
#   gen.py list                 scenario names
#
# Every listener binds 127.0.0.1. The masquerade SNI is decoy.invalid, which never
# resolves: a sniffer that routes by it breaks the connection instead of reaching
# a real host.
#
# WireGuard/AmneziaWG: the plain WireGuard server is an Xray `wireguard` inbound on
# 127.0.0.1 without sniffing (a sing-box wireguard endpoint binds *:port, its bind
# ignores inet4_bind_address). A gVisor WireGuard server drops inner packets to a
# loopback destination as martian, so the HTTP target behind the tunnel is a second
# origin on the first non-loopback IPv4 of the host (SG_LAN_IP, set by stand.sh).
import json
import os
import socket
import sys

HTTP_PORT = 28580
TROJAN, HY2, TUIC = 28501, 28502, 28503
RELAY = 28510
WG = 28530
CAPTURE = 28540
HTTP_LAN_PORT = 28581
LAN_IP = os.environ.get("SG_LAN_IP", "")
WG_NET_CLIENT = "10.66.0.2/32"
DOKO = {TROJAN: 28521, HY2: 28522, TUIC: 28523}
MIXED_BASE = 28600
UUID = "8c1a1d1e-6f7b-4f6a-9d65-2a3b4c5d6e7f"
PASSWORD = "lx-sniffguard-stand"
DECOY = "decoy.invalid"


def wg_keys(work):
    """stand.sh gen writes `sing-box generate wg-keypair` output for both sides."""
    keys = {}
    for side in ("server", "client"):
        with open(os.path.join(work, "wg-%s.keys" % side)) as f:
            kv = dict(line.split(": ", 1) for line in f.read().splitlines() if ": " in line)
        keys[side] = (kv["PrivateKey"], kv["PublicKey"])
    return keys


def server(work):
    tls = {
        "enabled": True,
        "certificate_path": os.path.join(work, "cert.pem"),
        "key_path": os.path.join(work, "key.pem"),
    }
    return {
        "log": {"level": "info", "timestamp": True},
        "inbounds": [
            {"type": "trojan", "tag": "trojan-in", "listen": "127.0.0.1", "listen_port": TROJAN,
             "users": [{"password": PASSWORD}], "tls": tls},
            {"type": "hysteria2", "tag": "hy2-in", "listen": "127.0.0.1", "listen_port": HY2,
             "users": [{"password": PASSWORD}], "tls": dict(tls, alpn=["h3"])},
            {"type": "tuic", "tag": "tuic-in", "listen": "127.0.0.1", "listen_port": TUIC,
             "users": [{"uuid": UUID, "password": PASSWORD}], "tls": dict(tls, alpn=["h3"])},
        ],
        "outbounds": [{"type": "direct", "tag": "direct"}],
    }


def xray(work):
    sniffing = {"enabled": True, "destOverride": ["http", "tls", "quic"], "routeOnly": False}
    inbounds = [{
        "tag": "relay", "listen": "127.0.0.1", "port": RELAY, "protocol": "vless",
        "settings": {"clients": [{"id": UUID}], "decryption": "none"},
        "sniffing": sniffing,
    }]
    for real, front in DOKO.items():
        inbounds.append({
            "tag": "doko-%d" % front, "listen": "127.0.0.1", "port": front,
            "protocol": "dokodemo-door",
            "settings": {"address": "127.0.0.1", "port": real,
                         "network": "tcp" if real == TROJAN else "udp"},
            "sniffing": sniffing,
        })
    keys = wg_keys(work)
    inbounds.append({
        "tag": "wg", "listen": "127.0.0.1", "port": WG, "protocol": "wireguard",
        "settings": {"secretKey": keys["server"][0], "mtu": 1280,
                     "peers": [{"publicKey": keys["client"][1], "allowedIPs": [WG_NET_CLIENT]}]},
    })
    allow = ["127.0.0.0/8", "::1/128"] + ([LAN_IP + "/32"] if LAN_IP else [])
    return {
        "log": {"loglevel": "debug", "access": os.path.join(work, "xray-access.log")},
        "inbounds": inbounds,
        # freedom blocks private destinations by default; the stand's servers live on
        # loopback, the WireGuard scenarios' origin on SG_LAN_IP.
        "outbounds": [{"tag": "direct", "protocol": "freedom",
                       "settings": {"finalRules": [{"action": "allow", "ip": allow}]}}],
    }


def node(kind, path, sni=DECOY, server="127.0.0.1", tls_extra=None):
    """path: relay (detour through the sniffing vless relay), direct (through the
    dokodemo-door middlebox, no detour), plain (straight to the server, no sniffer)."""
    real = {"trojan": TROJAN, "hy2": HY2, "tuic": TUIC}[kind]
    port = DOKO[real] if path == "direct" else real
    tls = {"enabled": True, "server_name": sni, "insecure": True}
    if kind != "trojan":
        tls["alpn"] = ["h3"]
    tls.update(tls_extra or {})
    out = {"tag": "%s-%s%s" % (kind, path, "" if sni == DECOY else "-own"),
           "server": server, "server_port": port, "tls": tls}
    if kind == "trojan":
        out.update(type="trojan", password=PASSWORD)
    elif kind == "hy2":
        out.update(type="hysteria2", password=PASSWORD)
    else:
        out.update(type="tuic", uuid=UUID, password=PASSWORD)
    if path == "relay":
        out["detour"] = "relay"
    return out


def awg(path, mask="quic", port=WG):
    """WireGuard endpoint to the stand's plain WireGuard server. path: relay or plain;
    mask: quic (ip/id/ib sugar with jc junk as in HISTORY 060 §6.2), i1 (static i1
    from WORK/capture.hex, plus i2 when the capture holds two datagrams), none.
    Keys and the capture are filled in by client()."""
    ep = {"type": "wireguard", "tag": "awg-%s-%s" % (path, mask), "system": False, "mtu": 1280,
          "address": [WG_NET_CLIENT],
          "peers": [{"address": "127.0.0.1", "port": port, "allowed_ips": ["0.0.0.0/0"]}],
          "endpoint": True}
    if mask != "none":
        ep.update(jc=4, jmin=40, jmax=70)
    if mask == "quic":
        ep.update(id=DECOY, ip="quic", ib="chrome")
    elif mask == "i1":
        ep["capture"] = True
    if path == "relay":
        ep["detour"] = "relay"
    return ep


def capture_lines(work):
    path = os.path.join(work, "capture.hex")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        return [line.strip() for line in f if line.strip()][:2]


def tls_guard(method, **kw):
    g = {"tls": dict({"method": method}, **kw)}
    return g


S = {}
# A: through the sniffing vless relay
S["a-tls-noguard"] = dict(nodes=[node("trojan", "relay")])
S["a-tls-rf"] = dict(nodes=[node("trojan", "relay")], guard=tls_guard("record_fragment"))
S["a-tls-delay"] = dict(nodes=[node("trojan", "relay")], guard=tls_guard("delay", delay="300ms"))
S["a-tls-frag"] = dict(nodes=[node("trojan", "relay")], guard=tls_guard("fragment"))
S["a-tls-clean"] = dict(nodes=[node("trojan", "relay")],
                        guard=dict(tls_guard("record_fragment"), detour_type={"vless": "clean"}))
S["a-tls-control"] = dict(nodes=[node("trojan", "relay", sni="localhost", server="localhost"),
                                 node("trojan", "relay")],
                          guard=tls_guard("record_fragment"))
S["a-tls-control-clean"] = dict(S["a-tls-control"],
                                guard=dict(tls_guard("record_fragment"), detour_type={"vless": "clean"}))
S["a-tls-persist"] = dict(nodes=[node("trojan", "relay")], cache=True,
                          guard=dict(tls_guard("record_fragment", persist=True), detour_type={"vless": "clean"}))
S["a-tls-false"] = dict(nodes=[node("trojan", "relay", tls_extra={"record_fragment": False})],
                        guard=tls_guard("record_fragment"))
S["a-tls-false-override"] = dict(nodes=[node("trojan", "relay", tls_extra={"record_fragment": False})],
                                 guard=dict(tls_guard("record_fragment"), override=True))
S["a-quic-noguard"] = dict(nodes=[node("hy2", "relay"), node("tuic", "relay")])
S["a-quic-decoy"] = dict(nodes=[node("hy2", "relay"), node("tuic", "relay")],
                         guard={"quic": {"method": "decoy_first_byte"}})
S["a-quic-clean"] = dict(nodes=[node("hy2", "relay"), node("tuic", "relay")],
                         guard={"quic": {"method": "decoy_first_byte"}, "detour_type": {"vless": "clean"}})
S["a-quic-delay"] = dict(nodes=[node("hy2", "relay"), node("tuic", "relay")],
                         guard={"quic": {"method": "delay"}})
# B: direct path through the dokodemo-door middlebox
S["b-tls-noguard"] = dict(nodes=[node("trojan", "direct")])
S["b-tls-rf"] = dict(nodes=[node("trojan", "direct")], guard=tls_guard("record_fragment"))
S["b-tls-rf-suspect"] = dict(nodes=[node("trojan", "direct")],
                             guard=dict(tls_guard("record_fragment"), detour_type={"direct": "suspect"}))
S["b-tls-delay-suspect"] = dict(nodes=[node("trojan", "direct")],
                                guard=dict(tls_guard("delay", delay="300ms"), detour_type={"direct": "suspect"}))
S["b-tls-frag-suspect"] = dict(nodes=[node("trojan", "direct")],
                               guard=dict(tls_guard("fragment"), detour_type={"direct": "suspect"}))
S["b-quic-noguard"] = dict(nodes=[node("hy2", "direct"), node("tuic", "direct")])
S["b-quic-decoy-suspect"] = dict(nodes=[node("hy2", "direct"), node("tuic", "direct")],
                                 guard={"quic": {"method": "decoy_first_byte", "detour_type": {"direct": "suspect"}}})
# P2: no sniffer at all, every method forced by suspect
PLAIN = [node("trojan", "plain"), node("hy2", "plain"), node("tuic", "plain")]
S["p2-rf-decoy"] = dict(nodes=PLAIN, guard={"detour_type": {"direct": "suspect"},
                                            "tls": {"method": "record_fragment"},
                                            "quic": {"method": "decoy_first_byte"}})
S["p2-frag-qdelay"] = dict(nodes=PLAIN, guard={"detour_type": {"direct": "suspect"},
                                               "tls": {"method": "fragment"},
                                               "quic": {"method": "delay"}})
S["p2-tdelay"] = dict(nodes=PLAIN[:1], guard={"detour_type": {"direct": "suspect"},
                                              "tls": {"method": "delay"}})
# broken configs: sing-box check must fail naming the key
# AmneziaWG (HISTORY 060 §6.2): AWG client -> plain WireGuard server, ip=quic id=decoy.invalid
S["awg-plain-none"] = dict(nodes=[awg("plain", "none")])        # W1 control
S["awg-plain-quic"] = dict(nodes=[awg("plain", "quic")])        # masquerade, no sniffer
S["awg-relay-none"] = dict(nodes=[awg("relay", "none")])        # relay, no masquerade
S["a-awg-noguard"] = dict(nodes=[awg("relay", "quic")])         # W2
S["a-awg-decoy"] = dict(nodes=[awg("relay", "quic")],           # W3
                        guard={"quic": {"method": "decoy_first_byte"}})
S["a-awg-clean"] = dict(nodes=[awg("relay", "quic")],           # W4
                        guard={"quic": {"method": "decoy_first_byte", "detour_type": {"vless": "clean"}}})
S["a-awg-hy2-clean"] = dict(nodes=[awg("relay", "quic"), node("hy2", "relay")], cache=True,  # W5
                            guard={"quic": {"method": "decoy_first_byte", "persist": True,
                                            "detour_type": {"vless": "clean"}}})
S["a-awg-delay"] = dict(nodes=[awg("relay", "quic")],           # W7
                        guard={"quic": {"method": "delay"}})
S["cap-hy2"] = dict(nodes=[dict(node("hy2", "plain"), server_port=CAPTURE)])  # W6 source
S["cap-awg"] = dict(nodes=[awg("plain", "quic", port=CAPTURE)])
S["awg-plain-i1"] = dict(nodes=[awg("plain", "i1")])            # W6 control, no sniffer
S["a-awg-i1-noguard"] = dict(nodes=[awg("relay", "i1")])
S["a-awg-i1-decoy"] = dict(nodes=[awg("relay", "i1")],          # W6
                           guard={"quic": {"method": "decoy_first_byte"}})
S["bad-quic-sniff-decoy"] = dict(nodes=[node("hy2", "relay")], lx_extra={"quic_sniff_decoy": True})
S["bad-outbound-type-empty"] = dict(nodes=[node("trojan", "relay")],
                                    guard=dict(tls_guard("record_fragment"), nodes={"outbound_type": []}))
S["bad-persist-no-cache"] = dict(nodes=[node("trojan", "relay")],
                                 guard=tls_guard("record_fragment", persist=True))


def client(work, name):
    sc = S[name]
    inbounds, rules, lines = [], [], []
    for i, n in enumerate(sc["nodes"]):
        port = MIXED_BASE + i
        inbounds.append({"type": "mixed", "tag": "in-" + n["tag"], "listen": "127.0.0.1", "listen_port": port})
        rules.append({"inbound": ["in-" + n["tag"]], "outbound": n["tag"]})
        url = ("http://%s:%d/" % (LAN_IP, HTTP_LAN_PORT) if n.get("endpoint")
               else "http://127.0.0.1:%d/" % HTTP_PORT)
        lines.append("%s %d %s" % (n["tag"], port, url))
    endpoints = []
    for n in sc["nodes"]:
        if not n.get("endpoint"):
            continue
        keys = wg_keys(work)
        ep = {k: v for k, v in n.items() if k not in ("endpoint", "capture")}
        ep["private_key"] = keys["client"][0]
        ep["peers"] = [dict(ep["peers"][0], public_key=keys["server"][1])]
        if n.get("capture"):
            lines_hex = capture_lines(work)
            if not lines_hex:
                sys.exit("no %s: run stand.sh capture first" % os.path.join(work, "capture.hex"))
            for i, h in enumerate(lines_hex):
                ep["i%d" % (i + 1)] = "<b 0x%s>" % h
        endpoints.append(ep)
    outbounds = [n for n in sc["nodes"] if not n.get("endpoint")]
    if any(n.get("detour") == "relay" for n in sc["nodes"]):
        outbounds.append({"type": "vless", "tag": "relay", "server": "127.0.0.1",
                          "server_port": RELAY, "uuid": UUID})
    outbounds.append({"type": "direct", "tag": "direct"})
    cfg = {
        # trace: the AmneziaWG decoy line ("sniff decoy datagram sent") is trace-level
        "log": {"level": "trace" if endpoints else "debug", "timestamp": True},
        "inbounds": inbounds,
        "outbounds": outbounds,
        "route": {"rules": rules, "final": "direct"},
    }
    if endpoints:
        cfg["endpoints"] = endpoints
    lx = {}
    if sc.get("guard"):
        lx["sniff_guard"] = sc["guard"]
    lx.update(sc.get("lx_extra", {}))
    if lx:
        cfg["lx"] = lx
    if sc.get("cache"):
        cfg["experimental"] = {"cache_file": {"enabled": True, "path": os.path.join(work, name + ".db")}}
    with open(os.path.join(work, name + ".json"), "w") as f:
        json.dump(cfg, f, indent=2)
    print("\n".join(lines))


def capture(work, port, count):
    """A UDP sink on 127.0.0.1:port: the first datagrams a QUIC client (or the
    AmneziaWG burst) sends, hex-dumped. Exits after count datagrams or 20 s."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", port))
    sock.settimeout(20)
    out = []
    try:
        while len(out) < count:
            data, _ = sock.recvfrom(65535)
            out.append(data.hex())
            print("datagram %d: %d bytes, first byte 0x%02x" % (len(out), len(data), data[0]), flush=True)
            with open(os.path.join(work, "capture.hex"), "w") as f:  # stand.sh may kill us any time
                f.write("\n".join(out) + "\n")
    except socket.timeout:
        pass


def main():
    cmd = sys.argv[1]
    if cmd == "list":
        print("\n".join(S))
        return
    work = sys.argv[2]
    if cmd == "client":
        client(work, sys.argv[3])
        return
    if cmd == "capture":
        capture(work, int(sys.argv[3]), int(sys.argv[4]))
        return
    cfg = {"server": server, "xray": xray}[cmd](work)
    with open(os.path.join(work, cmd + ".json"), "w") as f:
        json.dump(cfg, f, indent=2)


main()
