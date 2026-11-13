package workflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"datamesh/internal/platform/coderunner"
	"github.com/gin-gonic/gin"
)

const streamScriptPath = "/internal/governance/stream-script"
const streamScriptBodyLimit = coderunner.MaxRowBytes + 4096

var streamScriptSlots = make(chan struct{}, 2)

type streamScriptRequest struct {
	StreamID   string          `json:"stream_id"`
	Generation int             `json:"generation"`
	NodeID     string          `json:"node_id"`
	RowKey     string          `json:"row_key"`
	Payload    json.RawMessage `json:"payload"`
}

func streamScriptToken() ([]byte, error) {
	file := os.Getenv("GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE")
	if file == "" {
		return nil, problem(503, "STREAM_SCRIPT_UNAVAILABLE", "流式代码执行通道尚未配置")
	}
	raw, e := os.ReadFile(file)
	if e != nil || len(raw) != 64 {
		return nil, problem(503, "STREAM_SCRIPT_UNAVAILABLE", "流式代码执行通道配置不可用")
	}
	decoded, e := hex.DecodeString(string(raw))
	if e != nil || len(decoded) != 32 || string(raw) != strings.ToLower(string(raw)) {
		return nil, problem(503, "STREAM_SCRIPT_UNAVAILABLE", "流式代码执行通道配置不可用")
	}
	return raw, nil
}
func streamScriptMAC(key []byte, stamp string, raw []byte) string {
	digest := sha256.Sum256(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(stamp + "\nPOST\n" + streamScriptPath + "\n" + hex.EncodeToString(digest[:])))
	return hex.EncodeToString(mac.Sum(nil))
}
func validStreamScriptMAC(key []byte, stamp, signature string, raw []byte, now time.Time) bool {
	seconds, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || strconv.FormatInt(seconds, 10) != stamp || seconds < now.Unix()-60 || seconds > now.Unix()+60 || len(signature) != 64 {
		return false
	}
	return hmac.Equal([]byte(streamScriptMAC(key, stamp, raw)), []byte(signature))
}

// Duplicate field names must not produce different signed, validated and cached views.
func streamScriptObject(raw []byte) (DataRow, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, e := decoder.Token()
	if e != nil || first != json.Delim('{') {
		return nil, invalid
	}
	out := DataRow{}
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return nil, invalid
		}
		name, ok := key.(string)
		if !ok {
			return nil, invalid
		}
		if _, found := out[name]; found {
			return nil, invalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, invalid
		}
		out[name] = value
	}
	end, e := decoder.Token()
	if e != nil || end != json.Delim('}') {
		return nil, invalid
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, invalid
	}
	return out, nil
}
func streamScriptInput(nodeID string, fields []governanceFlowField, payload json.RawMessage) (DataRow, error) {
	if len(payload) > coderunner.MaxRowBytes {
		return nil, problem(413, "SCRIPT_INPUT_LIMIT", "流式代码输入超过单行限制")
	}
	row, e := streamScriptObject(payload)
	if e != nil || len(row) != len(fields) {
		return nil, invalid
	}
	declared := []GovernanceField{}
	for _, f := range fields {
		field := f.Field
		if _, ok := row[field.Name]; !ok {
			return nil, invalid
		}
		d := GovernanceField{Name: field.Name, LogicalType: field.LogicalType, Nullable: field.Nullable}
		if field.Precision != nil {
			d.Precision = *field.Precision
		}
		if field.Scale != nil {
			d.Scale = *field.Scale
		}
		declared = append(declared, d)
		if (field.LogicalType == "integer" || field.LogicalType == "decimal") && string(row[field.Name]) != "null" {
			row[field.Name] = encode(flowLiteralText(row[field.Name]))
		}
	}
	normalized, err := normalizeScriptRow(GovernanceFlowNode{ID: nodeID, Script: &GovernanceFlowScript{Language: "python", Fields: declared}}, row)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if (field.Field.LogicalType == "integer" || field.Field.LogicalType == "decimal") && string(normalized[field.Field.Name]) != "null" {
			normalized[field.Field.Name] = encode(flowLiteralText(normalized[field.Field.Name]))
		}
	}
	return normalized, nil
}

func normalizeStreamScriptRow(node GovernanceFlowNode, row DataRow) (DataRow, error) {
	result, e := normalizeScriptRow(node, row)
	if e != nil {
		return nil, e
	}
	for _, field := range node.Script.Fields {
		if (field.LogicalType == "decimal" || field.LogicalType == "integer" && node.Script.Language == "javascript") && string(result[field.Name]) != "null" {
			// JSON_VALUE must see exact decimal text; JS integers retain their string contract on cache hits.
			result[field.Name] = encode(flowLiteralText(result[field.Name]))
		}
		if field.LogicalType != "datetime" || string(result[field.Name]) == "null" {
			continue
		}
		stamp, e := governanceTimestamp(flowLiteralText(result[field.Name]))
		if e != nil || stamp.Nanosecond()%1000 != 0 {
			return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_INVALID", 0, 0, field.Name)
		}
		// Flink's typed projection reads UTC timestamps with exactly six fractional digits.
		result[field.Name] = encode(stamp.UTC().Format("2006-01-02T15:04:05.000000Z"))
	}
	return result, nil
}

func executeStreamScriptRow(ctx context.Context, node GovernanceFlowNode, parameters map[string]json.RawMessage, row DataRow) (DataRow, error) {
	values := map[string]json.RawMessage{}
	for name, value := range node.Script.Parameters {
		values[name] = value
		if override, ok := parameters[name]; ok {
			values[name] = override
		}
	}
	var created struct {
		JobID string `json:"job_id"`
	}
	if e := createCodeRunnerJob(ctx, coderunner.CreateJob{Language: node.Script.Language, RuntimeVersion: node.Script.RuntimeVersion, Source: node.Script.Source, Context: values, TimeoutMS: 20000}, &created); e != nil {
		return nil, e
	}
	if !dorisObjectID.MatchString(created.JobID) {
		return nil, scriptFailure(node.ID, "SCRIPT_PROTOCOL_INVALID", 0, 0, "")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = codeRunnerRequest(cleanup, "DELETE", "/v1/jobs/"+created.JobID, nil, nil)
	}()
	var reply coderunner.Reply
	if e := codeRunnerRequest(ctx, "POST", "/v1/jobs/"+created.JobID+"/batches", coderunner.Batch{Sequence: 1, Rows: []coderunner.Row{{Ordinal: 1, Values: row}}}, &reply); e != nil {
		return nil, e
	}
	if reply.Error != nil {
		return nil, scriptFailure(node.ID, reply.Error.Code, reply.Error.Line, reply.Error.Column, "")
	}
	if reply.Sequence != 1 || len(reply.Rows) != 1 || reply.Rows[0].Ordinal != 1 {
		return nil, scriptFailure(node.ID, "SCRIPT_PROTOCOL_INVALID", 0, 0, "")
	}
	result, e := normalizeStreamScriptRow(node, reply.Rows[0].Values)
	if e == nil && len(encode(result)) > coderunner.MaxRowBytes {
		return nil, scriptFailure(node.ID, "SCRIPT_OUTPUT_LIMIT", 0, 0, "")
	}
	return result, e
}

func (s *Server) authorizeStreamScript(ctx context.Context, input streamScriptRequest) (GovernanceStream, governanceFlowExecution, GovernanceFlowNode, governanceFlowPlan, error) {
	var row GovernanceStream
	var def governanceFlowExecution
	var node GovernanceFlowNode
	var plan governanceFlowPlan
	if e := s.db.WithContext(ctx).Where("id=?", input.StreamID).Take(&row).Error; e != nil {
		return row, def, node, plan, denied
	}
	if row.Generation != input.Generation || row.DesiredState == "failed" || !activeStreamScriptState(row.State) {
		return row, def, node, plan, problem(409, "STREAM_SCRIPT_INACTIVE", "持续任务已停止或执行代次已变化")
	}
	var e error
	def, e = decode[governanceFlowExecution](row.Definition)
	if e != nil {
		return row, def, node, plan, e
	}
	if _, e = s.governanceStreamAuthorize(ctx, row, def); e != nil {
		return row, def, node, plan, e
	}
	graph, compiled, _, e := governanceExecutionPlan(def)
	if e != nil {
		return row, def, node, plan, e
	}
	plan = compiled
	for _, candidate := range graph.Nodes {
		if candidate.ID == input.NodeID && candidate.Kind == "script" && candidate.Script != nil {
			node = candidate
			break
		}
	}
	if node.Script == nil || len(node.Inputs) != 1 {
		return row, def, node, plan, denied
	}
	return row, def, node, plan, nil
}
func activeStreamScriptState(state string) bool {
	return state == "starting" || state == "running" || state == "pausing" || state == "unknown"
}

func streamScriptReply(c *gin.Context, status int, value interface{}) {
	raw := encode(value)
	c.Header("Content-Length", strconv.Itoa(len(raw)))
	c.Header("Cache-Control", "no-store")
	c.Data(status, "application/json; charset=utf-8", raw)
}
func streamScriptFailure(c *gin.Context, e error) {
	status, code, message := 503, "STREAM_SCRIPT_UNAVAILABLE", "流式代码执行暂不可用"
	var p *Problem
	if errors.As(e, &p) {
		status, code, message = p.Status, p.Code, p.Message
	}
	started, _ := c.Get("stream_script_started")
	elapsed := int64(0)
	if stamp, ok := started.(time.Time); ok {
		elapsed = time.Since(stamp).Milliseconds()
	}
	slog.Warn("continuous script request failed", "code", code, "status", status, "stream_id", c.GetString("stream_script_id"), "node_id", c.GetString("stream_script_node"), "elapsed_ms", elapsed)
	streamScriptReply(c, status, gin.H{"code": code, "message": message})
}
func (s *Server) registerStreamScriptBridge(r *gin.Engine) {
	r.POST(streamScriptPath, s.streamScriptBridge)
}

func streamScriptWriteDeadline(writer http.ResponseWriter) error {
	if e := http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(58 * time.Second)); e != nil {
		return problem(503, "STREAM_SCRIPT_UNAVAILABLE", "流式代码执行响应通道不可用")
	}
	return nil
}

func (s *Server) streamScriptBridge(c *gin.Context) {
	c.Set("stream_script_started", time.Now())
	key, e := streamScriptToken()
	if e != nil {
		streamScriptFailure(c, e)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(c.Request.Body, streamScriptBodyLimit+1))
	if e != nil || len(raw) > streamScriptBodyLimit {
		streamScriptFailure(c, problem(413, "SCRIPT_INPUT_LIMIT", "流式代码输入超过限制"))
		return
	}
	if !validStreamScriptMAC(key, c.GetHeader("X-Datamesh-Script-Time"), c.GetHeader("X-Datamesh-Script-Signature"), raw, time.Now()) {
		streamScriptFailure(c, denied)
		return
	}
	if _, e = streamScriptObject(raw); e != nil {
		streamScriptFailure(c, e)
		return
	}
	input, e := bind[streamScriptRequest](raw)
	if e != nil || !dorisObjectID.MatchString(input.StreamID) || input.Generation < 1 || !governanceIdentifier.MatchString(input.NodeID) || len(input.RowKey) < 1 || len(input.RowKey) > 128 {
		streamScriptFailure(c, invalid)
		return
	}
	c.Set("stream_script_id", input.StreamID)
	c.Set("stream_script_node", input.NodeID)
	if e := streamScriptWriteDeadline(c.Writer); e != nil {
		streamScriptFailure(c, e)
		return
	}
	requestContext, requestCancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer requestCancel()
	select {
	case streamScriptSlots <- struct{}{}:
		defer func() { <-streamScriptSlots }()
	case <-requestContext.Done():
		streamScriptFailure(c, problem(503, "STREAM_SCRIPT_BUSY", "流式代码执行等待超时"))
		return
	}
	ctx, cancel := context.WithCancelCause(requestContext)
	defer cancel(nil)
	_, def, node, plan, e := s.authorizeStreamScript(ctx, input)
	if e != nil {
		streamScriptFailure(c, e)
		return
	}
	row, e := streamScriptInput(node.ID, plan.Outputs[node.Inputs[0]], input.Payload)
	if e != nil {
		streamScriptFailure(c, e)
		return
	}
	// A watcher interrupts cold sandbox execution on stop, generation change or revocation.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, _, _, _, e := s.authorizeStreamScript(ctx, input); e != nil {
					cancel(e)
					return
				}
			}
		}
	}()
	cacheKey := "v1/" + input.StreamID + "/" + input.NodeID + "/" + fingerprint([]byte(input.RowKey)) + "/" + fingerprint(encode(row))
	result, e := s.streamScriptMemo(ctx, cacheKey, func() (DataRow, error) { return executeStreamScriptRow(ctx, node, def.Parameters, row) })
	if e == nil {
		_, _, _, _, e = s.authorizeStreamScript(ctx, input)
	}
	if ctx.Err() != nil {
		e = context.Cause(ctx)
	}
	if e == nil {
		result, e = normalizeStreamScriptRow(node, result)
	}
	if e != nil {
		streamScriptFailure(c, e)
		return
	}
	streamScriptReply(c, http.StatusOK, result)
}

// Keep the bridge opt-in; normal batch scripts do not require a Flink callback.
func streamScriptConfigured(def governanceFlowExecution) error {
	graph, _, _, e := governanceExecutionPlan(def)
	if e != nil {
		return e
	}
	for _, node := range graph.Nodes {
		if node.Kind == "script" {
			_, e = streamScriptToken()
			return e
		}
	}
	return nil
}
