// Package manager 是运行时托管的管理面（supervisor）：
// 持有已注册的 Runtime 实例（audio.cpp / sd-cpp），统一处理
// 安装（异步可取消）、卸载、模型→引擎选择、端口分配、启停与健康轮询。
//
// 设计上保持最小化：不引入通用事件总线，改为可选回调注入
// （InstallState/模型状态）。
package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/downloader"
	"github.com/AntNoHuabei/mediacraft/pkg/runtime"
)

// enginePriority 模型多引擎时的选择顺序。
var enginePriority = []runtime.Name{runtime.AudioCpp, runtime.SDCpp}

// HealthTickerInterval 健康轮询默认间隔。
const HealthTickerInterval = 30 * time.Second

// Options supervisor 配置。
type Options struct {
	RuntimesDir string // 运行时安装根目录
	ModelsDir   string // 模型安装根目录
	CacheDir    string // 下载缓存目录（默认 <RuntimesDir>/../cache/downloads）
	Logger      *slog.Logger
	Vendor      runtime.Vendor // 空表示自动探测
}

// Supervisor 运行时管理面。
type Supervisor struct {
	catalog catalog.Catalog
	opts    Options
	log     *slog.Logger

	mu       sync.RWMutex
	runtimes map[runtime.Name]runtime.Runtime // 已注册运行时（audio.cpp/sd-cpp）

	installMu      sync.Mutex
	installCancels map[string]context.CancelFunc // name → 可取消上下文（防重入）

	healthStop    chan struct{}
	healthDone    chan struct{}
	healthStarted bool

	// 事件回调（后续接入 Wails 事件通道；均可为空）
	OnInstallState func(name runtime.Name, state runtime.InstallState)
	OnModelState   func(name string, engine runtime.Name, info runtime.ModelRuntimeInfo)
}

// NewSupervisor 构造 supervisor：从 catalog 注册 audio.cpp/sd-cpp 两个运行时，
// 每个运行时携带按引擎过滤的模型清单。
func NewSupervisor(cat catalog.Catalog, opts Options) (*Supervisor, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.RuntimesDir == "" || opts.ModelsDir == "" {
		return nil, errors.New("manager: RuntimesDir and ModelsDir are required")
	}
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(filepath.Dir(opts.RuntimesDir), "cache", "downloads")
	}
	if opts.Vendor == "" {
		// 未显式指定时回退到运行时探测（环境变量/显卡名），
		// 否则空值会被当成 "cpu"，GPU 机上模型→引擎判定全部落空。
		opts.Vendor = runtime.CurrentVendor()
	}
	downloaderInst := downloader.NewHTTPDownloader(downloader.Options{
		ConcurrentDownloads: 3,
		MaxRetries:          3,
		Timeout:             300,
		Logger:              opts.Logger,
		CacheDir:            opts.CacheDir,
	})
	for _, dir := range []string{opts.RuntimesDir, opts.ModelsDir, opts.CacheDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("manager: create dir %s: %w", dir, err)
		}
	}
	base := runtime.BaseOptions{
		RuntimesDir: opts.RuntimesDir,
		ModelsDir:   opts.ModelsDir,
		Downloader:  downloaderInst,
		Logger:      opts.Logger,
		Vendor:      opts.Vendor,
	}
	s := &Supervisor{
		catalog:        cat,
		opts:           opts,
		log:            opts.Logger,
		runtimes:       map[runtime.Name]runtime.Runtime{},
		installCancels: map[string]context.CancelFunc{},
		healthStop:     make(chan struct{}),
		healthDone:     make(chan struct{}),
	}
	for _, name := range enginePriority {
		rt, err := s.buildRuntime(name, base)
		if err != nil {
			return nil, err
		}
		s.runtimes[name] = rt
	}
	return s, nil
}

// buildRuntime 依据 catalog 构造一个注册表项。
func (s *Supervisor) buildRuntime(name runtime.Name, base runtime.BaseOptions) (runtime.Runtime, error) {
	var runtimeManifest catalog.Runtime
	found := false
	for _, candidate := range s.catalog.Runtimes {
		if candidate.Name == string(name) {
			runtimeManifest = candidate
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("manager: runtime %s missing from catalog", name)
	}
	models := s.modelsForEngine(name, base.Vendor)
	switch name {
	case runtime.AudioCpp:
		return runtime.NewAudioCppRuntime(runtimeManifest, models, base), nil
	case runtime.SDCpp:
		return runtime.NewSDCppRuntime(runtimeManifest, models, base), nil
	}
	return nil, fmt.Errorf("manager: unsupported runtime %s", name)
}

// modelsForEngine 筛出属于该引擎且当前平台/厂商可用的模型清单。
func (s *Supervisor) modelsForEngine(engine runtime.Name, vendor runtime.Vendor) []catalog.Manifest {
	vendorName := runtime.VendorName(vendor)
	out := make([]catalog.Manifest, 0, len(s.catalog.Models))
	for _, m := range s.catalog.Models {
		platformOK := false
		if _, xpu := m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, vendorName); xpu != nil {
			platformOK = true
		} else if _, xpuAll := m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, "all"); xpuAll != nil {
			platformOK = true
		}
		if !platformOK {
			continue
		}
		for _, a := range m.Architectures {
			for _, x := range a.XPUs {
				if containsFold(x.InferenceEngines, string(engine)) {
					out = append(out, m)
					goto next
				}
			}
		}
	next:
	}
	return out
}

// manifestByName 按模型名查 catalog 清单（不存在返回零值）。
func (s *Supervisor) manifestByName(modelName string) catalog.Manifest {
	for _, m := range s.catalog.Models {
		if m.Name == modelName {
			return m
		}
	}
	return catalog.Manifest{}
}

// Runtime 按名字取注册实例。
func (s *Supervisor) Runtime(name runtime.Name) (runtime.Runtime, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rt, ok := s.runtimes[name]
	return rt, ok
}

// ListRuntimes 列出全部运行时信息（按 enginePriority 顺序）。
func (s *Supervisor) ListRuntimes() []runtime.RuntimeInfo {
	out := make([]runtime.RuntimeInfo, 0, len(enginePriority))
	for _, name := range enginePriority {
		if rt, ok := s.Runtime(name); ok {
			out = append(out, rt.GetInfo())
		}
	}
	return out
}

// GetRuntimeInfo 取单个运行时信息。
func (s *Supervisor) GetRuntimeInfo(name runtime.Name) (runtime.RuntimeInfo, error) {
	rt, ok := s.Runtime(name)
	if !ok {
		return runtime.RuntimeInfo{}, fmt.Errorf("runtime %q not registered", name)
	}
	return rt.GetInfo(), nil
}

// InstallRuntime 安装运行时（防重入，可经 CancelRuntimeInstall 取消）。
func (s *Supervisor) InstallRuntime(ctx context.Context, name runtime.Name, callback runtime.InstallCallback) error {
	rt, ok := s.Runtime(name)
	if !ok {
		return fmt.Errorf("runtime %q not registered", name)
	}
	s.installMu.Lock()
	if _, busy := s.installCancels[string(name)]; busy {
		s.installMu.Unlock()
		return runtime.NewInstallError(runtime.CodeInstallInProgress, fmt.Sprintf("runtime %s install is already in progress", name))
	}
	installCtx, cancel := context.WithCancel(ctx)
	s.installCancels[string(name)] = cancel
	s.installMu.Unlock()

	defer func() {
		cancel()
		s.installMu.Lock()
		delete(s.installCancels, string(name))
		s.installMu.Unlock()
	}()

	cb := callback
	if s.OnInstallState != nil {
		inner := cb
		cb = func(state runtime.InstallState) {
			s.OnInstallState(name, state)
			if inner != nil {
				inner(state)
			}
		}
	}
	return rt.Install(installCtx, cb)
}

// CancelRuntimeInstall 取消进行中的运行时安装。
func (s *Supervisor) CancelRuntimeInstall(name runtime.Name) error {
	s.installMu.Lock()
	defer s.installMu.Unlock()
	if cancel, ok := s.installCancels[string(name)]; ok {
		cancel()
		return nil
	}
	return fmt.Errorf("runtime %q is not installing", name)
}

// UninstallRuntime 卸载运行时（先停其名下全部模型进程）。
func (s *Supervisor) UninstallRuntime(ctx context.Context, name runtime.Name) error {
	rt, ok := s.Runtime(name)
	if !ok {
		return fmt.Errorf("runtime %q not registered", name)
	}
	return rt.Uninstall(ctx)
}

// engineCandidatesForModel 返回能服务该模型、且已注册的运行时（保持引擎优先级）。
func (s *Supervisor) engineCandidatesForModel(modelName string) []runtime.Runtime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var runtimes []runtime.Runtime
	for _, engine := range enginePriority {
		rt, ok := s.runtimes[engine]
		if !ok {
			continue
		}
		if s.modelSupportedByEngineLocked(modelName, engine) {
			runtimes = append(runtimes, rt)
		}
	}
	return runtimes
}

// modelSupportedByEngineLocked 判断模型清单是否有可匹配厂商的 XPU 声明该引擎。
func (s *Supervisor) modelSupportedByEngineLocked(modelName string, engine runtime.Name) bool {
	vendorName := runtime.VendorName(s.opts.Vendor)
	for _, m := range s.catalog.Models {
		if m.Name != modelName {
			continue
		}
		for _, a := range m.Architectures {
			for _, x := range a.XPUs {
				if !containsFold(x.InferenceEngines, string(engine)) {
					continue
				}
				if strings.EqualFold(x.Vendor, vendorName) || strings.EqualFold(x.Vendor, "all") {
					return true
				}
			}
		}
		return false
	}
	return false
}

// ListModels 跨引擎聚合模型列表（运行中的实例优先覆盖）。
func (s *Supervisor) ListModels(ctx context.Context, filter runtime.ModelFilter) ([]runtime.ModelInfo, error) {
	seen := map[string]int{}
	var out []runtime.ModelInfo
	for _, engine := range enginePriority {
		rt, ok := s.Runtime(engine)
		if !ok {
			continue
		}
		list, err := rt.ListModels(ctx, filter)
		if err != nil {
			s.log.Warn("list models from runtime failed", "runtime", engine, "error", err)
			continue
		}
		for _, info := range list {
			if idx, ok := seen[info.Name]; ok {
				if info.RuntimeInfo.RunStatus == runtime.RunStatusRunning {
					out[idx] = info
				}
				continue
			}
			seen[info.Name] = len(out)
			out = append(out, info)
		}
	}
	return out, nil
}

// resolveRuntimeForModel 返回服务该模型的首选引擎（运行中 > 已装启用 > 首个）。
func (s *Supervisor) resolveRuntimeForModel(modelName string) (runtime.Runtime, error) {
	rts := s.engineCandidatesForModel(modelName)
	if len(rts) == 0 {
		return nil, fmt.Errorf("model %q has no supported local inference engine", modelName)
	}
	for _, rt := range rts {
		if info, err := rt.GetModelInfo(modelName); err == nil && info.RuntimeInfo.RunStatus == runtime.RunStatusRunning {
			return rt, nil
		}
	}
	for _, rt := range rts {
		if rt.GetInfo().Installed && rt.IsEnabled() {
			return rt, nil
		}
	}
	return rts[0], nil
}

// StartModel 启动模型：引擎选择 → 确保运行时已装 → 端口分配 → rt.StartModel。
// 已在运行时直接复用（返回 running 状态）。
func (s *Supervisor) StartModel(ctx context.Context, modelName string) (runtime.ModelInfo, error) {
	rt, err := s.resolveRuntimeForModel(modelName)
	if err != nil {
		return runtime.ModelInfo{}, err
	}
	info, err := rt.GetModelInfo(modelName)
	if err != nil {
		return runtime.ModelInfo{}, err
	}
	if info.RuntimeInfo.RunStatus == runtime.RunStatusRunning && info.RuntimeInfo.Port > 0 {
		return info, nil
	}
	if !info.Installed {
		return runtime.ModelInfo{}, runtime.NewStartModelError(runtime.CodeModelNotInstalled, fmt.Sprintf("model %q is not installed", modelName))
	}
	if !rt.GetInfo().Installed {
		s.log.Info("installing runtime before model start", "model", modelName, "runtime", rt.Name())
		if err := s.InstallRuntime(ctx, rt.Name(), nil); err != nil {
			return runtime.ModelInfo{}, runtime.NewStartModelError(runtime.CodeRuntimeNotInstalled,
				fmt.Sprintf("install runtime %s before model start", rt.Name())).WithCause(err)
		}
	}
	port, err := allocatePort()
	if err != nil {
		return runtime.ModelInfo{}, runtime.NewStartModelError(runtime.CodePortBindFailed, "allocate port for model").WithCause(err)
	}
	// 重取一次模型信息保证路径与清单一致（可能在自动安装期间有变化）
	startInfo, err := rt.GetModelInfo(modelName)
	if err != nil {
		return runtime.ModelInfo{}, err
	}
	if err := rt.StartModel(ctx, startInfo, port); err != nil {
		ReleasePort(port)
		return runtime.ModelInfo{}, err
	}
	running, err := rt.GetModelInfo(modelName)
	if err != nil {
		return running, nil // 已启动，查询失败不致命
	}
	if s.OnModelState != nil {
		s.OnModelState(modelName, rt.Name(), running.RuntimeInfo)
	}
	return running, nil
}

// StopModel 停止模型（若在多个引擎运行则全部停止）。
func (s *Supervisor) StopModel(ctx context.Context, modelName string) error {
	rts := s.engineCandidatesForModel(modelName)
	if len(rts) == 0 {
		return fmt.Errorf("model %q has no supported inference engine", modelName)
	}
	var firstErr error
	for _, rt := range rts {
		info, err := rt.GetModelInfo(modelName)
		if err != nil || info.RuntimeInfo.RunStatus != runtime.RunStatusRunning {
			continue
		}
		if err := rt.StopModel(ctx, modelName); err != nil && firstErr == nil {
			firstErr = err
		} else {
			ReleasePort(info.RuntimeInfo.Port)
		}
		if s.OnModelState != nil {
			stopped := info.RuntimeInfo
			stopped.RunStatus = runtime.RunStatusStopped
			s.OnModelState(modelName, rt.Name(), stopped)
		}
	}
	return firstErr
}

// StopAll 停止全部运行中的模型进程。
func (s *Supervisor) StopAll(ctx context.Context) error {
	var firstErr error
	for _, engine := range enginePriority {
		if rt, ok := s.Runtime(engine); ok {
			if err := rt.StopAllModels(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// CheckHealth 对全部运行时做一次健康巡检。
func (s *Supervisor) CheckHealth() error {
	var firstErr error
	for _, engine := range enginePriority {
		if rt, ok := s.Runtime(engine); ok {
			if err := rt.CheckHealth(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// StartHealthChecks 启动后台健康轮询 goroutine（幂等）。
func (s *Supervisor) StartHealthChecks(interval time.Duration) {
	s.mu.Lock()
	if s.healthStarted {
		s.mu.Unlock()
		return
	}
	s.healthStarted = true
	s.mu.Unlock()
	if interval <= 0 {
		interval = HealthTickerInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(s.healthDone)
		for {
			select {
			case <-s.healthStop:
				return
			case <-ticker.C:
				if err := s.CheckHealth(); err != nil {
					s.log.Warn("runtime health check failed", "error", err)
					if healthErr, ok := runtime.AsRuntimeHealthError(err); ok && s.OnModelState != nil {
						for _, name := range healthErr.Models {
							if info, gErr := s.modelRuntimeInfo(healthErr.Runtime, name); gErr == nil {
								info.RunStatus = runtime.RunStatusError
								s.OnModelState(name, healthErr.Runtime, info)
							}
						}
					}
				}
			}
		}
	}()
}

func (s *Supervisor) modelRuntimeInfo(engine runtime.Name, name string) (runtime.ModelRuntimeInfo, error) {
	if rt, ok := s.Runtime(engine); ok {
		if info, err := rt.GetModelInfo(name); err == nil {
			return info.RuntimeInfo, nil
		}
	}
	return runtime.ModelRuntimeInfo{}, fmt.Errorf("model %s not found under %s", name, engine)
}

// StopHealthChecks 停止健康轮询并等待 goroutine 退出（幂等；未启动时直接返回）。
func (s *Supervisor) StopHealthChecks() {
	s.mu.Lock()
	if !s.healthStarted {
		s.mu.Unlock()
		return
	}
	s.healthStarted = false
	s.mu.Unlock()
	select {
	case <-s.healthStop:
	default:
		close(s.healthStop)
	}
	<-s.healthDone
}

// Close 停止健康轮询并停止所有模型进程。
func (s *Supervisor) Close(ctx context.Context) error {
	s.StopHealthChecks()
	return s.StopAll(ctx)
}

// allocatePort 分配空闲本地端口（避开短暂占用窗口）。
func allocatePort() (int, error) {
	for attempt := 0; attempt < 20; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		if _, loaded := usedPorts.LoadOrStore(port, struct{}{}); !loaded {
			return port, nil
		}
	}
	return 0, errors.New("no free port found")
}

var usedPorts sync.Map

// ReleasePort 释放端口占用标记（StopModel 后调用）。
func ReleasePort(port int) {
	usedPorts.Delete(port)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
