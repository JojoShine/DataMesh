#!/usr/bin/env python3
import argparse
import subprocess
import sys
import secrets
from urllib.parse import urlsplit, urlunsplit
import uuid

from dev_env import ENV_FILE, ROOT, compose, load_env


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument("--run",default="")
    parser.add_argument("--timeout", type=int, default=420, help="Timeout for each migration/test command, in seconds")
    parser.add_argument("--postgres-cdc", action="store_true", help="Provision an isolated PostgreSQL CDC source regardless of test-name filter")
    parser.add_argument("--no-race", action="store_true", help="Run external-engine acceptance without race instrumentation; race checks remain the default")
    args=parser.parse_args()
    if args.timeout < 1:
        parser.error("--timeout must be positive")
    name = "datamesh_test_" + uuid.uuid4().hex
    env = load_env()
    oracle_capture = "CDCRemoteCaptureOracle" in args.run or "CDCOracleWorkTable" in args.run
    def database_url(value):
        parts = urlsplit(value)
        return urlunsplit(parts._replace(path="/" + name))
    env["TEST_DSN"] = database_url(env["MIGRATION_DSN"])
    env["TEST_APP_DSN"] = database_url(env["APP_DSN"])
    env["MIGRATION_DSN"] = env["TEST_DSN"]
    if not args.run or "Multiformat" in args.run or "MySQL" in args.run:
        compose("up", "-d", "--wait", "--wait-timeout", "180", "mysql-source")
    if "Oracle" in args.run:
        env["ORACLE_TEST_PASSWORD"] = secrets.token_hex(24)
        env["ORACLE_TEST_USER"] = ("C##DMT_" if oracle_capture else "DMTEST_") + uuid.uuid4().hex[:16].upper()
        compose("up", "-d", "--wait", "--wait-timeout", "180", "oracle-source")
    created = False
    pg_cdc_created = False
    oracle_created = False
    mysql_created = False
    mysql_cdc_created = False
    base = ["docker", "compose", "--project-name", "datamesh", "--env-file", str(ENV_FILE)]
    def oracle_sql(sql,container="FREEPDB1"):
        result = subprocess.run(base+["exec","-T","oracle-source","sqlplus","-s","/","as","sysdba"], input="set echo off\nwhenever sqlerror exit failure\nalter session set container="+container+";\n"+sql+"\nexit\n",cwd=ROOT,text=True,capture_output=True,timeout=30)
        if result.returncode: raise RuntimeError("Oracle fixture setup or cleanup failed; credentials omitted")
    def mysql_sql(sql):
        result=subprocess.run(base+["exec","-T","mysql-source","sh","-c",'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot'],input=sql,cwd=ROOT,text=True,capture_output=True,timeout=30)
        if result.returncode:raise RuntimeError("MySQL isolated fixture setup or cleanup failed; credentials omitted")
    try:
        if args.postgres_cdc or not args.run or "CDCControl" in args.run or "CDCRemoteCapturePostgres" in args.run or "CDCLegacy" in args.run:
            compose("up", "-d", "--wait", "--wait-timeout", "180", "postgres-source")
            if args.postgres_cdc or not args.run or "CDCRemoteCapture" in args.run:
                compose("up", "-d", "--wait", "--wait-timeout", "180", "connect")
            compose("exec", "-T", "postgres-source", "createdb", "-U", "source_owner", name)
            pg_cdc_created = True
            source_origin = urlsplit(env["TEST_DSN"])
            from urllib.parse import quote
            env["CDC_TEST_SOURCE_DSN"] = urlunsplit(source_origin._replace(netloc="source_owner:"+quote(env["SOURCE_DB_PASSWORD"], safe="")+"@postgres-source:5432"))
        if not args.run or "MySQL" in args.run:
            env["MYSQL_ETL_TEST_DATABASE"]=name
            env["MYSQL_ETL_TEST_USER"]="etlt_"+uuid.uuid4().hex[:16]
            env["MYSQL_ETL_TEST_PASSWORD"]=secrets.token_hex(24)
            mysql_sql(f"CREATE DATABASE `{name}` CHARACTER SET utf8mb4; CREATE USER '{env['MYSQL_ETL_TEST_USER']}'@'%' IDENTIFIED BY '{env['MYSQL_ETL_TEST_PASSWORD']}'; GRANT ALL ON `{name}`.* TO '{env['MYSQL_ETL_TEST_USER']}'@'%';")
            mysql_created=True
            if "CDCRemoteCaptureMySQL" in args.run:
                compose("up", "-d", "--wait", "--wait-timeout", "180", "connect")
                mysql_sql(f"GRANT RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '{env['MYSQL_ETL_TEST_USER']}'@'%';")
            if "CDCMySQL" in args.run:
                env["MYSQL_CDC_WRITE_USER"]="cdct_"+uuid.uuid4().hex[:16]
                env["MYSQL_CDC_WRITE_PASSWORD"]=secrets.token_hex(24)
                mysql_sql(f"CREATE USER '{env['MYSQL_CDC_WRITE_USER']}'@'%' IDENTIFIED BY '{env['MYSQL_CDC_WRITE_PASSWORD']}';")
                mysql_cdc_created=True
                mysql_sql(f"GRANT INSERT,UPDATE,DELETE ON `{name}`.* TO '{env['MYSQL_CDC_WRITE_USER']}'@'%';")
        if "Oracle" in args.run:
            if oracle_capture:
                compose("up", "-d", "--wait", "--wait-timeout", "180", "connect")
                oracle_sql(f'CREATE USER {env["ORACLE_TEST_USER"]} IDENTIFIED BY "{env["ORACLE_TEST_PASSWORD"]}" CONTAINER=ALL;',"CDB$ROOT")
                oracle_created = True
                grants=["CREATE SESSION","SET CONTAINER","SELECT_CATALOG_ROLE","EXECUTE_CATALOG_ROLE","SELECT ANY TRANSACTION","LOGMINING","CREATE TABLE","LOCK ANY TABLE","CREATE SEQUENCE"]
                views=["DATABASE","LOG","LOG_HISTORY","LOGMNR_LOGS","LOGMNR_CONTENTS","LOGMNR_PARAMETERS","LOGFILE","ARCHIVED_LOG","ARCHIVE_DEST_STATUS","TRANSACTION","MYSTAT","STATNAME"]
                commands=[f'GRANT {grant} TO {env["ORACLE_TEST_USER"]} CONTAINER=ALL;' for grant in grants]
                commands += [f'GRANT SELECT ON V_${view} TO {env["ORACLE_TEST_USER"]} CONTAINER=ALL;' for view in views]
                commands += [f'GRANT EXECUTE ON {package} TO {env["ORACLE_TEST_USER"]} CONTAINER=ALL;' for package in ["DBMS_LOGMNR","DBMS_LOGMNR_D"]]
                oracle_sql("\n".join(commands),"CDB$ROOT")
                oracle_sql(f'ALTER USER {env["ORACLE_TEST_USER"]} QUOTA 10M ON USERS CONTAINER=ALL;',"CDB$ROOT")
                if "CDCOracleWorkTable" in args.run:
                    oracle_sql(f'ALTER USER {env["ORACLE_TEST_USER"]} QUOTA 0 ON USERS;',"CDB$ROOT")
            else:
                oracle_sql(f'CREATE USER {env["ORACLE_TEST_USER"]} IDENTIFIED BY "{env["ORACLE_TEST_PASSWORD"]}" DEFAULT TABLESPACE USERS QUOTA 10M ON USERS;')
                oracle_created = True
                oracle_sql(f'GRANT CREATE SESSION, CREATE TABLE TO {env["ORACLE_TEST_USER"]};')
        compose("exec", "-T", "postgres", "createdb", "-U", "datamesh_owner", name)
        created = True
        base = ["docker", "compose", "--project-name", "datamesh", "--env-file", str(ENV_FILE)]
        race_args = [] if args.no_race else ["-race"]
        for command in [base + ["run", "--rm", "migrate"], base + ["run", "--rm", "-e", "CDC_TEST_SOURCE_DSN", "-e", "MYSQL_ETL_TEST_DATABASE", "-e", "MYSQL_ETL_TEST_USER", "-e", "MYSQL_ETL_TEST_PASSWORD", "-e", "MYSQL_CDC_WRITE_USER", "-e", "MYSQL_CDC_WRITE_PASSWORD", "go-tools", "go", "test"] + race_args + ["-count=1", "-timeout", str(args.timeout) + "s", "./tests/integration/..."] + (["-run",args.run] if args.run else [])]:
            result = subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, timeout=args.timeout)
            output = result.stdout + result.stderr
            for key, value in env.items():
                if any(word in key for word in ["PASSWORD", "DSN", "SECRET", "KEY"]) and len(value) > 8:
                    output = output.replace(value, "[redacted]")
            print(output, end="")
            if result.returncode:
                raise RuntimeError("Isolated identity integration failed")
        print("Identity tests passed on real PostgreSQL; temporary database removed after use.")
    finally:
        if pg_cdc_created:
            compose("exec", "-T", "postgres-source", "dropdb", "--force", "-U", "source_owner", name)
        if mysql_cdc_created:
            mysql_sql(f"DROP USER '{env['MYSQL_CDC_WRITE_USER']}'@'%';")
        if mysql_created:
            mysql_sql(f"DROP DATABASE `{name}`; DROP USER '{env['MYSQL_ETL_TEST_USER']}'@'%';")
        if oracle_created:
            oracle_sql(f'DROP USER {env["ORACLE_TEST_USER"]} CASCADE;',"CDB$ROOT" if oracle_capture else "FREEPDB1")
        if created:
            compose("exec", "-T", "postgres", "dropdb", "--force", "-U", "datamesh_owner", name)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Identity integration failed; no credentials logged", file=sys.stderr)
        sys.exit(1)
