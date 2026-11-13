package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
)

func (s *Server) executeGovernanceFlow(ctx context.Context, p identity.Principal, run GovernanceRun, def governanceFlowExecution, guard func() error) (json.RawMessage, int64, error) {
	standards := map[GovernanceRef]GovernanceStandard{}
	for _, pinned := range def.Pinned {
		if pinned.Definition.Standard != nil {
			standards[pinned.Ref] = *pinned.Definition.Standard
		}
	}
	graph, plan, models, e := governanceExecutionPlan(def)
	if e != nil {
		return nil, 0, e
	}
	if e = s.validateGovernanceScriptRuntimes(ctx, graph); e != nil {
		return nil, 0, e
	}
	nodes := map[string]GovernanceFlowNode{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	modelRef := GovernanceRef{}
	if nodes[plan.OutputNode].Model != nil {
		modelRef = *nodes[plan.OutputNode].Model
	}
	model := models[modelRef]
	unmodeled := nodes[plan.OutputNode].InferModel
	if unmodeled {
		model = governanceResultModel(plan.Outputs[GovernanceFlowEdge{Node: plan.OutputNode}])
	}
	report := governanceFlowReport{Unmodeled: unmodeled, DatasetID: run.ID, Flow: def.Flow, Model: modelRef, OutputNode: plan.OutputNode, SnapshotScope: "per_source_statement", Nodes: []governanceFlowNodeResult{}, Lineage: map[string][]GovernanceFieldOrigin{}}
	if unmodeled {
		report.DatasetID = ""
	}
	sourceLabels, labelErr := governanceInputFieldLabels(s.db.WithContext(ctx), def.Inputs)
	if labelErr != nil {
		return nil, 0, labelErr
	}
	report.FieldLabels = governanceOutputFieldLabels(graph, plan, sourceLabels, model)
	for _, node := range graph.Nodes {
		if node.Kind == "script" {
			report.ConservativeLineage = true
		}
	}
	for _, input := range def.Inputs {
		if input.Resource.Type == "file_resource" || input.Resource.Type == "api_resource" || input.Resource.Type == "governed_dataset" {
			report.SnapshotScope = "per_source_snapshot"
		}
	}
	stage, e := newGovernanceDorisStage(ctx, run.ID)
	if e != nil {
		return nil, 0, e
	}
	defer stage.close()
	commitAttempted := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if !commitAttempted {
			for _, table := range []string{stage.ods, dorisDatabase("quality") + ".rejected_" + run.ID, dorisDatabase(report.DataLayer) + ".result_" + run.ID} {
				if report.DataLayer != "" {
					if _, err := stage.db.ExecContext(cleanup, "DROP TABLE IF EXISTS "+table); err != nil {
						slog.Warn("unpublished governance storage cleanup failed", "run_id", run.ID)
					}
				}
			}
		}
		if err := stage.drop(cleanup); err != nil {
			slog.Warn("governance stage cleanup failed", "run_id", run.ID)
		}
	}()
	report.Engine = "doris_sql"
	report.DataLayer = outputDataLayer(nodes[plan.OutputNode].DataLayer)
	if model.DataLayer != "" {
		report.DataLayer = model.DataLayer
	}
	var processed, stagedBytes int64
	counts := map[GovernanceFlowEdge]int64{}
	progress := func(n int64) error {
		if e := guard(); e != nil {
			return e
		}
		result := s.db.WithContext(ctx).Model(&GovernanceRun{}).Where("id=? AND lease_owner=? AND state='running' AND lease_expires_at>CURRENT_TIMESTAMP AND cancel_requested=false", run.ID, run.LeaseOwner).Updates(map[string]interface{}{"processed": n, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return problem(409, "GOVERNANCE_LEASE_LOST", "运行已取消或租约失效")
		}
		return nil
	}
	if def.StreamSnapshot != nil {
		if e = s.streamSnapshotStage(ctx, stage, def, plan, &report, counts); e != nil {
			return nil, 0, e
		}
	} else {
		for _, id := range plan.Order {
			if e = guard(); e != nil {
				return nil, processed, e
			}
			node := nodes[id]
			started := time.Now()
			metric := governanceFlowNodeResult{NodeID: id}
			for _, edge := range node.Inputs {
				metric.InputCount += counts[edge]
			}
			if node.Kind == "source" {
				basis := def.Inputs[id]
				batch := []interface{}{}
				marks := []string{}
				batchBytes := 0
				var ordinal int64
				flush := func() error {
					if len(batch) == 0 {
						return nil
					}
					_, err := stage.ExecContext(ctx, "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload) VALUES "+strings.Join(marks, ","), batch...)
					batch = nil
					marks = nil
					batchBytes = 0
					if err == nil {
						err = progress(processed)
					}
					return err
				}
				sourceLimit := s.etl.MaxRows + 1
				if def.Preview != nil {
					sourceLimit = 20
				}
				_, e = s.governanceReadInput(ctx, basis, sourceLimit, func(values []interface{}) error {
					if ordinal%500 == 0 {
						if err := guard(); err != nil {
							return err
						}
					}
					for i, field := range basis.Fields {
						if field.Sensitive && !admin(p) && values[i] != nil {
							values[i] = "***"
						}
					}
					row, err := resultDataRow(basis.Fields, values)
					if err != nil {
						return err
					}
					for _, field := range basis.Fields {
						if field.LogicalType == "datetime" && string(row[field.Name]) != "null" {
							stamp, err := governanceTimestamp(flowLiteralText(row[field.Name]))
							if err != nil || stamp.Nanosecond()%1000 != 0 {
								return problem(409, "GOVERNANCE_TIME_PRECISION", "Doris 时间精度最高为微秒，未对来源时间静默截断")
							}
							row[field.Name] = encode(stamp.UTC().Format(time.RFC3339Nano))
						}
					}
					encoded := string(encode(row))
					ordinal++
					processed++
					stagedBytes += int64(len(encoded))
					if processed > int64(s.etl.MaxRows) || stagedBytes > s.etl.MaxBytes {
						return problem(409, "GOVERNANCE_LIMIT", "数据流输入总量超过配置上限")
					}
					offset := len(batch)
					batch = append(batch, id, ordinal, encoded)
					marks = append(marks, fmt.Sprintf("($%d,'',$%d,$%d)", offset+1, offset+2, offset+3))
					batchBytes += len(encoded)
					if len(marks) == 250 || batchBytes >= 2*1024*1024 {
						return flush()
					}
					return nil
				})
				if e == nil {
					e = flush()
				}
				if e != nil {
					return nil, processed, e
				}
				if e = stage.sealSource(ctx, id); e != nil {
					return nil, processed, e
				}
				metric.OutputCount = ordinal
				metric.InputCount = ordinal
				counts[GovernanceFlowEdge{Node: id}] = ordinal
			} else if node.Kind == "script" {
				metric.InputCount, metric.OutputCount, e = executeScriptNode(ctx, stage, node, plan, def.Parameters, guard)
				if e != nil {
					return nil, processed, e
				}
				counts[GovernanceFlowEdge{Node: id}] = metric.OutputCount
				var bytes int64
				if e = stage.QueryRowContext(ctx, "SELECT COALESCE(sum(LENGTH(CAST(payload AS STRING))),0) FROM governance_flow_stage WHERE node_id=$1", id).Scan(&bytes); e != nil {
					return nil, processed, e
				}
				stagedBytes += bytes
				if stagedBytes > s.etl.MaxBytes {
					return nil, processed, problem(409, "GOVERNANCE_LIMIT", "代码处理后的数据量超过运行上限")
				}
			} else {
				// Masked identifiers cannot collapse unrelated rows through an equality, grouping or deduplication key.
				if !admin(p) {
					for side, edge := range node.Inputs {
						fields := flowFieldsByName(plan.Outputs[edge])
						keys := node.Keys
						if node.Kind == "quality" {
							for _, check := range node.Checks {
								if check.Kind == "compare_fields" {
									keys = append(keys, check.Field, flowLiteralText(check.Parameters["other_field"]))
								}
								if check.Kind == "reference_exists" {
									if side == 0 {
										keys = append(keys, check.Field)
									} else {
										keys = append(keys, flowLiteralText(check.Parameters["other_field"]))
									}
								}
							}
						}
						if node.Kind == "aggregate" {
							keys = node.GroupBy
						}
						if node.Join != nil {
							keys = node.Join.LeftKeys
							if side == 1 {
								keys = node.Join.RightKeys
							}
						}
						for _, key := range keys {
							if fields[key].Field.Sensitive {
								return nil, processed, problem(403, "GOVERNANCE_MASKED_KEY", "脱敏字段不能作为关联、分组或去重键")
							}
						}
					}
				}
				if node.Kind == "code_map" && node.Code.Unmatched == "reject" {
					q := &governanceDorisSQL{}
					where := q.input("l", node.Inputs[0])
					value := "(" + q.field("l", node.Code.Field) + ")"
					allowed := []string{}
					for _, entry := range node.Code.Values {
						allowed = append(allowed, value+"="+q.bind(entry.From))
					}
					var invalidCount int64
					if e = stage.QueryRowContext(ctx, "SELECT count(*) FROM governance_flow_stage l WHERE "+where+" AND "+value+" IS NOT NULL AND NOT ("+strings.Join(allowed, " OR ")+")", q.args...).Scan(&invalidCount); e != nil {
						return nil, processed, e
					}
					if invalidCount > 0 {
						return nil, processed, problem(409, "GOVERNANCE_CODE_UNMATCHED", "存在未匹配码值，已阻止本次结果产出")
					}
				}
				if node.Kind == "join" {
					if err := s.governanceDorisJoinBudget(ctx, stage, node, plan, stagedBytes); err != nil {
						return nil, processed, err
					}
				}
				query, args, err := governanceDorisSelect(node, plan)
				if err != nil {
					return nil, processed, err
				}
				args = append(args, id)
				nodeParameter := fmt.Sprintf("$%d", len(args))
				insert := "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload,violations) SELECT " + nodeParameter + ",port,row_number() OVER(ORDER BY ordinal),payload,violations FROM (" + query + ") computed LIMIT " + fmt.Sprint(s.etl.MaxRows+1)
				if _, e = stage.ExecContext(ctx, insert, args...); e != nil {
					return nil, processed, e
				}
				var total, bytes int64
				if e = stage.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(LENGTH(CAST(payload AS STRING))),0) FROM governance_flow_stage WHERE node_id=$1", id).Scan(&total, &bytes); e != nil {
					return nil, processed, e
				}
				stagedBytes += bytes
				if total > int64(s.etl.MaxRows) || stagedBytes > s.etl.MaxBytes {
					return nil, processed, problem(409, "GOVERNANCE_LIMIT", "数据流中间结果超过配置上限")
				}
				metric.OutputCount = total
				if node.Kind == "quality" {
					if e = stage.QueryRowContext(ctx, "SELECT count(*) FROM governance_flow_stage WHERE node_id=$1 AND port='bad'", id).Scan(&metric.RejectedCount); e != nil {
						return nil, processed, e
					}
					metric.OutputCount = total - metric.RejectedCount
					counts[GovernanceFlowEdge{Node: id, Port: "good"}] = metric.OutputCount
					counts[GovernanceFlowEdge{Node: id, Port: "bad"}] = metric.RejectedCount
					report.RejectedRows += metric.RejectedCount
					metric.InputCount = total
					if e = s.db.WithContext(ctx).Create(&GovernanceRunEvent{RunID: run.ID, NodeID: id, Code: "QUALITY_CHECKED", InputCount: total, OutputCount: metric.OutputCount, Processed: processed}).Error; e != nil {
						return nil, processed, e
					}
					if !unmodeled && def.Preview == nil && total > 0 && float64(metric.RejectedCount)/float64(total) > model.Contract.MaxFailureRate {
						return nil, processed, problem(409, "GOVERNANCE_QUALITY_BLOCKED", "质量节点的不合格比例超过模型契约门槛，未产出成果")
					}
				} else {
					counts[GovernanceFlowEdge{Node: id}] = total
				}
			}
			metric.DurationMS = time.Since(started).Milliseconds()
			report.Nodes = append(report.Nodes, metric)
			if e = s.db.WithContext(ctx).Create(&GovernanceRunEvent{RunID: run.ID, NodeID: id, Code: "FLOW_NODE_COMPLETED", Processed: processed, InputCount: metric.InputCount, OutputCount: metric.OutputCount, DurationMS: metric.DurationMS}).Error; e != nil {
				return nil, processed, e
			}
		}
	}
	if def.StreamSnapshot != nil {
		for _, node := range graph.Nodes {
			if node.Kind != "quality" {
				continue
			}
			good, bad := counts[GovernanceFlowEdge{Node: node.ID, Port: "good"}], counts[GovernanceFlowEdge{Node: node.ID, Port: "bad"}]
			if !unmodeled && good+bad > 0 && float64(bad)/float64(good+bad) > model.Contract.MaxFailureRate {
				return nil, processed, problem(409, "GOVERNANCE_QUALITY_BLOCKED", "质量节点的不合格比例超过模型契约门槛，未产出成果")
			}
		}
	}
	report.OutputRows = counts[GovernanceFlowEdge{Node: plan.OutputNode}]
	for _, field := range plan.Outputs[GovernanceFlowEdge{Node: plan.OutputNode}] {
		report.Lineage[field.Field.Name] = field.Origins
	}
	if def.Preview != nil {
		return s.finishGovernanceDorisCodePreview(ctx, stage, p, run, def, plan, report, processed, guard)
	}
	// Read bounded pages rather than buffering a complete governed result in the worker.
	keys := []string{}
	q := &governanceDorisSQL{}
	for _, field := range governanceModelFields(model) {
		if field.PrimaryKey {
			keys = append(keys, q.value("l", field))
		}
	}
	if len(keys) > 0 {
		args := append(q.args, plan.OutputNode)
		var duplicates int64
		query := "SELECT count(*) FROM (SELECT 1 FROM governance_flow_stage l WHERE l.node_id=$" + fmt.Sprint(len(args)) + " AND l.port='' GROUP BY " + strings.Join(keys, ",") + " HAVING count(*)>1 LIMIT 1) duplicates"
		if e = stage.QueryRowContext(ctx, query, args...).Scan(&duplicates); e != nil {
			return nil, processed, e
		}
		if duplicates > 0 {
			return nil, processed, problem(409, "GOVERNANCE_OUTPUT_DUPLICATE_KEY", "输出存在重复模型主键，未产出成果")
		}
	}
	var lastNode, lastPort string
	var lastOrdinal int64
	var saved int64
	for {
		if e = guard(); e != nil {
			return nil, processed, e
		}
		rows, err := stage.QueryContext(ctx, "SELECT node_id,port,ordinal,payload,violations FROM governance_flow_stage WHERE (node_id=$1 AND port='' OR port='bad') AND (node_id>$2 OR (node_id=$2 AND port>$3) OR (node_id=$2 AND port=$3 AND ordinal>$4)) ORDER BY node_id,port,ordinal LIMIT 250", plan.OutputNode, lastNode, lastPort, lastOrdinal)
		if err != nil {
			return nil, processed, err
		}
		batchCount := 0
		for rows.Next() {
			var node, port string
			var ordinal int64
			var body, reasons []byte
			if e = rows.Scan(&node, &port, &ordinal, &body, &reasons); e != nil {
				rows.Close()
				return nil, processed, e
			}
			row, err := decode[DataRow](body)
			if err != nil {
				rows.Close()
				return nil, processed, err
			}
			if node == plan.OutputNode && port == "" {
				if !unmodeled {
					if e = validateGovernanceOutputRow(row, model, standards); e != nil {
						rows.Close()
						return nil, processed, e
					}
				}
				if e = normalizeSnapshotNumbers(row, Object{Fields: governanceModelFields(model)}); e != nil {
					rows.Close()
					return nil, processed, e
				}
			}
			batchCount++
			lastNode, lastPort, lastOrdinal = node, port, ordinal
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, processed, e
		}
		if batchCount == 0 {
			break
		}
		saved += int64(batchCount)
		if batchCount < 250 {
			break
		}
	}
	if saved != report.OutputRows+report.RejectedRows {
		return nil, processed, problem(409, "GOVERNANCE_OUTPUT_COUNT", "输出快照与质量分支数量不一致")
	}
	if e = guard(); e != nil {
		return nil, processed, e
	}
	report.Storage, e = stage.publish(ctx, run.ID, plan.OutputNode, report.DataLayer, governanceModelFields(model))
	if e != nil {
		return nil, processed, e
	}
	db, e := s.db.DB()
	if e != nil {
		return nil, processed, e
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return nil, processed, e
	}
	defer tx.Rollback()
	if def.Trial != nil || def.WorkflowTrial != nil {
		report.DatasetID = ""
	}
	if unmodeled && def.Lightweight == nil && def.Trial == nil && def.WorkflowTrial == nil {
		draftTx := s.db.Session(&gorm.Session{NewDB: true, SkipDefaultTransaction: true}).WithContext(ctx)
		draftTx.Statement.ConnPool = tx
		report.GeneratedModel, e = governanceCreateResultModel(draftTx, run, def, plan.Outputs[GovernanceFlowEdge{Node: plan.OutputNode}], report.FieldLabels)
		if e != nil {
			return nil, processed, e
		}
	} else if !unmodeled && def.Trial == nil && def.WorkflowTrial == nil {
		if e = s.registerGovernanceOutput(ctx, tx, run, def, model, report); e != nil {
			return nil, processed, e
		}
	}
	payload := encode(report)
	result, e := tx.ExecContext(ctx, "UPDATE governance_runs SET state='succeeded',result=$1::jsonb,processed=$2,finished_at=now(),updated_at=now() WHERE id=$3 AND state='running' AND lease_owner=$4 AND lease_expires_at>now() AND cancel_requested=false", string(payload), processed, run.ID, run.LeaseOwner)
	if e != nil {
		return nil, processed, e
	}
	affected, e := result.RowsAffected()
	if e != nil {
		return nil, processed, e
	}
	if affected != 1 {
		return nil, processed, problem(409, "GOVERNANCE_LEASE_LOST", "输出提交前租约或取消状态已变化")
	}
	if e = s.governanceFlowAuthorize(ctx, p, def); e != nil {
		return nil, processed, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO governance_run_events(run_id,code,processed) VALUES($1,'FLOW_COMPLETED',$2)", run.ID, processed); e != nil {
		return nil, processed, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO audit_records(actor_id,action,object_id,result,trace_id) VALUES($1,'governance_run_finished',$2,'succeeded','')", p.UserID, run.ID); e != nil {
		return nil, processed, e
	}
	// The immutable Doris result is private until this metadata transaction commits.
	// Unknown commits retain the physical result for reconciliation, never publish a partial batch.
	commitAttempted = true
	if e = tx.Commit(); e != nil {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var durable GovernanceRun
		if err := s.db.WithContext(checkCtx).Where("id=? AND state='succeeded'", run.ID).Take(&durable).Error; err == nil && func() bool {
			decoded, decodeErr := decode[governanceFlowReport](durable.Result)
			return decodeErr == nil && fingerprint(encode(decoded)) == fingerprint(payload)
		}() {
			return payload, processed, nil
		}
		return nil, processed, e
	}
	return payload, processed, nil
}
func governanceModelFields(model GovernanceModel) []Field {
	fields := []Field{}
	for _, f := range model.Fields {
		field := Field{Name: f.Name, LogicalType: f.LogicalType, Nullable: f.Nullable, PrimaryKey: f.PrimaryKey, Sensitive: f.Sensitive}
		if f.LogicalType == "decimal" {
			precision, scale := f.Precision, f.Scale
			field.Precision = &precision
			field.Scale = &scale
		}
		fields = append(fields, field)
	}
	return fields
}
func validateGovernanceOutputRow(row DataRow, model GovernanceModel, standards map[GovernanceRef]GovernanceStandard) error {
	for _, field := range model.Fields {
		raw, exists := row[field.Name]
		if !exists || string(raw) == "null" {
			if !field.Nullable || field.PrimaryKey {
				return problem(409, "GOVERNANCE_OUTPUT_NULL", "输出包含不满足模型非空约束的字段")
			}
			continue
		}
		text := flowLiteralText(raw)
		if field.LogicalType == "string" {
			var value string
			if json.Unmarshal(raw, &value) != nil || field.Length > 0 && utf8.RuneCountInString(value) > field.Length {
				return problem(409, "GOVERNANCE_OUTPUT_TYPE", "输出字段不符合模型类型或长度约束")
			}
		} else if field.LogicalType == "integer" || field.LogicalType == "decimal" {
			value, ok := new(big.Rat).SetString(text)
			if !ok {
				return problem(409, "GOVERNANCE_OUTPUT_TYPE", "输出数值格式无效")
			}
			if field.LogicalType == "integer" {
				minValue, _ := new(big.Int).SetString("-9223372036854775808", 10)
				maxValue, _ := new(big.Int).SetString("9223372036854775807", 10)
				if !value.IsInt() || value.Num().Cmp(minValue) < 0 || value.Num().Cmp(maxValue) > 0 {
					return problem(409, "GOVERNANCE_OUTPUT_TYPE", "输出整数超过模型范围")
				}
				row[field.Name] = json.RawMessage(value.Num().String())
				text = value.Num().String()
			} else {
				if !governanceDecimalFits(value, field.Precision, field.Scale) {
					return problem(409, "GOVERNANCE_OUTPUT_TYPE", "输出小数超过模型范围或需要舍入")
				}
				text = value.FloatString(field.Scale)
				row[field.Name] = encode(text)
			}
		} else {
			parsed, e := parseImportValue(text, DirectoryItem{LogicalType: field.LogicalType})
			if e != nil {
				return problem(409, "GOVERNANCE_OUTPUT_TYPE", "输出字段不符合模型类型")
			}
			row[field.Name] = encode(parsed)
			text = fmt.Sprint(parsed)
		}
		if field.Standard != nil {
			standard, ok := standards[*field.Standard]
			if !ok {
				return problem(409, "GOVERNANCE_REFERENCE_UNAVAILABLE", "输出标准版本不可用")
			}
			if standard.Pattern != "" {
				pattern, e := regexp.Compile(standard.Pattern)
				if e != nil || !pattern.MatchString(text) {
					return problem(409, "GOVERNANCE_STANDARD_FAILED", "输出字段不符合引用标准格式")
				}
			}
			if len(standard.Values) > 0 && !slices.Contains(standard.Values, text) {
				return problem(409, "GOVERNANCE_STANDARD_FAILED", "输出字段不在标准码表中")
			}
		}
	}
	return nil
}

func governanceDecimalFits(value *big.Rat, precision, scale int) bool {
	if precision < 1 || precision > 38 || scale < 0 || scale > precision {
		return false
	}
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(multiplier))
	if !scaled.IsInt() {
		return false
	}
	bound := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(precision)), nil)
	return new(big.Int).Abs(scaled.Num()).Cmp(bound) < 0
}
