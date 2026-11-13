package workflow

import (
	"context"
	"database/sql"
	"strings"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

func cdcProbeSource(src SourceSecret, pdb string) (SourceSecret, error) {
	if pdb == "" {
		if src.Kind == "oracle" {
			return src, problem(400, "CDC_PDB_REQUIRED", "请填写采集表所在的目标 PDB")
		}
		return src, nil
	}
	if src.Kind != "oracle" || !valid(pdb, 128) || strings.ContainsAny(pdb, "\x00\r\n") {
		return src, invalid
	}
	if strings.EqualFold(src.Database, pdb) {
		return src, problem(400, "CDC_CDB_REQUIRED", "CDC 数据源需登记 CDB 服务，并单独填写目标 PDB；SQL 查询使用的 PDB 数据源不能直接当成 CDB")
	}
	src.Database = pdb
	return src, nil
}

func (s *Server) cdcSourceForRead(ctx context.Context, tx *gorm.DB, p identity.Principal, id, pdb string) (Record, SourceSecret, *sql.DB, error) {
	record, src, db, e := s.sourceForRead(ctx, tx, p, id)
	if e != nil {
		return record, src, db, e
	}
	probe, e := cdcProbeSource(src, pdb)
	if e != nil {
		db.Close()
		return record, src, nil, e
	}
	if src.Kind == "oracle" {
		db.Close()
		db, e = s.runtime.database(ctx, tx, probe)
	}
	return record, src, db, e
}

func cdcOracleMiningCheck(ctx context.Context, db *sql.DB, b CDCBinding, c preflightCheck) preflightCheck {
	c.Expected = "LogMiner 账号、目标 PDB 及表日志满足要求"
	c.VerifySQL = "SELECT SYS_CONTEXT('USERENV','CON_NAME') FROM DUAL;\nSELECT PRIVILEGE FROM SESSION_PRIVS WHERE PRIVILEGE IN ('CREATE SESSION','SET CONTAINER','SELECT ANY TRANSACTION','LOGMINING');\nSELECT OWNER,TABLE_NAME,LOG_GROUP_TYPE FROM ALL_LOG_GROUPS WHERE LOG_GROUP_TYPE='ALL COLUMN LOGGING';"
	c.Steps = []string{"数据源登记 CDB 服务，单独填写目标 PDB；可信范围需同时允许这两个服务名", "由 DBA 按当前 Oracle 和 Debezium 版本配置公共采集账号的 LogMiner 权限和动态视图读取权限", "为所选表配置主键、SELECT/FLASHBACK 权限和 ALL COLUMNS 补充日志", "检查成功后启动采集；运行状态和快照事件仍需分别核对"}
	var container string
	if e := db.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','CON_NAME') FROM DUAL").Scan(&container); e != nil || !strings.EqualFold(container, b.PDB) {
		return cdcOutcome(c, "failed", "PDB 未确认", "连接未落在所选 PDB，请核对 CDB/PDB 服务配置")
	}
	var privileges, packages, logging int
	e := db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT PRIVILEGE) FROM SESSION_PRIVS WHERE PRIVILEGE IN ('CREATE SESSION','SET CONTAINER','SELECT ANY TRANSACTION','LOGMINING')").Scan(&privileges)
	if e != nil {
		return cdcOutcome(c, "unknown", "权限未确认", "无法读取采集账号权限，请 DBA 核对")
	}
	if privileges != 4 {
		return cdcOutcome(c, "failed", "缺少 LogMiner 基础权限", "请 DBA 核对 CREATE SESSION、SET CONTAINER、SELECT ANY TRANSACTION 和 LOGMINING 权限")
	}
	e = db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT TABLE_NAME) FROM ALL_TAB_PRIVS WHERE TABLE_SCHEMA='SYS' AND TABLE_NAME IN ('DBMS_LOGMNR','DBMS_LOGMNR_D') AND PRIVILEGE='EXECUTE' AND (GRANTEE=USER OR GRANTEE='PUBLIC' OR GRANTEE IN (SELECT ROLE FROM SESSION_ROLES))").Scan(&packages)
	if e != nil || packages != 2 {
		return cdcOutcome(c, "unknown", "LogMiner 包执行权限未确认", "请 DBA 核对 DBMS_LOGMNR 和 DBMS_LOGMNR_D 的 EXECUTE 授权")
	}
	for _, view := range []string{"DATABASE", "LOG", "LOG_HISTORY", "LOGMNR_LOGS", "LOGMNR_CONTENTS", "LOGMNR_PARAMETERS", "LOGFILE", "ARCHIVED_LOG", "ARCHIVE_DEST_STATUS", "TRANSACTION", "MYSTAT", "STATNAME"} {
		rows, err := db.QueryContext(ctx, "SELECT 1 FROM V$"+view+" WHERE 1=0")
		if err != nil {
			return cdcOutcome(c, "unknown", "动态视图读取未确认", "请 DBA 核对 V$"+view+" 的读取权限")
		}
		if err = rows.Close(); err != nil {
			return cdcOutcome(c, "unknown", "查询未完成", "请核对数据库连接后重试")
		}
	}
	e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ALL_LOG_GROUPS WHERE OWNER=:1 AND TABLE_NAME=:2 AND LOG_GROUP_TYPE='ALL COLUMN LOGGING'", b.Schema, b.Table).Scan(&logging)
	if e != nil {
		return cdcOutcome(c, "unknown", "表日志未确认", "无法查询所选表补充日志，请 DBA 核对")
	}
	if logging == 0 {
		return cdcOutcome(c, "failed", "所选表缺少完整补充日志", "请 DBA 为所选表配置 ALL COLUMNS 补充日志")
	}
	return cdcOutcome(c, "passed", "账号、PDB 和补充日志已核对", "当前只读检查通过；实际 LogMiner 运行及首次快照仍以作业状态和采集事件为准")
}

func (s *Server) cdcOracleWorkTableCheck(ctx context.Context, metadata *gorm.DB, src SourceSecret) preflightCheck {
	check := cdcRequirement("mining_work_table", "Oracle 采集工作表创建条件", "CDB 默认表空间允许创建采集工作表", "请 DBA 核对公共采集账号的 CREATE TABLE 权限和默认表空间额度", "SELECT PRIVILEGE FROM SESSION_PRIVS WHERE PRIVILEGE IN ('CREATE TABLE','UNLIMITED TABLESPACE');\nSELECT DEFAULT_TABLESPACE FROM USER_USERS;\nSELECT TABLESPACE_NAME,BYTES,MAX_BYTES FROM USER_TS_QUOTAS;", "在 CDB 中核对公共采集账号的默认表空间和剩余额度；每个新任务会创建独立的 DMF_ 前缀 LogMiner 工作表", "由 DBA 按最小权限授予 CREATE TABLE；示例：GRANT CREATE TABLE TO <公共采集账号> CONTAINER=ALL;", "按实际并发任务数量配置额度；示例：ALTER USER <公共采集账号> QUOTA 10M ON <默认表空间> CONTAINER=ALL;", "完成后重新检查；平台不会自动创建账号、扩大权限或授予无限表空间")
	db, e := s.runtime.database(ctx, metadata, src)
	if e != nil {
		return cdcOutcome(check, "unknown", "CDB 未连接", "无法核查 CDB 工作表权限及表空间额度，请先恢复连接")
	}
	defer db.Close()
	var create, unlimited int
	e = db.QueryRowContext(ctx, "SELECT COUNT(CASE WHEN PRIVILEGE='CREATE TABLE' THEN 1 END),COUNT(CASE WHEN PRIVILEGE='UNLIMITED TABLESPACE' THEN 1 END) FROM SESSION_PRIVS WHERE PRIVILEGE IN ('CREATE TABLE','UNLIMITED TABLESPACE')").Scan(&create, &unlimited)
	if e != nil {
		return cdcOutcome(check, "unknown", "权限未确认", "无法核查 CDB 工作表创建权限，请 DBA 核对")
	}
	if create == 0 {
		return cdcOutcome(check, "failed", "缺少 CREATE TABLE", "CDB 公共采集账号缺少 CREATE TABLE，无法创建新任务的 LogMiner 工作表")
	}
	if unlimited > 0 {
		return cdcOutcome(check, "passed", "已有创建权限及表空间权限", "当前账号具备工作表创建条件；实际空间和运行仍以数据库状态为准")
	}
	var free int64
	e = db.QueryRowContext(ctx, "SELECT NVL(MAX(CASE WHEN MAX_BYTES=-1 THEN -1 ELSE MAX_BYTES-BYTES END),0) FROM USER_TS_QUOTAS WHERE TABLESPACE_NAME IN (SELECT DEFAULT_TABLESPACE FROM USER_USERS)").Scan(&free)
	if e != nil {
		return cdcOutcome(check, "unknown", "额度未确认", "无法核查 CDB 默认表空间额度，请 DBA 核对 USER_TS_QUOTAS")
	}
	if free == 0 || free < -1 {
		return cdcOutcome(check, "failed", "默认表空间额度不足", "CDB 默认表空间缺少可用额度，新建 LogMiner 工作表会失败，请 DBA 配置 QUOTA")
	}
	return cdcOutcome(check, "passed", "创建权限及可用额度已确认", "当前具备创建条件；请 DBA 根据任务数量持续核对表空间实际可用容量")
}
