// Package runtime 提供本地推理运行时（runtime）的托管抽象。
//
// 本包覆盖两类"每模型一个 server 进程"的本地推理运行时：
// audio.cpp（音频：ASR/TTS）与 sd-cpp（图像生成）。按职责分成几层：
//
//	runtime.go —— Name/枚举/进度结构/核心接口
//	base_runtime.go —— 安装/卸载/模型安装的通用基座（BaseRuntime）
//	audio_cpp.go / sd_cpp.go —— 两个具体运行时的进程托管
//	health*.go / proc_*.go / errors.go —— 共享原语
package runtime

import (
	"context"
	"time"

	"github.com/AntNoHuabei/mediacraft/catalog"
)

// Name 运行时类型标识。
type Name string

const (
	// SDCpp sd.cpp：Stable Diffusion C++ server（sd-server.exe）。
	SDCpp Name = "sd-cpp"
	// AudioCpp audio.cpp：audio.cpp server（audiocpp_server.exe），承载 ASR/TTS/音乐。
	AudioCpp Name = "audio.cpp"
)

// Vendor GPU/硬件厂商标识，决定运行时后端（cpu/cuda/hip）与下载包选择。
type Vendor string

const (
	VendorNvidia Vendor = "nvidia"
	VendorAMD    Vendor = "amd"
	VendorIntel  Vendor = "intel"
	VendorCPU    Vendor = "cpu"
)

// 每个运行时在安装目录内可执行文件名（与 embed manifest 的 commands.start 一致）。
const (
	// SDCppExecutable 是 sd.cpp server 的唯一可执行文件名。
	SDCppExecutable = "sd-server.exe"
	// AudioCppExecutable 是 audio.cpp server 的唯一可执行文件名。
	AudioCppExecutable = "audiocpp_server.exe"
)

// ExecutableFor 返回运行时对应 server 可执行文件名。
func ExecutableFor(name Name) string {
	switch name {
	case SDCpp:
		return SDCppExecutable
	case AudioCpp:
		return AudioCppExecutable
	}
	return ""
}

// InstallStatusEnum 运行时/模型的安装状态。
type InstallStatusEnum string

const (
	StatusUninstalled    InstallStatusEnum = "uninstalled"
	StatusUninstalling   InstallStatusEnum = "uninstalling"
	StatusInstalled      InstallStatusEnum = "installed"
	StatusRepairRequired InstallStatusEnum = "repair_required"
	StatusDownloading    InstallStatusEnum = "downloading"
	StatusInstalling     InstallStatusEnum = "installing"
	StatusError          InstallStatusEnum = "error"
	StatusUpdating       InstallStatusEnum = "updating"
)

// InstallStage 安装阶段（InstallState.Stage）。
type InstallStage string

const (
	InstallStagePreparing   InstallStage = "preparing"
	InstallStageDownloading InstallStage = "downloading"
	InstallStageVerifying   InstallStage = "verifying"
	InstallStageExtracting  InstallStage = "extracting"
	InstallStageInstalling  InstallStage = "installing"
	InstallStageCompleted   InstallStage = "completed"
)

// InstallState 安装进度快照，通过 InstallCallback 逐阶段推送。
type InstallState struct {
	DownloadedBytes int64             `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64             `json:"total_bytes,omitempty"`
	Status          InstallStatusEnum `json:"status"`
	Progress        int               `json:"progress"` // 0-100
	Message         string            `json:"message"`
	Stage           InstallStage      `json:"stage"`
	Speed           int64             `json:"speed"`     // 字节/秒
	Remaining       string            `json:"remaining"` // 剩余时间描述
}

// InstallCallback 安装进度回调。约定 Install 返回前会收到一条
// Status=installed/error 的终态回调。
type InstallCallback func(state InstallState)

// RunStatus 模型级运行状态。
type RunStatus string

const (
	RunStatusStopped RunStatus = "stopped"
	RunStatusRunning RunStatus = "running"
	RunStatusError   RunStatus = "error"
	RunStatusLoading RunStatus = "loading"
	RunStatusFailed  RunStatus = "failed"
)

// ModelRuntimeInfo 模型在某个运行时上的实时运行信息。
type ModelRuntimeInfo struct {
	Engine          Name
	RunStatus       RunStatus
	StartTime       time.Time
	LastHealthCheck time.Time
	HealthError     string
	Port            int
	InstallPath     string // 模型文件安装目录
	RuntimePath     string // 运行时安装目录（本进程 exe 所在目录）
	Options         map[string]any
}

// ModelInfo 运行时视角的模型描述（来自 embed 模型 manifest + 本地安装状态）。
type ModelInfo struct {
	Name        string
	DisplayName string
	Type        string // asr / tts / image-generation / ...
	Version     string
	Installed   bool
	InstallPath string
	Engine      Name
	Parameters  map[string]any
	RuntimeInfo ModelRuntimeInfo
}

// ModelFilter ListModels 过滤条件。
type ModelFilter struct {
	Engine    Name
	Type      string
	Installed *bool
}

// RuntimeInfo 运行时完整信息快照（供管理面/前端展示）。
type RuntimeInfo struct {
	Name              string
	Version           string
	InstallPath       string
	RootPath          string
	Type              Name
	InstallStatus     InstallStatusEnum
	Installed         bool
	Os                string
	Arch              string
	Vendor            string
	InstalledVersions []string
	CurrentVersion    string
}

// Runtime 运行时抽象。audio.cpp / sd-cpp 采用"每模型一个 server 进程"
// 模型：Install/Uninstall 面向运行时二进制，StartModel 面向模型进程。
type Runtime interface {
	// Name 返回运行时类型标识。
	Name() Name
	// GetInfo 返回运行时完整信息快照。
	GetInfo() RuntimeInfo
	// IsEnabled 当前环境是否应把该运行时当作可用推理后端。
	IsEnabled() bool
	// Install 安装运行时二进制（含进度回调，支持 ctx 取消）。
	Install(ctx context.Context, callback InstallCallback) error
	// Uninstall 卸载运行时（先停止其名下所有模型进程）。
	Uninstall(ctx context.Context) error
	// Start/Stop 引擎级启停（本类运行时无守护进程，通常为空操作）。
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// StartModel 启动某模型的 server 进程；port 由上层分配并传入。
	StartModel(ctx context.Context, model ModelInfo, port int) error
	// StopModel 停止指定模型的 server 进程。
	StopModel(ctx context.Context, modelName string) error
	// StopAllModels 停止该运行时名下的所有模型进程。
	StopAllModels(ctx context.Context) error
	// CheckHealth 周期健康检查：进程存活 + 端口/HTTP 探测。
	CheckHealth() error
	// ListModels 列出该运行时可见模型及运行状态。
	ListModels(ctx context.Context, filter ModelFilter) ([]ModelInfo, error)
	// GetModelInfo 获取单模型信息。
	GetModelInfo(modelName string) (ModelInfo, error)
	// InstallModel 安装一个模型到该运行时（多文件/压缩包 + 进度回调）。
	InstallModel(ctx context.Context, manifest catalog.Manifest, callback InstallCallback) error
	// UninstallModel 卸载模型（运行时名下）。
	UninstallModel(ctx context.Context, modelName string) error
	// TriggerModelRefresh 触发本地模型安装状态重扫。
	TriggerModelRefresh()
}
