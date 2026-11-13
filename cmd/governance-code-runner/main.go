package main

import (
	"context"
	"datamesh/internal/platform/coderunner"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	token, err := os.ReadFile(os.Getenv("CODE_RUNNER_TOKEN_FILE"))
	if err != nil {
		slog.Error("代码执行器令牌不可用")
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "--health" {
		request, _ := http.NewRequest("GET", "http://127.0.0.1:8080/v1/runtimes", nil)
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		client := &http.Client{Timeout: 5 * time.Second}
		response, e := client.Do(request)
		if e != nil {
			os.Exit(1)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	namespace := os.Getenv("CODE_RUNNER_NAMESPACE")
	if namespace == "" {
		namespace = "datamesh-governance"
	}
	socket := os.Getenv("CODE_RUNNER_DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	maxJobs := 2
	if raw := os.Getenv("CODE_RUNNER_MAX_JOBS"); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > 2 {
			slog.Error("代码执行器并发配置无效")
			os.Exit(1)
		}
		maxJobs = value
	}
	runner, err := coderunner.New(coderunner.Config{MaxJobs: maxJobs, Token: strings.TrimSpace(string(token)), Socket: socket, Namespace: namespace, Images: map[string]string{"python": os.Getenv("CODE_PYTHON_IMAGE"), "javascript": os.Getenv("CODE_JAVASCRIPT_IMAGE")}})
	if err != nil {
		slog.Error("代码执行器初始化失败")
		os.Exit(1)
	}
	server := &http.Server{Addr: ":8080", Handler: runner.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 130 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = runner.Close(closeCtx)
		_ = server.Shutdown(closeCtx)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("代码执行器服务异常")
		os.Exit(1)
	}
}
