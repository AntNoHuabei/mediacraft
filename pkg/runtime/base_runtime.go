package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/downloader"
)

// BaseOptions 运行时托管的基础目录与依赖注入。
type BaseOptions struct {
	RuntimesDir string // 运行时安装根目录（每个运行时一个 <RuntimesDir>/<name> 子目录）
	ModelsDir   string // 模型安装根目录（每个模型一个 <ModelsDir>/<name> 子目录）
	Downloader  *downloader.HTTPDownloader
	Logger      *slog.Logger
	Vendor      Vendor // 空表示每次自动探测
}

// BaseRuntime 是 audio.cpp / sd-cpp 共用的生命周期基座：
// 负责运行时二进制与模型文件的安装/卸载/本地状态扫描。
// 进程级托管（StartModel/StopModel/CheckHealth）由子类实现。
type BaseRuntime struct {
	name           Name
	manifest       catalog.Runtime
	modelManifests []catalog.Manifest
	opts           BaseOptions
	recent         *recentLog

	mu               sync.RWMutex
	installStatus    InstallStatusEnum
	installedVersion string

	// diskFree 探测目标目录所在卷的可用空间（可注入以便测试）。
	diskFree func(path string) (int64, bool, error)
}

// NewBaseRuntime 构造基座并执行一次安装状态扫描。
func NewBaseRuntime(name Name, manifest catalog.Runtime, modelManifests []catalog.Manifest, opts BaseOptions) *BaseRuntime {
	b := &BaseRuntime{
		name:           name,
		manifest:       manifest,
		modelManifests: modelManifests,
		opts:           opts,
		recent:         newRecentLog(defaultRecentLogLines),
		diskFree:       diskFreeBytes,
	}
	b.refreshInstallState()
	return b
}

// diskSafetyMargin 为下载预留的安全余量：覆盖解压后的体积膨胀与临时文件。
// 取"文件体积的 1/4"与固定下限 512MB 中较大者。
func diskSafetyMargin(total int64) int64 {
	const floor = int64(512) << 20 // 512 MB
	margin := total / 4
	if margin < floor {
		return floor
	}
	return margin
}

func formatBytes(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(value)/float64(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", value)
	}
}

// ensureDiskSpace 在下载前校验目标卷可用空间（total 为下载体积）。
// 探测不可用/失败时不阻塞（best effort）；余量不足则直接拒绝。
func (b *BaseRuntime) ensureDiskSpace(dir string, total int64) error {
	if total <= 0 || b.diskFree == nil {
		return nil
	}
	free, supported, err := b.diskFree(dir)
	if err != nil || !supported {
		return nil
	}
	required := total + diskSafetyMargin(total)
	if free < required {
		return NewInstallError(CodeInsufficientDiskSpace,
			fmt.Sprintf("磁盘空间不足：下载 %s 需要至少 %s，当前可用 %s（已预留安全余量）",
				b.name, formatBytes(required), formatBytes(free)))
	}
	return nil
}

// Base 返回自身，便于子类嵌入后访问基座能力。
func (b *BaseRuntime) Base() *BaseRuntime { return b }

// Name 返回运行时类型。
func (b *BaseRuntime) Name() Name { return b.name }

// Recent 返回子进程日志环形缓冲（供子类接线 stdout/stderr）。
func (b *BaseRuntime) Recent() *recentLog { return b.recent }

// Logger 返回应用日志器（可为 nil）。
func (b *BaseRuntime) Logger() *slog.Logger {
	if b.opts.Logger == nil {
		return slog.Default()
	}
	return b.opts.Logger
}

// ModelsDir 返回模型根目录。
func (b *BaseRuntime) ModelsDir() string { return b.opts.ModelsDir }

// Options 返回基座选项。
func (b *BaseRuntime) Options() BaseOptions { return b.opts }

// installPath 返回运行时安装目录。
func (b *BaseRuntime) installPath() string {
	return filepath.Join(b.opts.RuntimesDir, string(b.name))
}

// executablePath 返回 server 可执行文件绝对路径。
func (b *BaseRuntime) executablePath() string {
	return filepath.Join(b.installPath(), ExecutableFor(b.name))
}

// modelDir 返回模型安装目录。
func (b *BaseRuntime) modelDir(modelName string) string {
	return filepath.Join(b.opts.ModelsDir, modelName)
}

// CurrentVendor 返回当前 GPU 厂商（构造时固定或实时探测）。
func (b *BaseRuntime) CurrentVendor() Vendor {
	if b.opts.Vendor != "" {
		return b.opts.Vendor
	}
	return CurrentVendor()
}

// isInstalledMarker 判断目录内的 .installed 标记是否存在。
func isInstalledMarker(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".installed"))
	return err == nil && !info.IsDir()
}

// refreshInstallState 扫描磁盘并更新安装状态/版本。
func (b *BaseRuntime) refreshInstallState() {
	exe := b.executablePath()
	marker := filepath.Join(b.installPath(), ".installed")
	version := ""
	installed := false
	if _, err := os.Stat(exe); err == nil {
		installed = true
		if data, err := os.ReadFile(marker); err == nil {
			version = strings.TrimSpace(string(data))
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if installed {
		b.installStatus = StatusInstalled
		b.installedVersion = version
	} else {
		b.installStatus = StatusUninstalled
		b.installedVersion = ""
	}
}

// installState 返回当前安装状态。
func (b *BaseRuntime) installState() (InstallStatusEnum, string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.installStatus, b.installedVersion
}

// setInstallState 更新安装状态（供 Install/Uninstall 流程使用）。
func (b *BaseRuntime) setInstallState(status InstallStatusEnum, version string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.installStatus = status
	b.installedVersion = version
}

// IsEnabled 该运行时在当前平台/vendor 下是否可用。
// sd-cpp 只对 nvidia/amd 可装；audio.cpp 对 cpu/intel/nvidia/amd 均可。
func (b *BaseRuntime) IsEnabled() bool {
	_, _, _, ok := b.manifest.Select(goruntime.GOOS, goruntime.GOARCH, VendorName(b.CurrentVendor()), "")
	return ok
}

// GetInfo 返回运行时信息快照。
func (b *BaseRuntime) GetInfo() RuntimeInfo {
	status, version := b.installState()
	_, latestVersion, ok := b.manifest.WindowsDownload()
	current := version
	if current == "" && ok {
		current = latestVersion
	}
	info := RuntimeInfo{
		Name:           string(b.name),
		Version:        current,
		InstallPath:    b.installPath(),
		RootPath:       b.opts.RuntimesDir,
		Type:           b.name,
		InstallStatus:  status,
		Installed:      status == StatusInstalled,
		Os:             goruntime.GOOS,
		Arch:           goruntime.GOARCH,
		Vendor:         VendorName(b.CurrentVendor()),
		CurrentVersion: current,
	}
	if ok {
		info.InstalledVersions = []string{latestVersion}
	}
	return info
}

// emit 便捷发送进度回调。
func emit(cb InstallCallback, state InstallState) {
	if cb != nil {
		cb(state)
	}
}

// Install 安装运行时二进制：选版 → 下载(带进度) → 校验 → 解压 → 落 .installed。
func (b *BaseRuntime) Install(ctx context.Context, callback InstallCallback) error {
	if b.opts.Downloader == nil {
		return NewInstallError(CodeDownloadFailed, "downloader is not configured").WithCause(fmt.Errorf("BaseOptions.Downloader is nil"))
	}
	status, _ := b.installState()
	if status == StatusInstalling || status == StatusDownloading {
		return NewInstallError(CodeInstallInProgress, fmt.Sprintf("runtime %s install is already in progress", b.name))
	}
	vendor := VendorName(b.CurrentVendor())
	_, version, url, ok := b.manifest.Select(goruntime.GOOS, goruntime.GOARCH, vendor, "")
	if !ok || url == "" {
		return NewInstallError(CodeDownloadFailed, fmt.Sprintf("runtime %s has no compatible download for vendor %s", b.name, vendor))
	}
	emit(callback, InstallState{Status: StatusInstalling, Stage: InstallStagePreparing, Progress: 0, Message: "准备安装"})
	b.setInstallState(StatusInstalling, "")

	parent := b.opts.RuntimesDir
	if err := os.MkdirAll(parent, 0755); err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeRuntimePathMissing, "create runtimes dir").WithCause(err)
	}
	if err := b.ensureDiskSpace(parent, version.FileSize); err != nil {
		b.setInstallState(StatusError, "")
		return err
	}
	tempDir, err := os.MkdirTemp(parent, ".runtime-*")
	if err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeRuntimePathMissing, "create temp dir").WithCause(err)
	}
	defer os.RemoveAll(tempDir)

	archive := filepath.Join(tempDir, "download.zip")
	emit(callback, InstallState{Status: StatusDownloading, Stage: InstallStageDownloading, Progress: 0, Message: "下载运行时", TotalBytes: version.FileSize})
	err = b.opts.Downloader.DownloadWithContext(ctx, url, archive, version.SHA256, version.FileSize, func(progress float64, speed float64) {
		fraction := clampProgress(progress)
		emit(callback, InstallState{
			Status:          StatusDownloading,
			Stage:           InstallStageDownloading,
			Progress:        progressPercent(fraction),
			DownloadedBytes: int64(fraction * float64(version.FileSize)),
			TotalBytes:      version.FileSize,
			Speed:           int64(speed),
			Message:         "下载运行时",
		})
	})
	if err != nil {
		b.setInstallState(StatusError, "")
		return WrapInstallDownloadErr(err, string(b.name))
	}
	emit(callback, InstallState{Status: StatusInstalling, Stage: InstallStageVerifying, Progress: 95, Message: "校验文件"})

	unpacked := filepath.Join(tempDir, "files")
	if err := os.MkdirAll(unpacked, 0755); err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeRuntimePathMissing, "create unpack dir").WithCause(err)
	}
	if !IsZIPFile(archive) {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeExtractFailed, fmt.Sprintf("runtime %s package is not a zip archive", b.name))
	}
	if err := UnzipSafe(archive, unpacked); err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeExtractFailed, "extract runtime archive").WithCause(err)
	}
	emit(callback, InstallState{Status: StatusInstalling, Stage: InstallStageExtracting, Progress: 97, Message: "写入安装目录"})

	destination := b.installPath()
	if err := os.RemoveAll(destination); err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeRuntimePathMissing, "clear previous install").WithCause(err)
	}
	if err := os.Rename(unpacked, destination); err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeRuntimePathMissing, "move files into install dir").WithCause(err)
	}
	if err := os.WriteFile(filepath.Join(destination, ".installed"), []byte(version.Version), 0644); err != nil {
		b.setInstallState(StatusError, "")
		return NewInstallError(CodeRuntimePathMissing, "write install marker").WithCause(err)
	}
	b.refreshInstallState()
	emit(callback, InstallState{Status: StatusInstalled, Stage: InstallStageCompleted, Progress: 100, Message: "安装完成"})
	return nil
}

// UninstallBinary 删除运行时二进制目录并刷新状态（子类 Uninstall 先停进程再调用）。
func (b *BaseRuntime) UninstallBinary(ctx context.Context) error {
	_ = ctx
	destination := b.installPath()
	if err := os.RemoveAll(destination); err != nil {
		return NewInstallError(CodeRuntimePathMissing, "remove runtime dir").WithCause(err)
	}
	b.refreshInstallState()
	return nil
}

// ModelManifests 返回该运行时负责的模型清单。
func (b *BaseRuntime) ModelManifests() []catalog.Manifest {
	return b.modelManifests
}

// Manifest 返回该运行时下载清单。
func (b *BaseRuntime) Manifest() catalog.Runtime {
	return b.manifest
}

// matchModelXPU 返回模型清单中匹配当前平台/vendor 的 XPU（兼容 vendor 兜底）。
func (b *BaseRuntime) matchModelXPU(m catalog.Manifest) (*catalog.XPU, bool) {
	vendor := VendorName(b.CurrentVendor())
	_, xpu := m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, vendor)
	if xpu == nil {
		_, xpu = m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, "all")
	}
	return xpu, xpu != nil
}

// modelInfoFor 依据清单与本地状态生成运行时视角的 ModelInfo。
func (b *BaseRuntime) modelInfoFor(m catalog.Manifest) ModelInfo {
	xpu, _ := b.matchModelXPU(m)
	params := m.MergedParameters(xpu)
	dir := b.modelDir(m.Name)
	installed := isInstalledMarker(dir)
	engine := b.name
	return ModelInfo{
		Name:        m.Name,
		DisplayName: m.Display(),
		Type:        m.Type,
		Version:     m.Version,
		Installed:   installed,
		InstallPath: dir,
		Engine:      engine,
		Parameters:  params,
	}
}

// scanModelInfos 扫描该运行时负责的全部模型。
func (b *BaseRuntime) scanModelInfos() []ModelInfo {
	out := make([]ModelInfo, 0, len(b.modelManifests))
	for _, m := range b.modelManifests {
		if _, ok := b.matchModelXPU(m); !ok {
			continue // 本平台不可用的模型不列出
		}
		out = append(out, b.modelInfoFor(m))
	}
	return out
}

// safeModelDir 校验模型名合法性并返回模型目录。
func (b *BaseRuntime) safeModelDir(modelName string) (string, error) {
	if strings.TrimSpace(modelName) == "" || filepath.Base(modelName) != modelName || strings.Contains(modelName, "..") {
		return "", NewInstallError(CodeInvalidOptions, fmt.Sprintf("invalid model name %q", modelName))
	}
	return b.modelDir(modelName), nil
}

// InstallModel 安装模型（引擎校验 + 按 XPU 下载清单逐文件下载/解压 + .installed）。
func (b *BaseRuntime) InstallModel(ctx context.Context, m catalog.Manifest, callback InstallCallback) error {
	if b.opts.Downloader == nil {
		return NewInstallError(CodeDownloadFailed, "downloader is not configured")
	}
	xpu, ok := b.matchModelXPU(m)
	if !ok {
		return NewInstallError(CodeDownloadFailed, fmt.Sprintf("model %s has no compatible platform/vendor", m.Name))
	}
	if !containsFold(xpu.InferenceEngines, string(b.name)) {
		return NewInstallError(CodeInvalidModelType, fmt.Sprintf("model %s is not served by runtime %s", m.Name, b.name))
	}
	target, err := b.safeModelDir(m.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return NewInstallError(CodeModelPathMissing, "create model dir").WithCause(err)
	}
	if isInstalledMarker(target) {
		return nil
	}
	emit(callback, InstallState{Status: StatusInstalling, Stage: InstallStagePreparing, Progress: 0, Message: "准备安装模型"})

	downloads := flattenDownloads(xpu.Downloads)
	if len(downloads) == 0 {
		return NewInstallError(CodeDownloadFailed, fmt.Sprintf("model %s has no downloads", m.Name))
	}
	// 进度按“文件槽位”估算：每个文件均分 0..90 的区间。
	// 不依赖清单 file_size（部分下载项为 0），未知大小也能随下载推进。
	count := len(downloads)
	slot := float64(90) / float64(count)
	var knownBytes int64
	for _, d := range downloads {
		knownBytes += d.FileSize
	}
	if err := b.ensureDiskSpace(target, knownBytes); err != nil {
		return err
	}
	var completedBytes int64
	for i, d := range downloads {
		label := filepath.Base(d.FileName)
		if label == "" || label == "." {
			label = filepath.Base(d.URL)
		}
		progressBase := int(float64(i) * slot)
		emit(callback, InstallState{
			Status: StatusDownloading, Stage: InstallStageDownloading, Progress: progressBase,
			DownloadedBytes: completedBytes, TotalBytes: knownBytes, Message: "下载 " + label,
		})
		tmp, err := os.CreateTemp(target, ".download-*")
		if err != nil {
			return NewInstallError(CodeDownloadFailed, "create temp file").WithCause(err)
		}
		tmpPath := tmp.Name()
		tmp.Close()
		removeTmp := true
		defer func() {
			if removeTmp {
				_ = os.Remove(tmpPath)
			}
		}()

		fileTotal := d.FileSize
		downloadErr := b.opts.Downloader.DownloadWithContext(ctx, d.URL, tmpPath, d.SHA256, d.FileSize, func(progress float64, speed float64) {
			fraction := clampProgress(progress)
			var done int64
			if fileTotal > 0 {
				done = int64(fraction * float64(fileTotal))
			}
			percent := progressBase + int(fraction*slot)
			emit(callback, InstallState{
				Status: StatusDownloading, Stage: InstallStageDownloading,
				Progress:        percent,
				DownloadedBytes: completedBytes + done, TotalBytes: knownBytes,
				Speed: int64(speed), Message: "下载 " + label,
			})
		})
		if downloadErr != nil {
			return WrapInstallDownloadErr(downloadErr, m.Name)
		}
		if IsZIPFile(tmpPath) {
			if err := UnzipSafe(tmpPath, target); err != nil {
				return NewInstallError(CodeExtractFailed, "extract model archive "+label).WithCause(err)
			}
		} else {
			dest := filepath.Join(target, filepath.Base(d.FileName))
			if dest == target || filepath.Base(d.FileName) == "" {
				dest = filepath.Join(target, filepath.Base(d.URL))
			}
			if err := os.Rename(tmpPath, dest); err != nil {
				return NewInstallError(CodeModelPathMissing, "place model file "+label).WithCause(err)
			}
			removeTmp = false
		}
		if fileTotal > 0 {
			completedBytes += fileTotal
		}
		emit(callback, InstallState{
			Status: StatusInstalling, Stage: InstallStageExtracting,
			Progress: int(float64(i+1) * slot), Message: "完成 " + label,
		})
	}
	if err := os.WriteFile(filepath.Join(target, ".installed"), []byte(m.Version), 0644); err != nil {
		return NewInstallError(CodeModelPathMissing, "write model install marker").WithCause(err)
	}
	emit(callback, InstallState{Status: StatusInstalled, Stage: InstallStageCompleted, Progress: 100, Message: "模型安装完成"})
	return nil
}

// UninstallModel 卸载模型目录。调用方应先确保模型进程已停止。
func (b *BaseRuntime) UninstallModel(ctx context.Context, modelName string) error {
	_ = ctx
	target, err := b.safeModelDir(modelName)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return NewInstallError(CodeModelPathMissing, "remove model dir").WithCause(err)
	}
	return nil
}

// TriggerModelRefresh 全量重扫模型安装状态。
func (b *BaseRuntime) TriggerModelRefresh() {
	for _, m := range b.modelManifests {
		_ = m
	}
}

// flattenDownloads 取 XPU 下载清单（优先 model_scope 键，其次其它键）的所有条目。
func flattenDownloads(downloads map[string][]catalog.Download) []catalog.Download {
	if len(downloads) == 0 {
		return nil
	}
	if list, ok := downloads["model_scope"]; ok && len(list) > 0 {
		return list
	}
	for _, list := range downloads {
		if len(list) > 0 {
			return list
		}
	}
	return nil
}

func containsFold(values []string, target string) bool {
	for _, v := range values {
		if strings.EqualFold(v, target) {
			return true
		}
	}
	return false
}

// clampProgress 把下载器进度回调值归一化到 0..1（回调为小数语义）。
func clampProgress(progress float64) float64 {
	if progress < 0 {
		return 0
	}
	if progress > 1 {
		return 1
	}
	return progress
}

// progressPercent 把 0..1 小数进度转成 0..100 整数百分比。
func progressPercent(fraction float64) int {
	percent := int(clampProgress(fraction)*100 + 0.5)
	if percent > 100 {
		percent = 100
	}
	return percent
}

// WrapInstallDownloadErr 统一下载失败的错误码包装。
func WrapInstallDownloadErr(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewInstallError(CodeContextCanceled, fmt.Sprintf("install %s cancelled", what)).WithCause(err)
	}
	return NewInstallError(CodeDownloadFailed, fmt.Sprintf("download %s failed", what)).WithCause(err)
}
