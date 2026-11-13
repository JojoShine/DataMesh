package coderunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Docker struct {
	socket string
	client *http.Client
}

func NewDocker(socket string) *Docker {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Docker{socket: socket, client: &http.Client{Transport: transport, Timeout: 15 * time.Second}}
}
func (d *Docker) request(ctx context.Context, method, path string, input, output interface{}) error {
	var body io.Reader
	if input != nil {
		data, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(data)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://docker/v1.47"+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	response, e := d.client.Do(req)
	if e != nil {
		return errors.New("container service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return errContainerMissing
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("container operation failed: %d", response.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(output)
	}
	return nil
}

var errContainerMissing = errors.New("container missing")

func (d *Docker) image(ctx context.Context, id string) bool {
	return d.request(ctx, "GET", "/images/"+url.PathEscape(id)+"/json", nil, &struct{}{}) == nil
}
func (d *Docker) create(ctx context.Context, image, namespace, job string) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	input := map[string]interface{}{"Image": image, "User": "65532:65532", "OpenStdin": true, "StdinOnce": true, "AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": false, "NetworkDisabled": true, "Labels": map[string]string{"datamesh.code.namespace": namespace, "datamesh.code.job": job}, "HostConfig": map[string]interface{}{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": int64(256 * 1024 * 1024), "MemorySwap": int64(256 * 1024 * 1024), "NanoCpus": int64(1000000000), "PidsLimit": 32, "Tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=33554432,mode=1777"}, "LogConfig": map[string]string{"Type": "none"}, "Ulimits": []map[string]interface{}{{"Name": "nofile", "Soft": 128, "Hard": 128}, {"Name": "core", "Soft": 0, "Hard": 0}, {"Name": "fsize", "Soft": 33554432, "Hard": 33554432}}}}
	e := d.request(ctx, "POST", "/containers/create", input, &out)
	return out.ID, e
}
func (d *Docker) start(ctx context.Context, id string) error {
	return d.request(ctx, "POST", "/containers/"+id+"/start", nil, nil)
}
func (d *Docker) remove(ctx context.Context, id string) error {
	e := d.request(ctx, "DELETE", "/containers/"+id+"?force=true&v=true", nil, nil)
	if errors.Is(e, errContainerMissing) {
		return nil
	}
	return e
}
func (d *Docker) cleanup(ctx context.Context, namespace string) error {
	filters, _ := json.Marshal(map[string][]string{"label": {"datamesh.code.namespace=" + namespace}})
	var rows []struct {
		ID string `json:"Id"`
	}
	if e := d.request(ctx, "GET", "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &rows); e != nil {
		return e
	}
	for _, row := range rows {
		if e := d.remove(ctx, row.ID); e != nil {
			return e
		}
	}
	return nil
}

type attached struct {
	net.Conn
	reader *bufio.Reader
}

func (a *attached) Read(b []byte) (int, error) { return a.reader.Read(b) }
func (d *Docker) attach(ctx context.Context, id string) (io.ReadWriteCloser, error) {
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", d.socket)
	if e != nil {
		return nil, e
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req, _ := http.NewRequest("POST", "http://docker/v1.47/containers/"+id+"/attach?stream=1&stdin=1&stdout=1&stderr=1", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if e = req.Write(conn); e != nil {
		conn.Close()
		return nil, e
	}
	reader := bufio.NewReader(conn)
	response, e := http.ReadResponse(reader, req)
	if e != nil {
		conn.Close()
		return nil, e
	}
	if response.StatusCode != 101 && response.StatusCode != 200 {
		conn.Close()
		return nil, errors.New("container stream unavailable")
	}
	_ = conn.SetDeadline(time.Time{})
	return &attached{Conn: conn, reader: reader}, nil
}

// Only stdout participates in the bounded protocol. stderr is drained and never persisted.
func demultiplex(ctx context.Context, stream io.Reader, stdout *io.PipeWriter) error {
	defer stdout.Close()
	var header [8]byte
	var stderrBytes int64
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		if _, e := io.ReadFull(stream, header[:]); e != nil {
			return e
		}
		size := binary.BigEndian.Uint32(header[4:])
		if size > MaxBatchBytes+65536 {
			return errors.New("container frame too large")
		}
		switch header[0] {
		case 1:
			if _, e := io.CopyN(stdout, stream, int64(size)); e != nil {
				return e
			}
		case 2:
			stderrBytes += int64(size)
			if stderrBytes > 65536 {
				return errors.New("script log limit")
			}
			if _, e := io.CopyN(io.Discard, stream, int64(size)); e != nil {
				return e
			}
		default:
			return errors.New("invalid container stream")
		}
	}
}
func validImage(id string) bool {
	if !strings.HasPrefix(id, "sha256:") || len(id) != 71 {
		return false
	}
	for _, c := range id[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (d *Docker) isolation(ctx context.Context) error {
	var info struct {
		MemoryLimit     bool
		SwapLimit       bool
		CPUCfsQuota     bool
		PidsLimit       bool
		SecurityOptions []string
	}
	if e := d.request(ctx, "GET", "/info", nil, &info); e != nil {
		return e
	}
	seccomp := false
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "seccomp") {
			seccomp = true
		}
	}
	if !info.MemoryLimit || !info.SwapLimit || !info.CPUCfsQuota || !info.PidsLimit || !seccomp {
		return errors.New("required code isolation limits unavailable")
	}
	return nil
}
