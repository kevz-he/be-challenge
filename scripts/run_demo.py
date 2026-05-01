#!/usr/bin/env python3
"""End-to-end demo for the Yuno Transaction Health Monitor.

What it does (default = production-like, hits docker compose):
  1. `docker compose down -v` to wipe any prior state.
  2. `docker compose up -d --build api` to boot ONLY the API service
     (the bundled `seeder` is intentionally left off so this script can
     own the ingest and the oracle stays in sync).
  3. Waits until the container's healthcheck reports healthy.
  4. Bulk-ingests `testdata/transactions.json` via POST /v1/transactions/batch.
  5. Hits every analytical endpoint and checks the response against
     `testdata/expected_counts.json` (counts + ID-set equality + health score).
  6. `docker compose down -v` to clean up (skip with --keep).
  7. Prints a green/red checklist and exits non-zero on the first failure.

Usage:
  python3 scripts/run_demo.py                          # docker compose (default)
  python3 scripts/run_demo.py --keep                   # do not tear down docker at the end
  python3 scripts/run_demo.py --mode local             # spawn `go run ./cmd/server`
  python3 scripts/run_demo.py --base http://localhost:8080  # use an already-running server

Requires only Python 3.9+ (stdlib). Default mode also requires docker + docker compose.
"""
from __future__ import annotations

import argparse
import json
import math
import os
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import closing
from pathlib import Path
from typing import Any, Iterable

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_TX_FILE = REPO_ROOT / "testdata" / "transactions.json"
DEFAULT_EXPECTED = REPO_ROOT / "testdata" / "expected_counts.json"
DEFAULT_DB_FILE = REPO_ROOT / "demo.db"

GREEN = "\033[32m"
RED = "\033[31m"
YELLOW = "\033[33m"
BOLD = "\033[1m"
RESET = "\033[0m"


def color(s: str, c: str) -> str:
    if not sys.stdout.isatty():
        return s
    return f"{c}{s}{RESET}"


def ok(msg: str) -> None:
    print(f"  {color('PASS', GREEN)}  {msg}")


def fail(msg: str) -> None:
    print(f"  {color('FAIL', RED)}  {msg}")


def info(msg: str) -> None:
    print(f"{color('==>', YELLOW)} {msg}")


def free_port() -> int:
    with closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_get_json(url: str, timeout: float = 30.0) -> Any:
    req = urllib.request.Request(url, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def http_post_json(url: str, body: Any, timeout: float = 120.0) -> tuple[int, Any]:
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8") or "{}"
            return resp.status, json.loads(raw)
    except urllib.error.HTTPError as e:
        body_text = e.read().decode("utf-8", errors="replace")
        try:
            return e.code, json.loads(body_text)
        except Exception:
            return e.code, {"raw": body_text}


def wait_healthy(base: str, attempts: int = 60, delay: float = 0.5) -> None:
    url = f"{base}/healthz"
    last_err: Exception | None = None
    for _ in range(attempts):
        try:
            with urllib.request.urlopen(url, timeout=2.0) as resp:
                if resp.status == 200:
                    return
        except Exception as e:
            last_err = e
        time.sleep(delay)
    raise RuntimeError(f"server never became healthy at {url}: {last_err}")


def docker_compose_cmd() -> list[str]:
    """Return the docker compose invocation, preferring the v2 plugin."""
    if shutil.which("docker") is None:
        sys.exit("docker not found in PATH; install Docker Desktop or use --mode local")
    return ["docker", "compose"]


def docker_up() -> None:
    base_cmd = docker_compose_cmd()
    info("docker compose down -v (reset previous state)")
    subprocess.run(base_cmd + ["down", "-v"], cwd=str(REPO_ROOT), check=False)
    info("docker compose up -d --build api  (no seeder; this script owns the ingest)")
    res = subprocess.run(
        base_cmd + ["up", "-d", "--build", "api"],
        cwd=str(REPO_ROOT),
    )
    if res.returncode != 0:
        sys.exit(f"docker compose up failed (exit={res.returncode})")


def docker_down() -> None:
    base_cmd = docker_compose_cmd()
    info("docker compose down -v (cleanup)")
    subprocess.run(base_cmd + ["down", "-v"], cwd=str(REPO_ROOT), check=False)


def docker_logs_tail(lines: int = 80) -> None:
    base_cmd = docker_compose_cmd()
    print(color("--- docker compose logs api (tail) ---", YELLOW))
    subprocess.run(
        base_cmd + ["logs", "--tail", str(lines), "api"],
        cwd=str(REPO_ROOT),
        check=False,
    )
    print(color("--- end logs ---", YELLOW))


def start_server(port: int, db_file: Path) -> subprocess.Popen[bytes]:
    if shutil.which("go") is None:
        sys.exit("go toolchain not found in PATH; install Go or use --no-server with --base")
    if db_file.exists():
        db_file.unlink()
    for ext in ("-shm", "-wal", "-journal"):
        sidecar = Path(str(db_file) + ext)
        if sidecar.exists():
            sidecar.unlink()

    env = os.environ.copy()
    env["PORT"] = str(port)
    env["SQLITE_DSN"] = f"file:{db_file}?_pragma=journal_mode(WAL)"
    env["LOG_LEVEL"] = env.get("LOG_LEVEL", "warn")

    log_path = REPO_ROOT / "demo-server.log"
    log_fh = log_path.open("wb")
    info(f"starting server on :{port} (logs: {log_path})")
    proc = subprocess.Popen(
        ["go", "run", "./cmd/server"],
        cwd=str(REPO_ROOT),
        env=env,
        stdout=log_fh,
        stderr=subprocess.STDOUT,
        preexec_fn=os.setsid if os.name != "nt" else None,
    )
    return proc


def stop_server(proc: subprocess.Popen[bytes]) -> None:
    if proc.poll() is not None:
        return
    info("stopping server")
    try:
        if os.name == "nt":
            proc.terminate()
        else:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        proc.wait(timeout=10)
    except Exception:
        try:
            if os.name == "nt":
                proc.kill()
            else:
                os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except Exception:
            pass


def chunked(seq: list[Any], size: int) -> Iterable[list[Any]]:
    for i in range(0, len(seq), size):
        yield seq[i : i + size]


def ingest(base: str, tx_file: Path, batch_size: int) -> None:
    info(f"ingesting {tx_file} into {base}/v1/transactions/batch (batch_size={batch_size})")
    txs: list[Any] = json.loads(tx_file.read_text())
    total = len(txs)
    sent = 0
    for chunk in chunked(txs, batch_size):
        status, body = http_post_json(f"{base}/v1/transactions/batch", {"transactions": chunk})
        if status >= 300:
            raise RuntimeError(f"batch ingest failed (status={status}): {body}")
        sent += len(chunk)
        print(f"    ingested {sent}/{total}")
    if sent != total:
        raise RuntimeError(f"ingested {sent} but file has {total}")


class Checks:
    def __init__(self) -> None:
        self.passed = 0
        self.failed = 0

    def expect_eq(self, label: str, got: Any, want: Any) -> None:
        if got == want:
            ok(f"{label}: {got}")
            self.passed += 1
        else:
            fail(f"{label}: got={got!r} want={want!r}")
            self.failed += 1

    def expect_set_eq(self, label: str, got: Iterable[str], want: Iterable[str]) -> None:
        g = set(got)
        w = set(want)
        if g == w:
            ok(f"{label}: {len(g)} IDs match")
            self.passed += 1
        else:
            missing = sorted(w - g)
            extra = sorted(g - w)
            fail(f"{label}: ID set mismatch (missing={missing[:5]} extra={extra[:5]})")
            self.failed += 1

    def expect_close(self, label: str, got: float, want: float, tol: float = 1e-6) -> None:
        if math.isfinite(got) and math.isfinite(want) and abs(got - want) < tol:
            ok(f"{label}: {got:.6f} (~ {want:.6f})")
            self.passed += 1
        else:
            fail(f"{label}: got={got!r} want={want!r}")
            self.failed += 1

    def expect_status(self, label: str, url: str, want_status: int, *, method: str = "GET") -> None:
        try:
            req = urllib.request.Request(url, method=method)
            with urllib.request.urlopen(req, timeout=10.0) as resp:
                got = resp.status
        except urllib.error.HTTPError as e:
            got = e.code
        if got == want_status:
            ok(f"{label}: HTTP {got}")
            self.passed += 1
        else:
            fail(f"{label}: HTTP {got} (want {want_status}) url={url}")
            self.failed += 1


def run_checks(base: str, expected: dict[str, Any]) -> Checks:
    checks = Checks()
    win_from = expected["window_from"]
    win_to = expected["window_to"]
    qs = urllib.parse.urlencode({"from": win_from, "to": win_to})

    info("GET /v1/health (windowed)")
    health = http_get_json(f"{base}/v1/health?{qs}")
    counts = health.get("anomaly_counts", {})
    expected_counts = expected["anomaly_counts"]
    for kind in ("orphaned", "ghost", "duplicate", "pending_limbo"):
        checks.expect_eq(f"health.anomaly_counts.{kind}", counts.get(kind), expected_counts[kind])

    expected_score = expected.get("expected_health_score", expected.get("health_score"))
    checks.expect_close("health.score", float(health.get("score", -1)), float(expected_score))

    info("GET /v1/anomalies?type=...")
    for kind in ("orphaned", "ghost", "pending_limbo"):
        url = f"{base}/v1/anomalies?{qs}&type={kind}&limit=1000"
        body = http_get_json(url)
        items = body.get("items", [])
        checks.expect_eq(f"items[{kind}].len", len(items), expected_counts[kind])
        checks.expect_set_eq(
            f"items[{kind}].ids",
            (it["transaction_id"] for it in items),
            expected["ids"][kind],
        )

    url_dup = f"{base}/v1/anomalies?{qs}&type=duplicate&limit=1000"
    dup = http_get_json(url_dup)
    groups = dup.get("groups", [])
    checks.expect_eq("groups[duplicate].len", len(groups), expected_counts["duplicate"])
    checks.expect_set_eq(
        "groups[duplicate].ids",
        (g["transaction_id"] for g in groups),
        expected["ids"]["duplicate"],
    )

    info("GET /v1/anomalies (summary, no type)")
    summary = http_get_json(f"{base}/v1/anomalies?{qs}")
    sum_counts = summary.get("counts") or summary.get("anomaly_counts") or {}
    if sum_counts:
        for kind in ("orphaned", "ghost", "duplicate", "pending_limbo"):
            if kind in sum_counts:
                checks.expect_eq(f"summary.{kind}", sum_counts[kind], expected_counts[kind])

    info("GET /v1/alerts")
    alerts = http_get_json(f"{base}/v1/alerts?{qs}")
    if "alert" in alerts:
        ok(f"alerts.alert={alerts['alert']} triggered={len(alerts.get('triggered', []))}")
        checks.passed += 1
    else:
        fail(f"alerts response missing 'alert' field: {alerts}")
        checks.failed += 1

    info("error responses")
    inverted = urllib.parse.urlencode({"from": win_to, "to": win_from})
    checks.expect_status("invalid window (422)", f"{base}/v1/health?{inverted}", 422)
    checks.expect_status("invalid type (400)", f"{base}/v1/anomalies?type=nope", 400)

    def check_breakdown(kind: str) -> None:
        body = http_get_json(f"{base}/v1/health?{qs}&breakdown={kind}")
        bd = body.get("breakdown")
        buckets: list[Any] = []
        if isinstance(bd, list):
            buckets = bd
        elif isinstance(bd, dict):
            buckets = list(bd.keys())
        if buckets:
            ok(f"breakdown={kind}: {len(buckets)} buckets ({', '.join(map(str, buckets[:6]))})")
            checks.passed += 1
        else:
            fail(f"breakdown={kind} missing or empty: {body}")
            checks.failed += 1

    info("GET /v1/health (breakdown=processor)  [stretch]")
    check_breakdown("processor")
    info("GET /v1/health (breakdown=payment_method)  [stretch]")
    check_breakdown("payment_method")

    return checks


def main() -> int:
    p = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    p.add_argument(
        "--mode",
        choices=("docker", "local"),
        default="docker",
        help="how to provision the server (default: docker — production-like)",
    )
    p.add_argument(
        "--base",
        default=None,
        help="point at an already-running server (overrides --mode, no boot/teardown)",
    )
    p.add_argument("--tx-file", default=str(DEFAULT_TX_FILE), help="transactions JSON array")
    p.add_argument("--expected", default=str(DEFAULT_EXPECTED), help="oracle JSON")
    p.add_argument("--db-file", default=str(DEFAULT_DB_FILE), help="SQLite path when --mode local")
    p.add_argument("--batch-size", type=int, default=1000)
    p.add_argument(
        "--keep",
        action="store_true",
        help="do not tear down the server after the run (docker compose stays up, or local server stays running)",
    )
    args = p.parse_args()

    tx_file = Path(args.tx_file)
    expected_path = Path(args.expected)
    if not tx_file.exists():
        sys.exit(f"transactions file not found: {tx_file}")
    if not expected_path.exists():
        sys.exit(f"expected counts file not found: {expected_path}")
    expected = json.loads(expected_path.read_text())

    spawned: subprocess.Popen[bytes] | None = None
    docker_owned = False
    base = args.base

    if base is None:
        if args.mode == "docker":
            docker_up()
            docker_owned = True
            base = "http://localhost:8080"
            try:
                wait_healthy(base, attempts=120, delay=0.5)
            except Exception as e:
                print(color(f"server never became healthy: {e}", RED + BOLD))
                docker_logs_tail()
                if not args.keep:
                    docker_down()
                return 2
        else:  # local
            port = free_port()
            base = f"http://127.0.0.1:{port}"
            spawned = start_server(port, Path(args.db_file))
            try:
                wait_healthy(base)
            except Exception as e:
                print(color(f"server never became healthy: {e}", RED + BOLD))
                stop_server(spawned)
                return 2

    info(f"target server: {base}  (mode={'external' if args.base else args.mode})")
    print(f"  dataset:  {tx_file}  ({len(json.loads(tx_file.read_text()))} rows)")
    print(f"  oracle:   {expected_path}")

    rc = 0
    try:
        ingest(base, tx_file, args.batch_size)
        checks = run_checks(base, expected)
        print()
        total = checks.passed + checks.failed
        if checks.failed == 0:
            print(color(f"ALL GREEN — {checks.passed}/{total} checks passed", GREEN + BOLD))
            rc = 0
        else:
            print(color(f"FAILED — {checks.failed}/{total} checks failed", RED + BOLD))
            rc = 1
    except Exception as e:
        print(color(f"ERROR: {e}", RED + BOLD))
        if docker_owned:
            docker_logs_tail()
        rc = 2
    finally:
        if not args.keep:
            if spawned is not None:
                stop_server(spawned)
            if docker_owned:
                docker_down()
        else:
            if docker_owned:
                info("--keep: docker compose left running. Tear down with: docker compose down -v")
            elif spawned is not None:
                info("--keep: local server left running.")
    return rc


if __name__ == "__main__":
    sys.exit(main())
