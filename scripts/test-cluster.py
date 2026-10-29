#!/usr/bin/env python3
"""Run Go tests against a disposable Docker Redis cluster (3 primaries + 3 replicas).

    python3 scripts/test-cluster.py
    REDIS_TEST_IMAGE=redis:6.2 python3 scripts/test-cluster.py
"""

import os
import pathlib
import random
import socket
import subprocess
import time
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[1]
IMAGE = os.environ.get("REDIS_TEST_IMAGE", "redis:7-alpine")
NAME = "redis-tools-test-" + uuid.uuid4().hex[:12]


def run(*args, timeout=30):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    return result.stdout.strip()


def redis(port, *args):
    return run("docker", "exec", NAME, "redis-cli", "-p", str(port), "--raw", *args)


def wait_for(check, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (RuntimeError, subprocess.SubprocessError):
            pass
        time.sleep(0.1)
    raise RuntimeError("timed out waiting for test cluster")


def free_ports():
    for _ in range(100):
        base = random.randrange(20000, 39000)
        sockets = []
        try:
            for port in range(base, base + 6):
                sock = socket.socket()
                sockets.append(sock)
                sock.bind(("127.0.0.1", port))
            return list(range(base, base + 6))
        except OSError:
            pass
        finally:
            for sock in sockets:
                sock.close()
    raise RuntimeError("could not allocate local test ports")


def main():
    ports = free_ports()
    command = ["docker", "run", "--rm", "--detach", "--name", NAME]
    for port in ports:
        command.extend(["--publish", f"127.0.0.1:{port}:{port}"])
    # All six Redis instances share this disposable container's loopback network.
    # Announced addresses work from both Redis peers and the Go tests on the host.
    startup = """
for port in $REDIS_TEST_PORTS; do
  redis-server --port "$port" --bind 0.0.0.0 --protected-mode no \
    --cluster-enabled yes --cluster-config-file "nodes-$port.conf" \
    --cluster-node-timeout 2000 --cluster-announce-ip 127.0.0.1 \
    --cluster-announce-port "$port" --repl-diskless-sync-delay 0 \
    --save "" --appendonly no > "/data/redis-$port.log" 2>&1 &
done
wait
"""
    command.extend(["--env", "REDIS_TEST_PORTS=" + " ".join(map(str, ports)), IMAGE, "sh", "-c", startup])
    created = False
    try:
        run(*command, timeout=120)
        created = True
        wait_for(lambda: all(redis(port, "PING") == "PONG" for port in ports))
        run("docker", "exec", NAME, "redis-cli", "--cluster", "create",
            *[f"127.0.0.1:{port}" for port in ports], "--cluster-replicas", "1", "--cluster-yes")
        wait_for(lambda: all("cluster_state:ok" in redis(port, "CLUSTER", "INFO") for port in ports))
        wait_for(lambda: all("role:master" in redis(port, "INFO", "replication") or
                            "master_link_status:up" in redis(port, "INFO", "replication") for port in ports))
        print(f"Docker cluster ready: {IMAGE}, 3 primaries + 3 replicas", flush=True)
        env = dict(os.environ, REDIS_TOOLS_CLUSTER_ADDR=f"127.0.0.1:{ports[0]}")
        result = subprocess.run(["go", "test", "-race", "./...", "-count=1"], cwd=ROOT, env=env, timeout=180)
        return result.returncode
    finally:
        if created:
            run("docker", "rm", "--force", "--volumes", NAME)
            print("Removed disposable Redis test container", flush=True)


if __name__ == "__main__":
    raise SystemExit(main())
