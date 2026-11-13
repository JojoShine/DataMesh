#!/usr/bin/env python3
"""Local environment operations. Never execute env-file contents as shell code."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import re
import shutil
import socket
import subprocess
import sys
from urllib.parse import urlsplit
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
LOCAL = ROOT / ".local"
ENV_FILE = LOCAL / "runtime.env"
PROFILE_SERVICES = {
    "base": ["postgres", "rustfs"],
    "app": ["postgres", "server", "source-reader"],
    "pipeline": ["postgres", "rustfs", "kafka", "connect", "jobmanager", "taskmanager", "sql-gateway", "doris-fe", "doris-be"],
    "mysql-source": ["mysql-source"],
    "postgres-source": ["postgres-source"],
    "oracle-source": ["oracle-source"],
}


def load_env():
    if not ENV_FILE.exists():
        raise RuntimeError("Run scripts/dev-env.sh init first")
    env = os.environ.copy()
    for line in ENV_FILE.read_text().splitlines():
        if line and not line.startswith("#"):
            key, value = line.split("=", 1)
            env[key] = value
    # These values are rendered into local fixture SQL, so keep their generated alphabet.
    for key in ["SOURCE_CDC_PASSWORD", "DORIS_ROOT_PASSWORD", "DORIS_WRITER_PASSWORD", "DORIS_READER_PASSWORD"]:
        if key in env and not re.fullmatch(r"[0-9a-f]{48}", env[key]):
            raise RuntimeError(key + " must remain a generated 48-character hex fixture secret")
    return env


def compose(*args, capture=False):
    return subprocess.run(
        ["docker", "compose", "--project-name", "datamesh", "--env-file", str(ENV_FILE), *args],
        cwd=ROOT, env=load_env(), check=True, text=True,
        stdout=subprocess.PIPE if capture else None,
    )


def prepare_stream_script_token():
    folder = LOCAL / 'stream-script'
    token = folder / 'token'
    if folder.is_symlink() or token.is_symlink():
        raise RuntimeError('Unsafe stream script token path')
    folder.mkdir(mode=0o700, exist_ok=True)
    folder.chmod(0o700)
    if not token.exists():
        fd = os.open(token, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o640)
        with os.fdopen(fd, 'w') as stream:
            stream.write(secrets.token_hex(32))
    if not token.is_file() or not re.fullmatch(r'[0-9a-f]{64}', token.read_text(encoding='ascii')):
        raise RuntimeError('Invalid existing stream script token; preserved for operator review')
    token.chmod(0o640)


def init():
    LOCAL.mkdir(mode=0o700, exist_ok=True)
    LOCAL.chmod(0o700)
    if not ENV_FILE.exists():
        owner, app = secrets.token_hex(24), secrets.token_hex(24)
        values = {
            "POSTGRES_PASSWORD": owner, "APP_DB_PASSWORD": app,
            "APP_DSN": f"postgres://datamesh_app:{app}@postgres:5432/datamesh?sslmode=disable",
            "MIGRATION_DSN": f"postgres://datamesh_owner:{owner}@postgres:5432/datamesh?sslmode=disable",
            "CSRF_KEY": secrets.token_hex(32), "APP_ORIGIN": "https://localhost:18443",
            "RUSTFS_ACCESS_KEY": "dm" + secrets.token_hex(10), "RUSTFS_SECRET_KEY": secrets.token_hex(24),
            "MYSQL_ROOT_PASSWORD": secrets.token_hex(24), "SOURCE_DB_PASSWORD": secrets.token_hex(24),
            "SOURCE_CDC_PASSWORD": secrets.token_hex(24),
            "ORACLE_PASSWORD": secrets.token_hex(16), "LOCAL_UID": str(os.getuid()), "LOCAL_GID": str(os.getgid()),
        }
        fd = os.open(ENV_FILE, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write("".join(f"{key}={value}\n" for key, value in values.items()))
    else:
        ENV_FILE.chmod(0o600)
    existing = {line.split("=", 1)[0] for line in ENV_FILE.read_text().splitlines() if "=" in line}
    with ENV_FILE.open("a") as stream:
        for key in ["DORIS_ROOT_PASSWORD", "DORIS_WRITER_PASSWORD", "DORIS_READER_PASSWORD", "SOURCE_CDC_PASSWORD", "CREDENTIAL_KEY", "SOURCE_READER_TOKEN", "SANDBOX_DORIS_READER_PASSWORD", "SANDBOX_DORIS_WRITER_PASSWORD"]:
            if key not in existing:
                stream.write(f"{key}={secrets.token_hex(32 if key in {'CREDENTIAL_KEY','SOURCE_READER_TOKEN'} else 24)}\n")
    (LOCAL / "source-reader-drivers").mkdir(mode=0o755,exist_ok=True)
    bootstrap = LOCAL / "bootstrap-admin.json"
    if not bootstrap.exists():
        fd = os.open(bootstrap, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, "w") as stream:
            json.dump({"username": "admin", "password": secrets.token_urlsafe(24),
                       "department_id": "platform-admin", "platforms": ["exchange", "governance", "assets", "security"]}, stream)
    for name in ["flink-jars", "smoke", "bindings", "public-test-accounts"]:
        (LOCAL / name).mkdir(exist_ok=True)
    prepare_stream_script_token()
    certs = LOCAL / "certs"
    certs.mkdir(mode=0o700, exist_ok=True)
    if not (certs / "server.crt").exists():
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "30",
                        "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1",
                        "-keyout", str(certs / "server.key"), "-out", str(certs / "server.crt")],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (certs / "server.key").chmod(0o600)
    print("Local configuration ready; existing credentials preserved.")


def preflight(profile):
    info = json.loads(subprocess.check_output(["docker", "info", "--format", "{{json .}}"], text=True))
    services = PROFILE_SERVICES[profile]
    config = json.loads(compose("--profile", "*", "config", "--format", "json", capture=True).stdout)
    architecture = {"aarch64": "arm64", "x86_64": "amd64"}.get(info["Architecture"], info["Architecture"])
    if architecture != "arm64":
        raise RuntimeError("This dependency lock has only been verified on linux/arm64")
    if shutil.disk_usage(ROOT).free < 5 * 1024 ** 3:
        raise RuntimeError("Less than 5 GiB free workspace disk space")
    running_ids = subprocess.check_output(["docker", "ps", "--filter", "label=com.docker.compose.project=datamesh",
                                           "--filter", "label=com.docker.compose.oneoff=False", "-q"], text=True).split()
    running = {}
    if running_ids:
        details = subprocess.check_output(["docker", "inspect", "--format",
                                           '{{index .Config.Labels "com.docker.compose.service"}}|{{json .NetworkSettings.Ports}}', *running_ids], text=True)
        for line in details.splitlines():
            service_name, ports = line.split("|", 1)
            running[service_name] = {(item["HostIp"], int(item["HostPort"]))
                                     for bindings in (json.loads(ports) or {}).values() for item in bindings or []}
    memory = 0
    new_memory = 0
    for service in services:
        if service not in config["services"]:
            raise RuntimeError(f"{service}: configuration not yet available")
        item = config["services"][service]
        if "image" in item and "build" not in item:
            image = item["image"]
            if "@sha256:" not in image:
                raise RuntimeError(f"{service}: image digest is not locked")
            result = subprocess.run(["docker", "image", "inspect", image, "--format", "{{.Architecture}}"], text=True, capture_output=True)
            if result.returncode or result.stdout.strip() != architecture:
                raise RuntimeError(f"{service}: locked native image missing")
        memory += int(item.get("mem_limit", 0))
        if service not in running:
            new_memory += int(item.get("mem_limit", 0))
        for port in item.get("ports", []):
            if port.get("host_ip") != "127.0.0.1":
                raise RuntimeError(f"{service}: port must bind only to loopback")
            published = int(port["published"])
            if ("127.0.0.1", published) in running.get(service, set()):
                continue
            with socket.socket() as probe:
                try:
                    probe.bind(("127.0.0.1", published))
                except OSError as exc:
                    raise RuntimeError(f"{service}: port {published} is occupied") from exc
    if memory > info["MemTotal"] * 0.9:
        raise RuntimeError("Profile memory limits exceed 90% of Docker memory; reduce concurrent profiles or explicitly adjust resources")
    stats = subprocess.check_output(["docker", "stats", "--no-stream", "--format", "{{.MemUsage}}"], text=True)
    usage = 0
    units = {"B": 1, "KiB": 1024, "MiB": 1024 ** 2, "GiB": 1024 ** 3, "TiB": 1024 ** 4,
             "kB": 1000, "KB": 1000, "MB": 1000 ** 2, "GB": 1000 ** 3}
    for line in stats.splitlines():
        value = line.split("/", 1)[0].strip()
        match = re.fullmatch(r"([0-9.]+)\s*([A-Za-z]+)", value)
        if not match or match[2] not in units:
            raise RuntimeError("Docker memory usage could not be interpreted")
        usage += float(match[1]) * units[match[2]]
    if usage + new_memory > info["MemTotal"] * 0.9:
        raise RuntimeError("Insufficient Docker memory headroom; use scripts/check.sh data for staged fixture startup")
    print(f"Preflight passed: {profile}, native={architecture}, configured memory={memory // 1048576} MiB")


def fetch(profile):
    config = json.loads(compose("--profile", "*", "config", "--format", "json", capture=True).stdout)
    references = set()
    for name in PROFILE_SERVICES[profile]:
        service = config["services"][name]
        if "build" not in service:
            references.add(service["image"])
    for reference in sorted(references):
        if "@sha256:" not in reference:
            raise RuntimeError("Cannot fetch an unlocked image")
        subprocess.run(["docker", "pull", "--platform", "linux/arm64", reference], check=True)
    if profile == "pipeline":
        lock = json.loads((ROOT / "deploy/versions.lock.json").read_text())
        for plugin in lock["plugins"]:
            if Path(plugin["file"]).name != plugin["file"] or not plugin["file"].endswith(".jar"):
                raise RuntimeError("Plugin filename must be a plain JAR basename")
            destination = LOCAL / "flink-jars" / plugin["file"]
            if destination.exists() and hashlib.sha256(destination.read_bytes()).hexdigest() == plugin["sha256"]:
                continue
            url = plugin["url"]
            parsed = urlsplit(url)
            if parsed.scheme != "https" or parsed.netloc != "repo.maven.apache.org":
                raise RuntimeError("Plugin must come from the locked Maven HTTPS origin")
            with urlopen(url, timeout=60) as response:
                data = response.read(64 * 1024 ** 2 + 1)
            if len(data) > 64 * 1024 ** 2 or hashlib.sha256(data).hexdigest() != plugin["sha256"]:
                raise RuntimeError("Plugin size or SHA-256 validation failed")
            temporary = destination.with_suffix(".download")
            temporary.write_bytes(data)
            temporary.replace(destination)
        print("Locked Flink plugins downloaded and SHA-256 verified.")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["init", "fetch", "preflight", "up", "status", "stop", "migrate", "build", "code-runtimes", "create-user", "disable-user", "initialize-business"])
    parser.add_argument("profile", nargs="?", default="base", choices=PROFILE_SERVICES)
    args = parser.parse_args()
    if args.action == "init":
        init()
    elif args.action == "fetch":
        fetch(args.profile)
    elif args.action == "preflight":
        preflight(args.profile)
    elif args.action == "up":
        preflight(args.profile)
        compose("up", "-d", "--wait", "--wait-timeout", "180", *PROFILE_SERVICES[args.profile])
    elif args.action == "stop":
        compose("stop", *PROFILE_SERVICES[args.profile])
    elif args.action == "status":
        compose("ps", *PROFILE_SERVICES[args.profile])
    elif args.action == "migrate":
        compose("run", "--rm", "migrate")
    elif args.action == "initialize-business":
        compose("run", "--rm", "migrate")
        subprocess.run([sys.executable, str(ROOT / "scripts/initialize_business_scenarios.py"), "--initialize"], cwd=ROOT, check=True)
    elif args.action == "code-runtimes":
        from build_governance_code import main as build_code
        build_code()
    elif args.action == "build":
        compose("build", "server")
    elif args.action in ("create-user", "disable-user"):
        compose("run", "--rm", "-T", "--entrypoint", "/app/manage", "server", args.action)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError, FileNotFoundError, ValueError) as exc:
        # Docker config and database URLs can contain credentials; don't echo command arguments.
        message = str(exc) if isinstance(exc, RuntimeError) else "Environment operation failed; inspect component status without printing secrets."
        print(message, file=sys.stderr)
        sys.exit(1)
