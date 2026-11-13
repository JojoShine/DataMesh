package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var flinkHandle = regexp.MustCompile(`^[a-fA-F0-9-]{32,36}$`)

type governanceFlink struct {
	client           *http.Client
	gateway, cluster string
}
type flinkOperation struct {
	Handle string `json:"operationHandle"`
}
type flinkJob struct {
	ID    string `json:"jid"`
	Name  string `json:"name"`
	State string `json:"state"`
}

func (s *Server) flink() governanceFlink {
	gateway := os.Getenv("GOVERNANCE_FLINK_GATEWAY")
	if gateway == "" {
		gateway = "http://sql-gateway:8083"
	}
	cluster := os.Getenv("GOVERNANCE_FLINK_CLUSTER")
	if cluster == "" {
		cluster = "http://jobmanager:8081"
	}
	return governanceFlink{client: s.runtime.client, gateway: strings.TrimRight(gateway, "/"), cluster: strings.TrimRight(cluster, "/")}
}
func (f governanceFlink) call(ctx context.Context, cluster bool, method, path string, body interface{}, target interface{}) error {
	origin := f.gateway
	if cluster {
		origin = f.cluster
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return problem(503, "STREAM_RUNTIME_UNAVAILABLE", "持续更新执行服务配置不可用")
	}
	var reader io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return problem(503, "STREAM_RUNTIME_UNAVAILABLE", "持续更新执行服务暂不可用，请核对实际任务状态")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 {
		return problem(503, "STREAM_RUNTIME_RESPONSE", "持续更新执行服务响应异常")
	}
	if resp.StatusCode == 404 && method == http.MethodDelete && flinkHandle.MatchString(strings.TrimPrefix(path, "/v1/sessions/")) {
		return nil // Idle expiry may have already released this owned session.
	}
	if resp.StatusCode == 404 && strings.Contains(path, "/savepoints/") {
		return problem(503, "STREAM_TRIGGER_NOT_FOUND", "暂停操作尚未在集群确认")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return problem(503, "STREAM_RUNTIME_REJECTED", "持续更新执行服务未接受操作，请核对运行状态")
	}
	if target != nil && json.Unmarshal(raw, target) != nil {
		return problem(503, "STREAM_RUNTIME_RESPONSE", "持续更新执行服务响应异常")
	}
	return nil
}
func (f governanceFlink) session(ctx context.Context, name, savepoint string) (string, error) {
	properties := map[string]string{"execution.target": "remote", "execution.attached": "false", "rest.address": "jobmanager", "rest.port": "8081", "execution.runtime-mode": "STREAMING", "pipeline.name": name, "parallelism.default": "1", "execution.checkpointing.interval": "5 s", "execution.checkpointing.mode": "EXACTLY_ONCE", "execution.checkpointing.timeout": "60 s", "table.local-time-zone": "UTC", "table.exec.state.ttl": "0 ms", "restart-strategy.type": "fixed-delay", "restart-strategy.fixed-delay.attempts": "3", "restart-strategy.fixed-delay.delay": "5 s"}
	if savepoint != "" {
		properties["execution.savepoint.path"] = savepoint
		properties["execution.savepoint-restore-mode"] = "NO_CLAIM"
	}
	var response struct {
		Handle string `json:"sessionHandle"`
	}
	err := f.call(ctx, false, "POST", "/v1/sessions", map[string]interface{}{"sessionName": name, "properties": properties}, &response)
	if err != nil {
		return "", err
	}
	if !flinkHandle.MatchString(response.Handle) {
		return "", problem(503, "STREAM_RUNTIME_RESPONSE", "执行会话标识无效")
	}
	return response.Handle, nil
}
func (f governanceFlink) submit(ctx context.Context, session, statement string) (string, error) {
	if !flinkHandle.MatchString(session) {
		return "", invalid
	}
	var response flinkOperation
	err := f.call(ctx, false, "POST", "/v1/sessions/"+session+"/statements", map[string]interface{}{"statement": statement, "executionTimeout": 0}, &response)
	if err != nil {
		return "", err
	}
	if !flinkHandle.MatchString(response.Handle) {
		return "", problem(503, "STREAM_RUNTIME_RESPONSE", "执行操作标识无效")
	}
	return response.Handle, nil
}
func (f governanceFlink) wait(ctx context.Context, session, operation string) error {
	if !flinkHandle.MatchString(session) || !flinkHandle.MatchString(operation) {
		return invalid
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		var response struct {
			Status string `json:"status"`
		}
		if err := f.call(ctx, false, "GET", "/v1/sessions/"+session+"/operations/"+operation+"/status", nil, &response); err != nil {
			return err
		}
		switch response.Status {
		case "FINISHED":
			return nil
		case "ERROR", "CANCELED", "CLOSED":
			return problem(409, "STREAM_SQL_REJECTED", "处理方案未能提交执行，请检查持续更新支持的步骤和运行环境")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (f governanceFlink) closeSession(ctx context.Context, session string) error {
	if !flinkHandle.MatchString(session) {
		return invalid
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return f.call(cleanup, false, "DELETE", "/v1/sessions/"+session, nil, nil)
}
func (f governanceFlink) statement(ctx context.Context, session, sql string) (err error) {
	op, err := f.submit(ctx, session, sql)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, f.call(cleanup, false, "DELETE", "/v1/sessions/"+session+"/operations/"+op+"/close", nil, nil))
	}()
	return f.wait(ctx, session, op)
}
func (f governanceFlink) jobs(ctx context.Context, name string) ([]flinkJob, error) {
	var response struct {
		Jobs []flinkJob `json:"jobs"`
	}
	if err := f.call(ctx, true, "GET", "/jobs/overview", nil, &response); err != nil {
		return nil, err
	}
	jobs := []flinkJob{}
	for _, job := range response.Jobs {
		if job.Name == name {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}
func (f governanceFlink) job(ctx context.Context, id string) (flinkJob, error) {
	if !flinkHandle.MatchString(id) {
		return flinkJob{}, invalid
	}
	var job flinkJob
	err := f.call(ctx, true, "GET", "/jobs/"+id, nil, &job)
	return job, err
}
func (f governanceFlink) cancel(ctx context.Context, id string) error {
	if !flinkHandle.MatchString(id) {
		return invalid
	}
	return f.call(ctx, true, "PATCH", "/jobs/"+id+"?mode=cancel", nil, nil)
}
func (f governanceFlink) stop(ctx context.Context, id, trigger string) (string, error) {
	if !flinkHandle.MatchString(id) || !flinkHandle.MatchString(trigger) {
		return "", invalid
	}
	var response struct {
		ID string `json:"request-id"`
	}
	err := f.call(ctx, true, "POST", "/jobs/"+id+"/stop", map[string]interface{}{"triggerId": trigger, "drain": false, "targetDirectory": "file:///opt/flink/state/savepoints", "formatType": "CANONICAL"}, &response)
	if err != nil {
		return "", err
	}
	if response.ID != trigger {
		return "", problem(503, "STREAM_RUNTIME_RESPONSE", "暂停操作标识无效")
	}
	return response.ID, nil
}
func (f governanceFlink) savepoint(ctx context.Context, job, trigger string) (string, bool, error) {
	if !flinkHandle.MatchString(job) || !flinkHandle.MatchString(trigger) {
		return "", false, invalid
	}
	var response struct {
		Status struct {
			ID string `json:"id"`
		} `json:"status"`
		Operation struct {
			Location string          `json:"location"`
			Failure  json.RawMessage `json:"failure-cause"`
		} `json:"operation"`
	}
	if err := f.call(ctx, true, "GET", "/jobs/"+job+"/savepoints/"+trigger, nil, &response); err != nil {
		return "", false, err
	}
	if response.Status.ID != "COMPLETED" {
		return "", false, nil
	}
	if len(response.Operation.Failure) > 0 && string(response.Operation.Failure) != "null" {
		return "", true, problem(409, "STREAM_SAVEPOINT_FAILED", "暂停保存失败，原作业需要核对后重试暂停")
	}
	if !strings.HasPrefix(response.Operation.Location, "file:/") || !strings.Contains(response.Operation.Location, "/opt/flink/state/savepoints/") {
		return "", true, problem(503, "STREAM_RUNTIME_RESPONSE", "恢复点路径无效")
	}
	return response.Operation.Location, true, nil
}
func streamJobName(id string, generation int) string {
	return fmt.Sprintf("datamesh-stream-%s-%d", id, generation)
}
