package workflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"datamesh/internal/platform/coderunner"
	"github.com/gin-gonic/gin"
)

func codeRunnerRequest(ctx context.Context, method, path string, input, output interface{}) error {
	origin := strings.TrimRight(os.Getenv("CODE_RUNNER_URL"), "/")
	tokenFile := os.Getenv("CODE_RUNNER_TOKEN_FILE")
	if origin == "" || tokenFile == "" {
		return problem(503, "SCRIPT_RUNTIME_UNAVAILABLE", "代码运行环境暂不可用，请联系管理员检查执行服务")
	}
	token, e := os.ReadFile(tokenFile)
	if e != nil {
		return problem(503, "SCRIPT_RUNTIME_UNAVAILABLE", "代码运行环境暂不可用，请联系管理员检查执行服务")
	}
	var body io.Reader
	if input != nil {
		encoded, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(encoded)
	}
	req, e := http.NewRequestWithContext(ctx, method, origin+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 125 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return problem(503, "SCRIPT_RUNTIME_UNAVAILABLE", "代码运行服务连接中断，请稍后重试")
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, coderunner.MaxBatchBytes+65537)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var reply coderunner.Reply
		if json.NewDecoder(limited).Decode(&reply) == nil && reply.Error != nil {
			status := 409
			if response.StatusCode == http.StatusTooManyRequests && reply.Error.Code == "SCRIPT_CAPACITY" {
				status = http.StatusTooManyRequests
			}
			return &scriptExecutionError{Problem: &Problem{Status: status, Code: reply.Error.Code, Message: "代码处理未完成，请检查节点配置与运行记录"}, Line: reply.Error.Line, Column: reply.Error.Column}
		}
		return problem(503, "SCRIPT_RUNTIME_UNAVAILABLE", "代码运行环境暂不可用")
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(limited).Decode(output)
}

// Only explicit rejected admission is retryable. An interrupted POST may already
// have created a job, so transport errors and all other responses remain terminal.
func createCodeRunnerJob(ctx context.Context, input coderunner.CreateJob, output interface{}) error {
	deadline := time.Now().Add(10 * time.Second)
	delay := 50 * time.Millisecond
	for {
		err := codeRunnerRequest(ctx, "POST", "/v1/jobs", input, output)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		capacity, ok := err.(*scriptExecutionError)
		if !ok || capacity.Status != http.StatusTooManyRequests || capacity.Code != "SCRIPT_CAPACITY" || !time.Now().Before(deadline) {
			return err
		}
		wait := delay
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 500*time.Millisecond {
			delay *= 2
		}
	}
}

func governanceCodeRuntimes(ctx context.Context) ([]coderunner.Runtime, error) {
	var result struct {
		Items []coderunner.Runtime `json:"items"`
	}
	e := codeRunnerRequest(ctx, "GET", "/v1/runtimes", nil, &result)
	return result.Items, e
}
func (s *Server) validateGovernanceScriptRuntimes(ctx context.Context, graph GovernanceDataflow) error {
	required := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Script != nil {
			if e := validateGovernanceScript(node.Script); e != nil {
				return e
			}
			required[node.Script.RuntimeVersion] = true
		}
	}
	if len(required) == 0 {
		return nil
	}
	runtimes, e := governanceCodeRuntimes(ctx)
	if e != nil {
		return e
	}
	for _, runtime := range runtimes {
		if runtime.Available {
			delete(required, runtime.Version)
		}
	}
	if len(required) > 0 {
		return problem(409, "SCRIPT_RUNTIME_UNAVAILABLE", "此数据流指定的代码运行环境不可用，请检查执行服务或为新版本重新选择环境")
	}
	return nil
}
func (s *Server) registerGovernanceCode(api, write *gin.RouterGroup) {
	api.GET("/governance/code-runtimes", s.require("governance", "governor"), func(c *gin.Context) {
		items, e := governanceCodeRuntimes(c.Request.Context())
		if e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": items})
	})
	s.registerGovernanceCodePreview(api, write)
}

type governanceScriptStore interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

func executeScriptNode(ctx context.Context, tx governanceScriptStore, node GovernanceFlowNode, plan governanceFlowPlan, parameters map[string]json.RawMessage, guard func() error) (int64, int64, error) {
	contextValues := map[string]json.RawMessage{}
	for name, value := range node.Script.Parameters {
		contextValues[name] = value
		if override, ok := parameters[name]; ok {
			contextValues[name] = override
		}
	}
	var created struct {
		JobID string `json:"job_id"`
	}
	if e := createCodeRunnerJob(ctx, coderunner.CreateJob{Language: node.Script.Language, RuntimeVersion: node.Script.RuntimeVersion, Source: node.Script.Source, Context: contextValues}, &created); e != nil {
		if diagnostic, ok := e.(*scriptExecutionError); ok {
			diagnostic.NodeID = node.ID
		}
		return 0, 0, e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = codeRunnerRequest(cleanup, "DELETE", "/v1/jobs/"+created.JobID, nil, nil)
	}()
	input := node.Inputs[0]
	fields := plan.Outputs[input]
	var last, count int64
	sequence := 1
	for {
		if e := guard(); e != nil {
			return count, count, e
		}
		records, e := tx.QueryContext(ctx, "SELECT ordinal,payload FROM governance_flow_stage WHERE node_id=$1 AND port=$2 AND ordinal>$3 ORDER BY ordinal LIMIT 250", input.Node, input.Port, last)
		if e != nil {
			return count, count, e
		}
		batch := coderunner.Batch{Sequence: sequence, Rows: []coderunner.Row{}}
		bytesCount := 0
		for records.Next() {
			var ordinal int64
			var encoded []byte
			if e = records.Scan(&ordinal, &encoded); e != nil {
				records.Close()
				return count, count, e
			}
			row, e := decode[DataRow](encoded)
			if e != nil {
				records.Close()
				return count, count, e
			}
			for _, field := range fields {
				if raw := row[field.Field.Name]; len(raw) > 0 && string(raw) != "null" && (field.Field.LogicalType == "integer" || field.Field.LogicalType == "decimal") {
					row[field.Field.Name] = encode(flowLiteralText(raw))
				}
			}
			size := len(encode(row))
			if size > coderunner.MaxRowBytes {
				records.Close()
				return count, count, scriptFailure(node.ID, "SCRIPT_OUTPUT_LIMIT", 0, 0, "")
			}
			if len(batch.Rows) > 0 && bytesCount+size > coderunner.MaxBatchBytes-65536 {
				break
			}
			bytesCount += size
			batch.Rows = append(batch.Rows, coderunner.Row{Ordinal: ordinal, Values: row})
			last = ordinal
		}
		e = records.Err()
		records.Close()
		if e != nil {
			return count, count, e
		}
		if len(batch.Rows) == 0 {
			break
		}
		var reply coderunner.Reply
		if e = codeRunnerRequest(ctx, "POST", "/v1/jobs/"+created.JobID+"/batches", batch, &reply); e != nil {
			if diagnostic, ok := e.(*scriptExecutionError); ok {
				diagnostic.NodeID = node.ID
			}
			return count, count, e
		}
		if reply.Error != nil {
			return count, count, scriptFailure(node.ID, reply.Error.Code, reply.Error.Line, reply.Error.Column, "")
		}
		if reply.Sequence != sequence || len(reply.Rows) != len(batch.Rows) {
			return count, count, scriptFailure(node.ID, "SCRIPT_PROTOCOL_INVALID", 0, 0, "")
		}
		if e = guard(); e != nil {
			return count, count, e
		}
		args := []interface{}{}
		marks := []string{}
		for i, item := range reply.Rows {
			if item.Ordinal != batch.Rows[i].Ordinal {
				return count, count, scriptFailure(node.ID, "SCRIPT_PROTOCOL_INVALID", 0, 0, "")
			}
			row, e := normalizeScriptRow(node, item.Values)
			if e != nil {
				return count, count, e
			}
			offset := len(args)
			args = append(args, node.ID, item.Ordinal, string(encode(row)))
			marks = append(marks, fmt.Sprintf("($%d,'',$%d,$%d)", offset+1, offset+2, offset+3))
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO governance_flow_stage(node_id,port,ordinal,payload) VALUES "+strings.Join(marks, ","), args...); e != nil {
			return count, count, e
		}
		count += int64(len(reply.Rows))
		sequence++
	}
	return count, count, nil
}
