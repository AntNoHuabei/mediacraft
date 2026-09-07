package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AntNoHuabei/mediacraft/catalog"
)

const (
	// sdCppStartupTimeout sd-server 就绪最长等待（与服务端大模型加载耗时匹配）。
	sdCppStartupTimeout = 2 * time.Minute
	// sdCppProbeInterval 就绪探测间隔。
	sdCppProbeInterval = 500 * time.Millisecond
	// sdCppProbeTimeout 单次 TCP 探测超时。
	sdCppProbeTimeout = time.Second
)

// SDCppRuntime sd.cpp 运行时托管：每模型一个 sd-server.exe 进程，
// 模型子文件（diffusion_model/vae/llm/clip/t5）以命令行参数传入。
type SDCppRuntime struct {
	*BaseRuntime

	mu     sync.RWMutex
	models map[string]*runningServer
}

// NewSDCppRuntime 构造 sd.cpp 运行时（会扫描一次安装状态）。
func NewSDCppRuntime(manifest catalog.Runtime, modelManifests []catalog.Manifest, opts BaseOptions) *SDCppRuntime {
	return &SDCppRuntime{
		BaseRuntime: NewBaseRuntime(SDCpp, manifest, modelManifests, opts),
		models:      map[string]*runningServer{},
	}
}

// resolveModelFile 解析模型目录内的相对文件（防目录逃逸）。
func resolveModelFile(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("empty model file name")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) {
		return clean, nil
	}
	target := filepath.Join(root, clean)
	absRoot, _ := filepath.Abs(root)
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if absTarget != absRoot && !strings.HasPrefix(absTarget, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("model file path escapes model directory")
	}
	return absTarget, nil
}

// sdCppParamString 读取合并参数中的字符串。
func sdCppParamString(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

// sdCppParamBool 读取布尔参数（字符串 true/1 也接受）。
func sdCppParamBool(params map[string]any, key string) bool {
	switch value := params[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true") || strings.TrimSpace(value) == "1"
	case float64:
		return value != 0
	}
	return false
}

// buildSDCppArgs 依据模型合并参数构造 sd-server.exe 启动参数。
//
// 参数语义：可选模型文件（vae/llm/clip_l/clip_g/t5xxl）逐个校验存在；
// 开关类参数仅当显式为 true 才附加；未给的参数交给 sd-server 自身默认
// （width/height/steps/cfg 由推理请求控制）。
func (r *SDCppRuntime) buildSDCppArgs(model ModelInfo, port int) ([]string, error) {
	params := model.Parameters

	diffusionParam := sdCppParamString(params, "diffusion_model")
	if diffusionParam == "" {
		return nil, NewStartModelError(CodeInvalidOptions, fmt.Sprintf("model %s has no diffusion_model parameter", model.Name))
	}
	diffusionPath, err := resolveModelFile(model.InstallPath, diffusionParam)
	if err != nil {
		return nil, NewStartModelError(CodeInvalidOptions, "resolve diffusion model path").WithCause(err)
	}
	if _, err := os.Stat(diffusionPath); err != nil {
		return nil, NewStartModelError(CodeModelFileMissing, fmt.Sprintf("diffusion model not found: %s", diffusionPath))
	}

	args := []string{
		"--diffusion-model", diffusionPath,
		"-l", "127.0.0.1",
		"--listen-port", strconv.Itoa(port),
		"-v",
	}

	appendOptional := func(key string) error {
		name := sdCppParamString(params, key)
		if name == "" {
			return nil
		}
		path, err := resolveModelFile(model.InstallPath, name)
		if err != nil {
			return NewStartModelError(CodeInvalidOptions, fmt.Sprintf("resolve %s path", key)).WithCause(err)
		}
		if _, err := os.Stat(path); err != nil {
			return NewStartModelError(CodeModelFileMissing, fmt.Sprintf("%s file not found: %s", key, path))
		}
		args = append(args, "--"+key, path)
		return nil
	}
	for _, key := range []string{"vae", "llm", "clip_l", "clip_g", "t5xxl"} {
		if err := appendOptional(key); err != nil {
			return nil, err
		}
	}
	if loraDir := sdCppParamString(params, "lora_model_dir"); loraDir != "" {
		path, err := resolveModelFile(model.InstallPath, loraDir)
		if err == nil {
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				args = append(args, "--lora-model-dir", path)
			}
		}
	}
	if sdCppParamBool(params, "diffusion_fa") {
		args = append(args, "--diffusion-fa")
	}
	if sdCppParamBool(params, "offload_to_cpu") {
		args = append(args, "--offload-to-cpu")
	}
	if sdCppParamBool(params, "vae_on_cpu") {
		args = append(args, "--vae-on-cpu")
	}
	if sdCppParamBool(params, "clip_on_cpu") {
		args = append(args, "--clip-on-cpu")
	}
	if sdCppParamBool(params, "verbose") {
		args = append(args, "--verbose")
	}
	if value := sdCppParamString(params, "cache_mode"); value != "" {
		args = append(args, "--cache-mode", value)
		if option := sdCppParamString(params, "cache_option"); option != "" {
			args = append(args, "--cache-option", option)
		}
	}
	return args, nil
}

// StartModel 启动模型的 sd-server 进程（TCP 就绪探测）。
func (r *SDCppRuntime) StartModel(ctx context.Context, model ModelInfo, port int) error {
	if status, _ := r.installState(); status != StatusInstalled {
		return NewStartModelError(CodeRuntimeNotInstalled, fmt.Sprintf("runtime %s is not installed", r.name))
	}
	exe := r.executablePath()
	if _, err := os.Stat(exe); err != nil {
		return NewStartModelError(CodeExecutableMissing, "sd-server executable missing").WithDetail("path", exe)
	}
	args, err := r.buildSDCppArgs(model, port)
	if err != nil {
		return err
	}

	r.mu.Lock()
	if existing, ok := r.models[model.Name]; ok {
		if existing.cmd == nil || existing.cmd.Process == nil || runtimeProcessExited(existing.cmd) {
			delete(r.models, model.Name)
		} else {
			r.mu.Unlock()
			return nil // 幂等
		}
	}
	r.mu.Unlock()

	cmd := exec.Command(exe, args...)
	cmd.Dir = r.installPath()
	cmd.Env = withPathEntry(os.Environ(), r.installPath())
	cmd.SysProcAttr = serverSysProcAttr(true)
	cmd.Stdout = io.MultiWriter(r.Recent())
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		retry := exec.Command(exe, args...)
		retry.Dir = cmd.Dir
		retry.Env = cmd.Env
		retry.SysProcAttr = serverSysProcAttr(false)
		retry.Stdout = cmd.Stdout
		retry.Stderr = retry.Stdout
		if retryErr := retry.Start(); retryErr != nil {
			return NewStartModelError(CodeProcessStartFailed, fmt.Sprintf("start sd-server for %s", model.Name)).WithCause(retryErr)
		}
		cmd = retry
	}
	job, jobErr := assignToJobObject(cmd.Process.Pid)
	if jobErr != nil {
		r.Logger().Warn("assign sd-server to job object failed; taskkill will be used on stop", "pid", cmd.Process.Pid, "error", jobErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := r.waitReady(ctx, cmd, done, port, model.Name); err != nil {
		if job != 0 {
			closeJobHandle(job)
		}
		killProcessTree(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		return err
	}
	r.mu.Lock()
	r.models[model.Name] = &runningServer{
		cmd:  cmd,
		job:  job,
		done: done,
		info: ModelRuntimeInfo{
			Engine:      SDCpp,
			RunStatus:   RunStatusRunning,
			StartTime:   time.Now(),
			Port:        port,
			InstallPath: model.InstallPath,
			RuntimePath: r.installPath(),
			Options:     model.Parameters,
		},
	}
	r.mu.Unlock()
	return nil
}

// sdCppDriverLogBlocked 通过启动日志快速识别 CUDA 驱动版本过旧。
func sdCppDriverLogBlocked(logText string) bool {
	lower := strings.ToLower(logText)
	return strings.Contains(lower, "failed to initialize cuda") &&
		strings.Contains(lower, "cuda driver version is insufficient for cuda runtime version")
}

// waitReady 纯 TCP 就绪轮询；启动日志出现 CUDA 驱动过旧特征串时快速失败。
func (r *SDCppRuntime) waitReady(ctx context.Context, cmd *exec.Cmd, done chan error, port int, modelName string) error {
	deadline := time.Now().Add(sdCppStartupTimeout)
	for {
		if TCPPortOpen("127.0.0.1", port, sdCppProbeTimeout) {
			return nil
		}
		if text := r.Recent().Text(); sdCppDriverLogBlocked(text) {
			return NewStartModelError(CodeGPUDriverOutdated, "CUDA driver version is outdated for the sd-server runtime").
				WithDetail("recent_log", r.Recent().Tail(recentLogTailBytes))
		}
		select {
		case err := <-done:
			return NewStartModelError(CodeProcessExited, fmt.Sprintf("sd-server exited before ready for %s", modelName)).
				WithCause(err).
				WithDetail("recent_log", r.Recent().Tail(recentLogTailBytes))
		case <-ctx.Done():
			return NewStartModelError(CodeContextCanceled, "start image model cancelled")
		default:
		}
		if time.Now().After(deadline) {
			return NewStartModelError(CodeStartupTimeout, fmt.Sprintf("sd-server startup timeout on port %d", port)).
				WithDetail("recent_log", r.Recent().Tail(recentLogTailBytes))
		}
		time.Sleep(sdCppProbeInterval)
	}
}

// StopModel 停止指定模型进程；modelName 为空时停止全部。
func (r *SDCppRuntime) StopModel(ctx context.Context, modelName string) error {
	_ = ctx
	r.mu.Lock()
	entries := map[string]*runningServer{}
	if modelName == "" {
		for name, entry := range r.models {
			entries[name] = entry
		}
		r.models = map[string]*runningServer{}
	} else if entry, ok := r.models[modelName]; ok {
		entries[modelName] = entry
		delete(r.models, modelName)
	} else {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	for _, entry := range entries {
		if entry.job != 0 {
			closeJobHandle(entry.job)
		}
		if entry.cmd != nil && entry.cmd.Process != nil {
			killProcessTree(entry.cmd.Process.Pid)
			_ = entry.cmd.Process.Kill()
		}
		if entry.done != nil {
			select {
			case <-entry.done:
			default:
			}
		}
	}
	return nil
}

// StopAllModels 停止全部模型进程。
func (r *SDCppRuntime) StopAllModels(ctx context.Context) error {
	return r.StopModel(ctx, "")
}

// CheckHealth 周期健康检查：进程存活 + TCP 端口连通。
func (r *SDCppRuntime) CheckHealth() error {
	r.mu.RLock()
	entries := make(map[string]*runningServer, len(r.models))
	for name, entry := range r.models {
		entries[name] = entry
	}
	r.mu.RUnlock()
	if len(entries) == 0 {
		return nil
	}
	var failedModels []string
	var errs []error
	for name, entry := range entries {
		if entry.cmd == nil || runtimeProcessExited(entry.cmd) || !TCPPortOpen("127.0.0.1", entry.info.Port, defaultRuntimeHealthTimeout) {
			r.mu.Lock()
			delete(r.models, name)
			r.mu.Unlock()
			markRuntimeInfoError(&entry.info, "process exited or port unreachable")
			failedModels = append(failedModels, name)
			errs = append(errs, fmt.Errorf("model %s unhealthy on port %d", name, entry.info.Port))
			continue
		}
		markRuntimeInfoHealthy(&entry.info)
	}
	if len(failedModels) == 0 {
		return nil
	}
	return &RuntimeHealthError{Runtime: SDCpp, Models: failedModels, Err: joinErrors(errs)}
}

// ListModels 列出本运行时模型（含运行态覆盖）。
func (r *SDCppRuntime) ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error) {
	_ = ctx
	r.mu.RLock()
	running := make(map[string]*runningServer, len(r.models))
	for name, entry := range r.models {
		running[name] = entry
	}
	r.mu.RUnlock()
	out := make([]ModelInfo, 0, len(r.modelManifests))
	for _, info := range r.scanModelInfos() {
		if matchesFilter(info, filter) {
			out = append(out, info)
		}
	}
	for i := range out {
		if entry, ok := running[out[i].Name]; ok {
			out[i].RuntimeInfo = entry.info
		}
	}
	return out, nil
}

// GetModelInfo 获取单模型信息。
func (r *SDCppRuntime) GetModelInfo(modelName string) (ModelInfo, error) {
	models, err := r.ListModels(context.Background(), ModelFilter{})
	if err != nil {
		return ModelInfo{}, err
	}
	for _, info := range models {
		if info.Name == modelName {
			return info, nil
		}
	}
	return ModelInfo{}, NewStartModelError(CodeModelNotInstalled, fmt.Sprintf("model %q not found in runtime %s", modelName, r.name))
}

// Install 安装运行时二进制（进度 + 可取消）。
func (r *SDCppRuntime) Install(ctx context.Context, callback InstallCallback) error {
	return r.BaseRuntime.Install(ctx, callback)
}

// Uninstall 先停全部模型进程，再删除二进制。
func (r *SDCppRuntime) Uninstall(ctx context.Context) error {
	if err := r.StopAllModels(ctx); err != nil {
		return err
	}
	return r.UninstallBinary(ctx)
}

// Start 引擎级启动：无守护进程，空操作。
func (r *SDCppRuntime) Start(ctx context.Context) error { return nil }

// Stop 引擎级停止：等价停全部模型。
func (r *SDCppRuntime) Stop(ctx context.Context) error { return r.StopAllModels(ctx) }
