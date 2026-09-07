package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"testing"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/runtime"
)

func zipWith(entries map[string]string) []byte {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range entries {
		file, err := writer.Create(name)
		if err != nil {
			panic(err)
		}
		_, _ = file.Write([]byte(content))
	}
	_ = writer.Close()
	return buf.Bytes()
}

// roundtripCatalog 构造带本地下载源的 catalog，供安装/卸载回归测试使用。
func roundtripCatalog(runtimeURL, modelURL string) catalog.Catalog {
	osName, archName := goruntime.GOOS, goruntime.GOARCH
	return catalog.Catalog{
		Runtimes: []catalog.Runtime{
			{
				Name: "audio.cpp",
				Architectures: []catalog.RuntimeArchitecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.RuntimeXPU{{
						Vendor: "intel", Type: "cpu",
						Versions: []catalog.RuntimeVersion{{
							Version:   "1.0.0",
							Downloads: map[string]string{"model_scope": runtimeURL},
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
							Version:   "1.0.0",
							Downloads: map[string]string{"model_scope": runtimeURL},
						}},
					}},
				}},
			},
		},
		Models: []catalog.Manifest{{
			Name: "asr-roundtrip", Type: "asr", Version: "1.0.0",
			Architectures: []catalog.Architecture{{
				OS: osName, Arch: archName,
				XPUs: []catalog.XPU{{
					Vendor: "intel", Type: "cpu",
					InferenceEngines: []string{"audio.cpp"},
					Downloads: map[string][]catalog.Download{
						"model_scope": {{URL: modelURL, FileName: "asr.zip", SHA256: ""}},
					},
				}},
			}},
			Parameters: map[string]any{
				"audio_cpp_family": "qwen3_asr", "audio_cpp_task": "asr",
				"audio_cpp_mode": "streaming", "maingguf": "asr.gguf",
			},
		}},
	}
}

// TestSupervisorInstallUninstallReflectsInStatus 验证：卸载后运行时/模型的
// 状态经 ListRuntimes/ListModels/GetRuntimeInfo 立即可见（不依赖前端刷新）。
func TestSupervisorInstallUninstallReflectsInStatus(t *testing.T) {
	runtimeZip := zipWith(map[string]string{runtime.AudioCppExecutable: "placeholder"})
	modelZip := zipWith(map[string]string{"asr.gguf": "placeholder"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/runtime.zip" {
			w.Write(runtimeZip)
			return
		}
		w.Write(modelZip)
	}))
	defer server.Close()

	dir := t.TempDir()
	supervisor, err := NewSupervisor(
		roundtripCatalog(server.URL+"/runtime.zip", server.URL+"/model.zip"),
		Options{
			RuntimesDir: dir + "/runtimes",
			ModelsDir:   dir + "/models",
			Vendor:      runtime.VendorIntel,
		},
	)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	defer supervisor.StopHealthChecks()
	ctx := context.Background()

	// 初始：未安装
	if info, _ := supervisor.GetRuntimeInfo(runtime.AudioCpp); info.Installed {
		t.Fatal("runtime should start uninstalled")
	}

	// 安装运行时
	if err := supervisor.InstallRuntime(ctx, runtime.AudioCpp, nil); err != nil {
		t.Fatalf("InstallRuntime: %v", err)
	}
	if info, _ := supervisor.GetRuntimeInfo(runtime.AudioCpp); !info.Installed {
		t.Fatal("runtime should be installed after InstallRuntime")
	}
	runtimes := supervisor.ListRuntimes()
	audioInfo := findRuntime(runtimes, "audio.cpp")
	if audioInfo == nil || !audioInfo.Installed {
		t.Fatalf("ListRuntimes should reflect installed audio.cpp: %+v", runtimes)
	}

	// 安装模型
	if err := supervisor.InstallModel(ctx, "asr-roundtrip", nil); err != nil {
		t.Fatalf("InstallModel: %v", err)
	}
	if !supervisor.ModelInstalled("asr-roundtrip") {
		t.Fatal("model should be installed after InstallModel")
	}

	// 卸载模型
	if err := supervisor.UninstallModel(ctx, "asr-roundtrip"); err != nil {
		t.Fatalf("UninstallModel: %v", err)
	}
	if supervisor.ModelInstalled("asr-roundtrip") {
		t.Fatal("model should be uninstalled after UninstallModel")
	}
	models, err := supervisor.ListModels(ctx, runtime.ModelFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range models {
		if m.Name == "asr-roundtrip" {
			found = true
			if m.Installed {
				t.Fatal("ListModels should reflect uninstalled model")
			}
		}
	}
	if !found {
		t.Fatal("model should still be listed (as not installed) after uninstall")
	}

	// 卸载运行时
	if err := supervisor.UninstallRuntime(ctx, runtime.AudioCpp); err != nil {
		t.Fatalf("UninstallRuntime: %v", err)
	}
	if info, _ := supervisor.GetRuntimeInfo(runtime.AudioCpp); info.Installed {
		t.Fatal("runtime should be uninstalled after UninstallRuntime")
	}
	runtimes = supervisor.ListRuntimes()
	audioInfo = findRuntime(runtimes, "audio.cpp")
	if audioInfo == nil || audioInfo.Installed {
		t.Fatalf("ListRuntimes should reflect uninstalled audio.cpp: %+v", runtimes)
	}

	// 幂等：再次卸载不应报错
	if err := supervisor.UninstallRuntime(ctx, runtime.AudioCpp); err != nil {
		t.Fatalf("second UninstallRuntime should be idempotent: %v", err)
	}
}

func findRuntime(list []runtime.RuntimeInfo, name string) *runtime.RuntimeInfo {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}
