#!/usr/bin/env python3
"""Validate fresh snapshots at the final version without resetting saved source offsets."""
import json
import argparse
from pathlib import Path
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from dev_env import compose, load_env  # noqa: E402
from cdc import api, ensure_connector, execute, export, normalized  # noqa: E402


def validate(source):
    suffix = uuid.uuid4().hex[:8]
    prefix = "dm_fresh_" + source + "_" + suffix
    probe_id = 2000000 + int(suffix[:6], 16)
    created_id = probe_id + 1
    config = json.loads((ROOT / f"configs/connectors/{source}.json").read_text())
    config["database.password"] = load_env()["SOURCE_CDC_PASSWORD"]
    config["topic.prefix"] = prefix
    if source == "postgres":
        config["slot.name"] = "dm_fresh_" + suffix
        config["slot.drop.on.stop"] = "true"
        table, timestamp = "customers", "timestamp '2026-10-15 00:00:00'"
        topic = prefix + ".public.customers"
        expected_sql = "SELECT json_agg(json_build_object('id',id,'name',name,'amount',amount::text) ORDER BY id) FROM customers"
    else:
        config["database.server.id"] = str(42000000 + int(suffix[:6], 16))
        config["schema.history.internal.kafka.topic"] = prefix + ".schema_history"
        table, timestamp = "sample.customers", "'2026-10-15 00:00:00'"
        topic = prefix + ".sample.customers"
        expected_sql = "SELECT JSON_OBJECT('id',id,'name',name,'amount',CAST(amount AS CHAR)) FROM sample.customers ORDER BY id"
    if prefix in api("GET", "/connectors"):
        raise RuntimeError("Fresh capture namespace exists; no task replaced")
    if execute(source, f"SELECT COUNT(*) FROM {table} WHERE id IN ({probe_id},{created_id})").strip() != "0":
        raise RuntimeError("Probe keys already exist; no source rows changed")
    execute(source, f"INSERT INTO {table} (id,name,amount,updated_at) VALUES ({probe_id}, 'snapshot-delete-probe', 1.123456, {timestamp})")
    try:
        compose("exec", "-T", "kafka", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "kafka:9092", "--create", "--if-not-exists", "--topic", topic, "--partitions", "1", "--replication-factor", "1", capture=True)
        ensure_connector(prefix, config)
        first, _ = export(topic, source + "-fresh")
        snapshots = [item for item in first if item["raw_event"]["op"] == "r"]
        if not snapshots or any(item["raw_event"]["source"]["version"] != "3.6.3.Final" for item in snapshots):
            raise RuntimeError("Fresh snapshot was not produced by final Debezium version")
        marker = "fresh-" + suffix
        execute(source, f"UPDATE {table} SET name='{marker}' WHERE id=1; DELETE FROM {table} WHERE id={probe_id}; INSERT INTO {table} (id,name,amount,updated_at) VALUES ({created_id}, 'fresh-created-probe', 99.123456, {timestamp})")
        records, output = export(topic, source + "-fresh")
        state, operations = {}, set()
        for item in records:
            event = item["raw_event"]
            operations.add(event["op"])
            key = int(item["key"]["id"])
            if event["op"] == "d":
                state.pop(key, None)
            else:
                state[key] = normalized(event["after"])
        raw = execute(source, expected_sql)
        rows = json.loads(raw) if source == "postgres" else [json.loads(line) for line in raw.splitlines()]
        expected = {int(row["id"]): normalized(row) for row in rows}
        if state != expected or not {"r", "c", "u", "d"}.issubset(operations) or state[1]["name"] != marker:
            raise RuntimeError("Fresh final-version reconciliation failed")
        result = {"source": source, "result": "passed", "capture_version": "3.6.3.Final", "new_snapshot": True,
                  "snapshot_rows": len(snapshots), "operations": sorted(operations), "final_rows_at_cutoff": len(state),
                  "topic": topic, "raw_export": str(output.relative_to(ROOT)),
                  "scope": "fresh snapshot then I/U/D at final version; temporary capture and probe rows cleaned after cutoff"}
    finally:
        if prefix in api("GET", "/connectors"):
            api("DELETE", "/connectors/" + prefix)
        if source == "postgres":
            slot = config["slot.name"]
            for _ in range(30):
                active = execute(source, f"SELECT active::text FROM pg_replication_slots WHERE slot_name='{slot}'").strip()
                if active == "":
                    break
                if active == "false":
                    execute(source, f"SELECT pg_drop_replication_slot('{slot}')")
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError("Temporary source slot is still active; not forcibly dropped")
        execute(source, f"DELETE FROM {table} WHERE id IN ({probe_id},{created_id})")
    (ROOT / f".local/smoke/{source}-fresh-result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("source", nargs="?", default="all", choices=["all", "postgres", "mysql"])
    args = parser.parse_args()
    for source in (["postgres", "mysql"] if args.source == "all" else [args.source]):
        validate(source)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Fresh version-matrix validation failed", file=sys.stderr)
        sys.exit(1)
