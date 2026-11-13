#!/usr/bin/env python3
"""Prepare and test the dedicated local Oracle Free fixture, never an existing source."""
import json
from pathlib import Path, PurePosixPath
import subprocess
import sys
import time
from urllib.error import HTTPError
import uuid

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from dev_env import load_env, compose  # noqa: E402
from cdc import ensure_connector, export, normalized  # noqa: E402


def sql(statement, container="CDB$ROOT"):
    env = load_env()
    text = f"SET ECHO OFF VERIFY OFF HEADING OFF FEEDBACK OFF PAGESIZE 0 LINESIZE 32767 TRIMSPOOL ON\nWHENEVER SQLERROR EXIT SQL.SQLCODE\nALTER SESSION SET CONTAINER = {container};\n{statement}\nEXIT;\n"
    command = ["docker", "compose", "--project-name", "datamesh", "--env-file", str(ROOT / ".local/runtime.env"),
               "exec", "-T", "-e", "NLS_LANG=.AL32UTF8", "oracle-source", "sqlplus", "-s", "/", "as", "sysdba"]
    result = subprocess.run(command, input=text, capture_output=True, text=True, timeout=120, env=env, cwd=ROOT)
    if result.returncode:
        diagnostic = result.stdout + result.stderr
        for key, value in env.items():
            if any(part in key for part in ["PASSWORD", "SECRET", "DSN", "KEY"]) and len(value) > 8:
                diagnostic = diagnostic.replace(value, "[redacted]")
        (ROOT / ".local/smoke/oracle-diagnostic.log").write_text(diagnostic)
        raise RuntimeError("Oracle fixture SQL failed; see redacted .local/smoke/oracle-diagnostic.log")
    return result.stdout.strip()


def prepare():
    env = load_env()
    if sql("SELECT log_mode FROM v$database;") != "ARCHIVELOG":
        compose("exec", "-T", "oracle-source", "mkdir", "-p", "/opt/oracle/oradata/recovery_area", capture=True)
        sql("ALTER SYSTEM SET db_recovery_file_dest_size=2G SCOPE=BOTH;\nALTER SYSTEM SET db_recovery_file_dest='/opt/oracle/oradata/recovery_area' SCOPE=BOTH;")
        sql("SHUTDOWN IMMEDIATE;\nSTARTUP MOUNT;\nALTER DATABASE ARCHIVELOG;\nALTER DATABASE OPEN;\nALTER PLUGGABLE DATABASE ALL OPEN;")
    if sql("SELECT supplemental_log_data_min FROM v$database;") not in ("YES", "IMPLICIT"):
        sql("ALTER DATABASE ADD SUPPLEMENTAL LOG DATA;")
    for container in ["CDB$ROOT", "FREEPDB1"]:
        if int(sql("SELECT COUNT(*) FROM dba_tablespaces WHERE tablespace_name='LOGMINER_TBS';", container)) == 0:
            system_file = sql("SELECT file_name FROM dba_data_files WHERE tablespace_name='SYSTEM' FETCH FIRST 1 ROW ONLY;", container)
            directory = PurePosixPath(system_file).parent
            if not str(directory).startswith("/opt/oracle/oradata/"):
                raise RuntimeError("Oracle fixture data directory is outside the expected volume")
            sql(f"CREATE TABLESPACE logminer_tbs DATAFILE '{directory}/datamesh_logminer.dbf' SIZE 25M AUTOEXTEND ON MAXSIZE 100M;", container)
    if int(sql("SELECT COUNT(*) FROM dba_users WHERE username='C##DATAMESH_CDC';")) == 0:
        sql(f'CREATE USER C##DATAMESH_CDC IDENTIFIED BY "{env["SOURCE_CDC_PASSWORD"]}" DEFAULT TABLESPACE logminer_tbs QUOTA 100M ON logminer_tbs CONTAINER=ALL;')
    grants = ["CREATE SESSION", "SET CONTAINER", "SELECT_CATALOG_ROLE", "EXECUTE_CATALOG_ROLE", "SELECT ANY TRANSACTION", "LOGMINING", "CREATE TABLE", "LOCK ANY TABLE", "CREATE SEQUENCE"]
    sql("\n".join(f"GRANT {grant} TO C##DATAMESH_CDC CONTAINER=ALL;" for grant in grants))
    views = ["DATABASE", "LOG", "LOG_HISTORY", "LOGMNR_LOGS", "LOGMNR_CONTENTS", "LOGMNR_PARAMETERS", "LOGFILE", "ARCHIVED_LOG", "ARCHIVE_DEST_STATUS", "TRANSACTION", "MYSTAT", "STATNAME"]
    sql("\n".join(f"GRANT SELECT ON V_${view} TO C##DATAMESH_CDC CONTAINER=ALL;" for view in views) + "\nGRANT EXECUTE ON DBMS_LOGMNR TO C##DATAMESH_CDC CONTAINER=ALL;\nGRANT EXECUTE ON DBMS_LOGMNR_D TO C##DATAMESH_CDC CONTAINER=ALL;")
    if int(sql("SELECT COUNT(*) FROM dba_tables WHERE owner='DATAMESH_SOURCE' AND table_name='CUSTOMERS';", "FREEPDB1")) == 0:
        sql("ALTER USER DATAMESH_SOURCE QUOTA 100M ON USERS;\nCREATE TABLE DATAMESH_SOURCE.CUSTOMERS (ID NUMBER(18) PRIMARY KEY, NAME VARCHAR2(100 CHAR), AMOUNT NUMBER(20,6));\nINSERT INTO DATAMESH_SOURCE.CUSTOMERS VALUES (1,'样例客户',1234.567890);\nINSERT INTO DATAMESH_SOURCE.CUSTOMERS VALUES (2,NULL,0.000001);\nINSERT INTO DATAMESH_SOURCE.CUSTOMERS VALUES (3,'删除样例',NULL);\nCOMMIT;\nALTER TABLE DATAMESH_SOURCE.CUSTOMERS ADD SUPPLEMENTAL LOG DATA (ALL) COLUMNS;", "FREEPDB1")
    sql("GRANT SELECT, FLASHBACK ON DATAMESH_SOURCE.CUSTOMERS TO C##DATAMESH_CDC;", "FREEPDB1")


def main():
    directory = ROOT / ".local/smoke"
    directory.mkdir(exist_ok=True)
    prepare()
    name, topic = "datamesh-oracle", "datamesh_oracle.DATAMESH_SOURCE.CUSTOMERS"
    compose("exec", "-T", "kafka", "/opt/kafka/bin/kafka-topics.sh", "--bootstrap-server", "kafka:9092", "--create", "--if-not-exists", "--topic", topic, "--partitions", "1", "--replication-factor", "1", capture=True)
    config = json.loads((ROOT / "configs/connectors/oracle.json").read_text())
    config["database.password"] = load_env()["SOURCE_CDC_PASSWORD"]
    ensure_connector(name, config)
    events, _ = export(topic, "oracle")
    if not any(event["raw_event"]["op"] == "r" for event in events):
        raise RuntimeError("Oracle snapshot evidence missing")
    marker = "更新-" + uuid.uuid4().hex[:8]
    sql(f"UPDATE DATAMESH_SOURCE.CUSTOMERS SET NAME='{marker}' WHERE ID=1;\nDELETE FROM DATAMESH_SOURCE.CUSTOMERS WHERE ID=2;\nMERGE INTO DATAMESH_SOURCE.CUSTOMERS target USING (SELECT 4 ID FROM DUAL) source ON (target.ID=source.ID) WHEN MATCHED THEN UPDATE SET NAME='新增样例' WHEN NOT MATCHED THEN INSERT (ID,NAME,AMOUNT) VALUES(4,'新增样例',99.123456);\nCOMMIT;", "FREEPDB1")
    events, output = export(topic, "oracle")
    state, operations = {}, set()
    for item in events:
        event = item["raw_event"]
        operations.add(event["op"])
        key = int(item["key"]["ID"])
        if event["op"] == "d":
            state.pop(key, None)
        else:
            row = {key.lower(): value for key, value in event["after"].items()}
            state[key] = normalized(row)
    text = sql("SELECT JSON_OBJECT('id' VALUE ID, 'name' VALUE NAME, 'amount' VALUE TO_CHAR(AMOUNT,'FM99999999999990D000000','NLS_NUMERIC_CHARACTERS=''.,''')) FROM DATAMESH_SOURCE.CUSTOMERS ORDER BY ID;", "FREEPDB1")
    expected = {int(row["id"]): normalized(row) for row in [json.loads(line) for line in text.splitlines()]}
    if expected != state or not {"r", "c", "u", "d"}.issubset(operations) or state[1]["name"] != marker:
        raise RuntimeError("Oracle snapshot/CDC final-state reconciliation failed")
    result = {"source": "oracle", "result": "passed", "topic": topic, "operations": sorted(operations), "events": len(events), "final_rows": len(state), "first_offset": events[0]["offset"], "last_offset": events[-1]["offset"], "raw_export": str(output.relative_to(ROOT)), "scope": "Oracle Free CDB/PDB snapshot plus LogMiner I/U/D; remaining acceptance cases separate"}
    (directory / "oracle-result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, HTTPError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Oracle smoke failed; inspect redacted fixture diagnostics", file=sys.stderr)
        sys.exit(1)
