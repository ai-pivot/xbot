package runnerclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"xbot/internal/runnerproto"
	"xbot/llm"
)

// Handler 处理从 server 收到的请求。
type Handler struct {
	Executor        Executor
	PathGuard       *PathGuard
	LLMClient       llm.LLM
	LLMModels       []string
	LLMProviderName string // provider name for self-reporting (e.g. "openai", "anthropic")
	Verbose         bool

	// 内部管理
	stdioMgr *stdioManager
	bgMgr    *bgTaskManager

	// 日志回调（nil 时静默）
	LogFunc LogFunc

	// 模式标记
	dockerMode bool
}

// HandlerOption 是 Handler 的可选配置函数。
type HandlerOption func(*Handler)

// WithVerbose 设置详细日志。
func WithVerbose(v bool) HandlerOption {
	return func(h *Handler) { h.Verbose = v }
}

// WithPathGuard 设置 PathGuard。
func WithPathGuard(pg *PathGuard) HandlerOption {
	return func(h *Handler) { h.PathGuard = pg }
}

// WithDockerMode 设置 Docker 模式。
func WithDockerMode(v bool) HandlerOption {
	return func(h *Handler) { h.dockerMode = v }
}

// WithLogFunc 设置日志回调函数（nil 时静默）。
func WithLogFunc(f LogFunc) HandlerOption {
	return func(h *Handler) { h.LogFunc = f }
}

// NewHandler 创建一个 Handler。
func NewHandler(exec Executor, opts ...HandlerOption) *Handler {
	h := &Handler{
		Executor: exec,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// InitLLM 初始化 LLM 客户端。
func (h *Handler) InitLLM(provider, baseURL, apiKey, model string) error {
	client, models, err := InitLLMClient(provider, baseURL, apiKey, model, h.LogFunc)
	if err != nil {
		return err
	}
	h.LLMClient = client
	h.LLMModels = models
	if client != nil {
		h.LLMProviderName = provider
	}
	return nil
}

// SetLLMClient 直接设置 LLM 客户端（用于 TUI runner 复用已有客户端）。
// provider 参数用于 runner 自报告 LLM 能力（传空字符串表示无 LLM）。
func (h *Handler) SetLLMClient(client llm.LLM, models []string, provider string) {
	h.LLMClient = client
	h.LLMModels = models
	if client != nil && provider != "" {
		h.LLMProviderName = provider
	}
}

// LLMProvider 返回 LLM provider 名称（空 = 无 LLM）。
func (h *Handler) LLMProvider() string {
	if h.LLMClient == nil {
		return ""
	}
	return h.LLMProviderName
}

// LLMModel 返回默认模型名称。
func (h *Handler) LLMModel() string {
	if len(h.LLMModels) > 0 {
		return h.LLMModels[0]
	}
	return ""
}

// SetWriteChannels 设置写通道（在启动 ReadLoop 前调用）。
func (h *Handler) SetWriteChannels(writeCh chan<- WriteMsg, writeDone <-chan struct{}) {
	h.ensureManagers()
	h.stdioMgr.SetWriteChannels(writeCh, writeDone)
}

// Cleanup 清理所有资源（stdio 进程、后台任务）。
func (h *Handler) Cleanup() {
	if h.stdioMgr != nil {
		h.stdioMgr.Cleanup()
	}
	if h.bgMgr != nil {
		h.bgMgr.Cleanup()
	}
}

// ensureManagers 确保 stdio 和 bg task 管理器已初始化。
func (h *Handler) ensureManagers() {
	if h.stdioMgr == nil {
		h.stdioMgr = newStdioManager(h.Verbose, h.dockerMode, h.LogFunc)
		h.stdioMgr.executor = h.Executor
	}
	if h.bgMgr == nil {
		ws := ""
		if h.PathGuard != nil {
			ws = h.PathGuard.Workspace
		}
		h.bgMgr = newBgTaskManager(h.Verbose, h.dockerMode, ws, h.LogFunc)
		h.bgMgr.executor = h.Executor
	}
}

// HandleRequest 处理一个请求并返回响应。
func (h *Handler) HandleRequest(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	resp := h.Dispatch(msg)

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if json.Unmarshal(resp.Body, &e) == nil {
			callLogf(h.LogFunc, "← %s [id=%s] error: %s — %s", msg.Type, msg.ID, e.Code, e.Message)
		}
	} else if h.Verbose {
		callLogf(h.LogFunc, "← %s [id=%s] ok", msg.Type, msg.ID)
	}

	return resp
}

// Dispatch 根据消息类型分发到对应的处理函数。
func (h *Handler) Dispatch(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	h.ensureManagers()

	switch msg.Type {
	case runnerproto.ProtoExec:
		return h.handleExec(msg)
	case runnerproto.ProtoBgExec:
		return h.handleBgExec(msg)
	case runnerproto.ProtoBgKill:
		return h.handleBgKill(msg)
	case runnerproto.ProtoBgStatus:
		return h.handleBgStatus(msg)
	case runnerproto.ProtoLLMGenerate:
		return handleLLMGenerate(msg, h.LLMClient, h.LogFunc)
	case runnerproto.ProtoLLMModels:
		return handleLLMModels(msg, h.LLMClient, h.LLMModels, h.LogFunc)
	case runnerproto.ProtoReadFile:
		return h.handleReadFile(msg)
	case runnerproto.ProtoWriteFile:
		return h.handleWriteFile(msg)
	case runnerproto.ProtoStat:
		return h.handleStat(msg)
	case runnerproto.ProtoReadDir:
		return h.handleReadDir(msg)
	case runnerproto.ProtoMkdirAll:
		return h.handleMkdirAll(msg)
	case runnerproto.ProtoRemove:
		return h.handleRemove(msg)
	case runnerproto.ProtoRemoveAll:
		return h.handleRemoveAll(msg)
	case runnerproto.ProtoDownloadFile:
		return h.handleDownloadFile(msg)
	case runnerproto.ProtoStdioStart:
		return h.stdioMgr.HandleStart(msg)
	case runnerproto.ProtoStdioClose:
		return h.stdioMgr.HandleClose(msg)
	default:
		return runnerproto.MakeError(msg.ID, "EINVAL", fmt.Sprintf("unknown request type: %s", msg.Type))
	}
}

// DispatchFireAndForget 处理不需要响应的消息。
func (h *Handler) DispatchFireAndForget(msg runnerproto.RunnerMessage) {
	h.ensureManagers()

	switch msg.Type {
	case runnerproto.ProtoStdioWrite:
		h.stdioMgr.HandleWrite(msg)
	}
}

// handleFileOp is a generic template for file operation handlers.
// It handles the common pattern: Unmarshal → safePath → Execute → Respond.
//
// Parameters:
//   - parseFn: unmarshals msg.Body into request type T
//   - getPath: extracts the path string from the unmarshaled request
//   - execFn: performs the actual operation using the safePath and parsed request,
//     returning the response body (or nil for OK responses) and any error
//   - respType: the response type constant for runnerproto.MakeResponse;
//     when empty, the function returns runnerproto.MakeOK
func handleFileOp[T any](h *Handler, msg runnerproto.RunnerMessage,
	parseFn func(json.RawMessage) (T, error),
	getPath func(T) string,
	execFn func(safePath string, req T) (any, error),
	respType string,
) *runnerproto.RunnerMessage {
	req, err := parseFn(msg.Body)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(getPath(req))
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	result, err := execFn(path, req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	if respType == "" {
		return runnerproto.MakeOK(msg.ID)
	}
	return runnerproto.MakeResponse(msg.ID, respType, result)
}

func (h *Handler) handleExec(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.ExecRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid exec request: "+err.Error())
	}

	timeout := time.Duration(req.Timeout) * time.Second
	// Guard against integer overflow: cap at 1 hour
	if req.Timeout <= 0 || req.Timeout > 3600 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	spec := ExecSpec{
		Command:   req.Command,
		Args:      req.Args,
		Shell:     req.Shell,
		Dir:       req.Dir,
		Env:       req.Env,
		Stdin:     req.Stdin,
		Timeout:   timeout,
		RunAsUser: req.RunAsUser,
	}

	// pathguard 检查工作目录：不通过则回退到 workspace（用户无需感知 runner 路径差异）
	if spec.Dir != "" && h.PathGuard != nil {
		if err := h.PathGuard.Validate(spec.Dir); err != nil {
			spec.Dir = h.PathGuard.Workspace
		}
	}

	result, err := h.Executor.Exec(ctx, spec)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "exec error: "+err.Error())
	}

	callLogf(h.LogFunc, "  exec done  exit=%d  stdout=%dB  stderr=%dB", result.ExitCode, len(result.Stdout), len(result.Stderr))
	return runnerproto.MakeResponse(msg.ID, "exec_result", runnerproto.ExecResultResponse{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
		TimedOut: result.TimedOut,
	})
}

func (h *Handler) handleReadFile(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.ReadFileRequest, error) {
			var req runnerproto.ReadFileRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.ReadFileRequest) string { return req.Path },
		func(safePath string, req runnerproto.ReadFileRequest) (any, error) {
			data, err := h.Executor.ReadFile(safePath)
			if err != nil {
				return nil, err
			}
			if h.Verbose {
				callLogf(h.LogFunc, "  read_file %s (%d bytes)", req.Path, len(data))
			}
			return runnerproto.FileContentResponse{
				Data: base64.StdEncoding.EncodeToString(data),
			}, nil
		},
		"file_content",
	)
}

func (h *Handler) handleWriteFile(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.WriteFileRequest, error) {
			var req runnerproto.WriteFileRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.WriteFileRequest) string { return req.Path },
		func(safePath string, req runnerproto.WriteFileRequest) (any, error) {
			data, err := base64.StdEncoding.DecodeString(req.Data)
			if err != nil {
				return nil, err
			}
			if err := h.Executor.WriteFile(safePath, data, os.FileMode(req.Perm)); err != nil {
				return nil, err
			}
			if h.Verbose {
				callLogf(h.LogFunc, "  write_file %s (%d bytes)", req.Path, len(data))
			}
			return nil, nil
		},
		"",
	)
}

func (h *Handler) handleStat(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.StatRequest, error) {
			var req runnerproto.StatRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.StatRequest) string { return req.Path },
		func(safePath string, _ runnerproto.StatRequest) (any, error) {
			info, err := h.Executor.Stat(safePath)
			if err != nil {
				return nil, err
			}
			return runnerproto.StatResponse{
				Name:    info.Name,
				Size:    info.Size,
				Mode:    uint32(info.Mode),
				ModTime: info.ModTime.Format(time.RFC3339),
				IsDir:   info.IsDir,
			}, nil
		},
		"file_info",
	)
}

func (h *Handler) handleReadDir(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.ReadDirRequest, error) {
			var req runnerproto.ReadDirRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.ReadDirRequest) string { return req.Path },
		func(safePath string, req runnerproto.ReadDirRequest) (any, error) {
			entries, err := h.Executor.ReadDir(safePath)
			if err != nil {
				return nil, err
			}
			resp := runnerproto.DirEntriesResponse{Entries: make([]runnerproto.DirEntryResponse, 0, len(entries))}
			for _, e := range entries {
				resp.Entries = append(resp.Entries, runnerproto.DirEntryResponse{
					Name:  e.Name,
					IsDir: e.IsDir,
					Size:  e.Size,
				})
			}
			if h.Verbose {
				callLogf(h.LogFunc, "  read_dir %s (%d entries)", req.Path, len(resp.Entries))
			}
			return resp, nil
		},
		"dir_entries",
	)
}

func (h *Handler) handleMkdirAll(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.PathRequest, error) {
			var req runnerproto.PathRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.PathRequest) string { return req.Path },
		func(safePath string, req runnerproto.PathRequest) (any, error) {
			if err := h.Executor.MkdirAll(safePath, os.FileMode(req.Perm)); err != nil {
				return nil, err
			}
			if h.Verbose {
				callLogf(h.LogFunc, "  mkdir_all %s", req.Path)
			}
			return nil, nil
		},
		"",
	)
}

func (h *Handler) handleRemove(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.PathRequest, error) {
			var req runnerproto.PathRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.PathRequest) string { return req.Path },
		func(safePath string, req runnerproto.PathRequest) (any, error) {
			if err := h.Executor.Remove(safePath); err != nil {
				return nil, err
			}
			if h.Verbose {
				callLogf(h.LogFunc, "  remove %s", req.Path)
			}
			return nil, nil
		},
		"",
	)
}

func (h *Handler) handleRemoveAll(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.PathRequest, error) {
			var req runnerproto.PathRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.PathRequest) string { return req.Path },
		func(safePath string, req runnerproto.PathRequest) (any, error) {
			if err := h.Executor.RemoveAll(safePath); err != nil {
				return nil, err
			}
			if h.Verbose {
				callLogf(h.LogFunc, "  remove_all %s", req.Path)
			}
			return nil, nil
		},
		"",
	)
}

func (h *Handler) handleDownloadFile(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	return handleFileOp(h, msg,
		func(raw json.RawMessage) (runnerproto.DownloadFileRequest, error) {
			var req runnerproto.DownloadFileRequest
			err := json.Unmarshal(raw, &req)
			return req, err
		},
		func(req runnerproto.DownloadFileRequest) string { return req.OutputPath },
		func(safePath string, req runnerproto.DownloadFileRequest) (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			size, err := h.Executor.DownloadFile(ctx, req.URL, safePath)
			if err != nil {
				return nil, err
			}
			callLogf(h.LogFunc, "  download_file %s → %s (%d bytes)", req.URL, req.OutputPath, size)
			return runnerproto.DownloadFileResponse{Size: size}, nil
		},
		runnerproto.ProtoOK,
	)
}

func (h *Handler) handleBgExec(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.BgExecRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid bg_exec request: "+err.Error())
	}

	// pathguard 检查工作目录：不通过则回退到 workspace
	if req.Dir != "" && h.PathGuard != nil {
		if err := h.PathGuard.Validate(req.Dir); err != nil {
			req.Dir = h.PathGuard.Workspace
		}
	}

	resp, err := h.bgMgr.Start(req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "bg_exec failed: "+err.Error())
	}

	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoBgStarted, resp)
}

func (h *Handler) handleBgKill(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.BgKillRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid bg_kill request: "+err.Error())
	}

	if err := h.bgMgr.Kill(req); err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "bg_kill failed: "+err.Error())
	}

	return runnerproto.MakeOK(msg.ID)
}

func (h *Handler) handleBgStatus(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.BgStatusRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid bg_status request: "+err.Error())
	}

	resp, err := h.bgMgr.Status(req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "bg_status failed: "+err.Error())
	}

	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoBgOutput, resp)
}

// safePath 是 PathGuard.SafePath 的便捷方法。
func (h *Handler) safePath(path string) (string, error) {
	if h.PathGuard == nil {
		return path, nil
	}
	return h.PathGuard.SafePath(path)
}
