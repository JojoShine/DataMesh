package workflow

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/segmentio/kafka-go"
	goora "github.com/sijms/go-ora/v3"
	"gorm.io/gorm"
)

type Runtime struct {
	key           []byte
	client        *http.Client
	store         *minio.Client
	connectOrigin string
	flinkJob      string
}

func newRuntime() (*Runtime, error) {
	if err := validateDorisConfiguration(); err != nil {
		return nil, err
	}
	key, e := hex.DecodeString(os.Getenv("CREDENTIAL_KEY"))
	if e != nil {
		return nil, errors.New("invalid credential key")
	}
	if len(key) != 0 && len(key) != 32 {
		return nil, errors.New("credential key must contain 32 bytes")
	}
	captureOrigin := os.Getenv("CAPTURE_ORIGIN")
	if captureOrigin == "" {
		captureOrigin = "http://connect:8083"
	}
	captureURL, err := url.Parse(captureOrigin)
	if err != nil || (captureURL.Scheme != "http" && captureURL.Scheme != "https") || captureURL.Host == "" || captureURL.Path != "" || captureURL.RawQuery != "" || captureURL.Fragment != "" || captureURL.User != nil {
		return nil, errors.New("invalid CAPTURE_ORIGIN")
	}
	rt := &Runtime{connectOrigin: captureOrigin, key: key, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, flinkJob: os.Getenv("FLINK_JOB_ID")}
	if os.Getenv("RUSTFS_ACCESS_KEY") != "" {
		rt.store, e = minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Secure: false, Region: "us-east-1"})
	}
	return rt, e
}
func (rt *Runtime) crypt(raw []byte, seal bool) ([]byte, error) {
	return rt.cryptScope(raw, seal, "datamesh-source-v1")
}
func (rt *Runtime) cryptScope(raw []byte, seal bool, scope string) ([]byte, error) {
	if len(rt.key) != 32 {
		return nil, problem(503, "CREDENTIAL_KEY_MISSING", "尚未配置数据源凭据加密密钥")
	}
	block, e := aes.NewCipher(rt.key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	if seal {
		nonce := make([]byte, g.NonceSize())
		if _, e = rand.Read(nonce); e != nil {
			return nil, e
		}
		return g.Seal(nonce, nonce, raw, []byte(scope)), nil
	}
	if len(raw) < g.NonceSize() {
		return nil, errors.New("invalid stored credential")
	}
	return g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(scope))
}
func (rt *Runtime) seal(v Credential) (string, error) {
	b, e := rt.crypt(encode(v), true)
	return base64.RawStdEncoding.EncodeToString(b), e
}
func (rt *Runtime) open(v string) (Credential, error) {
	b, e := base64.RawStdEncoding.DecodeString(v)
	if e != nil {
		return Credential{}, e
	}
	b, e = rt.crypt(b, false)
	if e != nil {
		return Credential{}, e
	}
	return decode[Credential](b)
}
func endpointAllowed(v SourceInput) bool {
	for _, a := range staticSourceTargets() {
		if a.Kind == v.Kind && a.Host == v.Host && a.Port == v.Port && a.Database == v.Database {
			return true
		}
	}
	return false
}
func localSourceTemplate(v SourceInput) bool {
	switch v.Kind {
	case "postgres":
		return v.Host == "postgres-source" && v.Port == 5432 && v.Database == "sample"
	case "mysql":
		return v.Host == "mysql-source" && v.Port == 3306 && v.Database == "sample"
	case "oracle":
		return v.Host == "oracle-source" && v.Port == 1521 && v.Database == "FREE"
	}
	return false
}
func (rt *Runtime) database(ctx context.Context, metadata *gorm.DB, v SourceSecret) (*sql.DB, error) {
	if readerSource(v.Kind) {
		return nil, problem(409, "SOURCE_WRITE_UNAVAILABLE", "该数据源当前仅提供只读接入；请通过目录挂载后作为资源读取，不支持直接写入或CDC")
	}
	if err := rt.allowSource(ctx, metadata, SourceInput{Kind: v.Kind, Host: v.Host, Port: v.Port, Database: v.Database}); err != nil {
		return nil, err
	}
	cred, e := rt.open(v.Ciphertext)
	if e != nil {
		return nil, e
	}
	var db *sql.DB
	if v.Kind == "postgres" {
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(v.Host, fmt.Sprint(v.Port)), Path: v.Database, User: url.UserPassword(cred.Username, cred.Password)}
		q := u.Query()
		q.Set("sslmode", "disable")
		q.Set("connect_timeout", "5")
		u.RawQuery = q.Encode()
		db, e = sql.Open("pgx", u.String())
	} else if v.Kind == "mysql" {
		cfg := mysql.NewConfig()
		cfg.User = cred.Username
		cfg.Passwd = cred.Password
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(v.Host, fmt.Sprint(v.Port))
		cfg.DBName = v.Database
		cfg.Timeout = 5 * time.Second
		cfg.ReadTimeout = 5 * time.Second
		db, e = sql.Open("mysql", cfg.FormatDSN())
	} else if v.Kind == "oracle" {
		u := url.URL{Scheme: "oracle", Host: net.JoinHostPort(v.Host, fmt.Sprint(v.Port)), Path: v.Database, User: url.UserPassword(cred.Username, cred.Password)}
		q := u.Query()
		q.Set("CONNECT TIMEOUT", "5")
		// Avoid cached fast-login cookies failing on repeated Oracle 23 connections.
		q.Set("FAST LOGIN", "false")
		q.Set("SOCKET TIMEOUT", "5")
		u.RawQuery = q.Encode()
		db = sql.OpenDB(goora.NewConnector(u.String()))
	} else {
		return nil, invalid
	}
	if e != nil {
		return nil, problem(503, "SOURCE_UNAVAILABLE", "无法连接数据源")
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(0)
	if v.Kind == "oracle" {
		db.SetMaxIdleConns(1)
	}
	if v.Kind == "oracle" {
		// A SQL round trip also verifies access to the selected service.
		var one int
		e = db.QueryRowContext(ctx, "SELECT 1 FROM DUAL").Scan(&one)
	} else {
		e = db.PingContext(ctx)
	}
	if e != nil {
		_ = db.Close()
		return nil, problem(503, "SOURCE_UNAVAILABLE", "数据库连接失败，请检查网络、地址、端口、数据库或服务名及访问凭据")
	}
	return db, nil
}

type ConnectorStatus struct {
	Connector struct {
		State string `json:"state"`
	} `json:"connector"`
	Tasks []struct {
		State string `json:"state"`
	} `json:"tasks"`
}

func (rt *Runtime) request(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, rt.connectOrigin+path, bytes.NewReader(payload))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := rt.client.Do(req)
	if e != nil {
		return nil, problem(503, "CAPTURE_UNAVAILABLE", "采集服务暂不可用")
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
	if e != nil {
		return nil, e
	}
	if res.StatusCode == 404 {
		return nil, problem(404, "CAPTURE_NOT_FOUND", "采集器尚未创建")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, problem(503, "CAPTURE_UNAVAILABLE", "采集器不存在或未就绪")
	}
	return data, nil
}
func (rt *Runtime) connector(ctx context.Context, kind string) (ConnectorStatus, error) {
	return rt.connectorByName(ctx, "datamesh-"+kind)
}
func (rt *Runtime) connectorByName(ctx context.Context, name string) (ConnectorStatus, error) {
	b, e := rt.request(ctx, "GET", "/connectors/"+url.PathEscape(name)+"/status", nil)
	if e != nil {
		return ConnectorStatus{}, e
	}
	return decode[ConnectorStatus](b)
}
func running(v ConnectorStatus) bool {
	if v.Connector.State != "RUNNING" || len(v.Tasks) == 0 {
		return false
	}
	for _, t := range v.Tasks {
		if t.State != "RUNNING" {
			return false
		}
	}
	return true
}
func (rt *Runtime) compute(ctx context.Context) error {
	jobID := rt.flinkJob
	if path := os.Getenv("FLINK_JOB_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			var value struct {
				JobID string `json:"job_id"`
			}
			if json.Unmarshal(data, &value) != nil {
				return problem(409, "COMPUTE_BINDING_INVALID", "处理作业绑定记录无效")
			}
			jobID = value.JobID
		} else if !errors.Is(err, os.ErrNotExist) {
			return problem(503, "COMPUTE_BINDING_UNAVAILABLE", "无法读取处理作业绑定记录")
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(jobID) {
		return problem(409, "COMPUTE_UNBOUND", "尚未绑定可核实的处理作业")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", "http://jobmanager:8081/jobs/"+jobID, nil)
	if e != nil {
		return e
	}
	res, e := rt.client.Do(req)
	if e != nil {
		return problem(503, "COMPUTE_UNAVAILABLE", "处理服务暂不可用")
	}
	defer res.Body.Close()
	var v struct {
		State string `json:"state"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1024*1024)).Decode(&v) != nil || v.State != "RUNNING" {
		return problem(409, "COMPUTE_NOT_RUNNING", "处理作业未运行，不能确认结果完整")
	}
	return nil
}
func (rt *Runtime) end(ctx context.Context, topic string) (int64, int64, error) {
	conn, e := kafka.DialLeader(ctx, "tcp", "kafka:9092", topic, 0)
	if e != nil {
		return 0, 0, problem(503, "KAFKA_UNAVAILABLE", "无法读取原始数据进度")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	first, last, e := conn.ReadOffsets()
	return first, last, e
}

type DataRow map[string]json.RawMessage

func (rt *Runtime) result(ctx context.Context) ([]DataRow, error) {
	cfg := mysql.NewConfig()
	cfg.User = dorisUser(false)
	cfg.Passwd = os.Getenv("DORIS_READER_PASSWORD")
	if cfg.Passwd == "" {
		return nil, problem(503, "RESULT_READER_UNCONFIGURED", "治理结果只读身份尚未配置")
	}
	cfg.Net = "tcp"
	cfg.Addr = dorisAddress()
	cfg.DBName = dorisDatabase("smoke")
	if err := configureDorisSession(cfg); err != nil {
		return nil, err
	}
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		return nil, e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, "SELECT CAST(id AS CHAR),name,CAST(amount AS CHAR) FROM customers WHERE source_id='source-postgres-demo' AND object_id='public.customers' ORDER BY customers.id LIMIT 10001")
	if e != nil {
		return nil, problem(503, "RESULT_UNAVAILABLE", "无法读取治理结果")
	}
	defer rows.Close()
	return scanData(rows)
}
func scanData(rows *sql.Rows) ([]DataRow, error) {
	result := []DataRow{}
	for rows.Next() {
		var id string
		var name, amount sql.NullString
		if e := rows.Scan(&id, &name, &amount); e != nil {
			return nil, e
		}
		v := DataRow{"id": json.RawMessage(id), "name": json.RawMessage("null"), "amount": json.RawMessage("null")}
		if name.Valid {
			v["name"] = encode(name.String)
		}
		if amount.Valid {
			v["amount"] = encode(amount.String)
		}
		result = append(result, v)
	}
	if len(result) > 10000 {
		return nil, problem(409, "TEMPLATE_LIMIT", "当前核对模板最多支持 10000 行，请缩小样例范围")
	}
	return result, rows.Err()
}
func (rt *Runtime) sourceRows(ctx context.Context, metadata *gorm.DB, v SourceSecret) ([]DataRow, error) {
	db, e := rt.database(ctx, metadata, v)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	rows, e := db.QueryContext(ctx, "SELECT id::text,btrim(name),amount::text FROM public.customers ORDER BY public.customers.id LIMIT 10001")
	if e != nil {
		return nil, problem(409, "SCHEMA_INCOMPATIBLE", "来源结构与当前结果模板不兼容")
	}
	defer rows.Close()
	return scanData(rows)
}
func sameRows(a, b []DataRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		for _, k := range []string{"id", "name", "amount"} {
			if strings.TrimSpace(string(a[i][k])) != strings.TrimSpace(string(b[i][k])) {
				return false
			}
		}
	}
	return true
}
