#!/usr/bin/env python3
"""Fail if an address on this node's container bridge answers ARP from two MACs.

Run on a cluster node as root (script/vagrant.sh monitor and chaos run it on
every node). Sends one ARP request from the bridge address to every address in
the bridge subnet, twice, and groups the replies by sender address. Two MACs
for one address means two containers (or a finished job's namespace that is
still on the bridge and a new job) hold the same IP: clients then reach
whichever answered ARP last.

Prints "answered=N duplicates=0" and exits 0, or one "duplicate <ip> answered
by <mac>,<mac>" line per address and exits 1. Exits 0 with a note when the node
has no bridge yet. BRIDGE_IPS_SELFTEST=1 checks the reply parsing offline.
"""
import ipaddress
import json
import os
import select
import socket
import struct
import subprocess
import sys
import time

ETH_P_ARP = 0x0806


def arp_request(src_mac, src_ip, target_ip):
    return (b"\xff" * 6 + src_mac + struct.pack("!H", ETH_P_ARP)
            + struct.pack("!HHBBH", 1, 0x0800, 6, 4, 1)
            + src_mac + src_ip + b"\x00" * 6 + target_ip)


def parse_reply(frame):
    """Returns (sender ip, sender mac) for an ARP reply frame, else None."""
    if len(frame) < 42 or frame[12:14] != struct.pack("!H", ETH_P_ARP):
        return None
    htype, ptype, hlen, plen, op = struct.unpack("!HHBBH", frame[14:22])
    if (htype, ptype, hlen, plen, op) != (1, 0x0800, 6, 4, 2):
        return None
    mac = ":".join("%02x" % b for b in frame[22:28])
    return socket.inet_ntoa(frame[28:32]), mac


def duplicates(replies):
    seen = {}
    for ip, mac in replies:
        seen.setdefault(ip, set()).add(mac)
    return seen, {ip: sorted(m) for ip, m in seen.items() if len(m) > 1}


def selftest():
    def reply(ip, mac):
        m = bytes.fromhex(mac.replace(":", ""))
        return (b"\xaa" * 6 + m + struct.pack("!H", ETH_P_ARP)
                + struct.pack("!HHBBH", 1, 0x0800, 6, 4, 2)
                + m + socket.inet_aton(ip) + b"\xaa" * 6 + socket.inet_aton("100.100.61.1"))
    frames = [
        reply("100.100.61.7", "fe:00:00:00:00:01"),
        reply("100.100.61.7", "fe:00:00:00:00:02"),
        reply("100.100.61.7", "fe:00:00:00:00:01"),
        reply("100.100.61.8", "fe:00:00:00:00:03"),
        reply("100.100.61.8", "fe:00:00:00:00:03"),
        arp_request(b"\xaa" * 6, socket.inet_aton("100.100.61.1"), socket.inet_aton("100.100.61.9")),
        b"\x00" * 20,
    ]
    replies = [r for r in map(parse_reply, frames) if r]
    seen, dups = duplicates(replies)
    assert sorted(seen) == ["100.100.61.7", "100.100.61.8"], seen
    assert dups == {"100.100.61.7": ["fe:00:00:00:00:01", "fe:00:00:00:00:02"]}, dups
    print("ok bridge-ips selftest")


def main():
    bridge = sys.argv[1] if len(sys.argv) > 1 else "flynnbr0"
    out = subprocess.run(["ip", "-j", "-4", "addr", "show", "dev", bridge],
                         capture_output=True, text=True).stdout
    addrs = [a for link in json.loads(out or "[]") for a in link.get("addr_info", [])]
    if not addrs:
        print("no %s address on this node" % bridge)
        return 0
    iface = ipaddress.ip_interface("%s/%s" % (addrs[0]["local"], addrs[0]["prefixlen"]))
    with open("/sys/class/net/%s/address" % bridge) as f:
        src_mac = bytes.fromhex(f.read().strip().replace(":", ""))
    s = socket.socket(socket.AF_PACKET, socket.SOCK_RAW, socket.htons(ETH_P_ARP))
    s.bind((bridge, 0))
    targets = [h for h in iface.network.hosts() if h != iface.ip]
    replies = []
    for _ in range(2):
        for h in targets:
            s.send(arp_request(src_mac, iface.ip.packed, h.packed))
        deadline = time.time() + 1.0
        while True:
            left = deadline - time.time()
            if left <= 0:
                break
            ready, _, _ = select.select([s], [], [], left)
            if not ready:
                break
            r = parse_reply(s.recv(256))
            if r and ipaddress.ip_address(r[0]) in iface.network:
                replies.append(r)
    seen, dups = duplicates(replies)
    for ip in sorted(dups, key=ipaddress.ip_address):
        print("duplicate %s answered by %s" % (ip, ",".join(dups[ip])))
    if dups:
        return 1
    print("answered=%d duplicates=0" % len(seen))
    return 0


if __name__ == "__main__":
    if os.environ.get("BRIDGE_IPS_SELFTEST"):
        selftest()
        sys.exit(0)
    sys.exit(main())
