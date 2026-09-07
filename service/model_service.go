// Package service 提供 Wails3 绑定层：ModelService 是 pkg/manager
// （supervisor）之上的薄门面，方法签名与返回结构与早期版本保持一致，
// 前端绑定无需改动。真正的安装/启动/停止/健康逻辑在 pkg/runtime + pkg/manager。
package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/manager"
	appruntime "github.com/AntNoHuabei/mediacraft/pkg/runtime"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ModelInfo 模型展示信息（与前端绑定契约一致）。
type ModelInfo struct {
	Name         string         `json:"name"`
	DisplayName  string         `json:"displayName"`
	Type         string         `json:"type"`
	Description  string         `json:"description"`
	Capabilities []string       `json:"capabilities"`
	Tags         []string       `json:"tags"`
	Runtimes     []string       `json:"runtimes"`
	Version      string         `json:"version"`
	Installed    bool           `json:"installed"`
	Status       string         `json:"status"`
	Vendor       string         `json:"vendor"`
	Engine       string         `json:"engine"`
	Parameters   map[string]any `json:"parameters,omitempty"`
}

// RuntimeInfo 运行时展示信息（与前端绑定契约一致）。
type RuntimeInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	Status      string `json:"status"`
	Vendor      string `json:"vendor"`
	Backend     string `json:"backend"`
}

type ImageRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Steps  int    `json:"steps"`
}

// laxInt 宽容整数：接受 JSON 数字、数字字符串、空字符串或 null。
type laxInt int

func (n *laxInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		*n = 0
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		*n = laxInt(f)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		if v, perr := strconv.ParseFloat(strings.TrimSpace(str), 64); perr == nil {
			*n = laxInt(v)
			return nil
		}
	}
	return fmt.Errorf("field is not an integer: %s", s)
}

// laxFloat 宽容小数：接受 JSON 数字、数字字符串、空字符串或 null。
type laxFloat float64

func (n *laxFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		*n = 0
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		*n = laxFloat(f)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		if v, perr := strconv.ParseFloat(strings.TrimSpace(str), 64); perr == nil {
			*n = laxFloat(v)
			return nil
		}
	}
	return fmt.Errorf("field is not a number: %s", s)
}

// parseImageRequest 解析图片生成请求，前端数字字段可能以字符串或 null 形式
// 进入（antd 输入框清空/输入时），这里统一宽容成 int。
func parseImageRequest(request string) (ImageRequest, error) {
	var out ImageRequest
	if strings.TrimSpace(request) == "" {
		return out, fmt.Errorf("request is required")
	}
	var lax struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
		Width  laxInt `json:"width"`
		Height laxInt `json:"height"`
		Steps  laxInt `json:"steps"`
	}
	if err := json.Unmarshal([]byte(request), &lax); err != nil {
		return out, fmt.Errorf("invalid image request json: %w", err)
	}
	out.Model = lax.Model
	out.Prompt = lax.Prompt
	out.Width = int(lax.Width)
	out.Height = int(lax.Height)
	out.Steps = int(lax.Steps)
	return out, nil
}

// parseAudioRequest 解析音频请求 JSON（TTS 的 speed 字段宽容字符串/number/null）。
func parseAudioRequest(request string) (AudioRequest, error) {
	var out AudioRequest
	if strings.TrimSpace(request) == "" {
		return out, fmt.Errorf("request is required")
	}
	var lax struct {
		Model   string   `json:"model"`
		Text    string   `json:"text"`
		Speaker string   `json:"speaker"`
		Speed   laxFloat `json:"speed"`
		Audio   string   `json:"audio"`
		Format  string   `json:"format"`
	}
	if err := json.Unmarshal([]byte(request), &lax); err != nil {
		return out, fmt.Errorf("invalid audio request json: %w", err)
	}
	out.Model = lax.Model
	out.Text = lax.Text
	out.Speaker = lax.Speaker
	out.Speed = float64(lax.Speed)
	out.Audio = lax.Audio
	out.Format = lax.Format
	return out, nil
}

type ImageResult struct {
	B64JSON string `json:"b64_json"`
}

type AudioRequest struct {
	Model   string  `json:"model"`
	Text    string  `json:"text,omitempty"`
	Speaker string  `json:"speaker,omitempty"`
	Speed   float64 `json:"speed,omitempty"`
	Audio   string  `json:"audio,omitempty"`
	Format  string  `json:"format,omitempty"`
}

type ASRResult struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
}

// ModelService Wails 绑定门面。
type ModelService struct {
	catalog catalog.Catalog
	manager *manager.Supervisor
	root    string
	log     *slog.Logger

	healthOnce sync.Once
	mu         sync.Mutex
	cancel     map[string]context.CancelFunc // 模型安装取消句柄
}

// NewModelService 构造服务门面并初始化 supervisor。
func NewModelService(cat catalog.Catalog) (*ModelService, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config dir: %w", err)
	}
	base := filepath.Join(root, "mediacraft")
	log := slog.Default()
	supervisor, err := manager.NewSupervisor(cat, manager.Options{
		RuntimesDir: filepath.Join(base, "runtimes"),
		ModelsDir:   filepath.Join(base, "models"),
		Logger:      log,
	})
	if err != nil {
		return nil, err
	}
	s := &ModelService{
		catalog: cat,
		manager: supervisor,
		root:    base,
		log:     log,
		cancel:  map[string]context.CancelFunc{},
	}
	// 安装进度与模型运行状态经 Wails 事件推给前端。
	supervisor.OnInstallState = func(name appruntime.Name, state appruntime.InstallState) {
		s.publishInstall("runtime", string(name), state)
	}
	supervisor.OnModelState = func(modelName string, engine appruntime.Name, info appruntime.ModelRuntimeInfo) {
		app := application.Get()
		if app == nil {
			return
		}
		_ = app.Event.Emit("mc:model", map[string]any{
			"name":         modelName,
			"engine":       string(engine),
			"run_status":   string(info.RunStatus),
			"port":         info.Port,
			"health_error": info.HealthError,
		})
	}
	return s, nil
}

// publishInstall 把安装进度转成前端事件 mc:install。
func (s *ModelService) publishInstall(kind, name string, state appruntime.InstallState) {
	app := application.Get()
	if app == nil {
		return
	}
	_ = app.Event.Emit("mc:install", map[string]any{
		"kind":        kind,
		"name":        name,
		"status":      string(state.Status),
		"stage":       string(state.Stage),
		"progress":    state.Progress,
		"message":     state.Message,
		"bytes_done":  state.DownloadedBytes,
		"bytes_total": state.TotalBytes,
		"speed":       state.Speed,
	})
}

// catalogRuntime 按名字在 catalog 中查找运行时清单。
func (s *ModelService) catalogRuntime(name string) (catalog.Runtime, bool) {
	for _, r := range s.catalog.Runtimes {
		if r.Name == name {
			return r, true
		}
	}
	return catalog.Runtime{}, false
}

func (s *ModelService) vendorName() string {
	return appruntime.VendorName(appruntime.CurrentVendor())
}

// ensureHealth 惰性启动后台健康轮询（首次启动模型时）。
func (s *ModelService) ensureHealth() {
	s.healthOnce.Do(func() {
		s.manager.StartHealthChecks(0)
	})
}

// ListModels 列出模型（catalog 展示信息 + manager 安装/运行状态）。
func (s *ModelService) ListModels(filter string) ([]ModelInfo, error) {
	kind := strings.TrimSpace(filter)
	vendor := s.vendorName()
	result := make([]ModelInfo, 0, len(s.catalog.Models))
	for _, m := range s.catalog.Models {
		_, xpu := m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, vendor)
		if xpu == nil {
			_, xpu = m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, "all")
		}
		if kind != "" && kind != "all" && m.Type != kind || xpu == nil {
			continue
		}
		installed := s.manager.ModelInstalled(m.Name)
		status := "available"
		if installed {
			status = "installed"
		}
		engine := ""
		if len(xpu.InferenceEngines) > 0 {
			engine = xpu.InferenceEngines[0]
		}
		if running, ok := s.manager.RunningModelInfo(m.Name); ok {
			status = "running"
			if running.Engine != "" {
				engine = string(running.Engine)
			}
		}
		result = append(result, ModelInfo{
			Name: m.Name, DisplayName: m.Display(), Type: m.Type,
			Description: m.Description.Zh, Capabilities: m.Capabilities, Tags: m.Tags,
			Runtimes: m.RuntimeNames(), Version: m.Version,
			Installed: installed, Status: status,
			Vendor: xpu.Vendor, Engine: engine,
			Parameters: m.MergedParameters(xpu),
		})
	}
	return result, nil
}

// runtimeInfoFor 由 catalog + manager 汇总运行时展示信息。
func (s *ModelService) runtimeInfoFor(r catalog.Runtime) RuntimeInfo {
	installed := false
	version := ""
	if info, err := s.manager.GetRuntimeInfo(appruntime.Name(r.Name)); err == nil {
		installed = info.Installed
		version = info.CurrentVersion
	}
	if version == "" {
		if selected, _, ok := r.WindowsDownload(); ok {
			version = selected.Version
		}
	}
	status := "available"
	if installed {
		status = "installed"
	}
	return RuntimeInfo{
		Name:        r.Name,
		DisplayName: r.DisplayName.Zh,
		Description: r.Description.Zh,
		Version:     version,
		Installed:   installed,
		Status:      status,
		Vendor:      s.vendorName(),
		Backend:     appruntime.BackendForVendor(appruntime.CurrentVendor()),
	}
}

// ListRuntimes 列出运行时。
func (s *ModelService) ListRuntimes() []RuntimeInfo {
	result := make([]RuntimeInfo, 0, len(s.catalog.Runtimes))
	for _, r := range s.catalog.Runtimes {
		result = append(result, s.runtimeInfoFor(r))
	}
	return result
}

// GetRuntime 取单个运行时。
func (s *ModelService) GetRuntime(name string) (RuntimeInfo, error) {
	r, ok := s.catalogRuntime(name)
	if !ok {
		return RuntimeInfo{}, fmt.Errorf("runtime %q not found", name)
	}
	return s.runtimeInfoFor(r), nil
}

// InstallRuntime 安装运行时（阻塞式 Wails 调用；进度经 mc:install 事件推送）。
func (s *ModelService) InstallRuntime(name string) error {
	if _, ok := s.catalogRuntime(name); !ok {
		return fmt.Errorf("runtime %q not found", name)
	}
	if info, err := s.manager.GetRuntimeInfo(appruntime.Name(name)); err == nil && info.Installed {
		return nil
	}
	err := s.manager.InstallRuntime(context.Background(), appruntime.Name(name), func(state appruntime.InstallState) {
		if state.Status == appruntime.StatusError {
			s.log.Warn("runtime install failed", "runtime", name, "message", state.Message)
		}
	})
	if err != nil {
		s.publishInstall("runtime", name, appruntime.InstallState{Status: appruntime.StatusError, Progress: 0, Message: err.Error()})
		return err
	}
	return nil
}

// UninstallRuntime 卸载运行时（先停其名下所有模型进程）。
func (s *ModelService) UninstallRuntime(name string) error {
	if _, ok := s.catalogRuntime(name); !ok {
		return fmt.Errorf("runtime %q not found", name)
	}
	return s.manager.UninstallRuntime(context.Background(), appruntime.Name(name))
}

// GetModel 取单个模型。
func (s *ModelService) GetModel(name string) (ModelInfo, error) {
	models, err := s.ListModels("all")
	if err != nil {
		return ModelInfo{}, err
	}
	for _, m := range models {
		if m.Name == name {
			return m, nil
		}
	}
	return ModelInfo{}, fmt.Errorf("model %q not found", name)
}

// InstallModel 安装模型（阻塞、可经 CancelInstallModel 取消）。
func (s *ModelService) InstallModel(name string) error {
	if !s.catalogHasModel(name) {
		return fmt.Errorf("model %q not found", name)
	}
	if s.manager.ModelInstalled(name) {
		return nil
	}
	s.mu.Lock()
	if _, exists := s.cancel[name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("model %q is already installing", name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel[name] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancel, name)
		s.mu.Unlock()
	}()
	cb := func(state appruntime.InstallState) {
		s.publishInstall("model", name, state)
	}
	if err := s.manager.InstallModel(ctx, name, cb); err != nil {
		s.publishInstall("model", name, appruntime.InstallState{Status: appruntime.StatusError, Progress: 0, Message: err.Error()})
		return err
	}
	return nil
}

// CancelInstallModel 取消进行中的模型安装。
func (s *ModelService) CancelInstallModel(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel, ok := s.cancel[name]
	if !ok {
		return fmt.Errorf("model %q is not installing", name)
	}
	cancel()
	return nil
}

// UninstallModel 卸载模型。
func (s *ModelService) UninstallModel(name string) error {
	if !s.catalogHasModel(name) {
		return fmt.Errorf("model %q not found", name)
	}
	return s.manager.UninstallModel(context.Background(), name)
}

func (s *ModelService) catalogHasModel(name string) bool {
	for _, m := range s.catalog.Models {
		if m.Name == name {
			return true
		}
	}
	return false
}

// StartModel 启动模型（自动前置安装引擎、复用已运行实例）。
func (s *ModelService) StartModel(name string) error {
	if !s.catalogHasModel(name) {
		return fmt.Errorf("model %q not found", name)
	}
	s.ensureHealth()
	_, err := s.manager.StartModel(context.Background(), name)
	return err
}

// StopModel 停止模型。
func (s *ModelService) StopModel(name string) error {
	return s.manager.StopModel(context.Background(), name)
}

// runningPort 确保模型运行并返回其监听端口。
func (s *ModelService) runningPort(name string) (int, error) {
	if err := s.StartModel(name); err != nil {
		return 0, err
	}
	if info, ok := s.manager.RunningModelInfo(name); ok {
		return info.Port, nil
	}
	return 0, fmt.Errorf("model %q did not report a running port", name)
}

// modelParameters 按当前平台/厂商返回模型合并参数（manifest 顶层 + xpu 覆盖）。
func (s *ModelService) modelParameters(name string) map[string]any {
	for i := range s.catalog.Models {
		m := &s.catalog.Models[i]
		if m.Name != name {
			continue
		}
		_, xpu := m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, s.vendorName())
		if xpu == nil {
			_, xpu = m.MatchPlatform(goruntime.GOOS, goruntime.GOARCH, "all")
		}
		return m.MergedParameters(xpu)
	}
	return nil
}

// paramNumber 读取合并参数中的数值（json 解码后常见 float64/int/json.Number）。
func paramNumber(params map[string]any, key string) (float64, bool) {
	if params == nil {
		return 0, false
	}
	switch v := params[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

func paramInt(params map[string]any, key string, fallback int) int {
	if v, ok := paramNumber(params, key); ok && v > 0 && v == float64(int64(v)) {
		return int(v)
	}
	return fallback
}

// buildImageRequestBody 组装 sd-server /sdapi/v1/txt2img 请求体。
//
// 关键：cfg_scale 必须显式下发。sd-server 缺省 cfg≈7.0，而
// Z-Image-Turbo 这类蒸馏/低引导模型要求 cfg=1.0（见 sd.cpp docs/z_image.md：
// turbo 示例 --cfg-scale 1.0 --steps 8），按缺省 7.0 生成会糊、过饱和、色彩发脏。
// 采样器不显式指定，沿用 sd-server 默认（与官方 sd-cli 示例一致）。
func buildImageRequestBody(in ImageRequest, params map[string]any) map[string]any {
	width := in.Width
	if width <= 0 {
		width = paramInt(params, "default_width", 1024)
	}
	height := in.Height
	if height <= 0 {
		height = paramInt(params, "default_height", 1024)
	}
	steps := in.Steps
	if steps <= 0 {
		steps = paramInt(params, "default_steps", 9)
	}
	body := map[string]any{
		"prompt":          in.Prompt,
		"negative_prompt": "",
		"width":           width,
		"height":          height,
		"steps":           steps,
		"seed":            int64(-1),
		"batch_size":      1,
	}
	if cfg, ok := paramNumber(params, "cfg_scale"); ok && cfg > 0 {
		body["cfg_scale"] = cfg
	}
	return body
}

// GenerateImage sd.cpp 文生图（/sdapi/v1/txt2img）。
func (s *ModelService) GenerateImage(request string) (string, error) {
	input, err := parseImageRequest(request)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Model) == "" || strings.TrimSpace(input.Prompt) == "" {
		return "", fmt.Errorf("model and prompt are required")
	}
	port, err := s.runningPort(input.Model)
	if err != nil {
		return "", err
	}
	params := s.modelParameters(input.Model)
	body, _ := json.Marshal(buildImageRequestBody(input, params))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/sdapi/v1/txt2img", port), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("image generation request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return "", fmt.Errorf("sd-server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Images) == 0 {
		return "", fmt.Errorf("sd-server returned no image")
	}
	return result.Images[0], nil
}

// Transcribe audio.cpp 离线语音识别（/v1/audio/transcriptions）。
func (s *ModelService) Transcribe(request string) (ASRResult, error) {
	input, err := parseAudioRequest(request)
	if err != nil {
		return ASRResult{}, err
	}
	if strings.TrimSpace(input.Model) == "" || strings.TrimSpace(input.Audio) == "" {
		return ASRResult{}, fmt.Errorf("model and audio are required")
	}
	if s.catalogModelType(input.Model) != "asr" {
		return ASRResult{}, fmt.Errorf("model %q is not an ASR model", input.Model)
	}
	port, err := s.runningPort(input.Model)
	if err != nil {
		return ASRResult{}, err
	}
	file, cleanup, err := audioInputFile(input.Audio, input.Format)
	if err != nil {
		return ASRResult{}, err
	}
	defer cleanup()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(file))
	if err != nil {
		return ASRResult{}, err
	}
	src, err := os.Open(file)
	if err != nil {
		return ASRResult{}, err
	}
	_, copyErr := io.Copy(part, src)
	src.Close()
	if copyErr != nil {
		return ASRResult{}, copyErr
	}
	_ = writer.WriteField("model", input.Model)
	if err := writer.Close(); err != nil {
		return ASRResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/audio/transcriptions", port), body)
	if err != nil {
		return ASRResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ASRResult{}, fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return ASRResult{}, fmt.Errorf("audio.cpp returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result ASRResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ASRResult{}, err
	}
	return result, nil
}

// Synthesize audio.cpp 语音合成（/v1/audio/speech）。
func (s *ModelService) Synthesize(request string) (string, error) {
	input, err := parseAudioRequest(request)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Model) == "" || strings.TrimSpace(input.Text) == "" {
		return "", fmt.Errorf("model and text are required")
	}
	if s.catalogModelType(input.Model) != "tts" {
		return "", fmt.Errorf("model %q is not a TTS model", input.Model)
	}
	port, err := s.runningPort(input.Model)
	if err != nil {
		return "", err
	}
	body := map[string]any{"model": input.Model, "input": input.Text, "response_format": "wav"}
	if input.Speaker != "" {
		body["voice"] = input.Speaker
		body["speaker"] = input.Speaker
	}
	if input.Speed > 0 {
		body["speed"] = input.Speed
	}
	data, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/audio/speech", port), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("audio synthesis request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return "", fmt.Errorf("audio.cpp returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if len(audio) == 0 {
		return "", fmt.Errorf("audio.cpp returned empty audio")
	}
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(audio), nil
}

func (s *ModelService) catalogModelType(name string) string {
	for _, m := range s.catalog.Models {
		if m.Name == name {
			return m.Type
		}
	}
	return ""
}

// audioInputFile 把 data URL 或本地路径物化为可上传文件。
func audioInputFile(value, format string) (string, func(), error) {
	if strings.HasPrefix(value, "data:") {
		parts := strings.SplitN(value, ",", 2)
		if len(parts) != 2 {
			return "", func() {}, fmt.Errorf("invalid audio data URL")
		}
		data, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return "", func() {}, err
		}
		if len(data) > 32<<20 {
			return "", func() {}, fmt.Errorf("audio input exceeds 32 MB")
		}
		ext := strings.TrimPrefix(strings.ToLower(format), ".")
		if ext == "" {
			ext = "wav"
		}
		f, err := os.CreateTemp("", "mediacraft-audio-*."+ext)
		if err != nil {
			return "", func() {}, err
		}
		if _, err = f.Write(data); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", func() {}, err
		}
		f.Close()
		return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
	}
	clean := filepath.Clean(value)
	info, err := os.Stat(clean)
	if err != nil || info.IsDir() {
		return "", func() {}, fmt.Errorf("audio file not found")
	}
	if info.Size() > 32<<20 {
		return "", func() {}, fmt.Errorf("audio input exceeds 32 MB")
	}
	return clean, func() {}, nil
}

// ServiceShutdown 应用退出钩子。
func (s *ModelService) ServiceShutdown() error { return s.Close() }

// Close 停止全部模型并关闭 supervisor（幂等）。
func (s *ModelService) Close() error {
	if s.manager == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.manager.Close(ctx)
}

// ParseRequest 解析前端传入的 JSON 请求字符串。
func (s *ModelService) ParseRequest(request string, out any) error {
	if strings.TrimSpace(request) == "" {
		return fmt.Errorf("request is required")
	}
	return json.Unmarshal([]byte(request), out)
}
