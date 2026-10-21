"""End-to-end driver for a running steamcmd-serve controller (stdlib only).

Runs two Enshrouded instances side by side through the public API: setup, login,
create, install, start, Steam A2S_INFO query on each port, live logs, stop, backup,
delete. Exits non-zero on the first failed check.

Usage: python driver.py http://127.0.0.1:18080
"""

import http.cookiejar
import json
import socket
import struct
import sys
import time
import urllib.error
import urllib.request

BASE = sys.argv[1].rstrip("/") + "/api/v1"
ADMIN = {"username": "e2e", "password": "e2e-only-password-0001"}
jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
csrf = None


def log(msg):
    print(time.strftime("%H:%M:%S"), msg, flush=True)


def fail(msg):
    log("FAIL " + msg)
    sys.exit(1)


def call(method, path, body=None, expect=(200,)):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if csrf and method not in ("GET", "HEAD"):
        req.add_header("X-CSRF-Token", csrf)
    try:
        with opener.open(req, timeout=60) as r:
            status, raw = r.status, r.read()
    except urllib.error.HTTPError as e:
        status, raw = e.code, e.read()
    if status not in expect:
        fail(f"{method} {path} -> {status} {raw[:300]!r}")
    return json.loads(raw) if raw.strip() else None


def wait_job(job, timeout):
    deadline = time.time() + timeout
    last = ""
    while time.time() < deadline:
        j = call("GET", f"/jobs/{job['id']}")
        msg = f"{j['kind']} {j['status']} {j.get('progress', 0):.0%} {j.get('message', '')[:80]}"
        if msg != last:
            log("  " + msg)
            last = msg
        if j["status"] in ("succeeded", "failed"):
            return j
        time.sleep(3)
    fail(f"job {job['id']} ({job['kind']}) did not finish in {timeout}s")


def a2s_info(port, host="127.0.0.1"):
    """Steam A2S_INFO; handles the S2C_CHALLENGE handshake. Returns the server name."""
    req = b"\xff\xff\xff\xffTSource Engine Query\x00"
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3)
    try:
        s.sendto(req, (host, port))
        data, _ = s.recvfrom(4096)
        if data[4:5] == b"A":  # challenge
            s.sendto(req + data[5:9], (host, port))
            data, _ = s.recvfrom(4096)
    finally:
        s.close()
    if data[4:5] != b"I":
        raise ValueError(f"unexpected A2S reply {data[:8]!r}")
    name_end = data.index(b"\x00", 6)
    return data[6:name_end].decode(errors="replace")


def sse_lines(instance_id, seconds):
    req = urllib.request.Request(f"{BASE}/instances/{instance_id}/logs?tail=2000")
    lines = []
    try:
        with opener.open(req, timeout=seconds) as r:
            end = time.time() + seconds
            while time.time() < end:
                line = r.readline().decode(errors="replace")
                if not line:
                    break
                if line.startswith("data: "):
                    lines.append(line[6:].rstrip("\n"))
                    if "'HostOnline' (up)!" in line:
                        break
    except (socket.timeout, TimeoutError):
        pass
    return lines


def main():
    global csrf
    if not call("GET", "/setup")["required"]:
        fail("controller is not fresh (setup already done)")
    call("POST", "/setup", ADMIN, expect=(201,))
    call("POST", "/auth/login", ADMIN, expect=(204,))
    csrf = call("GET", "/auth/session")["csrfToken"]
    log("logged in")

    games = {g["id"] for g in call("GET", "/games")}
    if "enshrouded" not in games:
        fail(f"enshrouded not in catalog {games}")

    insts = []
    for name in ("E2E One", "E2E Two"):
        body = call("POST", "/instances", {"name": name, "gameId": "enshrouded", "memoryMB": 8192,
                                           "settings": {"name": f"steamcmd-serve {name}"}}, expect=(201,))
        log(f"created {name}: id={body['id']} ports={body['ports']} warnings={body.get('warnings')}")
        insts.append(body)
    p1, p2 = (i["ports"][0]["port"] for i in insts)
    if p1 == p2:
        fail("instances share a port")

    for inst in insts:
        j = wait_job(call("POST", f"/instances/{inst['id']}/actions/install", expect=(202,)), 3600)
        if j["status"] != "succeeded":
            fail(f"install {inst['name']}: {j.get('message')}")

    s = call("GET", f"/instances/{insts[0]['id']}/settings")
    if any(not isinstance(g["password"], dict) for g in s["userGroups"]):
        fail("settings API leaked a group password")

    for inst in insts:
        j = wait_job(call("POST", f"/instances/{inst['id']}/actions/start", expect=(202,)), 1200)
        cur = call("GET", f"/instances/{inst['id']}")
        if j["status"] != "succeeded" or cur["state"] != "running" or cur.get("stateDetail"):
            fail(f"start {inst['name']}: job={j.get('message')} state={cur['state']} detail={cur.get('stateDetail')}")
        log(f"{inst['name']} running (readiness line seen)")

    for inst in insts:
        port = inst["ports"][0]["port"]
        want = f"steamcmd-serve {inst['name']}"
        for attempt in range(20):
            try:
                got = a2s_info(port)
                break
            except (OSError, ValueError) as e:
                err = e
                time.sleep(3)
        else:
            fail(f"A2S_INFO on {port}/udp: {err}")
        if got != want:
            fail(f"A2S name on {port}: {got!r} != {want!r}")
        log(f"A2S_INFO {port}/udp -> {got!r}")

    lines = sse_lines(insts[0]["id"], 30)
    if not any("'HostOnline' (up)!" in l for l in lines):
        fail(f"log stream lacks HostOnline ({len(lines)} lines)")
    log(f"log stream ok ({len(lines)} lines)")

    for inst in insts:
        t0 = time.time()
        j = wait_job(call("POST", f"/instances/{inst['id']}/actions/stop", expect=(202,)), 180)
        if j["status"] != "succeeded":
            fail(f"stop {inst['name']}: {j.get('message')}")
        log(f"{inst['name']} stopped in {time.time() - t0:.0f}s")

    j = wait_job(call("POST", f"/instances/{insts[0]['id']}/backups", expect=(202,)), 300)
    backups = call("GET", f"/instances/{insts[0]['id']}/backups")
    if j["status"] != "succeeded" or not backups or backups[0]["sizeBytes"] < 1024:
        fail(f"backup: job={j.get('message')} list={backups}")
    log(f"backup {backups[0]['id']} {backups[0]['sizeBytes']} bytes")

    for inst in insts:
        call("DELETE", f"/instances/{inst['id']}?deleteData=true", expect=(204,))
    if call("GET", "/instances"):
        fail("instances left after delete")
    log("PASS")


if __name__ == "__main__":
    main()
