package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) cdcConnectorValidation(ctx context.Context, src SourceSecret, b CDCBinding) preflightCheck {
	check := cdcRequirement("platform_cdc", "平台采集配置", "配置校验通过", "请平台管理员核对采集服务驱动、网络和数据库配置", "", "确认采集服务已安装对应数据库连接器及必要驱动", "确认采集服务容器可以访问登记的数据库地址和端口", "修改配置后重新执行检查；检查不会创建连接器或修改位点")
	check.Owner = "平台管理员 / 数据库管理员"
	credential, e := s.runtime.open(src.Ciphertext)
	if e != nil {
		return cdcOutcome(check, "unknown", "未获取", "无法读取加密凭据，请管理员核查")
	}
	cfg, e := buildCDCConfig(b, src, credential)
	if e != nil {
		return cdcOutcome(check, "failed", "配置不完整", "请填写所选数据库必需的采集配置")
	}
	raw, e := s.runtime.request(ctx, "PUT", "/connector-plugins/"+url.PathEscape(cfg["connector.class"])+"/config/validate", encode(cfg))
	if e != nil {
		return cdcOutcome(check, "unknown", "尚未通过", "采集服务或对应驱动不可用，请平台管理员检查")
	}
	var result struct {
		Errors *int `json:"error_count"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Errors == nil {
		return cdcOutcome(check, "unknown", "未获取", "采集配置校验响应不完整，请平台管理员核查")
	}
	if *result.Errors != 0 {
		return cdcOutcome(check, "failed", fmt.Sprintf("%d 项配置未通过", *result.Errors), "采集服务未通过配置校验，请核对网络、账号、库表和采集配置；敏感错误详情请管理员在内部诊断")
	}
	return cdcOutcome(check, "passed", "配置校验通过", "已校验采集服务配置；尚未创建或启动实际作业")
}

func (s *Server) cdcConfiguredChecks(ctx context.Context, tx *gorm.DB, src SourceSecret, b CDCBinding) []preflightCheck {
	probe, scopeError := cdcProbeSource(src, b.PDB)
	if scopeError != nil {
		return []preflightCheck{cdcOutcome(cdcRequirement("pdb", "目标 PDB", "PDB 配置完整", "请核对 CDB/PDB 和可信范围", "", "登记 CDB 数据源并单独填写目标 PDB"), "failed", "配置不完整", "请核对 CDB/PDB 服务配置")}
	}
	checks := s.cdcPreflight(ctx, tx, probe, cdcScope{Schema: b.Schema, Table: b.Table})
	db, e := s.runtime.database(ctx, tx, probe)
	if e == nil {
		defer db.Close()
	}
	for i, c := range checks {
		switch c.Check {
		case "replication_capacity":
			if e == nil {
				checks[i] = cdcCapacity(ctx, db, c, false, b.Slot)
			}
		case "mining_account":
			if e == nil {
				checks[i] = cdcOracleMiningCheck(ctx, db, b, c)
			}
		case "platform_cdc":
			checks[i] = s.cdcConnectorValidation(ctx, src, b)
		case "publication":
			if e != nil {
				checks[i] = cdcOutcome(c, "skipped", "等待连接", "请先恢复数据库连接")
				continue
			}
			var found bool
			err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_publication_tables WHERE pubname=$1 AND schemaname=$2 AND tablename=$3)", b.Publication, b.Schema, b.Table).Scan(&found)
			c.VerifySQL = "SELECT pubname,schemaname,tablename FROM pg_publication_tables;"
			c.Steps = []string{"由 DBA 核对填写的发布名称是否存在", "确认发布包含所选表；平台不会自动创建或扩大发布范围", "完成后使用相同账号重新检查"}
			if err != nil {
				checks[i] = cdcOutcome(c, "unknown", "未获取", "无法核对发布范围，请 DBA 验证")
			} else if !found {
				checks[i] = cdcOutcome(c, "failed", b.Publication, "指定发布未包含所选表，请 DBA 配置后重试")
			} else {
				checks[i] = cdcOutcome(c, "passed", b.Publication, "指定发布包含当前所选表")
			}
		}
	}
	identityCheck := cdcRequirement("instance_identity", "数据库实例标识", "已核对实例标识", "请 DBA 开放实例标识的只读查询", "", "PostgreSQL：核对 pg_control_system() 读取权限", "MySQL：核对 server_uuid 查询权限", "Oracle：核对 v$database 读取权限及 CDB/PDB 归属")
	if e != nil {
		checks = append(checks, cdcOutcome(identityCheck, "skipped", "等待连接", "请先恢复数据库连接"))
	} else {
		actual, err := cdcDatabaseIdentity(ctx, db, src, CDCConfigInput{PDB: b.PDB})
		if err != nil {
			checks = append(checks, cdcOutcome(identityCheck, "unknown", "未获取", "无法验证实例标识，请按处理步骤授予只读查询权限"))
		} else if b.SourceFingerprint != "" && actual != b.SourceFingerprint {
			checks = append(checks, cdcOutcome(identityCheck, "failed", "标识变化", "数据库实例已变化，请核查任务归属"))
		} else {
			checks = append(checks, cdcOutcome(identityCheck, "passed", "已核对实例标识", "可识别同一实例的重复登记，避免竞争采集"))
		}
	}
	if src.Kind == "oracle" {
		checks = append(checks, s.cdcOracleWorkTableCheck(ctx, tx, src))
	}
	return checks
}

func (s *Server) validateCDCTask(tx *gorm.DB, p identity.Principal, raw []byte) (int, json.RawMessage, string, error) {
	in, e := bind[CDCConfigInput](raw)
	if e != nil {
		return 0, nil, "", e
	}
	source, src, db, e := s.cdcSourceForRead(tx.Statement.Context, tx, p, in.SourceID, in.PDB)
	if e != nil {
		return 0, nil, "", e
	}
	defer db.Close()
	if source.Version != in.SourceVersion {
		return 0, nil, "", problem(409, "SOURCE_VERSION_CHANGED", "数据源配置已变化，请刷新后重新选择")
	}
	table, e := discoverTable(tx.Statement.Context, db, src, in.Schema, in.Table)
	if e != nil {
		return 0, nil, "", e
	}
	if len(table.Keys) == 0 {
		return 0, nil, "", problem(400, "CDC_PRIMARY_KEY_REQUIRED", "所选表缺少主键，请 DBA 配置后刷新")
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	b, e := newCDCBinding(id, src, in, "validation-only", "not-established")
	if e != nil {
		return 0, nil, "", e
	}
	b.SourceFingerprint = ""
	ctx, cancel := context.WithTimeout(tx.Statement.Context, 20*time.Second)
	defer cancel()
	checks := s.cdcConfiguredChecks(ctx, tx, src, b)
	passed := true
	for _, check := range checks {
		passed = passed && check.Passed
	}
	return 200, encode(gin.H{"source_id": in.SourceID, "kind": "cdc", "passed": passed, "checks": checks, "checked_at": time.Now().UTC()}), in.SourceID, nil
}
