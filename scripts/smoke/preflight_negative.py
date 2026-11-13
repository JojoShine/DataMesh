#!/usr/bin/env python3
import copy
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def check(config, expected, directory, environment=None):
    path = directory / "compose-test.json"
    path.write_text(json.dumps(config))
    env = os.environ.copy()
    env["COMPOSE_FILE"] = str(path)
    if environment:
        env.update(environment)
        env.pop("DOCKER_CONTEXT", None)
    result = subprocess.run(["scripts/dev-env.sh", "preflight", "base"], cwd=ROOT, env=env, text=True, capture_output=True, timeout=30)
    if result.returncode == 0 or expected not in result.stderr:
        raise RuntimeError("Preflight negative boundary was not rejected as expected")


def main():
    original = json.loads((ROOT / "compose.yaml").read_text())
    config = {"services": {name: {"image": original["services"][name]["image"], "mem_limit": "128m"} for name in ["postgres", "rustfs"]}}
    with tempfile.TemporaryDirectory(dir=ROOT / ".local") as temporary:
        directory = Path(temporary)
        with socket.socket() as occupied:
            occupied.bind(("127.0.0.1", 0))
            occupied.listen()
            port = occupied.getsockname()[1]
            collision = copy.deepcopy(config)
            collision["services"]["postgres"]["ports"] = [f"127.0.0.1:{port}:5432"]
            check(collision, "is occupied", directory)
        missing = copy.deepcopy(config)
        missing["services"]["postgres"]["image"] = "postgres@sha256:" + "0" * 64
        check(missing, "locked native image missing", directory)
        excessive = copy.deepcopy(config)
        excessive["services"]["postgres"]["mem_limit"] = "1t"
        check(excessive, "exceed 90%", directory)
        check(config, "Environment operation failed", directory, {"DOCKER_HOST": "unix:///nonexistent/datamesh-check.sock"})
    print("Preflight rejected occupied new ports, missing locked images, excessive memory and unavailable Docker; no services mutated.")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Preflight boundary check failed", file=sys.stderr)
        sys.exit(1)
