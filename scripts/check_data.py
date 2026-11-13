#!/usr/bin/env python3
"""Stage only this project's dedicated fixtures to fit the local Docker budget."""
import json
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/smoke"))
from cdc import api  # noqa: E402
from dev_env import compose, load_env  # noqa: E402
from processing import job_status  # noqa: E402


def execute(*args):
    subprocess.run(list(args), cwd=ROOT, env=load_env(), check=True, timeout=480)


def pause(name):
    if name not in api("GET", "/connectors"):
        return
    api("PUT", "/connectors/" + name + "/pause")
    for _ in range(60):
        status = api("GET", "/connectors/" + name + "/status")
        if status["connector"]["state"] == "PAUSED" and all(task["state"] == "PAUSED" for task in status.get("tasks", [])):
            return
        time.sleep(1)
    raise RuntimeError("Connector pause result is unknown; source was not stopped")


def stop_compute():
    metadata = ROOT / ".local/smoke/flink-job.json"
    running = compose("ps", "--status", "running", "-q", "jobmanager", capture=True).stdout.strip()
    if running and metadata.exists():
        job_id = json.loads(metadata.read_text())["job_id"]
        state = job_status(job_id)
        if state == "RUNNING":
            execute("python3", "scripts/smoke/savepoint.py")
        elif state not in ("CANCELED", "FAILED", "FINISHED"):
            raise RuntimeError("Sample job is not ready for a safe savepoint stop")
    compose("stop", "taskmanager", "jobmanager", "doris-be", "doris-fe")


def main():
    execute("scripts/dev-env.sh", "up", "base")
    compose("up", "-d", "--wait", "--wait-timeout", "180", "connect")
    pause("datamesh-oracle")
    pause("datamesh-mysql")
    compose("stop", "oracle-source", "mysql-source")
    execute("scripts/dev-env.sh", "up", "pipeline")
    execute("scripts/dev-env.sh", "up", "postgres-source")
    execute("python3", "scripts/smoke/cdc.py", "postgres")
    execute("python3", "scripts/smoke/fresh_cdc.py", "postgres")
    execute("python3", "scripts/smoke/processing.py")
    execute("python3", "scripts/smoke/object_store.py", "--input", ".local/smoke/postgres-events.jsonl")
    stop_compute()
    execute("scripts/dev-env.sh", "up", "mysql-source")
    execute("python3", "scripts/smoke/cdc.py", "mysql")
    execute("python3", "scripts/smoke/fresh_cdc.py", "mysql")
    execute("python3", "scripts/smoke/object_store.py", "--input", ".local/smoke/mysql-events.jsonl")
    pause("datamesh-mysql")
    compose("stop", "mysql-source")
    execute("scripts/dev-env.sh", "up", "oracle-source")
    execute("python3", "scripts/smoke/oracle.py")
    execute("python3", "scripts/smoke/object_store.py", "--input", ".local/smoke/oracle-events.jsonl")
    pause("datamesh-oracle")
    compose("stop", "oracle-source")
    execute("scripts/dev-env.sh", "up", "pipeline")
    execute("python3", "scripts/smoke/processing.py")
    print("All three CDC fixtures, bounded raw archives, real Flink/Doris path and savepoint restore passed.")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Data integration failed; no step was marked as skipped/pass", file=sys.stderr)
        sys.exit(1)
