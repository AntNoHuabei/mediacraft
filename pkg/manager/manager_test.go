package manager

import (
	"context"
	goruntime "runtime"
	"testing"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/runtime"
)

// testCatalog 构造一个仅含 audio.cpp/sd-cpp 运行时与两类模型的小型 catalog。
func testCatalog() catalog.Catalog {
	osName := goruntime.GOOS
	archName := goruntime.GOARCH
	return catalog.Catalog{
		Runtimes: []catalog.Runtime{
			{
				Name: "audio.cpp",
				Architectures: []catalog.RuntimeArchitecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.RuntimeXPU{{
						Vendor: "intel", Type: "cpu",
						Versions: []catalog.RuntimeVersion{{
							Version: "1.0.0",
							Downloads: map[string]string{
								"model_scope": "https://example.test/audio-cpu.zip",
							},
							SHA256: "",
						}},
					}},
				}},
			},
			{
				Name: "sd-cpp",
				Architectures: []catalog.RuntimeArchitecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.RuntimeXPU{{
						Vendor: "nvidia", Type: "gpu",
						Versions: []catalog.RuntimeVersion{{
							Version: "2.0.0",
							Downloads: map[string]string{
								"model_scope": "https://example.test/sd-cuda.zip",
							},
							SHA256: "",
						}},
					}},
				}},
			},
		},
		Models: []catalog.Manifest{
			{
				Name: "qwen3-asr-test", Type: "asr", Version: "1.0.0",
				Architectures: []catalog.Architecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.XPU{{
						Vendor: "intel", Type: "cpu",
						InferenceEngines: []string{"audio.cpp"},
						Downloads: map[string][]catalog.Download{
							"model_scope": {{URL: "https://example.test/asr.gguf", FileName: "asr.gguf"}},
						},
					}},
				}},
				Parameters: map[string]any{
					"audio_cpp_family": "qwen3_asr", "audio_cpp_task": "asr",
					"audio_cpp_mode": "streaming", "maingguf": "asr.gguf",
				},
			},
			{
				Name: "zimage-test", Type: "image-generation", Version: "1.0.0",
				Architectures: []catalog.Architecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.XPU{{
						Vendor: "nvidia", Type: "gpu",
						InferenceEngines: []string{"sd-cpp"},
						Downloads: map[string][]catalog.Download{
							"model_scope": {{URL: "https://example.test/zimage.zip", FileName: "zimage.zip"}},
						},
					}},
				}},
				Parameters: map[string]any{"diffusion_model": "z.gguf"},
			},
		},
	}
}

func newTestSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	dir := t.TempDir()
	supervisor, err := NewSupervisor(testCatalog(), Options{
		RuntimesDir: dir + "/runtimes",
		ModelsDir:   dir + "/models",
		Vendor:      runtime.VendorIntel,
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	return supervisor
}

func TestSupervisorRegistersRuntimes(t *testing.T) {
	s := newTestSupervisor(t)
	defer s.StopHealthChecks()
	infos := s.ListRuntimes()
	if len(infos) != 2 {
		t.Fatalf("expected 2 runtimes, got %d", len(infos))
	}
	if infos[0].Name != "audio.cpp" || infos[1].Name != "sd-cpp" {
		t.Fatalf("unexpected order: %#v", infos)
	}
	if infos[0].Installed {
		t.Fatal("audio.cpp should not be installed in a fresh temp dir")
	}
	audio, ok := s.Runtime(runtime.AudioCpp)
	if !ok || audio.Name() != runtime.AudioCpp {
		t.Fatal("audio.cpp runtime missing from registry")
	}
}

func TestSupervisorFiltersModelsPerEngineAndVendor(t *testing.T) {
	s := newTestSupervisor(t)
	defer s.StopHealthChecks()
	// intel vendor：audio 模型可见；nvidia-only 的 zimage 模型不可见（无 intel xpu）
	models, err := s.ListModels(context.Background(), runtime.ModelFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var audioSeen, imageSeen bool
	for _, m := range models {
		if m.Name == "qwen3-asr-test" {
			audioSeen = true
		}
		if m.Name == "zimage-test" {
			imageSeen = true
		}
	}
	if !audioSeen {
		t.Fatal("audio model missing for intel vendor")
	}
	if imageSeen {
		t.Fatal("nvidia-only image model must not be listed for intel vendor")
	}
}

func TestSupervisorEngineCandidates(t *testing.T) {
	s := newTestSupervisor(t)
	defer s.StopHealthChecks()
	rts := s.engineCandidatesForModel("qwen3-asr-test")
	if len(rts) != 1 || rts[0].Name() != runtime.AudioCpp {
		t.Fatalf("expected [audio.cpp] candidates, got %v", rts)
	}
	if got := s.engineCandidatesForModel("no-such-model"); len(got) != 0 {
		t.Fatalf("expected no candidates for unknown model, got %d", len(got))
	}
}

func TestSupervisorStartModelRequiresInstalledModel(t *testing.T) {
	s := newTestSupervisor(t)
	defer s.StopHealthChecks()
	_, err := s.StartModel(context.Background(), "qwen3-asr-test")
	if err == nil {
		t.Fatal("expected error for uninstalled model")
	}
	if sme, ok := runtime.AsStartModelError(err); !ok || sme.Code != runtime.CodeModelNotInstalled {
		t.Fatalf("expected MODEL_NOT_INSTALLED, got %v", err)
	}
}

func TestSupervisorUnregisteredRuntimeError(t *testing.T) {
	s := newTestSupervisor(t)
	defer s.StopHealthChecks()
	if _, err := s.GetRuntimeInfo(runtime.Name("missing")); err == nil {
		t.Fatal("expected error for unregistered runtime")
	}
	if err := s.CancelRuntimeInstall(runtime.AudioCpp); err == nil {
		t.Fatal("expected error when nothing is installing")
	}
}

func TestAllocatePortUnique(t *testing.T) {
	a, err := allocatePort()
	if err != nil {
		t.Fatal(err)
	}
	b, err := allocatePort()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("expected distinct ports, got %d and %d", a, b)
	}
	ReleasePort(a)
	ReleasePort(b)
}

func TestSupervisorHealthChecksIdempotent(t *testing.T) {
	s := newTestSupervisor(t)
	s.StartHealthChecks(10 * 1000 * 1000 * 1000) // 长时间间隔，避免测试内触发
	if err := s.CheckHealth(); err != nil {
		t.Fatalf("health check on idle manager: %v", err)
	}
	s.StopHealthChecks()
	s.StopHealthChecks() // 幂等
}

func TestSupervisorStopAllIdle(t *testing.T) {
	s := newTestSupervisor(t)
	defer s.StopHealthChecks()
	if err := s.StopAll(context.Background()); err != nil {
		t.Fatalf("stop all with nothing running: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
}
