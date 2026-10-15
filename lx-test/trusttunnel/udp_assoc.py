#!/usr/bin/env python3
"""SOCKS5 UDP ASSOCIATE probe for the TrustTunnel stand (SPEC 133 §6 p.3,
"large UDP frame"): sends one datagram of the given size to host:port through
the SOCKS5 proxy and prints what comes back.

    udp_assoc.py <socks host:port> <dest host:port> <payload size> [timeout s]

Exit 0 when the reply equals the payload (an echo server at the destination),
1 otherwise. `--udp-echo <port>` runs the echo server instead.
"""

import os
import socket
import struct
import sys


def echo(port):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("127.0.0.1", port))
    print("udp echo on 127.0.0.1:%d" % port, flush=True)
    while True:
        data, addr = s.recvfrom(65535)
        s.sendto(data, addr)


def probe(socks, dest, size, timeout):
    socks_host, socks_port = socks.rsplit(":", 1)
    dest_host, dest_port = dest.rsplit(":", 1)
    tcp = socket.create_connection((socks_host, int(socks_port)), timeout=timeout)
    tcp.sendall(b"\x05\x01\x00")
    if tcp.recv(2) != b"\x05\x00":
        raise SystemExit("socks5: no-auth method refused")
    tcp.sendall(b"\x05\x03\x00\x01" + socket.inet_aton("0.0.0.0") + struct.pack(">H", 0))
    reply = tcp.recv(10)
    if len(reply) < 10 or reply[1] != 0 or reply[3] != 1:
        raise SystemExit("socks5: UDP ASSOCIATE refused: %r" % reply)
    relay = (socket.inet_ntoa(reply[4:8]), struct.unpack(">H", reply[8:10])[0])
    if relay[0] == "0.0.0.0":
        relay = (socks_host, relay[1])
    payload = os.urandom(size)
    try:
        header = b"\x00\x00\x00\x01" + socket.inet_aton(dest_host) + struct.pack(">H", int(dest_port))
    except OSError:
        name = dest_host.encode()
        header = b"\x00\x00\x00\x03" + bytes([len(name)]) + name + struct.pack(">H", int(dest_port))
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.settimeout(timeout)
    udp.sendto(header + payload, relay)
    try:
        data, _ = udp.recvfrom(65535)
    except socket.timeout:
        print("sent %d bytes via %s:%d, no reply in %ss" % (size, relay[0], relay[1], timeout))
        return 1
    if data[3] == 1:
        body = data[10:]
    elif data[3] == 3:
        body = data[7 + data[4]:]
    else:
        body = data[22:]
    ok = body == payload
    print("sent %d bytes via %s:%d, reply %d bytes, %s" % (size, relay[0], relay[1], len(body), "match" if ok else "MISMATCH"))
    return 0 if ok else 1


if __name__ == "__main__":
    if sys.argv[1] == "--udp-echo":
        echo(int(sys.argv[2]))
    sys.exit(probe(sys.argv[1], sys.argv[2], int(sys.argv[3]), float(sys.argv[4]) if len(sys.argv) > 4 else 5.0))
