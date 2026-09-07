package downloader

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Run with HERDSMAN_QWEN36_DIAGNOSTIC=1 to inspect the first Range request.
// It intentionally cancels after the first committed chunk and never downloads the model.
func TestDiagnosticQwen36Download(t *testing.T) {
	if os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC") != "1" {
		t.Skip("set HERDSMAN_QWEN36_DIAGNOSTIC=1 to run the live ModelScope diagnostic")
	}

	const url = "https://modelscope.cn/models/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/master/Qwen3.6-35B-A3B-UD-Q4_K_M.gguf"
	fullDownload := os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC_FULL") == "1"
	root := t.TempDir()
	if configuredRoot := os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC_DEST"); fullDownload && configuredRoot != "" {
		root = configuredRoot
		if err := os.MkdirAll(root, 0755); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	concurrency := 1
	if value, parseErr := strconv.Atoi(os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC_CONCURRENCY")); parseErr == nil && value > 0 {
		concurrency = value
	}
	downloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: concurrency,
		MaxRetries:          1,
		Timeout:             120,
		ChunkSize:           16 * 1024 * 1024,
		ParallelThreshold:   1,
		CacheDir:            filepath.Join(root, "cache"),
		Logger:              logger,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dest := filepath.Join(root, "Qwen3.6-35B-A3B-UD-Q4_K_M.gguf")
	progressSeen := false
	targetChunks := 1
	if value, parseErr := strconv.Atoi(os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC_CHUNKS")); parseErr == nil && value > 0 {
		targetChunks = value
	}
	committedChunks := 0
	lastProgress := float64(0)
	err := downloader.DownloadWithContext(ctx, url, dest, "", 0, func(progress, speed float64) {
		logger.Info("diagnostic progress", "progress", progress, "speedBytesPerSecond", speed)
		if fullDownload {
			return
		}
		advanced := progress > lastProgress
		if progress > lastProgress {
			lastProgress = progress
			if progress > 0 {
				progressSeen = true
			}
		}
		if advanced && progressSeen {
			committedChunks++
			if committedChunks >= targetChunks {
				cancel()
			}
		}
	})
	t.Logf("diagnostic result: error=%v partial=%s", err, dest+".downloading")
	if info, statErr := os.Stat(dest + ".downloading"); statErr == nil {
		t.Logf("diagnostic partial size: %d bytes", info.Size())
	}
	if info, statErr := os.Stat(dest); statErr == nil {
		t.Logf("diagnostic final size: %d bytes", info.Size())
	}
	if fullDownload && err != nil {
		t.Fatalf("full diagnostic download failed: %v", err)
	}
	if err == nil {
		t.Log("diagnostic completed unexpectedly; remove the temporary file manually if needed")
	}
}

func TestDiagnosticQwen36Sources(t *testing.T) {
	if os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC") != "1" {
		t.Skip("set HERDSMAN_QWEN36_DIAGNOSTIC=1 to run the live ModelScope diagnostic")
	}
	sources := []struct {
		name string
		url  string
	}{
		{name: "main", url: "https://modelscope.cn/models/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/master/Qwen3.6-35B-A3B-UD-Q4_K_M.gguf"},
		{name: "mmproj", url: "https://modelscope.cn/models/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/master/mmproj-BF16.gguf"},
		{name: "dflash", url: "https://modelscope.cn/models/AI-ModelScope/Qwen3.6-35B-A3B-DFlash-GGUF-Test/resolve/master/Qwen3.6-35B-A3B-DFlash-q8_0.gguf"},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for _, source := range sources {
		source := source
		t.Run(source.name, func(t *testing.T) {
			client := newHTTPClient(120)
			remote, err := probeRemoteFile(context.Background(), client, source.url)
			if err != nil {
				t.Logf("source probe failed: url=%s error=%v", source.url, err)
				return
			}
			logger.Info("diagnostic source probe", "name", source.name, "url", source.url, "size", remote.size, "rangeable", remote.rangeable, "etag", remote.etag, "lastModified", remote.lastModified)
			if !remote.rangeable || remote.size <= 0 {
				return
			}
			file, err := os.CreateTemp(t.TempDir(), "first-range-*")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			rangeEnd := int64(16*1024*1024 - 1)
			if rangeEnd >= remote.size {
				rangeEnd = remote.size - 1
			}
			var received atomic.Int64
			err = (&HTTPDownloader{logger: logger}).downloadRange(context.Background(), client, source.url, file, byteRange{start: 0, end: rangeEnd}, remote, "", &received)
			logger.Info("diagnostic first range result", "name", source.name, "start", 0, "end", rangeEnd, "received", received.Load(), "error", err)
		})
	}
}

func TestDiagnosticQwen36BatchDownload(t *testing.T) {
	if os.Getenv("HERDSMAN_QWEN36_DIAGNOSTIC") != "1" {
		t.Skip("set HERDSMAN_QWEN36_DIAGNOSTIC=1 to run the live ModelScope diagnostic")
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	root := t.TempDir()
	downloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: 3,
		MaxRetries:          1,
		Timeout:             120,
		ChunkSize:           16 * 1024 * 1024,
		ParallelThreshold:   100 * 1024 * 1024,
		CacheDir:            filepath.Join(root, "cache"),
		Logger:              logger,
	})
	tasks := []DownloadTask{
		{URL: "https://modelscope.cn/models/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/master/Qwen3.6-35B-A3B-UD-Q4_K_M.gguf", DestPath: filepath.Join(root, "main.gguf"), FileSize: 22134528992},
		{URL: "https://modelscope.cn/models/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/master/mmproj-BF16.gguf", DestPath: filepath.Join(root, "mmproj.gguf"), FileSize: 902822624},
	}
	timer := time.AfterFunc(8*time.Second, func() {
		logger.Warn("diagnostic batch timeout; cancelling all downloads")
		if err := downloader.CancelAllDownloads(); err != nil {
			logger.Error("diagnostic batch cancel failed", "error", err)
		}
	})
	err := downloader.StartDownloadsWithProgress(tasks, func(progress, speed float64) {
		logger.Info("diagnostic batch progress", "progress", progress, "speedBytesPerSecond", speed)
	})
	timer.Stop()
	t.Logf("diagnostic batch result: error=%v", err)
}

func TestDiagnosticQwen38Download(t *testing.T) {
	if os.Getenv("HERDSMAN_QWEN38_DIAGNOSTIC") != "1" {
		t.Skip("set HERDSMAN_QWEN38_DIAGNOSTIC=1 to run the live Qwen3.8 diagnostic")
	}

	const (
		modelScopeURL  = "https://modelscope.cn/models/unsloth/Qwen3.8-27B-GGUF/resolve/master/Qwen3.8-27B-UD-Q4_K_M.gguf"
		huggingFaceURL = "https://huggingface.co/unsloth/Qwen3.8-27B-GGUF/resolve/main/Qwen3.8-27B-UD-Q4_K_M.gguf"
		fileName       = "Qwen3.8-27B-UD-Q4_K_M.gguf"
		fileSize       = int64(16464440224)
		fileSHA256     = "322e194ff79741c7baa497c240f677f54b201b0efab44ca8e50f122b39123482"
	)

	url := modelScopeURL
	if strings.EqualFold(os.Getenv("HERDSMAN_QWEN38_DIAGNOSTIC_SOURCE"), "hugging_face") {
		url = huggingFaceURL
	}
	fullDownload := os.Getenv("HERDSMAN_QWEN38_DIAGNOSTIC_FULL") == "1"
	root := t.TempDir()
	if configuredRoot := os.Getenv("HERDSMAN_QWEN38_DIAGNOSTIC_DEST"); configuredRoot != "" {
		root = configuredRoot
		if err := os.MkdirAll(root, 0755); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	concurrency := 3
	if value, parseErr := strconv.Atoi(os.Getenv("HERDSMAN_QWEN38_DIAGNOSTIC_CONCURRENCY")); parseErr == nil && value > 0 {
		concurrency = value
	}
	targetChunks := 6
	if value, parseErr := strconv.Atoi(os.Getenv("HERDSMAN_QWEN38_DIAGNOSTIC_CHUNKS")); parseErr == nil && value > 0 {
		targetChunks = value
	}

	downloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: concurrency,
		MaxRetries:          2,
		Timeout:             180,
		ChunkSize:           16 * 1024 * 1024,
		ParallelThreshold:   1,
		CacheDir:            filepath.Join(root, "cache"),
		Logger:              logger,
	})

	dest := filepath.Join(root, fileName)
	run := func(label string, dl *HTTPDownloader, chunks int) (error, float64, float64) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		lastProgress := float64(-1)
		firstProgress := float64(-1)
		committedChunks := 0
		err := dl.DownloadWithContext(ctx, url, dest, fileSHA256, fileSize, func(progress, speed float64) {
			logger.Info("qwen3.8 diagnostic progress", "phase", label, "progress", progress, "speedBytesPerSecond", speed)
			if firstProgress < 0 {
				firstProgress = progress
			}
			if fullDownload {
				return
			}
			if progress > lastProgress {
				lastProgress = progress
				committedChunks++
				if committedChunks >= chunks {
					cancel()
				}
			}
		})
		return err, firstProgress, lastProgress
	}

	err, firstProgress, lastProgress := run("initial", downloader, targetChunks)
	t.Logf("qwen3.8 diagnostic result: error=%v partial=%s", err, dest+".downloading")
	if info, statErr := os.Stat(dest + ".downloading"); statErr == nil {
		t.Logf("qwen3.8 diagnostic partial size: %d bytes", info.Size())
	}
	if info, statErr := os.Stat(dest); statErr == nil {
		t.Logf("qwen3.8 diagnostic final size: %d bytes", info.Size())
	}
	if fullDownload && err != nil {
		t.Fatalf("full qwen3.8 diagnostic download failed: %v", err)
	}
	if !fullDownload && !errors.Is(err, context.Canceled) {
		t.Fatalf("partial qwen3.8 diagnostic error = %v, want context.Canceled", err)
	}
	if fullDownload {
		return
	}
	if firstProgress != 0 {
		t.Fatalf("initial qwen3.8 diagnostic first progress = %v, want 0", firstProgress)
	}
	resumeDownloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: concurrency,
		MaxRetries:          2,
		Timeout:             180,
		ChunkSize:           16 * 1024 * 1024,
		ParallelThreshold:   1,
		CacheDir:            filepath.Join(root, "cache"),
		Logger:              logger,
	})
	resumeErr, resumeFirstProgress, resumeLastProgress := run("resume", resumeDownloader, 2)
	t.Logf("qwen3.8 diagnostic resume result: error=%v firstProgress=%v lastProgress=%v previousLastProgress=%v", resumeErr, resumeFirstProgress, resumeLastProgress, lastProgress)
	if !errors.Is(resumeErr, context.Canceled) {
		t.Fatalf("resume qwen3.8 diagnostic error = %v, want context.Canceled", resumeErr)
	}
	if resumeFirstProgress <= 0 || resumeFirstProgress+0.000000001 < lastProgress {
		t.Fatalf("resume qwen3.8 diagnostic first progress = %v, want at least previous progress %v", resumeFirstProgress, lastProgress)
	}
}
