package coderunner

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	MaxJobs   int
	Token     string
	Socket    string
	Namespace string
	Images    map[string]string
}
type Server struct {
	config  Config
	docker  *Docker
	mu      sync.Mutex
	jobs    map[string]*job
	slots   chan struct{}
	closing bool
}
type job struct {
	id, container string
	ctx           context.Context
	cancel        context.CancelFunc
	once          sync.Once
	stream        io.ReadWriteCloser
	reader        *io.PipeReader
	replies       chan Reply
	ioMu          sync.Mutex
	mu            sync.Mutex
	sequence      int
	lastOrdinal   int64
	lastUsed      atomic.Int64
}

func New(config Config) (*Server, error) {
	if config.MaxJobs == 0 {
		config.MaxJobs = 2
	}
	if config.MaxJobs < 1 || config.MaxJobs > 2 {
		return nil, errors.New("invalid runner concurrency")
	}
	if len(config.Token) < 32 || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(config.Namespace) || config.Socket == "" {
		return nil, errors.New("invalid runner configuration")
	}
	for language, image := range config.Images {
		if language != "python" && language != "javascript" || !validImage(image) {
			return nil, errors.New("invalid runtime image")
		}
	}
	s := &Server{config: config, docker: NewDocker(config.Socket), jobs: map[string]*job{}, slots: make(chan struct{}, config.MaxJobs)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := s.docker.isolation(ctx); e != nil {
		return nil, e
	}
	if e := s.docker.cleanup(ctx, config.Namespace); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.Token)) != 1 {
			s.failure(w, 401, "RUNNER_AUTH_REQUIRED", 0, 0)
			return
		}
		if r.URL.Path == "/v1/runtimes" && r.Method == "GET" {
			runtimes := []Runtime{}
			for _, language := range []string{"python", "javascript"} {
				if image := s.config.Images[language]; image != "" {
					libraries := []string{"Python standard library", "Decimal"}
					if language == "javascript" {
						libraries = []string{"Node.js standard library", "decimal.js 10.6.0"}
					}
					displayName := "Python 3.14.9"
					if language == "javascript" {
						displayName = "Node.js 24.22.1"
					}
					runtimes = append(runtimes, Runtime{DisplayName: displayName, Language: language, Version: language + "@" + image, Image: image, Libraries: libraries, Available: s.docker.image(r.Context(), image)})
				}
			}
			s.json(w, 200, map[string]interface{}{"items": runtimes})
			return
		}
		if r.URL.Path == "/v1/jobs" && r.Method == "POST" {
			s.create(w, r)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 || parts[0] != "v1" || parts[1] != "jobs" {
			s.failure(w, 404, "JOB_NOT_FOUND", 0, 0)
			return
		}
		s.mu.Lock()
		j := s.jobs[parts[2]]
		s.mu.Unlock()
		if j == nil {
			s.failure(w, 404, "JOB_NOT_FOUND", 0, 0)
			return
		}
		if len(parts) == 3 && r.Method == "DELETE" {
			s.closeJob(j)
			w.WriteHeader(204)
			return
		}
		if len(parts) == 4 && parts[3] == "batches" && r.Method == "POST" {
			s.batch(w, r, j)
			return
		}
		s.failure(w, 404, "JOB_NOT_FOUND", 0, 0)
	})
}
func (s *Server) json(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) failure(w http.ResponseWriter, status int, code string, line, column int) {
	s.json(w, status, Reply{Error: &ScriptError{Code: code, Line: line, Column: column}})
}
func decodeRequest(w http.ResponseWriter, r *http.Request, max int64, target interface{}) error {
	reader := http.MaxBytesReader(w, r.Body, max)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(target); e != nil {
		return e
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return errors.New("trailing request content")
	}
	return nil
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var input CreateJob
	if decodeRequest(w, r, 512*1024, &input) != nil || len(input.Source) == 0 || len(input.Source) > MaxCodeBytes {
		s.failure(w, 400, "SCRIPT_INPUT_INVALID", 0, 0)
		return
	}
	image := s.config.Images[input.Language]
	if image == "" || input.RuntimeVersion != input.Language+"@"+image {
		s.failure(w, 409, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	if input.TimeoutMS == 0 {
		input.TimeoutMS = 120000
	}
	if input.TimeoutMS < 1000 || input.TimeoutMS > 120000 {
		s.failure(w, 400, "SCRIPT_INPUT_INVALID", 0, 0)
		return
	}
	contextBytes, e := json.Marshal(input.Context)
	if e != nil || len(contextBytes) > 65536 {
		s.failure(w, 400, "SCRIPT_INPUT_INVALID", 0, 0)
		return
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		s.failure(w, 503, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.failure(w, 429, "SCRIPT_CAPACITY", 0, 0)
		return
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		<-s.slots
		s.failure(w, 503, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	id := hex.EncodeToString(random[:])
	container, e := s.docker.create(r.Context(), image, s.config.Namespace, id)
	if e != nil {
		<-s.slots
		s.failure(w, 503, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(input.TimeoutMS)*time.Millisecond)
	j := &job{id: id, container: container, ctx: ctx, cancel: cancel, replies: make(chan Reply, 1)}
	j.lastUsed.Store(time.Now().UnixNano())
	s.mu.Lock()
	s.jobs[id] = j
	closing = s.closing
	s.mu.Unlock()
	if closing {
		s.closeJob(j)
		s.failure(w, 503, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	success := false
	defer func() {
		if !success {
			s.closeJob(j)
		}
	}()
	stream, e := s.docker.attach(r.Context(), container)
	if e != nil {
		s.failure(w, 503, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	j.ioMu.Lock()
	if j.ctx.Err() != nil {
		j.ioMu.Unlock()
		stream.Close()
		s.failure(w, 422, "SCRIPT_TERMINATED", 0, 0)
		return
	}
	j.stream = stream
	reader, writer := io.Pipe()
	j.reader = reader
	j.ioMu.Unlock()
	go func() { _ = demultiplex(ctx, stream, writer) }()
	go func() {
		defer close(j.replies)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 65536), MaxBatchBytes+65536)
		for scanner.Scan() {
			var reply Reply
			if json.Unmarshal(scanner.Bytes(), &reply) != nil {
				select {
				case j.replies <- Reply{Error: &ScriptError{Code: "SCRIPT_PROTOCOL_INVALID"}}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case j.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.closeJob(j)
				return
			case <-ticker.C:
				if time.Since(time.Unix(0, j.lastUsed.Load())) > 30*time.Second {
					s.closeJob(j)
					return
				}
			}
		}
	}()
	if e = s.docker.start(r.Context(), container); e != nil {
		s.failure(w, 503, "SCRIPT_RUNTIME_UNAVAILABLE", 0, 0)
		return
	}
	if e = json.NewEncoder(stream).Encode(input); e != nil {
		s.failure(w, 422, "SCRIPT_TERMINATED", 0, 0)
		return
	}
	reply, e := j.receive(r.Context())
	if e != nil {
		s.failure(w, 422, "SCRIPT_TERMINATED", 0, 0)
		return
	}
	if reply.Error != nil {
		sanitizeScriptError(reply.Error)
		s.failure(w, 422, reply.Error.Code, reply.Error.Line, reply.Error.Column)
		return
	}
	if reply.Type != "ready" {
		s.failure(w, 422, "SCRIPT_PROTOCOL_INVALID", 0, 0)
		return
	}
	success = true
	s.json(w, 201, map[string]string{"job_id": id})
}
func (j *job) receive(ctx context.Context) (Reply, error) {
	select {
	case reply, ok := <-j.replies:
		if !ok {
			return Reply{}, errors.New("script terminated")
		}
		return reply, nil
	default:
	}
	select {
	case reply, ok := <-j.replies:
		if !ok {
			return Reply{}, errors.New("script terminated")
		}
		return reply, nil
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	case <-j.ctx.Done():
		return Reply{}, j.ctx.Err()
	}
}
func (s *Server) batch(w http.ResponseWriter, r *http.Request, j *job) {
	var input Batch
	if decodeRequest(w, r, MaxBatchBytes, &input) != nil || len(input.Rows) == 0 || len(input.Rows) > MaxRows {
		s.failure(w, 400, "SCRIPT_BATCH_INVALID", 0, 0)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if input.Sequence != j.sequence+1 {
		s.failure(w, 409, "SCRIPT_SEQUENCE_INVALID", 0, 0)
		return
	}
	last := j.lastOrdinal
	for _, row := range input.Rows {
		encoded, e := json.Marshal(row.Values)
		if e != nil || row.Values == nil || len(encoded) > MaxRowBytes || row.Ordinal <= last {
			s.failure(w, 400, "SCRIPT_ROW_INVALID", 0, 0)
			return
		}
		last = row.Ordinal
	}
	j.lastUsed.Store(time.Now().UnixNano())
	if e := json.NewEncoder(j.stream).Encode(input); e != nil {
		s.closeJob(j)
		s.failure(w, 422, "SCRIPT_TERMINATED", 0, 0)
		return
	}
	reply, e := j.receive(r.Context())
	if e != nil {
		s.closeJob(j)
		code := "SCRIPT_TERMINATED"
		if errors.Is(j.ctx.Err(), context.DeadlineExceeded) {
			code = "SCRIPT_TIMEOUT"
		}
		s.failure(w, 422, code, 0, 0)
		return
	}
	if reply.Error != nil {
		sanitizeScriptError(reply.Error)
		s.closeJob(j)
		s.failure(w, 422, reply.Error.Code, reply.Error.Line, reply.Error.Column)
		return
	}
	if reply.Type != "batch" || reply.Sequence != input.Sequence || len(reply.Rows) != len(input.Rows) {
		s.closeJob(j)
		s.failure(w, 422, "SCRIPT_PROTOCOL_INVALID", 0, 0)
		return
	}
	for i, row := range reply.Rows {
		encoded, e := json.Marshal(row.Values)
		if e != nil || row.Values == nil || len(encoded) > MaxRowBytes || row.Ordinal != input.Rows[i].Ordinal {
			s.closeJob(j)
			s.failure(w, 422, "SCRIPT_PROTOCOL_INVALID", 0, 0)
			return
		}
	}
	j.sequence = input.Sequence
	j.lastOrdinal = last
	j.lastUsed.Store(time.Now().UnixNano())
	s.json(w, 200, reply)
}
func (s *Server) closeJob(j *job) {
	j.once.Do(func() {
		s.mu.Lock()
		delete(s.jobs, j.id)
		s.mu.Unlock()
		j.cancel()
		j.ioMu.Lock()
		if j.stream != nil {
			_ = j.stream.Close()
		}
		if j.reader != nil {
			_ = j.reader.Close()
		}
		j.ioMu.Unlock()
		go func() {
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := s.docker.remove(ctx, j.container)
				cancel()
				if err == nil {
					break
				}
				time.Sleep(3 * time.Second)
			}
			s.mu.Lock()
			delete(s.jobs, j.id)
			s.mu.Unlock()
			<-s.slots
		}()
	})
}
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	jobs := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	for _, j := range jobs {
		s.closeJob(j)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(s.slots) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func sanitizeScriptError(e *ScriptError) {
	allowed := map[string]bool{"SCRIPT_ERROR": true, "SCRIPT_SYNTAXERROR": true, "SCRIPT_TYPEERROR": true, "SCRIPT_VALUEERROR": true, "SCRIPT_NAMEERROR": true, "SCRIPT_ZERODIVISIONERROR": true, "SCRIPT_OVERFLOWERROR": true, "SCRIPT_INDEXERROR": true, "SCRIPT_KEYERROR": true, "SCRIPT_ATTRIBUTEERROR": true, "SCRIPT_MEMORYERROR": true, "SCRIPT_RECURSIONERROR": true, "SCRIPT_IMPORTERROR": true, "SCRIPT_RANGEERROR": true}
	if !allowed[e.Code] {
		e.Code = "SCRIPT_ERROR"
	}
	if e.Line < 0 || e.Line > 65536 {
		e.Line = 0
	}
	if e.Column < 0 || e.Column > 65536 {
		e.Column = 0
	}
}
