package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRetryableDownloadError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "canceled", err: context.Canceled, want: false},
		{name: "not found", err: &ErrServerReturnedError{StatusCode: http.StatusNotFound}, want: false},
		{name: "unauthorized", err: &ErrServerReturnedError{StatusCode: http.StatusUnauthorized}, want: false},
		{name: "too many requests", err: &ErrServerReturnedError{StatusCode: http.StatusTooManyRequests}, want: true},
		{name: "server error", err: &ErrServerReturnedError{StatusCode: http.StatusBadGateway}, want: true},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: true},
		{name: "short body", err: io.ErrUnexpectedEOF, want: true},
		{name: "size mismatch", err: &ErrCheckFileSizeVerificationFailed{ExpectedSize: 10, ActualSize: 5}, want: true},
		{name: "checksum mismatch", err: &ErrChecksumVerificationFailed{ExpectedSHA: "a", ActualSHA: "b"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableDownloadError(context.Background(), tt.err)
			if got != tt.want {
				t.Fatalf("IsRetryableDownloadError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRetryableDownloadErrorAcceptsHTTP2StreamInternalError(t *testing.T) {
	err := errors.New("stream error: stream ID 851; INTERNAL_ERROR; received from peer")
	if !IsRetryableDownloadError(context.Background(), err) {
		t.Fatal("HTTP/2 stream internal errors should be retryable")
	}
}

func TestDownloadWithContextRetriesServerError(t *testing.T) {
	withFastRetry(t)

	var requests atomic.Int32
	content := []byte("download succeeded after retries")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Length", "32")
		_, _ = w.Write(content)
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := newTestHTTPDownloader(3)

	if err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
}

func TestDownloadWithContextRetriesInterruptedBody(t *testing.T) {
	withFastRetry(t)

	var requests atomic.Int32
	content := []byte("download succeeds after interrupted bodies")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "42")
			_, _ = w.Write(content[:8])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		if r.Header.Get("Range") == "bytes=8-" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "34")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 8-41/%d", len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[8:])
			return
		}
		w.Header().Set("Content-Length", "42")
		_, _ = w.Write(content)
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := newTestHTTPDownloader(3)

	if err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
}

func TestDownloadWithContextDoesNotRetryNotFound(t *testing.T) {
	withFastRetry(t)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := newTestHTTPDownloader(3)

	err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, "", 0)
	if err == nil {
		t.Fatal("DownloadWithContext() expected error")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestDownloadWithContextDeletesCorruptPartialOnChecksumRetry(t *testing.T) {
	withFastRetry(t)

	good := []byte("good model data")
	bad := []byte("bad model data")
	sum := sha256.Sum256(good)
	expectedSHA := hex.EncodeToString(sum[:])

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", "14")
			_, _ = w.Write(bad)
			return
		}
		w.Header().Set("Content-Length", "15")
		_, _ = w.Write(good)
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := newTestHTTPDownloader(1)

	if err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, expectedSHA, int64(len(good))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != string(good) {
		t.Fatalf("downloaded content = %q, want %q", got, good)
	}
	if _, err := os.Stat(destPath + ".downloading"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file should be removed or renamed, stat error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestDownloadWithContextReportsIncrementalSpeed(t *testing.T) {
	withFastRetry(t)

	// 使用大于 1MB 的内容，确保走 got 分片下载路径而非 downloadSimple
	const contentSize = 2 * 1024 * 1024 // 2MB
	content := make([]byte, contentSize)
	for i := range content {
		content[i] = byte(i % 256)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		w.Header().Set("Accept-Ranges", "bytes")

		if rangeHeader == "bytes=0-0" {
			w.Header().Set("Content-Length", "1")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[:1])
			return
		}

		var start, end int
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
			t.Fatalf("unexpected range header: %q", rangeHeader)
		}
		if start < 0 || end >= len(content) || start > end {
			t.Fatalf("invalid range header: %q", rangeHeader)
		}

		chunk := content[start : end+1]
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(chunk)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)

		mid := len(chunk) / 2
		_, _ = w.Write(chunk[:mid])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(1100 * time.Millisecond)
		_, _ = w.Write(chunk[mid:])
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: 1,
		MaxRetries:          0,
		Timeout:             5,
		ParallelThreshold:   1,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	var speeds []float64
	if err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, "", int64(len(content)), func(_ float64, speed float64) {
		if speed > 0 {
			speeds = append(speeds, speed)
		}
	}); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}

	if len(speeds) == 0 {
		t.Fatal("expected at least one non-zero speed sample")
	}
	for _, speed := range speeds {
		if speed >= float64(len(content)) {
			t.Fatalf("speed = %v, want incremental speed smaller than total size %d", speed, len(content))
		}
	}
}

func TestDownloadWithContextReportsProgressForSingleStream(t *testing.T) {
	withFastRetry(t)
	content := bytes.Repeat([]byte("sd-cpp-runtime"), 8*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		if r.Header.Get("Range") != "" {
			_, _ = w.Write(content)
			return
		}
		for offset := 0; offset < len(content); offset += 64 * 1024 {
			end := offset + 64*1024
			if end > len(content) {
				end = len(content)
			}
			_, _ = w.Write(content[offset:end])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(300 * time.Millisecond)
		}
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "runtime.zip")
	downloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: 1,
		MaxRetries:          0,
		Timeout:             10,
		ParallelThreshold:   int64(100 * 1024 * 1024),
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	var progresses []float64
	if err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, "", int64(len(content)), func(progress, _ float64) {
		progresses = append(progresses, progress)
	}); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}

	var hasIntermediate bool
	for _, progress := range progresses {
		if progress > 0 && progress < 1 {
			hasIntermediate = true
			break
		}
	}
	if !hasIntermediate {
		t.Fatalf("expected an intermediate progress callback, got %v", progresses)
	}
}

func TestDownloadWithContextAcceptsUppercaseChecksum(t *testing.T) {
	withFastRetry(t)

	content := []byte("model data with uppercase checksum")
	sum := sha256.Sum256(content)
	expectedSHA := strings.ToUpper(hex.EncodeToString(sum[:]))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := newTestHTTPDownloader(0)

	if err := downloader.DownloadWithContext(context.Background(), server.URL, destPath, expectedSHA, int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
}

func TestDownloadWithContextCancelDoesNotWaitForRetry(t *testing.T) {
	withFastRetry(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()

	destPath := filepath.Join(t.TempDir(), "model.bin")
	downloader := newTestHTTPDownloader(3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := downloader.DownloadWithContext(ctx, server.URL, destPath, "", 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DownloadWithContext() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("cancel took %s, want under 200ms", elapsed)
	}
}

func newTestHTTPDownloader(maxRetries int) *HTTPDownloader {
	return NewHTTPDownloader(Options{
		MaxRetries: maxRetries,
		Timeout:    2,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func withFastRetry(t *testing.T) {
	t.Helper()
	oldInitial := downloadRetryInitialInterval
	oldMax := downloadRetryMaxInterval
	oldRandomization := downloadRetryRandomizationFactor
	downloadRetryInitialInterval = time.Millisecond
	downloadRetryMaxInterval = time.Millisecond
	downloadRetryRandomizationFactor = 0
	t.Cleanup(func() {
		downloadRetryInitialInterval = oldInitial
		downloadRetryMaxInterval = oldMax
		downloadRetryRandomizationFactor = oldRandomization
	})
}

func TestVerifyChecksum(t *testing.T) {
	content := []byte("test file content for checksum verification")
	sum := sha256.Sum256(content)
	expectedSHA := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	downloader := newTestHTTPDownloader(0)

	// 正确的校验和
	if err := downloader.VerifyChecksum(path, expectedSHA); err != nil {
		t.Fatalf("VerifyChecksum() error = %v", err)
	}

	// 大小写不敏感
	if err := downloader.VerifyChecksum(path, strings.ToUpper(expectedSHA)); err != nil {
		t.Fatalf("VerifyChecksum() uppercase error = %v", err)
	}

	// 错误的校验和
	if err := downloader.VerifyChecksum(path, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("VerifyChecksum() expected error for wrong checksum")
	}

	// 不存在的文件
	if err := downloader.VerifyChecksum(filepath.Join(dir, "nonexistent"), expectedSHA); err == nil {
		t.Fatal("VerifyChecksum() expected error for nonexistent file")
	}
}

func TestStartDownloadsWithProgress(t *testing.T) {
	withFastRetry(t)

	content := []byte("shared file content for batch download")
	sum := sha256.Sum256(content)

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	dest1 := filepath.Join(dir, "file1.bin")
	dest2 := filepath.Join(dir, "file2.bin")

	downloader := NewHTTPDownloader(Options{
		MaxRetries:          1,
		Timeout:             5,
		ConcurrentDownloads: 2,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	tasks := []DownloadTask{
		{URL: server.URL, DestPath: dest1, ExpectedSHA256: hex.EncodeToString(sum[:]), FileSize: int64(len(content))},
		{URL: server.URL, DestPath: dest2, ExpectedSHA256: hex.EncodeToString(sum[:]), FileSize: int64(len(content))},
	}

	err := downloader.StartDownloadsWithProgress(tasks, func(progress float64, speed float64) {})
	if err != nil {
		t.Fatalf("StartDownloadsWithProgress() error = %v", err)
	}

	for _, dest := range []string{dest1, dest2} {
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read %s: %v", dest, err)
		}
		if string(got) != string(content) {
			t.Fatalf("%s content = %q, want %q", dest, got, content)
		}
	}

	if requestCount.Load() < 2 {
		t.Fatalf("expected at least 2 requests, got %d", requestCount.Load())
	}
}

func TestStartDownloadsWithProgressEmptyTasks(t *testing.T) {
	downloader := newTestHTTPDownloader(0)
	err := downloader.StartDownloadsWithProgress(nil, nil)
	if err == nil {
		t.Fatal("StartDownloadsWithProgress() expected error for empty tasks")
	}
}

func TestStartDownloadsWithProgressSkipsExistingFiles(t *testing.T) {
	withFastRetry(t)

	content := []byte("existing file")
	dir := t.TempDir()
	dest := filepath.Join(dir, "existing.bin")
	if err := os.WriteFile(dest, content, 0644); err != nil {
		t.Fatal(err)
	}

	downloader := newTestHTTPDownloader(0)
	tasks := []DownloadTask{
		{URL: "http://unused", DestPath: dest, FileSize: int64(len(content))},
	}

	var callbackInvoked atomic.Bool
	err := downloader.StartDownloadsWithProgress(tasks, func(progress float64, speed float64) {
		callbackInvoked.Store(true)
	})
	if err != nil {
		t.Fatalf("StartDownloadsWithProgress() error = %v", err)
	}

	// 文件内容未改变
	got, _ := os.ReadFile(dest)
	if string(got) != string(content) {
		t.Fatalf("file content changed, got %q, want %q", got, content)
	}
}
