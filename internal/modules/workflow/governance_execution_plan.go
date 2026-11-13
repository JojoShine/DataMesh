package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"log/slog"
)

type GovernanceExecutionPlan struct {
	Mode         string `json:"mode"`
	Engine       string `json:"engine"`
	InputChannel string `json:"input_channel"`
	ResultStore  string `json:"result_store"`
	DataLayer    string `json:"data_layer"`
	Reason       string `json:"reason"`
	Archive      string `json:"archive"`
}

func governanceEnginePlan(def governanceFlowExecution, mode string) GovernanceExecutionPlan {
	plan := GovernanceExecutionPlan{Mode: mode, Engine: "doris_sql", InputChannel: "source_snapshot", ResultStore: "doris", DataLayer: streamResultLayer(def), Reason: "有界处理先固定每个来源的输入，在 Doris 执行清洗、关联和汇总；不按数据量暗中切换引擎。", Archive: "rustfs_on_demand"}
	if mode == "continuous" {
		plan.Engine = "flink"
		plan.InputChannel = "kafka_cdc"
		plan.Reason = "持续消费 CDC 变更，Flink 维护关联与汇总状态，受控服务将已提交的 Kafka 结果写入 Doris，支持更新和删除。"
		if graph, _, _, err := governanceExecutionPlan(def); err == nil {
			for _, node := range graph.Nodes {
				if node.Kind == "script" {
					plan.Reason += "代码步骤在隔离环境逐行执行；恢复和撤回复用已计算结果，跨行状态使用关联或聚合步骤。"
					break
				}
			}
		}
	}
	return plan
}
func (s *Server) registerGovernanceExecutionPlan(api, write *gin.RouterGroup) {
	write.POST("/governance/dataflows/:id/versions/:version/execution-plan", s.require("governance", "governor"), func(c *gin.Context) {
		version, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		ref := GovernanceRef{ID: c.Param("id"), Version: version}
		mode := c.Query("mode")
		if mode != "" && mode != "batch" && mode != "continuous" {
			s.fail(c, invalid)
			return
		}
		s.mutate("governance_execution_plan", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
			prepared, e := s.prepareGovernanceFlow(tx, p, ref, b, nil, "")
			if e != nil {
				return 0, nil, ref.ID, e
			}
			if mode == "" {
				mode = prepared.Mode
			}
			if e = requireGovernanceFlowMode(prepared.Mode, mode); e != nil {
				return 0, nil, ref.ID, e
			}
			plan := governanceEnginePlan(prepared.Execution, mode)
			if mode == "continuous" {
				if e = streamScriptConfigured(prepared.Execution); e != nil {
					return 0, nil, ref.ID, e
				}
				sources, e := s.governanceStreamSources(tx, p, prepared.Execution)
				if e != nil {
					return 0, nil, ref.ID, e
				}
				topics := map[string]string{}
				for node := range sources {
					topics[node] = "plan_" + node
				}
				if _, e = compileGovernanceFlink(prepared.Execution, topics, "plan_output", "plan"); e != nil {
					return 0, nil, ref.ID, e
				}
			}
			return 200, encode(plan), ref.ID, nil
		})(c)
	})
	api.GET("/governance/streams/:id/records", s.require("governance", "governor"), func(c *gin.Context) {
		ctx := c.Request.Context()
		row, e := governanceStreamRead(s.db.WithContext(ctx), principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		def, e := decode[governanceFlowExecution](row.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		if _, e = s.governanceStreamAuthorize(ctx, row, def); e != nil {
			if ctx.Err() != nil {
				slog.Warn("continuous records deadline", "stage", "authorize", "trace_id", c.GetString("trace_id"))
			}
			s.fail(c, e)
			return
		}
		page, e := intQuery(c, "page", 1, 100000)
		if e != nil {
			s.fail(c, e)
			return
		}
		size, e := intQuery(c, "page_size", 20, 100)
		if e != nil {
			s.fail(c, e)
			return
		}
		items, e := s.streamLivePage(ctx, row, def, (page-1)*size, size)
		if e != nil {
			if ctx.Err() != nil {
				slog.Warn("continuous records deadline", "stage", "result_read", "trace_id", c.GetString("trace_id"))
			}
			s.fail(c, e)
			return
		}
		if _, e = s.governanceStreamAuthorize(ctx, row, def); e != nil {
			if ctx.Err() != nil {
				slog.Warn("continuous records deadline", "stage", "authorize", "trace_id", c.GetString("trace_id"))
			}
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": items, "total": row.OutputRows, "page": page, "page_size": size, "plan": governanceEnginePlan(def, "continuous"), "materialized_offset": row.OutputOffset, "observed_at": row.UpdatedAt})
	})
}
