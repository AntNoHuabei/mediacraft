package runtime

import (
	"context"
	goruntime "runtime"
	"testing"

	"github.com/AntNoHuabei/mediacraft/catalog"
	"github.com/AntNoHuabei/mediacraft/pkg/downloader"
)

func tinyDisk(path string) (int64, bool, error) {
	return 256 << 20, true, nil // 256MB
}

func TestDiskSafetyMargin(t *testing.T) {
	if got := diskSafetyMargin(0); got < (512 << 20) {
		t.Fatalf("floor margin should be >= 512MB, got %d", got)
	}
	if got := diskSafetyMargin(1 << 30); got != 512<<20 { // 1GB → 256MB < floor，落到 512MB 下限
		t.Fatalf("1GB margin should fall to floor(512MB), got %d", got)
	}
	if got := diskSafetyMargin(8 << 30); got != 2<<30 { // 8GB → 2GB
		t.Fatalf("8GB margin should be 2GB, got %d", got)
	}
}

func TestInstallRuntimeRejectsInsufficientDisk(t *testing.T) {
	dir := t.TempDir()
	osName, archName := goruntime.GOOS, goruntime.GOARCH
	manifest := catalog.Runtime{
		Name: "audio.cpp",
		Architectures: []catalog.RuntimeArchitecture{{
			OS: osName, Arch: archName,
			XPUs: []catalog.RuntimeXPU{{
				Vendor: "all",
				Versions: []catalog.RuntimeVersion{{
					Version:   "1.0.0",
					Downloads: map[string]string{"model_scope": "http://127.0.0.1:1/x.zip"},
					FileSize:  4 << 30,
				}},
			}},
		}},
	}
	base := NewBaseRuntime(AudioCpp, manifest, nil, BaseOptions{
		RuntimesDir: dir + "/runtimes",
		ModelsDir:   dir + "/models",
		Downloader:  downloader.NewHTTPDownloader(downloader.Options{}),
		Vendor:      VendorCPU,
	})
	base.diskFree = tinyDisk
	err := base.Install(context.Background(), nil)
	if err == nil {
		t.Fatal("expected INSUFFICIENT_DISK_SPACE error before download")
	}
	ie, ok := AsInstallError(err)
	if !ok || ie.Code != CodeInsufficientDiskSpace {
		t.Fatalf("expected install[INSUFFICIENT_DISK_SPACE], got %v", err)
	}
}

func TestInstallModelRejectsInsufficientDisk(t *testing.T) {
	dir := t.TempDir()
	osName, archName := goruntime.GOOS, goruntime.GOARCH
	model := catalog.Manifest{
		Name: "asr-disk", Type: "asr", Version: "1.0.0",
		Architectures: []catalog.Architecture{{
			OS: osName, Arch: archName,
			XPUs: []catalog.XPU{{
				Vendor: "all", Type: "cpu",
				InferenceEngines: []string{"audio.cpp"},
				Downloads: map[string][]catalog.Download{
					"model_scope": {{URL: "http://127.0.0.1:1/m.zip", FileName: "m.zip", FileSize: 2 << 30}},
				},
			}},
		}},
	}
	base := NewBaseRuntime(AudioCpp, catalog.Runtime{}, []catalog.Manifest{model}, BaseOptions{
		RuntimesDir: dir + "/runtimes",
		ModelsDir:   dir + "/models",
		Downloader:  downloader.NewHTTPDownloader(downloader.Options{}),
		Vendor:      VendorCPU,
	})
	base.diskFree = tinyDisk
	err := base.InstallModel(context.Background(), model, nil)
	if err == nil {
		t.Fatal("expected INSUFFICIENT_DISK_SPACE error before model download")
	}
	ie, ok := AsInstallError(err)
	if !ok || ie.Code != CodeInsufficientDiskSpace {
		t.Fatalf("expected install[INSUFFICIENT_DISK_SPACE], got %v", err)
	}
}
