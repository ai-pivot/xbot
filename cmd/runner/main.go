package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"xbot/internal/runnerclient"
	"xbot/version"
)

var (
	flagServer      = flag.String("server", "", "WebSocket server URL (required)")
	flagToken       = flag.String("token", "", "Auth token (required)")
	flagWorkspace   = flag.String("workspace", "", "Workspace root directory (default: the current dir — i.e. $HOME over SSH; docker mode defaults to /workspace)")
	flagName        = flag.String("name", "", "Runner name reported to the server (default: hostname)")
	flagFullControl = flag.Bool("full-control", false, "Disable path restrictions (allow access to any file)")
	flagVerbose     = flag.Bool("v", false, "Verbose logging (log all requests)")
	flagMode        = flag.String("mode", "native", "Runner mode: native or docker")
	flagDockerImage = flag.String("docker-image", "ubuntu:22.04", "Docker image (docker mode)")
	flagLLMProvider = flag.String("llm-provider", "", "LLM provider: openai or anthropic (enables local LLM mode)")
	flagLLMBaseURL  = flag.String("llm-base-url", "", "LLM API base URL (for OpenAI-compatible endpoints)")
	flagLLMAPIKey   = flag.String("llm-api-key", "", "LLM API key")
	flagLLMModel    = flag.String("llm-model", "", "LLM model name")
)

const (
	baseDelay  = 1 * time.Second
	maxDelay   = 60 * time.Second
	maxRetries = 0 // 0 = infinite retries
)

func main() {
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	if *flagServer == "" {
		log.Fatal("--server is required")
	}
	if *flagToken == "" {
		log.Fatal("--token is required")
	}

	runnerName := *flagName
	if runnerName == "" {
		if h, err := os.Hostname(); err == nil {
			runnerName = h
		}
	}

	var err error
	var exec runnerclient.Executor
	var dockerMode bool
	var execWorkspace string

	// ⛔ 2026-09-19 用户实机 P0：**默认不再用硬编码 "/workspace"**。
	// 那个根目录在非 root 用户下 mkdir 必失败 ⇒ workspace 建不出来 ⇒ 后续 shell 执行
	// 失败（用户原话："总是试图创建他不一定有权限的目录然后 shell 执行失败"）。
	// native 模式默认用**当前目录**（SSH 会话里即 $HOME），再退 $HOME、最后 "."。
	workspace := *flagWorkspace
	if *flagMode == "docker" {
		if workspace == "" {
			workspace = "/workspace" // 容器内挂载点 —— docker 模式的正确默认
		}
		log.Printf("Docker mode: image=%s, workspace=%s", *flagDockerImage, workspace)
		exec, err = runnerclient.NewDockerExecutor(runnerName, *flagDockerImage, workspace)
		if err != nil {
			log.Fatalf("Failed to create docker executor: %v", err)
		}
		dockerMode = true
	} else {
		if workspace == "" {
			if wd, wdErr := os.Getwd(); wdErr == nil && wd != "" {
				workspace = wd
			} else if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
				workspace = home
			} else {
				workspace = "."
			}
		}
		exec = runnerclient.NewNativeExecutor(workspace)
		dockerMode = false
	}
	execWorkspace = workspace
	defer func() {
		if cerr := exec.Close(); cerr != nil {
			log.Printf("Executor close error: %v", cerr)
		}
	}()

	// 创建 handler
	runnerLogf := func(format string, args ...any) {
		log.Printf(format, args...)
	}
	handler := runnerclient.NewHandler(exec,
		runnerclient.WithVerbose(*flagVerbose),
		runnerclient.WithPathGuard(&runnerclient.PathGuard{
			Workspace:   execWorkspace,
			FullControl: *flagFullControl,
			DockerMode:  dockerMode,
		}),
		runnerclient.WithDockerMode(dockerMode),
		runnerclient.WithLogFunc(runnerLogf),
	)

	// 初始化本地 LLM 客户端
	if *flagLLMProvider != "" {
		if err := handler.InitLLM(*flagLLMProvider, *flagLLMBaseURL, *flagLLMAPIKey, *flagLLMModel); err != nil {
			log.Fatalf("Failed to init local LLM: %v", err)
		}
	}

	// 检测 shell
	shell := runnerclient.DetectShell(dockerMode, exec)

	log.Printf("Starting xbot-runner  mode=%s server=%s  name=%s  version=%s  workspace=%s  full-control=%v",
		*flagMode, *flagServer, runnerName, version.Version, execWorkspace, *flagFullControl)

	serverURL := *flagServer
	if !strings.Contains(serverURL, "://") {
		serverURL = "ws://" + serverURL
	}

	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("Received shutdown signal, stopping...")
		close(stopCh)
	}()

	attempt := 0
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		err := runSession(serverURL, runnerName, *flagToken, execWorkspace, shell, handler)
		// SA4023: runSession never returns nil (a clean read-loop exit is also a
		// disconnection from this side's perspective — it always returns an
		// error to drive the reconnect loop). No err == nil fast path exists.
		select {
		case <-stopCh:
			return
		default:
		}
		attempt++
		if maxRetries > 0 && attempt >= maxRetries {
			log.Fatalf("Max reconnect attempts (%d) reached, giving up", maxRetries)
		}
		delay := backoff(attempt)
		log.Printf("Connection lost: %v  — reconnecting in %v (attempt %d)", err, delay, attempt)
		select {
		case <-stopCh:
			return
		case <-time.After(delay):
		}
	}
}

// runSession 连接 server 并运行读写循环。
// 连接丢失时返回错误（触发重连）。
func runSession(serverURL, runnerName, authToken, workspace, shell string, handler *runnerclient.Handler) error {
	runnerLogf := handler.LogFunc
	conn, err := runnerclient.Connect(serverURL, authToken, workspace, shell, runnerclient.ConnectOptions{
		LLMProvider: handler.LLMProvider(),
		LLMModel:    handler.LLMModel(),
		LogFunc:     runnerLogf,
		RunnerName:  runnerName,
		Version:     version.Version,
	})
	if err != nil {
		return err
	}
	log.Printf("Connected to server, registered as runner=%s", runnerName)

	writeCh := make(chan runnerclient.WriteMsg, 64)
	stopWrite := make(chan struct{})
	writeDone := make(chan struct{})

	// 将写通道暴露给 stdio 处理器（用于推送消息）
	handler.SetWriteChannels(writeCh, writeDone)

	go runnerclient.WritePump(conn, writeCh, stopWrite, writeDone, runnerLogf)
	runnerclient.ReadLoop(conn, handler, writeCh, writeDone, runnerLogf)

	// 通知 writePump 立即退出
	close(stopWrite)

	// 断开连接时杀死活跃的 stdio 进程和后台任务
	handler.Cleanup()

	return fmt.Errorf("read loop exited")
}

// mustRandInt63n returns a cryptographically random int64 in [0, n).
func mustRandInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	r, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0 // fallback to no jitter on error
	}
	return r.Int64()
}

// backoff 返回带随机抖动的指数退避延迟。
func backoff(attempt int) time.Duration {
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
			break
		}
	}
	jitter := time.Duration(mustRandInt63n(int64(delay) / 4))
	return delay + jitter
}
