package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AntNoHuabei/mediacraft/catalog"
)

const (
	// audioCppStartupTimeout 启动就绪最长等待。
	audioCppStartupTimeout = 5 * time.Minute
	// audioCppHealthFailureAllowed 健康失败容忍次数（≥该值才判不健康）。
	audioCppHealthFailureAllowed = 2
	// audioCppProbeInterval 就绪/健康探测间隔。
	audioCppProbeInterval = 500 * time.Millisecond
	// audioCppProbeTimeout 单次探测超时。
	audioCppProbeTimeout = 1500 * time.Millisecond
)

// AudioCppRuntime audio.cpp 运行时托管：每模型一个 audiocpp_server.exe 进程。
// 启动形态由模型参数 family/task/mode 决定（ASR/TTS/音乐，离线/流式）。
type AudioCppRuntime struct {
	*BaseRuntime

	mu     sync.RWMutex
	models map[string]*runningServer
}

// runningServer 单个模型 server 进程的运行态。
type runningServer struct {
	cmd  *exec.Cmd
	job  uintptr // Windows Job Object（KILL_ON_JOB_CLOSE）
	done chan error
	info ModelRuntimeInfo
}

// NewAudioCppRuntime 构造 audio.cpp 运行时（会扫描一次安装状态）。
func NewAudioCppRuntime(manifest catalog.Runtime, modelManifests []catalog.Manifest, opts BaseOptions) *AudioCppRuntime {
	return &AudioCppRuntime{
		BaseRuntime: NewBaseRuntime(AudioCpp, manifest, modelManifests, opts),
		models:      map[string]*runningServer{},
	}
}

// audioCppParams 从模型参数解析出的启动所需配置。
type audioCppParams struct {
	Family    string
	Task      string
	Mode      string
	Backend   string
	ModelPath string
	Device    *int
	Threads   *int
}

// audioCppLiveIngestDefaults ASR 流式会话的 server 级 live_ingest 默认值。
type audioCppLiveIngestDefaults struct {
	IdleTimeoutMS  int64 `json:"idle_timeout_ms"`
	TotalTimeoutMS int64 `json:"total_timeout_ms"`
	MaxBodyBytes   int64 `json:"max_body_bytes"`
	MaxChunkBytes  int64 `json:"max_chunk_bytes"`
	SendTimeoutMS  int64 `json:"send_timeout_ms"`
}

// audioCppServerModel config JSON 中单个模型条目。
type audioCppServerModel struct {
	ID             string         `json:"id"`
	Family         string         `json:"family"`
	Path           string         `json:"path"`
	Task           string         `json:"task"`
	Mode           string         `json:"mode"`
	LoadOptions    map[string]any `json:"load_options,omitempty"`
	SessionOptions map[string]any `json:"session_options,omitempty"`
	LiveIngest     any            `json:"live_ingest,omitempty"`
	BusyTimeoutMS  int64          `json:"busy_timeout_ms,omitempty"`
}

// audioCppServerConfig 对应 audiocpp_server.exe --config 的 JSON。
type audioCppServerConfig struct {
	Host                string                `json:"host"`
	Port                int                   `json:"port"`
	Backend             string                `json:"backend"`
	Device              *int                  `json:"device,omitempty"`
	Threads             *int                  `json:"threads,omitempty"`
	LazyLoad            bool                  `json:"lazy_load"`
	BusyTimeoutMS       int64                 `json:"busy_timeout_ms,omitempty"`
	LiveIngest          any                   `json:"live_ingest,omitempty"`
	MaxRequestBodyBytes int64                 `json:"max_request_body_bytes"`
	Models              []audioCppServerModel `json:"models"`
}

// audioCppStringParam 读取模型参数中的字符串（含模型全局/XPU 合并结果）。
func audioCppStringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

// audioCppIntParam 读取整数参数。
func audioCppIntParam(params map[string]any, key string) (int, bool) {
	value, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := value.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// inferAudioCppFamily 从模型名兜底推断 family（manifest 未显式指定时）。
func inferAudioCppFamily(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "qwen3") && strings.Contains(lower, "asr"):
		return "qwen3_asr"
	case strings.Contains(lower, "qwen3") && strings.Contains(lower, "tts"):
		return "qwen3_tts"
	case strings.Contains(lower, "voxcpm"):
		return "voxcpm2"
	case strings.Contains(lower, "voxtral"):
		return "voxtral_realtime"
	case strings.Contains(lower, "ace"):
		return "ace_step"
	}
	return ""
}

// audioCppTaskForType 由模型 type 映射缺省 task。
func audioCppTaskForType(modelType string) string {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "asr":
		return "asr"
	case "tts":
		return "tts"
	default:
		return "gen"
	}
}

// audioCppIsTTSTask 判断 task 是否属于 TTS 语音合成。
func audioCppIsTTSTask(task string) bool {
	task = strings.ToLower(strings.TrimSpace(task))
	return task == "tts" || task == "vdes"
}

// resolveAudioCppParams 解析模型启动参数（manifest 参数优先，其余兜底推断）。
func (r *AudioCppRuntime) resolveAudioCppParams(model ModelInfo) (audioCppParams, error) {
	params := model.Parameters
	var out audioCppParams

	out.Family = audioCppStringParam(params, "audio_cpp_family")
	if out.Family == "" {
		out.Family = inferAudioCppFamily(model.Name)
	}
	if out.Family == "" {
		return out, NewStartModelError(CodeInvalidOptions, fmt.Sprintf("cannot determine audio.cpp family for model %s", model.Name))
	}

	out.Task = strings.ToLower(audioCppStringParam(params, "audio_cpp_task"))
	if out.Task == "" {
		out.Task = audioCppTaskForType(model.Type)
	}

	out.Mode = strings.ToLower(audioCppStringParam(params, "audio_cpp_mode"))
	if out.Mode == "" {
		if out.Task == "asr" || out.Task == "tts" {
			out.Mode = "streaming"
		} else {
			out.Mode = "offline"
		}
	}

	backend := audioCppStringParam(params, "backend")
	if backend == "" || strings.EqualFold(backend, "auto") {
		backend = BackendForVendor(r.CurrentVendor())
	}
	out.Backend = strings.ToLower(backend)

	if device, ok := audioCppIntParam(params, "device"); ok {
		out.Device = &device
	}
	if threads, ok := audioCppIntParam(params, "threads"); ok {
		out.Threads = &threads
	}

	relative := audioCppStringParam(params, "audio_cpp_model_path")
	if relative == "" {
		relative = audioCppStringParam(params, "maingguf")
	}
	if relative != "" {
		clean := filepath.Clean(filepath.FromSlash(relative))
		if filepath.IsAbs(clean) {
			out.ModelPath = clean
		} else {
			out.ModelPath = filepath.Join(model.InstallPath, clean)
		}
	} else {
		out.ModelPath = model.InstallPath
	}
	return out, nil
}

// buildAudioCppServerConfig 构造 server config JSON 内容。
func (r *AudioCppRuntime) buildAudioCppServerConfig(model ModelInfo, port int, params audioCppParams) audioCppServerConfig {
	cfg := audioCppServerConfig{
		Host:                "127.0.0.1",
		Port:                port,
		Backend:             params.Backend,
		LazyLoad:            true,
		MaxRequestBodyBytes: 2 << 30,
		Models: []audioCppServerModel{{
			ID:     model.Name,
			Family: params.Family,
			Path:   params.ModelPath,
			Task:   params.Task,
			Mode:   params.Mode,
		}},
	}
	if params.Device != nil {
		cfg.Device = params.Device
	}
	if params.Threads != nil {
		cfg.Threads = params.Threads
	}
	// ASR 流式：注入 server 级 live_ingest 默认值。
	if params.Task == "asr" && params.Mode == "streaming" {
		cfg.LiveIngest = audioCppLiveIngestDefaults{
			IdleTimeoutMS:  30000,
			TotalTimeoutMS: 600000,
			MaxBodyBytes:   536870912,
			MaxChunkBytes:  8388608,
			SendTimeoutMS:  30000,
		}
		cfg.Models[0].LiveIngest = cfg.LiveIngest
	}
	return cfg
}

// StartModel 启动模型的 audiocpp_server 进程。
func (r *AudioCppRuntime) StartModel(ctx context.Context, model ModelInfo, port int) error {
	if status, _ := r.installState(); status != StatusInstalled {
		return NewStartModelError(CodeRuntimeNotInstalled, fmt.Sprintf("runtime %s is not installed", r.name))
	}
	exe := r.executablePath()
	if _, err := os.Stat(exe); err != nil {
		return NewStartModelError(CodeExecutableMissing, "audio.cpp server executable missing").WithDetail("path", exe)
	}
	params, err := r.resolveAudioCppParams(model)
	if err != nil {
		return err
	}
	if _, err := os.Stat(params.ModelPath); err != nil {
		return NewStartModelError(CodeModelFileMissing, fmt.Sprintf("audio model file not found: %s", params.ModelPath))
	}

	r.mu.Lock()
	if existing, ok := r.models[model.Name]; ok {
		if existing.cmd == nil || existing.cmd.Process == nil || runtimeProcessExited(existing.cmd) {
			delete(r.models, model.Name) // 残留僵尸条目，重新启动
		} else {
			r.mu.Unlock()
			return nil // 幂等：同名模型已在运行
		}
	}
	r.mu.Unlock()

	cfg := r.buildAudioCppServerConfig(model, port, params)
	data, _ := json.Marshal(cfg)
	if err := os.MkdirAll(r.installPath(), 0755); err != nil {
		return NewStartModelError(CodeRuntimePathMissing, "ensure runtime dir").WithCause(err)
	}
	configFile, err := os.CreateTemp(r.installPath(), "audiocpp-"+sanitizeFileName(model.Name)+"-*.json")
	if err != nil {
		return NewStartModelError(CodeRuntimePathMissing, "create server config").WithCause(err)
	}
	configPath := configFile.Name()
	if _, err = configFile.Write(data); err != nil {
		configFile.Close()
		os.Remove(configPath)
		return NewStartModelError(CodeRuntimePathMissing, "write server config").WithCause(err)
	}
	configFile.Close()

	cmd := exec.Command(exe, "--config", configPath)
	cmd.Dir = r.installPath()
	cmd.Env = withPathEntry(os.Environ(), r.installPath())
	cmd.SysProcAttr = serverSysProcAttr(true)
	cmd.Stdout = io.MultiWriter(r.Recent())
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		os.Remove(configPath)
		return r.startFailedWithRetry(ctx, cmd, exe, configPath, err, port, model.Name)
	}
	job, jobErr := assignToJobObject(cmd.Process.Pid)
	if jobErr != nil {
		r.Logger().Warn("assign audio.cpp process to job object failed; taskkill will be used on stop", "pid", cmd.Process.Pid, "error", jobErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ready, startErr := r.waitReady(ctx, cmd, done, port, model.Name)
	if startErr != nil {
		r.cleanupProcess(cmd, job, configPath)
		return startErr
	}
	_ = ready
	// 服务已就绪：config 文件不再需要。
	os.Remove(configPath)

	entry := &runningServer{
		cmd:  cmd,
		job:  job,
		done: done,
		info: ModelRuntimeInfo{
			Engine:      AudioCpp,
			RunStatus:   RunStatusRunning,
			StartTime:   time.Now(),
			Port:        port,
			InstallPath: model.InstallPath,
			RuntimePath: r.installPath(),
			Options:     model.Parameters,
		},
	}
	r.mu.Lock()
	r.models[model.Name] = entry
	r.mu.Unlock()
	return nil
}

// startFailedWithRetry Start 失败且带 breakaway 标志时，去掉标志重试一次。
func (r *AudioCppRuntime) startFailedWithRetry(ctx context.Context, cmd *exec.Cmd, exe, configPath string, firstErr error, port int, modelName string) error {
	retry := exec.Command(exe, "--config", configPath)
	retry.Dir = cmd.Dir
	retry.Env = cmd.Env
	retry.SysProcAttr = serverSysProcAttr(false)
	retry.Stdout = io.MultiWriter(r.Recent())
	retry.Stderr = retry.Stdout
	if err := retry.Start(); err != nil {
		os.Remove(configPath)
		return NewStartModelError(CodeProcessStartFailed, fmt.Sprintf("start audiocpp_server for %s", modelName)).WithCause(err)
	}
	job, jobErr := assignToJobObject(retry.Process.Pid)
	if jobErr != nil {
		r.Logger().Warn("assign audio.cpp process to job object failed; taskkill will be used on stop", "pid", retry.Process.Pid, "error", jobErr)
	}
	done := make(chan error, 1)
	go func() { done <- retry.Wait() }()
	_, startErr := r.waitReady(ctx, retry, done, port, modelName)
	if startErr != nil {
		r.cleanupProcess(retry, job, configPath)
		return startErr
	}
	os.Remove(configPath)
	r.mu.Lock()
	r.models[modelName] = &runningServer{
		cmd:  retry,
		job:  job,
		done: done,
		info: ModelRuntimeInfo{
			Engine:      AudioCpp,
			RunStatus:   RunStatusRunning,
			StartTime:   time.Now(),
			Port:        port,
			RuntimePath: r.installPath(),
		},
	}
	r.mu.Unlock()
	return nil
}

// waitReady 探测 server 就绪：进程早退 / ctx 取消 / 超时 四方竞争。
func (r *AudioCppRuntime) waitReady(ctx context.Context, cmd *exec.Cmd, done chan error, port int, modelName string) (bool, error) {
	deadline := time.Now().Add(audioCppStartupTimeout)
	for {
		if TCPPortOpen("127.0.0.1", port, audioCppProbeTimeout) ||
			HTTPHealthOK(fmt.Sprintf("http://127.0.0.1:%d/health", port), audioCppProbeTimeout) {
			return true, nil
		}
		select {
		case err := <-done:
			return false, NewStartModelError(CodeProcessExited, fmt.Sprintf("audiocpp_server exited before ready for %s", modelName)).
				WithCause(err).
				WithDetail("recent_log", r.Recent().Tail(recentLogTailBytes))
		case <-ctx.Done():
			return false, NewStartModelError(CodeContextCanceled, "start audio model cancelled")
		default:
		}
		if time.Now().After(deadline) {
			return false, NewStartModelError(CodeStartupTimeout, fmt.Sprintf("audiocpp_server startup timeout on port %d", port)).
				WithDetail("recent_log", r.Recent().Tail(recentLogTailBytes))
		}
		time.Sleep(audioCppProbeInterval)
	}
}

// cleanupProcess 停止进程并清理资源（Job 关句柄杀树 + 兜底 taskkill/杀单进程）。
func (r *AudioCppRuntime) cleanupProcess(cmd *exec.Cmd, job uintptr, configPath string) {
	if job != 0 {
		closeJobHandle(job)
	}
	if cmd != nil && cmd.Process != nil {
		killProcessTree(cmd.Process.Pid)
		_ = cmd.Process.Kill()
	}
	if configPath != "" {
		_ = os.Remove(configPath)
	}
}

// StopModel 停止指定模型进程；modelName 为空时停止全部。
func (r *AudioCppRuntime) StopModel(ctx context.Context, modelName string) error {
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
		return nil // 幂等
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
func (r *AudioCppRuntime) StopAllModels(ctx context.Context) error {
	return r.StopModel(ctx, "")
}

// CheckHealth 周期健康检查：进程存活 + HTTP /health（失败容忍 N 次）。
func (r *AudioCppRuntime) CheckHealth() error {
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
		if entry.cmd == nil || runtimeProcessExited(entry.cmd) {
			r.mu.Lock()
			delete(r.models, name)
			r.mu.Unlock()
			markRuntimeInfoError(&entry.info, "process exited")
			failedModels = append(failedModels, name)
			errs = append(errs, fmt.Errorf("model %s process exited", name))
			continue
		}
		healthy := HTTPHealthOK(fmt.Sprintf("http://127.0.0.1:%d/health", entry.info.Port), defaultRuntimeHealthTimeout)
		if healthy {
			markRuntimeInfoHealthy(&entry.info)
			continue
		}
		// 失败计数由调用方（manager 30s 轮询）持久，这里记录到 info 即可。
		markRuntimeInfoError(&entry.info, "health probe failed")
		failedModels = append(failedModels, name)
		errs = append(errs, fmt.Errorf("model %s health check failed on port %d", name, entry.info.Port))
	}
	if len(failedModels) == 0 {
		return nil
	}
	return &RuntimeHealthError{Runtime: AudioCpp, Models: failedModels, Err: joinErrors(errs)}
}

// ListModels 列出本运行时模型（含运行态覆盖）。
func (r *AudioCppRuntime) ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error) {
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
	// 覆盖运行态
	for i := range out {
		if entry, ok := running[out[i].Name]; ok {
			out[i].RuntimeInfo = entry.info
		}
	}
	return out, nil
}

// GetModelInfo 获取单模型信息。
func (r *AudioCppRuntime) GetModelInfo(modelName string) (ModelInfo, error) {
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
func (r *AudioCppRuntime) Install(ctx context.Context, callback InstallCallback) error {
	return r.BaseRuntime.Install(ctx, callback)
}

// Uninstall 先停全部模型进程，再删除二进制。
func (r *AudioCppRuntime) Uninstall(ctx context.Context) error {
	if err := r.StopAllModels(ctx); err != nil {
		return err
	}
	return r.UninstallBinary(ctx)
}

// Start 引擎级启动：无守护进程，空操作。
func (r *AudioCppRuntime) Start(ctx context.Context) error { return nil }

// Stop 引擎级停止：等价停全部模型。
func (r *AudioCppRuntime) Stop(ctx context.Context) error { return r.StopAllModels(ctx) }

// sanitizeFileName 去掉文件名非法字符，用于临时文件名。
func sanitizeFileName(name string) string {
	replacer := strings.NewReplacer("\\", "_", "/", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", " ", "_")
	return replacer.Replace(name)
}

// matchesFilter 过滤模型列表。
func matchesFilter(info ModelInfo, filter ModelFilter) bool {
	if filter.Engine != "" && info.Engine != filter.Engine {
		return false
	}
	if filter.Type != "" && !strings.EqualFold(info.Type, filter.Type) {
		return false
	}
	if filter.Installed != nil && info.Installed != *filter.Installed {
		return false
	}
	return true
}

// joinErrors 合并错误列表。
func joinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		var sb strings.Builder
		for i, err := range errs {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(err.Error())
		}
		return fmt.Errorf("%s", sb.String())
	}
}

// withPathEntry 在环境 PATH 最前面注入目录（Windows 分隔符 ';'）。
func withPathEntry(environ []string, dir string) []string {
	entry := "PATH=" + dir
	sep := string(os.PathListSeparator)
	for i, kv := range environ {
		upper := strings.ToUpper(kv)
		if strings.HasPrefix(upper, "PATH=") {
			environ[i] = entry + sep + kv[len("PATH="):]
			return environ
		}
	}
	return append(environ, entry)
}
