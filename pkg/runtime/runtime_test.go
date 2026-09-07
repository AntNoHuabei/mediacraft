package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AntNoHuabei/mediacraft/catalog"
)

// ---------- 通用小工具 ----------

func testAudioRuntime() *AudioCppRuntime {
	return NewAudioCppRuntime(catalog.Runtime{}, nil, BaseOptions{})
}

func testAudioRuntimeVendor(vendor Vendor) *AudioCppRuntime {
	return NewAudioCppRuntime(catalog.Runtime{}, nil, BaseOptions{Vendor: vendor})
}

func modelParams(kv ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		out[key] = kv[i+1]
	}
	return out
}

func newModel(name, modelType string, params map[string]any) ModelInfo {
	return ModelInfo{
		Name:        name,
		Type:        modelType,
		Installed:   true,
		InstallPath: filepath.Join(string(os.PathSeparator), "models", name),
		Parameters:  params,
	}
}

// ---------- audio.cpp：参数推导与 config 构造 ----------

func TestAudioCppResolveParamsFromManifest(t *testing.T) {
	r := testAudioRuntime()
	model := newModel("qwen3-asr-0.6b", "asr", modelParams(
		"audio_cpp_family", "qwen3_asr",
		"audio_cpp_task", "asr",
		"audio_cpp_mode", "offline",
		"maingguf", "qwen3-asr-0.6b-q8_0.gguf",
	))
	params, err := r.resolveAudioCppParams(model)
	if err != nil {
		t.Fatalf("resolve params: %v", err)
	}
	if params.Family != "qwen3_asr" || params.Task != "asr" || params.Mode != "offline" {
		t.Fatalf("unexpected params: %+v", params)
	}
	want := filepath.Join(model.InstallPath, "qwen3-asr-0.6b-q8_0.gguf")
	if params.ModelPath != want {
		t.Fatalf("model path = %q, want %q", params.ModelPath, want)
	}
}

func TestAudioCppResolveParamsInferFromName(t *testing.T) {
	r := testAudioRuntimeVendor(VendorCPU)
	model := newModel("Qwen3-TTS-VoiceClone", "tts", nil)
	params, err := r.resolveAudioCppParams(model)
	if err != nil {
		t.Fatalf("resolve params: %v", err)
	}
	if params.Family != "qwen3_tts" {
		t.Fatalf("family = %q, want qwen3_tts", params.Family)
	}
	if params.Task != "tts" || params.Mode != "streaming" {
		t.Fatalf("task/mode = %s/%s, want tts/streaming", params.Task, params.Mode)
	}
	if params.Backend != "cpu" {
		t.Fatalf("backend = %q, want cpu (no GPU env)", params.Backend)
	}
}

func TestAudioCppBackendFromVendor(t *testing.T) {
	cases := map[Vendor]string{
		VendorNvidia: "cuda",
		VendorAMD:    "hip",
		VendorIntel:  "cpu",
		VendorCPU:    "cpu",
	}
	for vendor, want := range cases {
		if got := BackendForVendor(vendor); got != want {
			t.Errorf("BackendForVendor(%s) = %q, want %q", vendor, got, want)
		}
	}
}

func TestAudioCppServerConfigStreamingASRLiveIngest(t *testing.T) {
	r := testAudioRuntimeVendor(VendorCPU)
	model := newModel("qwen3-asr", "asr", modelParams(
		"audio_cpp_family", "qwen3_asr",
		"audio_cpp_task", "asr",
		"audio_cpp_mode", "streaming",
		"maingguf", "m.gguf",
	))
	params, err := r.resolveAudioCppParams(model)
	if err != nil {
		t.Fatal(err)
	}
	cfg := r.buildAudioCppServerConfig(model, 9123, params)
	if cfg.Port != 9123 || cfg.Backend != "cpu" || !cfg.LazyLoad {
		t.Fatalf("bad base config: %+v", cfg)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != model.Name {
		t.Fatalf("bad models: %+v", cfg.Models)
	}
	live, ok := cfg.LiveIngest.(audioCppLiveIngestDefaults)
	if !ok {
		t.Fatalf("live_ingest missing for streaming ASR: %+v", cfg)
	}
	if live.IdleTimeoutMS != 30000 || live.MaxChunkBytes != 8388608 {
		t.Fatalf("bad live_ingest defaults: %+v", live)
	}
}

func TestAudioCppServerConfigDeviceThreads(t *testing.T) {
	r := testAudioRuntimeVendor(VendorCPU)
	model := newModel("qwen3-tts", "tts", modelParams(
		"audio_cpp_family", "qwen3_tts",
		"maingguf", "m.gguf",
		"device", 1,
		"threads", 8,
		"backend", "hip",
	))
	params, err := r.resolveAudioCppParams(model)
	if err != nil {
		t.Fatal(err)
	}
	cfg := r.buildAudioCppServerConfig(model, 9000, params)
	if cfg.Backend != "hip" {
		t.Fatalf("backend = %q, want hip", cfg.Backend)
	}
	if cfg.Device == nil || *cfg.Device != 1 {
		t.Fatalf("device not propagated: %+v", cfg.Device)
	}
	if cfg.Threads == nil || *cfg.Threads != 8 {
		t.Fatalf("threads not propagated: %+v", cfg.Threads)
	}
}

// ---------- sd.cpp：命令行构造 ----------

func TestSDCppBuildArgsIncludesModelAndOptionalFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"z_image_turbo-Q4_K.gguf":             "diffusion",
		"diffusion_pytorch_model.safetensors": "vae",
		"Qwen3-4B-Instruct-2507-Q4_K_M.gguf":  "llm",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	r := NewSDCppRuntime(catalog.Runtime{}, nil, BaseOptions{})
	model := ModelInfo{
		Name:        "zimage-turbo",
		Type:        "image-generation",
		Installed:   true,
		InstallPath: dir,
		Parameters: modelParams(
			"diffusion_model", "z_image_turbo-Q4_K.gguf",
			"vae", "diffusion_pytorch_model.safetensors",
			"llm", "Qwen3-4B-Instruct-2507-Q4_K_M.gguf",
			"diffusion_fa", true,
		),
	}
	args, err := r.buildSDCppArgs(model, 7777)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--diffusion-model", filepath.Join(dir, "z_image_turbo-Q4_K.gguf"),
		"--listen-port", "7777", "-l", "127.0.0.1",
		"--vae", filepath.Join(dir, "diffusion_pytorch_model.safetensors"),
		"--llm", filepath.Join(dir, "Qwen3-4B-Instruct-2507-Q4_K_M.gguf"),
		"--diffusion-fa",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%s", want, joined)
		}
	}
}

func TestSDCppBuildArgsMissingDiffusionModel(t *testing.T) {
	r := NewSDCppRuntime(catalog.Runtime{}, nil, BaseOptions{})
	model := newModel("zimage-turbo", "image-generation", modelParams("vae", "x.safetensors"))
	_, err := r.buildSDCppArgs(model, 1)
	if err == nil {
		t.Fatal("expected error for missing diffusion_model")
	}
	if sme, ok := AsStartModelError(err); !ok || sme.Code != CodeInvalidOptions {
		t.Fatalf("expected INVALID_OPTIONS error, got %v", err)
	}
}

func TestSDCppBuildArgsMissingOptionalFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m.gguf"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewSDCppRuntime(catalog.Runtime{}, nil, BaseOptions{})
	model := ModelInfo{Name: "m", InstallPath: dir, Parameters: modelParams("diffusion_model", "m.gguf", "vae", "missing.safetensors")}
	_, err := r.buildSDCppArgs(model, 1)
	if err == nil {
		t.Fatal("expected error for missing vae file")
	}
	if sme, ok := AsStartModelError(err); !ok || sme.Code != CodeModelFileMissing {
		t.Fatalf("expected MODEL_FILE_MISSING error, got %v", err)
	}
}

// ---------- 健康助手 ----------

func TestHTTPHealthOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	if !HTTPHealthOK(server.URL, time.Second) {
		t.Fatal("expected healthy")
	}
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := closed.URL
	closed.Close()
	if HTTPHealthOK(url, time.Second) {
		t.Fatal("expected unhealthy for closed server")
	}
}

func TestTCPPortOpen(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if !TCPPortOpen("127.0.0.1", port, time.Second) {
		t.Fatal("expected open port to be reachable")
	}
	if TCPPortOpen("127.0.0.1", 1, 300*time.Millisecond) {
		t.Fatal("expected port 1 to be unreachable")
	}
}

// ---------- 错误码/健康聚合 ----------

func TestStartModelErrorAs(t *testing.T) {
	err := NewStartModelError(CodeExecutableMissing, "missing exe").WithCause(errors.New("boom"))
	if !strings.Contains(err.Error(), CodeExecutableMissing) {
		t.Fatalf("error string missing code: %v", err)
	}
	wrapped := WrapStartModelError(err, CodeProcessStartFailed, "start failed")
	if sme, ok := AsStartModelError(wrapped); !ok || sme.Code != CodeProcessStartFailed {
		t.Fatalf("WrapStartModelError lost outer code: %v", wrapped)
	}
	if !errors.Is(wrapped, err) {
		t.Fatal("unwrap chain broken")
	}
}

func TestRuntimeHealthErrorAs(t *testing.T) {
	inner := errors.New("port down")
	healthErr := &RuntimeHealthError{Runtime: AudioCpp, Models: []string{"m1"}, Err: inner}
	if got, ok := AsRuntimeHealthError(healthErr); !ok || got.Runtime != AudioCpp {
		t.Fatalf("AsRuntimeHealthError failed: %v", got)
	}
	if !errors.Is(healthErr, inner) {
		t.Fatal("health error must unwrap to cause")
	}
}

// ---------- 日志环形缓冲 ----------

func TestRecentLogWritesLinesAndStripsANSI(t *testing.T) {
	log := newRecentLog(8)
	// "line2" 前有 ANSI、行以 \r 结尾（进度行原地刷新语义），line3 是未换行的尾部。
	_, _ = log.Write([]byte("line1\n\x1b[31mline2\x1b[0m\r\nline3"))
	lines := log.SnapshotLines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %#v", len(lines), lines)
	}
	if lines[0] != "line1" || lines[1] != "line2" {
		t.Fatalf("lines = %#v, want [line1 line2]", lines)
	}
	if lines[1] != "line2" {
		t.Fatalf("ANSI not stripped: %q", lines[1])
	}
	if !strings.Contains(log.Text(), "line3") {
		t.Fatal("partial tail missing from Text()")
	}
	if log.Tail(6) == "" {
		t.Fatal("tail should be non-empty")
	}
}

func TestRecentLogEvictsOldest(t *testing.T) {
	log := newRecentLog(2)
	_, _ = log.Write([]byte("a\nb\nc\n"))
	lines := log.SnapshotLines()
	if len(lines) != 2 || lines[0] != "b" || lines[1] != "c" {
		t.Fatalf("ring eviction wrong: %#v", lines)
	}
}

// ---------- 无副作用冒烟：StartModel 前置校验 ----------

func TestAudioCppStartModelRequiresInstalledRuntime(t *testing.T) {
	dir := t.TempDir()
	r := NewAudioCppRuntime(catalog.Runtime{}, nil, BaseOptions{RuntimesDir: dir, ModelsDir: dir})
	err := r.StartModel(context.Background(), newModel("m", "asr", nil), 1)
	if err == nil {
		t.Fatal("expected error when runtime not installed")
	}
	if sme, ok := AsStartModelError(err); !ok || sme.Code != CodeRuntimeNotInstalled {
		t.Fatalf("expected RUNTIME_NOT_INSTALLED, got %v", err)
	}
}

func TestSDCppStartModelRequiresInstalledRuntime(t *testing.T) {
	dir := t.TempDir()
	r := NewSDCppRuntime(catalog.Runtime{}, nil, BaseOptions{RuntimesDir: dir, ModelsDir: dir})
	err := r.StartModel(context.Background(), newModel("z", "image-generation", nil), 1)
	if err == nil {
		t.Fatal("expected error when runtime not installed")
	}
	if sme, ok := AsStartModelError(err); !ok || sme.Code != CodeRuntimeNotInstalled {
		t.Fatalf("expected RUNTIME_NOT_INSTALLED, got %v", err)
	}
}
