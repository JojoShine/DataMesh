#!/usr/bin/env python3
"""Real CDC smoke checks for dedicated sample databases; never use production sources."""
import argparse
from decimal import Decimal
import json
from pathlib import Path
import subprocess
import sys
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from dev_env import compose, load_env  # noqa: E402


def api(method, path, data=None):
    body = None if data is None else json.dumps(data).encode()
    request = Request("http://127.0.0.1:18083" + path, data=body, method=method, headers={"Content-Type": "application/json"})
    with urlopen(request, timeout=20) as response:
        content = response.read()
        return json.loads(content) if content else {}


def ensure_connector(name, config):
    if name not in api("GET", "/connectors"):
        try:
            api("POST", "/connectors", {"name": name, "config": config})
        except HTTPError as exc:
            if exc.code != 409:
                raise
    deadline = time.monotonic() + 90
    resumed = False
    checked_config = False
    while time.monotonic() < deadline:
        try:
            if not checked_config:
                current = api("GET", "/connectors/" + name + "/config")
                if any(current.get(key) != value for key, value in config.items()) or set(current) - set(config) - {"name"}:
                    raise RuntimeError(name + " has a different stored configuration; refusing to replace or validate the wrong task")
                checked_config = True
            status = api("GET", "/connectors/" + name + "/status")
        except HTTPError as exc:
            if exc.code != 404:
                raise
            # Config and runtime status propagate independently through Kafka Connect.
            time.sleep(0.5)
            continue
        if status["connector"]["state"] == "PAUSED" and not resumed:
            api("PUT", "/connectors/" + name + "/resume")
            resumed = True
        states = [status["connector"]["state"]] + [task["state"] for task in status.get("tasks", [])]
        if "FAILED" in states:
            raise RuntimeError(name + " failed; inspect redacted connector diagnostics")
        if len(states) > 1 and all(state == "RUNNING" for state in states):
            return
        time.sleep(1)
    raise RuntimeError(name + " did not become running within 90 seconds; no duplicate submission made")


def execute(source, sql):
    if source == "postgres":
        return compose("exec", "-T", "postgres-source", "psql", "-U", "source_owner", "-d", "sample", "-v", "ON_ERROR_STOP=1", "-At", "-c", sql, capture=True).stdout
    return compose("exec", "-T", "mysql-source", "sh", "-c", 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql --default-character-set=utf8mb4 -uroot -N -B -e "$1"', "mysql-smoke", sql, capture=True).stdout


def export(topic, source):
    result = subprocess.run(["docker", "compose", "--project-name", "datamesh", "--env-file", str(ROOT / ".local/runtime.env"),
                             "exec", "-T", "kafka", "/opt/kafka/bin/kafka-console-consumer.sh", "--bootstrap-server", "kafka:9092", "--topic", topic,
                             "--from-beginning", "--timeout-ms", "6000", "--max-messages", "10000", "--property", "print.partition=true",
                             "--property", "print.offset=true", "--property", "print.key=true"],
                            cwd=ROOT, env=load_env(), text=True, capture_output=True, timeout=60)
    if result.returncode and "TimeoutException" not in result.stderr:
        raise RuntimeError("Kafka export failed")
    events = []
    for line in result.stdout.splitlines():
        parts = line.split("\t", 3)
        if len(parts) != 4 or not parts[0].startswith("Partition:") or not parts[1].startswith("Offset:"):
            raise RuntimeError("Unexpected Kafka metadata format")
        event = json.loads(parts[3])
        if event is None:
            continue
        events.append({"topic": topic, "partition": int(parts[0].split(":")[1]), "offset": int(parts[1].split(":")[1]), "key": json.loads(parts[2]), "raw_event": event})
    if not events or len(events) >= 10000:
        raise RuntimeError("No events found or smoke export bound reached")
    directory = ROOT / ".local/smoke"
    directory.mkdir(exist_ok=True)
    output = directory / f"{source}-events.jsonl"
    output.write_text("".join(json.dumps(event, ensure_ascii=False) + "\n" for event in events))
    return events, output


def normalized(row):
    return {"id": int(row["id"]), "name": row["name"], "amount": None if row["amount"] is None else str(Decimal(str(row["amount"])).normalize())}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("source", choices=["postgres", "mysql"])
    args = parser.parse_args()
    source = args.source
    env = load_env()
    topic = f"datamesh_{source}." + ("public.customers" if source == "postgres" else "sample.customers")
    if source == "postgres":
        if execute(source, "SHOW wal_level").strip() != "logical":
            raise RuntimeError("PostgreSQL SQL connectivity passed but logical replication is unavailable")
    else:
        preflight = execute(source, "SELECT @@binlog_format, @@binlog_row_image, @@log_bin").strip()
        if preflight != "ROW\tFULL\t1":
            raise RuntimeError("MySQL CDC log conditions failed")
    compose("exec", "-T", "kafka", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "kafka:9092", "--create", "--if-not-exists", "--topic", topic, "--partitions", "1", "--replication-factor", "1", capture=True)
    name = "datamesh-" + source
    config = json.loads((ROOT / f"configs/connectors/{source}.json").read_text())
    config["database.password"] = env["SOURCE_CDC_PASSWORD"]
    ensure_connector(name, config)
    snapshot, _ = export(topic, source)
    if not any(event["raw_event"]["op"] == "r" for event in snapshot):
        raise RuntimeError("No actual snapshot evidence")
    marker = "更新-" + uuid.uuid4().hex[:8]
    new_id = 1001 if source == "postgres" else 4
    if source == "postgres":
        sql = f"UPDATE customers SET name='{marker}' WHERE id=1; DELETE FROM customers WHERE id=2; INSERT INTO customers VALUES ({new_id}, '新增样例', 99.123456, timestamp '2026-10-15 00:00:00') ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name;"
        expected_sql = "SELECT json_agg(json_build_object('id',id,'name',name,'amount',amount::text) ORDER BY id) FROM customers"
    else:
        sql = f"UPDATE sample.customers SET name='{marker}' WHERE id=1; DELETE FROM sample.customers WHERE id=2; INSERT INTO sample.customers VALUES ({new_id}, '新增样例', 99.123456, '2026-10-15 00:00:00') ON DUPLICATE KEY UPDATE name=VALUES(name);"
        expected_sql = "SELECT JSON_OBJECT('id',id,'name',name,'amount',CAST(amount AS CHAR)) FROM sample.customers ORDER BY id"
    execute(source, sql)
    events, output = export(topic, source)
    state = {}
    operations = set()
    for envelope in events:
        event = envelope["raw_event"]
        operations.add(event["op"])
        key = int(envelope["key"]["id"])
        if event["op"] == "d":
            state.pop(key, None)
        elif event["op"] in ("r", "c", "u"):
            state[key] = normalized(event["after"])
        else:
            raise RuntimeError("Unexpected CDC operation")
    source_rows = execute(source, expected_sql)
    rows = json.loads(source_rows) if source == "postgres" else [json.loads(row) for row in source_rows.splitlines()]
    expected = {int(row["id"]): normalized(row) for row in rows}
    if state != expected or not {"r", "c", "u", "d"}.issubset(operations) or state[1]["name"] != marker:
        raise RuntimeError("Snapshot/CDC final-state reconciliation failed")
    result = {"source": source, "result": "passed", "topic": topic, "operations": sorted(operations), "events": len(events), "final_rows": len(state), "first_offset": events[0]["offset"], "last_offset": events[-1]["offset"], "raw_export": str(output.relative_to(ROOT)), "scope": "snapshot then ongoing I/U/D; concurrent snapshot, recovery and replay not yet accepted"}
    (ROOT / f".local/smoke/{source}-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, HTTPError, OSError, ValueError, subprocess.SubprocessError) as exc:
        message = str(exc) if isinstance(exc, RuntimeError) else "CDC smoke failed; inspect source and connector state"
        print(message, file=sys.stderr)
        sys.exit(1)
