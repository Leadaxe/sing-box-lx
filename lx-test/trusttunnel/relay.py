#!/usr/bin/env python3
"""Counting loopback relay for the TrustTunnel stand (SPEC 133 §6 p.5/p.6).

Forwards TCP and UDP from a listen port to a target port on 127.0.0.1 and logs
every forwarded chunk with a timestamp, so that "no packets while idle" can be
read off the log without tcpdump (BPF needs root on macOS).

SIGUSR1 freezes every flow that exists at that moment: their data is dropped in
both directions while the sockets stay open, which is what an expired NAT
mapping looks like to the client. New TCP connections and new UDP source ports
are forwarded normally. SIGUSR2 lifts the freeze.

    relay.py <listen port> <target port> [log file]
"""

import os
import select
import signal
import socket
import sys
import threading
import time

listen_port = int(sys.argv[1])
target_port = int(sys.argv[2])
log = open(sys.argv[3], "a", buffering=1) if len(sys.argv) > 3 else sys.stderr

frozen_tcp = set()
frozen_udp = set()
flows = {}  # tcp id -> (client, upstream); udp peer -> upstream socket
lock = threading.Lock()


def out(msg):
    log.write("%s %s\n" % (time.strftime("%H:%M:%S"), msg))


def freeze(*_):
    with lock:
        frozen_tcp.update(k for k in flows if isinstance(k, int))
        frozen_udp.update(k for k in flows if not isinstance(k, int))
    out("FREEZE tcp=%d udp=%d" % (len(frozen_tcp), len(frozen_udp)))


def unfreeze(*_):
    with lock:
        frozen_tcp.clear()
        frozen_udp.clear()
    out("UNFREEZE")


def pump(flow_id, src, dst, direction):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            with lock:
                dropped = flow_id in frozen_tcp
            if dropped:
                out("tcp#%d %s DROP %d" % (flow_id, direction, len(data)))
                continue
            out("tcp#%d %s %d" % (flow_id, direction, len(data)))
            dst.sendall(data)
    except OSError:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass


def serve_tcp():
    ln = socket.socket()
    ln.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    ln.bind(("127.0.0.1", listen_port))
    ln.listen(64)
    n = 0
    while True:
        client, addr = ln.accept()
        n += 1
        up = socket.create_connection(("127.0.0.1", target_port))
        with lock:
            flows[n] = (client, up)
        out("tcp#%d OPEN from %s:%d" % (n, addr[0], addr[1]))
        threading.Thread(target=pump, args=(n, client, up, ">"), daemon=True).start()
        threading.Thread(target=pump, args=(n, up, client, "<"), daemon=True).start()


def serve_udp():
    ln = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    ln.bind(("127.0.0.1", listen_port))
    socks = {ln: None}
    while True:
        ready, _, _ = select.select(list(socks), [], [])
        for s in ready:
            data, addr = s.recvfrom(65536)
            if s is ln:
                with lock:
                    up = flows.get(addr)
                    if up is None:
                        up = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                        up.connect(("127.0.0.1", target_port))
                        flows[addr] = up
                        socks[up] = addr
                        out("udp %s:%d OPEN" % addr)
                    dropped = addr in frozen_udp
                if dropped:
                    out("udp %s:%d > DROP %d" % (addr[0], addr[1], len(data)))
                    continue
                out("udp %s:%d > %d" % (addr[0], addr[1], len(data)))
                up.send(data)
            else:
                peer = socks[s]
                with lock:
                    dropped = peer in frozen_udp
                if dropped:
                    out("udp %s:%d < DROP %d" % (peer[0], peer[1], len(data)))
                    continue
                out("udp %s:%d < %d" % (peer[0], peer[1], len(data)))
                ln.sendto(data, peer)


signal.signal(signal.SIGUSR1, freeze)
signal.signal(signal.SIGUSR2, unfreeze)
out("relay 127.0.0.1:%d -> 127.0.0.1:%d pid %d" % (listen_port, target_port, os.getpid()))
threading.Thread(target=serve_udp, daemon=True).start()
serve_tcp()
