#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from dev_env import compose, load_env  # noqa: E402


def run(*args, **kwargs):
    return subprocess.run(["docker", *args], env=load_env(), check=True, capture_output=True, **kwargs)


def main():
    backup_dir = ROOT / ".local/backups"
    backup_dir.mkdir(mode=0o700, exist_ok=True)
    backup = backup_dir / "platform.dump"
    count_sql = "SELECT (SELECT COUNT(*) FROM users),(SELECT COUNT(*) FROM sessions),(SELECT COUNT(*) FROM audit_records),(SELECT COUNT(*) FROM user_platform_access),(SELECT COUNT(*) FROM user_roles),(SELECT COUNT(*) FROM business_records),(SELECT COUNT(*) FROM business_operations)"
    before = compose("exec", "-T", "postgres", "psql", "-U", "datamesh_owner", "-d", "datamesh", "-At", "-c", count_sql, capture=True).stdout.strip()
    result = run("compose", "--project-name", "datamesh", "--env-file", str(ROOT / ".local/runtime.env"), "exec", "-T", "postgres", "pg_dump", "-U", "datamesh_owner", "-d", "datamesh", "--format=custom")
    fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(result.stdout)
    after = compose("exec", "-T", "postgres", "psql", "-U", "datamesh_owner", "-d", "datamesh", "-At", "-c", count_sql, capture=True).stdout.strip()
    if before != after:
        raise RuntimeError("Platform changed during the controlled backup check; retry during a quiet period")
    image = json.loads((ROOT / "deploy/versions.lock.json").read_text())["images"]["postgres"]["reference"]
    name = "datamesh-restore-check-" + uuid.uuid4().hex[:8]
    container = None
    try:
        container = run("run", "-d", "--name", name, "--network", "none", "--memory", "384m",
                        "--tmpfs", "/var/lib/postgresql/data:rw,nosuid,size=384m", "-e", "POSTGRES_PASSWORD", "-e", "APP_DB_PASSWORD",
                        "-e", "POSTGRES_USER=datamesh_owner", "-e", "POSTGRES_DB=datamesh",
                        "--mount", f"type=bind,src={ROOT / 'deploy/docker/postgres/01-app-role.sh'},dst=/docker-entrypoint-initdb.d/01-app-role.sh,readonly", image, text=True).stdout.strip()
        for _ in range(60):
            ready = subprocess.run(["docker", "exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "datamesh_owner", "-d", "datamesh"], capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Isolated restore database did not become ready")
        run("exec", "-i", container, "pg_restore", "-U", "datamesh_owner", "-d", "datamesh", "--exit-on-error", input=backup.read_bytes())
        restored = run("exec", container, "psql", "-U", "datamesh_owner", "-d", "datamesh", "-At", "-c", count_sql, text=True).stdout.strip()
        if restored != before:
            raise RuntimeError("Restored platform row counts differ")
        output = {"result": "passed", "backup": ".local/backups/platform.dump", "restored_tables": ["users", "sessions", "audit_records", "user_platform_access", "user_roles", "business_records", "business_operations"], "counts": restored, "isolation": "temporary network-none container with tmpfs; original database untouched"}
        (ROOT / ".local/smoke/restore-result.json").write_text(json.dumps(output, indent=2) + "\n")
        print(json.dumps(output))
    finally:
        if container:
            run("rm", "--force", container)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Backup/restore check failed; no credentials logged", file=sys.stderr)
        sys.exit(1)
