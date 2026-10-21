#!/usr/bin/env python3
"""Small network probe for the olcWRT test bench (stdlib only).

  netcheck.py get URL [--socks host:port]   HTTP(S) GET, prints status, time, body head
  netcheck.py dns NAME [server]             resolve NAME (system resolver or UDP server)
"""
import socket
import ssl
import struct
import sys
import time
import random
from urllib.parse import urlparse


def socks_connect(proxy, host, port, timeout=90):
    ph, pp = proxy.rsplit(":", 1)
    s = socket.create_connection((ph, int(pp)), timeout=timeout)
    s.sendall(b"\x05\x01\x00")
    if s.recv(2) != b"\x05\x00":
        raise RuntimeError("socks auth refused")
    hb = host.encode()
    s.sendall(b"\x05\x01\x00\x03" + bytes([len(hb)]) + hb + struct.pack(">H", port))
    rep = s.recv(10)
    if len(rep) < 2 or rep[1] != 0:
        raise RuntimeError("socks connect failed: %r" % rep)
    return s


def get(url, socks=None):
    u = urlparse(url)
    port = u.port or (443 if u.scheme == "https" else 80)
    start = time.time()
    if socks:
        s = socks_connect(socks, u.hostname, port)
    else:
        s = socket.create_connection((u.hostname, port), timeout=60)
    if u.scheme == "https":
        s = ssl.create_default_context().wrap_socket(s, server_hostname=u.hostname)
    path = u.path or "/"
    if u.query:
        path += "?" + u.query
    s.sendall(("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: netcheck\r\nConnection: close\r\n\r\n" % (path, u.hostname)).encode())
    data = b""
    while True:
        chunk = s.recv(65536)
        if not chunk:
            break
        data += chunk
    head, _, body = data.partition(b"\r\n\r\n")
    status = head.split(b"\r\n", 1)[0].decode(errors="replace")
    print("%s | %.0f ms | %d bytes | %s" % (status, (time.time() - start) * 1000, len(body), body[:80].decode(errors="replace").strip()))


def dns(name, server=None):
    if not server:
        start = time.time()
        addrs = sorted({ai[4][0] for ai in socket.getaddrinfo(name, 443, proto=socket.IPPROTO_TCP)})
        print("%s -> %s (%.0f ms)" % (name, ", ".join(addrs), (time.time() - start) * 1000))
        return
    qid = random.randint(0, 65535)
    q = struct.pack(">HHHHHH", qid, 0x0100, 1, 0, 0, 0)
    for label in name.split("."):
        q += bytes([len(label)]) + label.encode()
    q += b"\x00" + struct.pack(">HH", 1, 1)
    host, _, port = server.partition(":")
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(10)
    start = time.time()
    s.sendto(q, (host, int(port or 53)))
    resp = s.recv(4096)
    print("%s @%s: rcode=%d answers=%d (%.0f ms)" % (name, server, resp[3] & 0x0F,
          struct.unpack(">H", resp[6:8])[0], (time.time() - start) * 1000))


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(2)
    cmd, arg = sys.argv[1], sys.argv[2]
    try:
        if cmd == "get":
            socks = sys.argv[4] if len(sys.argv) > 4 and sys.argv[3] == "--socks" else None
            get(arg, socks)
        elif cmd == "dns":
            dns(arg, sys.argv[3] if len(sys.argv) > 3 else None)
    except Exception as e:  # report and fail
        print("ERROR: %s" % e)
        sys.exit(1)


if __name__ == "__main__":
    main()
