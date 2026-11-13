package workflow

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"strings"

	"gorm.io/gorm"
)

type cdcScope struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

func (v cdcScope) valid() bool {
	return (v.Schema == "" && v.Table == "") || (valid(v.Schema, 128) && valid(v.Table, 128) && !strings.ContainsAny(v.Schema+v.Table, "\x00\r\n"))
}
func cdcRequirement(code, label, expected, suggestion, query string, steps ...string) preflightCheck {
	return preflightCheck{Check: code, Label: label, Owner: "数据库管理员", Expected: expected, Suggestion: &suggestion, Steps: steps, VerifySQL: query}
}
func cdcOutcome(c preflightCheck, status, actual, reason string) preflightCheck {
	c.Status = status
	c.Passed = status == "passed"
	c.Actual = actual
	c.Reason = &reason
	return c
}
func cdcScalar(ctx context.Context, db *sql.DB, c preflightCheck) preflightCheck {
	var value string
	query := c.query
	if query == "" {
		query = c.VerifySQL
	}
	if err := db.QueryRowContext(ctx, query, c.args...).Scan(&value); err != nil {
		return cdcOutcome(c, "unknown", "未获取", "无法读取检查值，可能涉及系统视图权限或查询超时；请数据库管理员执行下方只读命令确认")
	}
	if value == c.Expected {
		return cdcOutcome(c, "passed", value, "当前检查项满足条件")
	}
	return cdcOutcome(c, "failed", value, "当前值未满足预期，请按处理步骤完成后重新检查")
}
func cdcLiteral(kind, v string) string {
	if kind == "mysql" {
		return "CONVERT(0x" + hex.EncodeToString([]byte(v)) + " USING utf8mb4)"
	}
	return "E'" + strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "'", "''") + "'"
}
func cdcRequirements(kind string, scope cdcScope, local bool) []preflightCheck {
	switch kind {
	case "postgres":
		filter := ""
		if scope.Table != "" {
			filter = " AND n.nspname=" + cdcLiteral(kind, scope.Schema) + " AND c.relname=" + cdcLiteral(kind, scope.Table)
		}
		identity := "(c.relreplident IN ('f','i') OR (c.relreplident='d' AND EXISTS(SELECT 1 FROM pg_catalog.pg_index i WHERE i.indrelid=c.oid AND i.indisprimary)))"
		tableSQL := "SELECT CASE WHEN count(*)=0 THEN '未找到可读普通表' WHEN bool_and(" + identity + ") THEN '所查表均具备复制标识' ELSE '缺少复制标识：'||left(string_agg(n.nspname||'.'||c.relname,', ') FILTER (WHERE NOT " + identity + "),500) END FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND has_schema_privilege(n.oid,'USAGE') AND has_table_privilege(c.oid,'SELECT')" + filter + ";"
		rows := []preflightCheck{
			cdcRequirement("wal_level", "逻辑复制日志", "logical", "在实例参数中开启逻辑复制后重新检查", "SHOW wal_level;", "在数据库配置文件或云数据库参数组中将 wal_level 设置为 logical", "确认是否需要重启；需要时由 DBA 安排维护窗口并重启实例", "重新执行 SHOW wal_level，确认返回 logical"),
			cdcRequirement("replication_account", "采集账号复制权限", "具备复制权限", "为专用采集账号配置登录、复制和所选表读取权限", "SELECT CASE WHEN rolreplication OR rolsuper THEN '具备复制权限' ELSE '缺少复制权限' END FROM pg_roles WHERE rolname=current_user;", "使用专用采集账号，由 DBA 授予 LOGIN 和 REPLICATION 属性，以及目标库 CONNECT、schema USAGE、所选表 SELECT 权限", "在 pg_hba.conf 或云数据库访问控制中允许采集服务使用该账号连接目标数据库", "使用相同采集账号重新执行下方检查，不需要授予超级用户权限"),
			cdcRequirement("replication_capacity", "复制槽及日志发送容量", "具备可用容量或本作业已有复制槽", "核对现有复制槽和发送连接，按采集并发预留容量", "SHOW max_replication_slots;\nSHOW max_wal_senders;\nSELECT slot_name,active FROM pg_replication_slots;\nSELECT count(*) FROM pg_stat_replication;", "对比现有占用，为新采集作业预留复制槽和 WAL sender 容量；已有本作业复制槽可复用", "如需调整 max_replication_slots / max_wal_senders，由 DBA 根据现有复制业务修改并安排必要的重启", "检查复制槽日志保留及磁盘容量，完成后重新预检"),
			cdcRequirement("table_identity", "表访问与复制标识", "所查表均具备复制标识", "核对目标表读取权限与主键或复制标识", tableSQL, "在建立任务中选择具体表后再次检查，可缩小本项检测范围", "为所选表配置主键或合适的复制标识；只有无法使用合适键时才考虑 REPLICA IDENTITY FULL", "账号不可见的表请由 DBA 核对目标库、schema USAGE 和表 SELECT 权限"),
		}
		if scope.Table != "" {
			rows[3].query = strings.TrimSuffix(tableSQL, filter+";") + " AND n.nspname=$1 AND c.relname=$2;"
			rows[3].args = []interface{}{scope.Schema, scope.Table}
		}
		publication := cdcRequirement("publication", "发布与采集配置", "发布名称及表范围与作业一致", "发布和复制槽需与平台采集配置配套，不要复用无关作业", "SELECT pubname FROM pg_publication;\nSELECT slot_name,plugin,active FROM pg_replication_slots;", "平台具备远程 CDC 作业创建能力后，由平台管理员与 DBA 确认发布、复制槽及所选表范围", "通过预检及配置核对后，再启动首次快照和持续增量采集")
		publication.Owner = "平台管理员 / 数据库管理员"
		if local {
			publication.VerifySQL = "SELECT CASE WHEN EXISTS(SELECT 1 FROM pg_publication_tables WHERE pubname='datamesh_publication' AND schemaname='public' AND tablename='customers') THEN '发布名称及表范围与作业一致' ELSE '样例发布未包含 public.customers' END;"
			publication.Steps = []string{"此入口绑定内置 public.customers 采集作业，由 DBA 检查 datamesh_publication 是否包含该表", "平台管理员核对 datamesh_slot 及 pgoutput 配置，保留已有位点后重新检查"}
		}
		return append(rows, publication)
	case "mysql":
		schema := scope.Schema
		tableFilter := ""
		if scope.Table != "" {
			tableFilter = " AND t.table_schema=" + cdcLiteral(kind, schema) + " AND t.table_name=" + cdcLiteral(kind, scope.Table)
		}
		tableSQL := "SELECT CASE WHEN count(*)=0 THEN '未找到可见普通表' WHEN sum(CASE WHEN EXISTS(SELECT 1 FROM information_schema.table_constraints k WHERE k.table_schema=t.table_schema AND k.table_name=t.table_name AND k.constraint_type='PRIMARY KEY') THEN 0 ELSE 1 END)=0 THEN '所查表均具备主键' ELSE '存在缺少主键的表' END FROM information_schema.tables t WHERE t.table_schema=DATABASE() AND t.table_type='BASE TABLE'" + tableFilter + ";"
		rows := []preflightCheck{
			cdcRequirement("binlog_enabled", "二进制日志", "1", "在实例参数中开启 binlog", "SELECT @@GLOBAL.log_bin;", "由 DBA 在配置文件或云数据库参数组中开启二进制日志", "需要重启时安排维护窗口，保留足够日志并重新检查"),
			cdcRequirement("binlog_format", "日志记录格式", "ROW", "将 binlog_format 配置为 ROW", "SELECT @@GLOBAL.binlog_format;", "由 DBA 在实例参数中配置 binlog_format=ROW", "确认全局及写入业务会话的实际生效情况后重新检查"),
			cdcRequirement("binlog_row_image", "行日志内容", "FULL", "将 binlog_row_image 配置为 FULL", "SELECT @@GLOBAL.binlog_row_image;", "由 DBA 在实例参数中配置 binlog_row_image=FULL", "确认写入会话已生效，并按业务写入量配置日志保留后重新检查"),
			cdcRequirement("replication_account", "采集账号权限", "直接授权满足日志采集要求", "由 DBA 核对直接授权和角色继承权限", "SELECT CASE WHEN count(DISTINCT privilege_type)=4 THEN '直接授权满足日志采集要求' ELSE '需核对直接授权及角色权限' END FROM information_schema.user_privileges WHERE grantee=CONCAT(QUOTE(SUBSTRING_INDEX(CURRENT_USER(),'@',1)),'@',QUOTE(SUBSTRING_INDEX(CURRENT_USER(),'@',-1))) AND privilege_type IN ('RELOAD','SHOW DATABASES','REPLICATION SLAVE','REPLICATION CLIENT');", "由 DBA 核对采集账号的 RELOAD、SHOW DATABASES、REPLICATION SLAVE、REPLICATION CLIENT 权限，以及所选表 SELECT 权限", "角色继承或云数据库受管权限无法由直接授权视图完整确认时，由 DBA 按实际账号检查", "权限处理后重新预检；具体首次快照锁权限仍须结合采集器版本核对"),
			cdcRequirement("table_identity", "表访问与主键", "所查表均具备主键", "确认所选表可读且具备主键", tableSQL, "选定具体表后检查；由 DBA 授予 SELECT 并确认主键", "补齐必要主键后重新预检"),
		}

		if scope.Table != "" {
			rows[4].query = strings.TrimSuffix(tableSQL, tableFilter+";") + " AND t.table_schema=? AND t.table_name=?;"
			rows[4].args = []interface{}{scope.Schema, scope.Table}
		}
		read := cdcRequirement("table_read", "所选表读取权限", "可读取所选表", "由 DBA 授予所选表及其字段的 SELECT 权限", "SHOW GRANTS FOR CURRENT_USER;", "在任务表单选择具体表后检查读取权限；仅有 INSERT/UPDATE 权限不能完成首次快照", "由 DBA 核对账号及角色的所选表 SELECT 权限，完成后重新检查")
		if scope.Table != "" && !strings.ContainsAny(scope.Schema+scope.Table, "\\/.") {
			read.VerifySQL = "SELECT * FROM " + quoteIdentifier("mysql", scope.Schema) + "." + quoteIdentifier("mysql", scope.Table) + " WHERE 1=0;"
		}
		return append(rows, read)
	case "oracle":
		return []preflightCheck{
			cdcRequirement("archive_log", "归档日志模式", "ARCHIVELOG", "由 DBA 开启 ARCHIVELOG 并配置归档日志保留", "SELECT LOG_MODE FROM V$DATABASE;", "由 DBA 检查归档目标、容量和保留策略", "需要切换归档模式时由 DBA 安排实例维护窗口，完成后执行下方只读验证"),
			cdcRequirement("supplemental_logging", "数据库补充日志", "已开启", "由 DBA 开启数据库级最小补充日志", "SELECT CASE WHEN SUPPLEMENTAL_LOG_DATA_MIN IN ('YES','IMPLICIT') THEN '已开启' ELSE '未开启' END FROM V$DATABASE;", "由 DBA 在正确的 CDB/PDB 上下文核对并配置最小补充日志", "每张采集表还需配置 ALL COLUMNS 补充日志，不能仅完成数据库级配置"),
			cdcRequirement("mining_account", "LogMiner 权限及表日志", "待 DBA 核对", "由 DBA 按 Oracle 版本、CDB/PDB 和 LogMiner 要求逐项确认", "SELECT PRIVILEGE FROM SESSION_PRIVS WHERE PRIVILEGE IN ('CREATE SESSION','LOGMINING','SELECT ANY TRANSACTION');\nSELECT OWNER,TABLE_NAME,LOG_GROUP_TYPE FROM ALL_LOG_GROUPS WHERE LOG_GROUP_TYPE='ALL COLUMN LOGGING';", "由 DBA 核对专用采集账号的 LogMiner、日志字典和动态性能视图访问权限", "确认采集表主键以及 ALL COLUMNS 补充日志，按版本核对 CDB 与 PDB 配置", "此权限清单需要 DBA 验证，平台不会凭普通 SQL 连接成功将其标记为已通过"),
		}
	}
	return nil
}
func cdcCapacity(ctx context.Context, db *sql.DB, c preflightCheck, local bool, bindingSlot ...string) preflightCheck {
	var slots, used, senders, active int
	var own, ownActive bool
	slot := ""
	if local {
		slot = "datamesh_slot"
	}
	if len(bindingSlot) > 0 {
		slot = bindingSlot[0]
	}
	err := db.QueryRowContext(ctx, "SELECT current_setting('max_replication_slots')::int,(SELECT count(*) FROM pg_replication_slots),current_setting('max_wal_senders')::int,(SELECT count(*) FROM pg_stat_replication),EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name=$1 AND slot_type='logical' AND plugin='pgoutput' AND database=current_database()),EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name=$1 AND slot_type='logical' AND plugin='pgoutput' AND database=current_database() AND active)", slot).Scan(&slots, &used, &senders, &active, &own, &ownActive)
	if err != nil {
		return cdcOutcome(c, "unknown", "未获取", "无法读取复制容量，请 DBA 执行下方检查命令确认")
	}
	actual := fmt.Sprintf("复制槽 %d/%d，发送连接 %d/%d", used, slots, active, senders)
	if slots > 0 && senders > 0 && (slots > used || own) && (senders > active || ownActive) {
		return cdcOutcome(c, "passed", actual, "当前具备新作业容量或已有本作业复制槽")
	}
	return cdcOutcome(c, "failed", actual, "没有确认到可供本作业使用的复制容量")
}
func (s *Server) cdcPlatformCheck(ctx context.Context, src SourceSecret, scope cdcScope) preflightCheck {
	c := cdcRequirement("platform_cdc", "平台采集作业", "已绑定可用采集作业", "联系平台管理员处理采集作业接入", "", "确认平台已实现此数据源的 CDC 作业创建及生命周期管理", "平台条件满足后重新检查；仅调整数据库参数不能解除平台能力限制")
	c.Owner = "平台管理员"
	if !localSourceTemplate(SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database}) || scope.Table != "" && scope.Schema+"."+scope.Table != qualified(src.Kind) {
		c.Expected = "对应采集驱动已安装"
		c.Steps = []string{"在 CDC 接入任务选择真实库表", "PostgreSQL 填写发布名；Oracle 登记 CDB 数据源并填写目标 PDB", "检查完整配置后保存草稿，再从任务列表启动"}
		plugins, e := s.runtime.request(ctx, "GET", "/connector-plugins", nil)
		var installed []struct {
			Class string `json:"class"`
		}
		if e != nil || json.Unmarshal(plugins, &installed) != nil {
			return cdcOutcome(c, "unknown", "未获取", "无法确认采集服务驱动，请平台管理员检查服务")
		}
		classes := map[string]string{"postgres": "io.debezium.connector.postgresql.PostgresConnector", "mysql": "io.debezium.connector.mysql.MySqlConnector", "oracle": "io.debezium.connector.oracle.OracleConnector"}
		for _, plugin := range installed {
			if plugin.Class == classes[src.Kind] {
				return cdcOutcome(c, "passed", "对应采集驱动已安装", "可在 CDC 接入任务配置独立作业；该检查不代表所选表的完整配置已通过")
			}
		}
		return cdcOutcome(c, "failed", "未找到对应驱动", "请平台管理员安装已锁定版本的数据库采集驱动后重新检查")
	}

	state, err := s.runtime.connector(ctx, src.Kind)
	if err != nil {
		return cdcOutcome(c, "unknown", "未获取", "采集服务不可达或作业不存在，请管理员检查采集服务和作业配置")
	}
	if !running(state) && state.Connector.State != "PAUSED" {
		return cdcOutcome(c, "failed", state.Connector.State, "已有采集作业处于异常状态，请检查连接器及任务日志后重新预检")
	}
	if src.Kind == "oracle" {
		config, e := s.runtime.request(ctx, "GET", "/connectors/datamesh-oracle/config", nil)
		credential, ce := s.runtime.open(src.Ciphertext)
		var actual map[string]string
		if e != nil || ce != nil || json.Unmarshal(config, &actual) != nil {
			return cdcOutcome(c, "unknown", "未获取", "无法核对 Oracle 采集器配置与登记账号")
		}
		if actual["database.user"] != credential.Username || actual["database.password"] != credential.Password {
			return cdcOutcome(c, "failed", "配置不一致", "Oracle 已有采集作业与登记账号不一致，请平台管理员核对加密凭据配置")
		}
	}
	return cdcOutcome(c, "passed", state.Connector.State, "已有采集作业可核对；暂停状态保留位点，不代表正在运行")
}
func cdcTableRead(ctx context.Context, db *sql.DB, c preflightCheck, scope cdcScope) preflightCheck {
	if scope.Table == "" {
		return cdcOutcome(c, "skipped", "尚未选择具体表", "请选择具体表后检查当前账号的真实读取权限")
	}
	if strings.ContainsAny(scope.Schema+scope.Table, "\\/.") {
		return cdcOutcome(c, "failed", "目标表名无效", "MySQL 库名/表名包含不支持的路径或分隔字符，请从真实库表列表重新选择")
	}
	rows, err := db.QueryContext(ctx, c.VerifySQL)
	if err == nil {
		if closeErr := rows.Close(); closeErr != nil {
			return cdcOutcome(c, "unknown", "读取验证未完成", "无法完成读取权限验证，请核对连接状态后重试")
		}
		return cdcOutcome(c, "passed", "可读取所选表", "已执行零行查询验证全部所选表字段的读取权限，未读取业务记录")
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) && (my.Number == 1142 || my.Number == 1143) {
		return cdcOutcome(c, "failed", "缺少 SELECT 权限", "账号缺少表或字段 SELECT 权限，无法完成首次数据快照")
	}
	return cdcOutcome(c, "unknown", "读取验证失败", "无法确认读取权限，请 DBA 核对表是否存在、访问授权及连接状态")
}
func (s *Server) cdcPreflight(ctx context.Context, metadata *gorm.DB, src SourceSecret, scope cdcScope) []preflightCheck {
	if readerSource(src.Kind) {
		return []preflightCheck{sourceCheck("platform_cdc", false, "该数据源当前提供只读接入和全量抽取，尚未提供日志CDC采集")}
	}
	local := localSourceTemplate(SourceInput{Kind: src.Kind, Host: src.Host, Port: src.Port, Database: src.Database})
	if local && scope.Table == "" {
		parts := strings.SplitN(qualified(src.Kind), ".", 2)
		scope = cdcScope{Schema: parts[0], Table: parts[1]}
	}
	bound := local && scope.Schema+"."+scope.Table == qualified(src.Kind)
	requirements := cdcRequirements(src.Kind, scope, bound)
	connection := cdcRequirement("authentication", "数据库连接", "连接成功", "先完成数据源测试连接，恢复连接后重新预检", "", "数据源维护人员核对可信白名单、地址、端口、数据库/服务名及登记凭据", "DBA 核对网络访问控制和账号登录权限，完成后在平台重新测试连接")
	connection.Owner = "数据源维护人员 / 数据库管理员"
	db, err := s.runtime.database(ctx, metadata, src)
	checks := []preflightCheck{}
	if err != nil {
		checks = append(checks, cdcOutcome(connection, "failed", "连接失败", "未建立数据库连接，后续数据库条件尚未检测"))
		for _, c := range requirements {
			checks = append(checks, cdcOutcome(c, "skipped", "尚未检测", "需先恢复数据库连接，再执行本项检查"))
		}
	} else {
		defer db.Close()
		checks = append(checks, cdcOutcome(connection, "passed", "连接成功", "使用已登记账号完成数据库连接"))
		for _, c := range requirements {
			switch {
			case c.Check == "table_read":
				c = cdcTableRead(ctx, db, c, scope)
			case c.Check == "replication_capacity":
				c = cdcCapacity(ctx, db, c, bound)
			case c.Check == "publication" && !bound:
				c = cdcOutcome(c, "skipped", "等待平台配置", "请在 CDC 接入任务填写发布名后检查完整配置；此入口未指定发布范围")
			case c.Check == "mining_account":
				c = cdcOutcome(c, "unknown", "需 DBA 验证", "普通 SQL 连接不能证明具备完整 LogMiner 及逐表补充日志权限，请按下方步骤由 DBA 确认")
			default:
				c = cdcScalar(ctx, db, c)
				if src.Kind == "mysql" && c.Check == "replication_account" && c.Status == "failed" {
					c = cdcOutcome(c, "unknown", c.Actual, "未从直接授权视图确认完整权限；角色继承或受管权限需要 DBA 进一步核对")
				}
			}
			checks = append(checks, c)
		}
	}
	platform := s.cdcPlatformCheck(ctx, src, scope)
	if err == nil && local && src.Kind == "oracle" && platform.Passed {
		for i, c := range checks {
			if c.Check == "mining_account" {
				checks[i] = cdcOutcome(c, "passed", "内置作业已验证", "内置 Oracle 作业及登记凭据一致，按已验证的固定采集范围核实；其他表及远程授权仍需专项核对")
			}
		}
	}
	checks = append(checks, platform)
	return checks
}
