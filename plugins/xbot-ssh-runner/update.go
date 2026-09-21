package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ai-pivot/xbot/plugin/protocol"
)

// ============================================================================
// Runner 自动更新（VS Code 语义）—— 用户要求 2026-09-19
//
// 目标：**后台检查 + 后台暂存**，**下次启动才生效**，绝不打断正在跑的 runner。
//
// 检查很便宜：远端 `sha256sum <install_dir>/xbot-runner`（一个数）+ 本地取
// `<download_base>/checksums.txt`（几百字节）比对期望 sha。不等（或缺失）⇒ 走与
// provision 完全相同的 `download → sha256 校验 → 原子替换`，但**不做 kill-old**：
// 正在运行的进程持有旧 inode，继续服务；下一次 connect（= 下一次启动）自然是新二进制。
//
// 依赖全部来自标准库（本插件是独立 module，只依赖 plugin/protocol）。
// ============================================================================

// needsUpdate 纯函数：远端已装 sha 与最新 sha 不同（含缺失）即需要更新。
// 空 installed（未安装/不可读）⇒ 需要（由调用方决定是否安装）。
func needsUpdate(installed, latest string) bool {
	installed = strings.TrimSpace(installed)
	latest = strings.TrimSpace(latest)
	if latest == "" {
		return false // 拿不到期望值 ⇒ 不做判断（绝不因基础设施抖动而乱动文件）
	}
	if installed == "" {
		return true
	}
	return !strings.EqualFold(installed, latest)
}

// fetchChecksums 从 download_base 取 checksums.txt（本地 HTTP；代理/网络问题只报错，不 panic）。
func fetchChecksums(ctx context.Context, base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", errors.New("download_base is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/checksums.txt", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch checksums: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	return string(body), nil
}

// installedShaScript 远端计算已装二进制的 sha256；文件缺失/不可读 ⇒ 输出空行（不失败）。
func installedShaScript(binPath string) string {
	p := shellQuote(binPath)
	return "if [ -f " + p + " ]; then sha256sum " + p + " 2>/dev/null | awk '{print $1}'; fi"
}

// updateState 记录最近一次"检查/暂存"的结果，供 status 展示（进程内即可）。
type updateState struct {
	CheckedAt   time.Time
	InstalledAt string // 检查时远端的 sha
	LatestSha   string // checksums.txt 里的期望 sha
	Staged      bool   // 是否已把新二进制暂存（原子替换完成）⇒ 下次启动生效
	Detail      string
	Err         string
}

// checkAndStageUpdate 后台"检查 + 暂存"：
//
//	① 远端 installed sha（缺失 ⇒ 视为需要更新）
//	② 本地取 checksums.txt → parseChecksums(asset)
//	③ 相同 ⇒ 无需更新；不同 ⇒ download → sha256 校验 → **原子替换**（不 kill、不重启）
//
// 任何一步失败都只返回错误（调用方打日志/状态），**绝不影响正在运行的 runner**。
func (s *service) checkAndStageUpdate(ctx context.Context, p provisionParams, plat platform, installDir string) (staged bool, detail string, err error) {
	binPath := strings.TrimRight(installDir, "/") + "/xbot-runner"

	// ① 远端当前 sha
	out, err := s.exec(ctx, p.SSH, sshCommandTimeout, installedShaScript(binPath))
	if err != nil {
		return false, "", fmt.Errorf("read installed sha: %w", err)
	}
	installed := strings.TrimSpace(out)

	// ② 期望 sha
	checksums, err := fetchChecksums(ctx, p.DownloadBase)
	if err != nil {
		return false, "", err
	}
	expected, err := parseChecksums(checksums, plat.Asset)
	if err != nil {
		return false, "", err
	}
	if !needsUpdate(installed, expected) {
		return false, "up to date (" + clipRunes(expected, 12) + "…)", nil
	}

	// ③ 暂存：download → 校验 → 原子替换（与 provision 同一套脚本；**不做 kill-old**）
	out, err = s.exec(ctx, p.SSH, sshDownloadTimeout, downloadScript(p.DownloadBase, plat.Asset, p.Name, installDir))
	if err != nil {
		return false, "", fmt.Errorf("update download failed: %w", err)
	}
	kv := parseKeyValueLines(out)
	binRemote := strings.TrimSpace(kv["BIN_PATH"])
	tmpDir := strings.TrimSpace(kv["TMP_DIR"])
	if binRemote == "" || tmpDir == "" {
		return false, "", errors.New("update download did not report artifact paths")
	}
	body, ok := extractBetween(out, "CHECKSUMS_BEGIN=", "CHECKSUMS_END")
	if !ok {
		return false, "", errors.New("update: checksums.txt content missing")
	}
	expected2, err := parseChecksums(body, plat.Asset)
	if err != nil {
		return false, "", err
	}
	if _, err := s.exec(ctx, p.SSH, sshCommandTimeout, verifyScript(binRemote, expected2)); err != nil {
		return false, "", fmt.Errorf("update verify failed: %w", err)
	}
	// 原子替换：mv 同文件系统 rename ⇒ 不触碰正在运行的进程（它持有旧 inode）。
	if _, err := s.exec(ctx, p.SSH, sshCommandTimeout, installScript(binRemote, installDir, tmpDir)); err != nil {
		return false, "", fmt.Errorf("update install (atomic replace) failed: %w", err)
	}
	return true, "staged " + clipRunes(expected2, 12) + "… (effective on next start)", nil
}

// maybeAutoUpdate 在 (re)connect 之后**后台**跑一次检查+暂存（best-effort，绝不阻塞/影响连接）。
func (s *service) maybeAutoUpdate(sshField, name, downloadBase, installDir string, plat platform) {
	if !s.autoUpdateEnabled() {
		return
	}
	p := provisionParams{SSH: sshField, Name: name, DownloadBase: downloadBase, InstallDir: installDir}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		staged, detail, err := s.checkAndStageUpdate(ctx, p, plat, installDir)
		state := updateState{CheckedAt: time.Now(), Staged: staged, Detail: detail}
		if err != nil {
			state.Err = err.Error()
			logf("auto-update %s: check/stage failed: %v", name, err)
		} else if staged {
			logf("auto-update %s: %s", name, detail)
		} else {
			logf("auto-update %s: %s", name, detail)
		}
		s.setUpdateState(name, state)
	}()
}

// autoUpdateEnabled：默认开启（用户要求"默认像 vsc 一样后台自动更新"）。
func (s *service) autoUpdateEnabled() bool {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	if s.autoUpdate == nil {
		return true
	}
	return *s.autoUpdate
}

// setUpdateState / UpdateStateOf：记录与读取最近一次检查结果（供 status 展示）。
func (s *service) setUpdateState(name string, st updateState) {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	if s.updates == nil {
		s.updates = map[string]updateState{}
	}
	s.updates[name] = st
}

// UpdateStateOf 返回该 target 最近一次自动更新的结果（零值 = 从未检查）。
func (s *service) UpdateStateOf(name string) updateState {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	return s.updates[name]
}

// rememberDownloadBase 记住该 target 的下载基址（status/手动检查复用；download_base
// 在 connect 时可能未传，则回落 defaultDownloadBase）。
func (s *service) rememberDownloadBase(name, base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return
	}
	s.updMu.Lock()
	defer s.updMu.Unlock()
	if s.dlBases == nil {
		s.dlBases = map[string]string{}
	}
	s.dlBases[name] = base
}

// DownloadBaseOf 返回该 target 记下的下载基址（空 ⇒ 调用方用 defaultDownloadBase）。
func (s *service) DownloadBaseOf(name string) string {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	return s.dlBases[name]
}

// detectPlatformOn 远端探测平台（与 provision 的 detect 步完全同源：detectScript +
// normalizePlatform），供自动更新/手动检查复用。
func (s *service) detectPlatformOn(ctx context.Context, sshField string) (platform, error) {
	out, err := s.exec(ctx, sshField, sshCommandTimeout, detectScript())
	if err != nil {
		return platform{}, fmt.Errorf("detect platform: %w", err)
	}
	kv := parseKeyValueLines(out)
	return normalizePlatform(kv["OS"], kv["ARCH"])
}

// handleCheckUpdate：手动触发一次"检查 + 暂存"（面板按钮用）。同步执行但带超时；
// 语义与后台自动更新完全一致（**不 kill、不重启**，只原子替换 ⇒ 下次启动生效）。
func (s *service) handleCheckUpdate(params map[string]any) (*protocol.WebPluginRPCResult, error) {
	sshField := strParam(params, "ssh")
	if sshField == "" {
		return rpcErr(`ssh is required (e.g. "ssh user@host")`), nil
	}
	name := strParam(params, "name")
	if err := validateTargetName(name); err != nil {
		return rpcErr(err.Error()), nil
	}
	installDir := strParam(params, "install_dir")
	if installDir == "" {
		if prev, ok := s.state.Get(name); ok && prev.InstallDir != "" {
			installDir = prev.InstallDir
		}
	}
	if installDir == "" {
		installDir = "/usr/local/bin"
	}
	base := strParam(params, "download_base")
	if base == "" {
		base = s.DownloadBaseOf(name)
	}
	if base == "" {
		base = defaultDownloadBase
	}
	s.rememberDownloadBase(name, base)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	plat, err := s.detectPlatformOn(ctx, sshField)
	if err != nil {
		return rpcErr(err.Error()), nil
	}
	staged, detail, cerr := s.checkAndStageUpdate(ctx, provisionParams{
		SSH: sshField, Name: name, DownloadBase: strings.TrimRight(base, "/"), InstallDir: installDir,
	}, plat, installDir)
	st := updateState{CheckedAt: time.Now(), Staged: staged, Detail: detail}
	if cerr != nil {
		st.Err = cerr.Error()
	}
	s.setUpdateState(name, st)
	out := map[string]any{
		"staged":      staged,
		"detail":      detail,
		"checked_at":  st.CheckedAt,
		"auto_update": s.autoUpdateEnabled(),
	}
	if cerr != nil {
		out["error"] = cerr.Error()
	}
	return rpcOK(out), nil
}
