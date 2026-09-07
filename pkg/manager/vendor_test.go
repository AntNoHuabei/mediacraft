package manager

import (
	"context"
	goruntime "runtime"
	"testing"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/runtime"
)

// vendorFallbackCatalog 构造只含 amd 厂商可用的 zimage（sd-cpp）清单。
func vendorFallbackCatalog() catalog.Catalog {
	osName, archName := goruntime.GOOS, goruntime.GOARCH
	return catalog.Catalog{
		Runtimes: []catalog.Runtime{
			{
				Name: "audio.cpp",
				Architectures: []catalog.RuntimeArchitecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.RuntimeXPU{{
						Vendor: "amd", Type: "gpu",
						Versions: []catalog.RuntimeVersion{{
							Version:   "1.0.0",
							Downloads: map[string]string{"model_scope": "http://127.0.0.1:1/a.zip"},
						}},
					}},
				}},
			},
			{
				Name: "sd-cpp",
				Architectures: []catalog.RuntimeArchitecture{{
					OS: osName, Arch: archName,
					XPUs: []catalog.RuntimeXPU{{
						Vendor: "amd", Type: "gpu",
						Versions: []catalog.RuntimeVersion{{
							Version:   "1.0.0",
							Downloads: map[string]string{"model_scope": "http://127.0.0.1:1/s.zip"},
						}},
					}},
				}},
			},
		},
		Models: []catalog.Manifest{{
			Name: "zimage-vendor-test", Type: "image-generation", Version: "1.0.0",
			Architectures: []catalog.Architecture{{
				OS: osName, Arch: archName,
				XPUs: []catalog.XPU{{
					Vendor: "amd", Type: "gpu",
					InferenceEngines: []string{"sd-cpp"},
					Downloads: map[string][]catalog.Download{
						"model_scope": {{URL: "http://127.0.0.1:1/m.zip", FileName: "m.zip"}},
					},
				}},
			}},
			Parameters: map[string]any{"diffusion_model": "z.gguf"},
		}},
	}
}

// TestSupervisorVendorAutoDetect 覆盖：未显式传 Vendor 时，supervisor 应回退到
// 运行时探测（MEDIACRAFT_GPU_VENDOR=amd），否则 amd 上 zimage 会被误判
// "no supported local inference engine"。
func TestSupervisorVendorAutoDetect(t *testing.T) {
	t.Setenv("MEDIACRAFT_GPU_VENDOR", "amd")
	dir := t.TempDir()
	supervisor, err := NewSupervisor(vendorFallbackCatalog(), Options{
		RuntimesDir: dir + "/runtimes",
		ModelsDir:   dir + "/models",
		// Vendor 留空：必须自动探测为 amd
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	defer supervisor.StopHealthChecks()

	rts := supervisor.engineCandidatesForModel("zimage-vendor-test")
	if len(rts) != 1 || rts[0].Name() != runtime.SDCpp {
		t.Fatalf("expected [sd-cpp] candidate for amd zimage, got %v", rts)
	}
	// 引擎解析成功后，未安装模型应报 MODEL_NOT_INSTALLED，而不是“无引擎”
	_, err = supervisor.StartModel(context.Background(), "zimage-vendor-test")
	if err == nil {
		t.Fatal("expected error for uninstalled model")
	}
	if sme, ok := runtime.AsStartModelError(err); ok && sme.Code == runtime.CodeModelNotInstalled {
		return
	}
	t.Fatalf("expected MODEL_NOT_INSTALLED after engine resolution, got %v", err)
}
