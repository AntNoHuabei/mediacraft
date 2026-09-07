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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testChunkSize = 512 * 1024

func TestSegmentedDownloadRetriesOnlyIncompleteRange(t *testing.T) {
	withFastRetry(t)
	content := testContent(3*testChunkSize - 137)
	sum := sha256.Sum256(content)

	var mu sync.Mutex
	requests := make(map[int64]int)
	served := map[int64]chan struct{}{
		0:                 make(chan struct{}),
		2 * testChunkSize: make(chan struct{}),
	}
	var servedOnce sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"stable"`)
		if probe {
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[:1])
			return
		}

		mu.Lock()
		requests[start]++
		attempt := requests[start]
		mu.Unlock()
		if start == testChunkSize && attempt == 1 {
			<-served[0]
			<-served[2*testChunkSize]
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[start : start+(end-start+1)/2])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}

		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
		if ch, ok := served[start]; ok {
			onceValue, _ := servedOnce.LoadOrStore(start, &sync.Once{})
			onceValue.(*sync.Once).Do(func() { close(ch) })
		}
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 3, 2)
	var progressMu sync.Mutex
	var progresses []float64
	err := downloader.DownloadWithContext(context.Background(), server.URL, dest, hex.EncodeToString(sum[:]), int64(len(content)), func(progress, _ float64) {
		progressMu.Lock()
		progresses = append(progresses, progress)
		progressMu.Unlock()
	})
	if err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)

	mu.Lock()
	defer mu.Unlock()
	if requests[0] != 1 || requests[2*testChunkSize] != 1 {
		t.Fatalf("completed ranges were requested again: %#v", requests)
	}
	if requests[testChunkSize] != 2 {
		t.Fatalf("interrupted range requests = %d, want 2", requests[testChunkSize])
	}
	assertMonotonicProgress(t, progresses)
}

func TestSegmentedDownloadAcceptsOKWithContentRange(t *testing.T) {
	content := testContent(testChunkSize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusOK)
		if probe {
			_, _ = w.Write(content[:1])
			return
		}
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	client := server.Client()
	remote, err := probeRemoteFile(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("probeRemoteFile() error = %v", err)
	}
	if !remote.rangeable || remote.size != int64(len(content)) {
		t.Fatalf("remote file = %#v, want rangeable size %d", remote, len(content))
	}

	file, err := os.CreateTemp(t.TempDir(), "range-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(int64(len(content))); err != nil {
		t.Fatal(err)
	}
	var received atomic.Int64
	rng := byteRange{start: 0, end: int64(len(content) - 1)}
	downloader := &HTTPDownloader{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := downloader.downloadRange(context.Background(), client, server.URL, file, rng, remote, "", &received); err != nil {
		t.Fatalf("downloadRange() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("downloaded content does not match")
	}
}

func TestLargeDownloadFallsBackToSingleStreamAfterFullOKRangeResponse(t *testing.T) {
	content := testContent(2 * testChunkSize)
	var singleStreamRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			singleStreamRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = w.Write(content)
			return
		}
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		w.Header().Set("ETag", `"stable"`)
		if probe {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[:1])
			return
		}
		// Simulate a CDN returning the full object after an If-Range mismatch.
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 1, 0)
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	if singleStreamRequests.Load() != 1 {
		t.Fatalf("single stream requests = %d, want 1", singleStreamRequests.Load())
	}
}

func TestSegmentedDownloadWithChecksumSkipsIfRange(t *testing.T) {
	content := testContent(2 * testChunkSize)
	sum := sha256.Sum256(content)
	var ifRangeRequests atomic.Int64
	var singleStreamRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			singleStreamRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = w.Write(content)
			return
		}
		if r.Header.Get("If-Range") != "" {
			ifRangeRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.Header().Set("ETag", `"cdn-full-object"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"probe-etag"`)
		w.WriteHeader(http.StatusPartialContent)
		if probe {
			_, _ = w.Write(content[:1])
			return
		}
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 2, 0)
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, hex.EncodeToString(sum[:]), int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	if ifRangeRequests.Load() != 0 {
		t.Fatalf("If-Range requests = %d, want 0 when checksum is known", ifRangeRequests.Load())
	}
	if singleStreamRequests.Load() != 0 {
		t.Fatalf("single stream requests = %d, want 0", singleStreamRequests.Load())
	}
}

func TestSegmentedDownloadRetriesOuterAttemptAfterWorkersAbort(t *testing.T) {
	withFastRetry(t)
	content := testContent(3 * testChunkSize)
	var mu sync.Mutex
	probeCount := 0
	rangeRequests := 0
	const failedRangeRequests = 6 // each of three workers exhausts one retry
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"outer-retry"`)
		if probe {
			mu.Lock()
			probeCount++
			mu.Unlock()
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[:1])
			return
		}

		mu.Lock()
		rangeRequests++
		shouldFail := rangeRequests <= failedRangeRequests
		mu.Unlock()
		if shouldFail {
			w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[start : start+1])
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 3, 1)
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)

	mu.Lock()
	defer mu.Unlock()
	if probeCount < 2 {
		t.Fatalf("range probes = %d, want an outer retry", probeCount)
	}
}

func TestSegmentedDownloadResumesAcrossDownloaderInstances(t *testing.T) {
	content := testContent(3 * testChunkSize)
	sum := sha256.Sum256(content)
	cacheDir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "model.bin")

	var mu sync.Mutex
	requests := make(map[int64]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, _ := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"resume"`)
		if start != 0 || end != 0 {
			mu.Lock()
			requests[start]++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	first := newSegmentedTestDownloader(cacheDir, 1, 0)
	err := first.DownloadWithContext(ctx, server.URL, dest, hex.EncodeToString(sum[:]), int64(len(content)), func(progress, _ float64) {
		if progress >= 1.0/3.0 && progress < 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first DownloadWithContext() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dest + ".downloading"); err != nil {
		t.Fatalf("partial download was not preserved: %v", err)
	}

	second := newSegmentedTestDownloader(cacheDir, 2, 0)
	var progresses []float64
	err = second.DownloadWithContext(context.Background(), server.URL, dest, hex.EncodeToString(sum[:]), int64(len(content)), func(progress, _ float64) {
		progresses = append(progresses, progress)
	})
	if err != nil {
		t.Fatalf("resumed DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	mu.Lock()
	defer mu.Unlock()
	if requests[0] != 1 {
		t.Fatalf("completed first range requests = %d, want 1", requests[0])
	}
	if len(progresses) == 0 || progresses[0] < 1.0/3.0 {
		t.Fatalf("resumed initial progress = %v, want at least 1/3", progresses)
	}
	assertMonotonicProgress(t, progresses)
}

func TestSegmentedDownloadInvalidatesCheckpointWhenETagChanges(t *testing.T) {
	contentV1 := testContent(3 * testChunkSize)
	contentV2 := append([]byte(nil), contentV1...)
	for i := range contentV2 {
		contentV2[i] ^= 0xff
	}
	cacheDir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "model.bin")

	var mu sync.Mutex
	version := 1
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		currentVersion := version
		mu.Unlock()
		content := contentV1
		etag := `"v1"`
		if currentVersion == 2 {
			content = contentV2
			etag = `"v2"`
		}
		start, end, _ := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), etag)
		if start != 0 || end != 0 {
			mu.Lock()
			requests[fmt.Sprintf("v%d:%d", currentVersion, start)]++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	first := newSegmentedTestDownloader(cacheDir, 1, 0)
	err := first.DownloadWithContext(ctx, server.URL, dest, "", int64(len(contentV1)), func(progress, _ float64) {
		if progress >= 1.0/3.0 && progress < 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first DownloadWithContext() error = %v, want context.Canceled", err)
	}
	mu.Lock()
	version = 2
	mu.Unlock()

	second := newSegmentedTestDownloader(cacheDir, 2, 0)
	if err := second.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(contentV2))); err != nil {
		t.Fatalf("DownloadWithContext() after ETag change error = %v", err)
	}
	assertFileContent(t, dest, contentV2)
	mu.Lock()
	defer mu.Unlock()
	if requests["v2:0"] != 1 {
		t.Fatalf("first range was not redownloaded after ETag change: %#v", requests)
	}
}

func TestSegmentedDownloadKeepsChecksumCheckpointWhenETagChanges(t *testing.T) {
	content := testContent(3 * testChunkSize)
	sum := sha256.Sum256(content)
	cacheDir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "model.bin")

	var mu sync.Mutex
	etag := `"v1"`
	requests := make(map[int64]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		currentETag := etag
		mu.Unlock()
		start, end, _ := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), currentETag)
		if start != 0 || end != 0 {
			mu.Lock()
			requests[start]++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	first := newSegmentedTestDownloader(cacheDir, 1, 0)
	err := first.DownloadWithContext(ctx, server.URL, dest, hex.EncodeToString(sum[:]), int64(len(content)), func(progress, _ float64) {
		if progress >= 1.0/3.0 && progress < 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first DownloadWithContext() error = %v, want context.Canceled", err)
	}

	mu.Lock()
	etag = `"v2"`
	mu.Unlock()

	second := newSegmentedTestDownloader(cacheDir, 2, 0)
	if err := second.DownloadWithContext(context.Background(), server.URL, dest, hex.EncodeToString(sum[:]), int64(len(content))); err != nil {
		t.Fatalf("resumed DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	mu.Lock()
	defer mu.Unlock()
	if requests[0] != 1 {
		t.Fatalf("completed first range requests = %d, want 1 despite ETag change", requests[0])
	}
}

func TestSegmentedDownloadRestartsWhenObjectChangesMidTransfer(t *testing.T) {
	withFastRetry(t)
	contentV2 := testContent(2*testChunkSize + 31)
	for i := range contentV2 {
		contentV2[i] ^= 0x5a
	}
	var mu sync.Mutex
	version := 1
	probes := 0
	singleStreamRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			mu.Lock()
			singleStreamRequests++
			mu.Unlock()
			w.Header().Set("Content-Length", fmt.Sprint(len(contentV2)))
			w.Header().Set("ETag", `"v2"`)
			_, _ = w.Write(contentV2)
			return
		}
		start, end, probe := testRequestedRange(t, r, int64(len(contentV2)))
		mu.Lock()
		if probe {
			probes++
		}
		currentVersion := version
		if !probe && r.Header.Get("If-Range") == `"v1"` {
			version = 2
			currentVersion = 2
		}
		mu.Unlock()
		if !probe && r.Header.Get("If-Range") == `"v1"` {
			w.Header().Set("Content-Length", fmt.Sprint(len(contentV2)))
			w.Header().Set("ETag", `"v2"`)
			w.WriteHeader(http.StatusOK)
			return
		}
		etag := `"v1"`
		if currentVersion == 2 {
			etag = `"v2"`
		}
		writeRangeHeaders(w, start, end, int64(len(contentV2)), etag)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(contentV2[start : end+1])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 2, 1)
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(contentV2))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, contentV2)
	mu.Lock()
	defer mu.Unlock()
	if probes != 1 {
		t.Fatalf("range probes = %d, want 1 before single stream fallback", probes)
	}
	if singleStreamRequests != 1 {
		t.Fatalf("single stream requests = %d, want 1 after object change", singleStreamRequests)
	}
}

func TestCancelDownloadWaitsAndDiscardsCheckpoint(t *testing.T) {
	content := testContent(2 * testChunkSize)
	cacheDir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "model.bin")
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"cancel"`)
		w.WriteHeader(http.StatusPartialContent)
		if probe {
			_, _ = w.Write(content[:1])
			return
		}
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer server.Close()

	downloader := newSegmentedTestDownloader(cacheDir, 2, 0)
	done := make(chan error, 1)
	go func() {
		done <- downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content)))
	}()
	<-started
	id := downloader.GenerateDownloadID(server.URL, dest)
	if err := downloader.CancelDownload(id); err != nil {
		t.Fatalf("CancelDownload() error = %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("DownloadWithContext() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dest + ".downloading"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file still exists after explicit cancel: %v", err)
	}
	if _, err := os.Stat(downloader.getCachePath(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint still exists after explicit cancel: %v", err)
	}
}

func TestLargeDownloadFallsBackWhenRangeIsUnsupported(t *testing.T) {
	content := testContent(2 * testChunkSize)
	var requests int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 2, 0)
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("requests = %d, want range probe plus fallback GET", requests)
	}
}

func TestDownloadBelowParallelThresholdUsesSingleGET(t *testing.T) {
	content := testContent(2 * testChunkSize)
	var mu sync.Mutex
	requests := 0
	var rangeHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		rangeHeader = r.Header.Get("Range")
		mu.Unlock()
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := NewHTTPDownloader(Options{
		ConcurrentDownloads: 3,
		ChunkSize:           testChunkSize,
		ParallelThreshold:   int64(len(content)) + 1,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("requests = %d, want one GET below parallel threshold", requests)
	}
	if rangeHeader != "" {
		t.Fatalf("Range header = %q, want empty below parallel threshold", rangeHeader)
	}
}

func TestBatchProgressRemainsMonotonicDuringRangeRetry(t *testing.T) {
	withFastRetry(t)
	content := testContent(3 * testChunkSize)
	var mu sync.Mutex
	failed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"batch"`)
		w.WriteHeader(http.StatusPartialContent)
		if probe {
			_, _ = w.Write(content[:1])
			return
		}
		mu.Lock()
		interrupt := start == testChunkSize && !failed
		if interrupt {
			failed = true
		}
		mu.Unlock()
		if interrupt {
			_, _ = w.Write(content[start : start+(end-start+1)/2])
			return
		}
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 3, 2)
	var progresses []float64
	err := downloader.StartDownloadsWithProgress([]DownloadTask{{
		URL: server.URL, DestPath: dest, FileSize: int64(len(content)),
	}}, func(progress, _ float64) {
		progresses = append(progresses, progress)
	})
	if err != nil {
		t.Fatalf("StartDownloadsWithProgress() error = %v", err)
	}
	if len(progresses) == 0 {
		t.Fatal("no batch progress callbacks")
	}
	for i := 1; i < len(progresses); i++ {
		if progresses[i] < progresses[i-1] {
			t.Fatalf("batch progress decreased at %d: %v", i, progresses)
		}
	}
	if progresses[len(progresses)-1] != 100 {
		t.Fatalf("final batch progress = %v, want 100", progresses[len(progresses)-1])
	}
}

func TestSegmentedDownloadRejectsInvalidContentRange(t *testing.T) {
	withFastRetry(t)
	content := testContent(2 * testChunkSize)
	var invalidRequests int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		if probe {
			writeRangeHeaders(w, start, end, int64(len(content)), `"invalid"`)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[:1])
			return
		}
		mu.Lock()
		invalidRequests++
		mu.Unlock()
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start+1, end, len(content)))
		w.Header().Set("ETag", `"invalid"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.bin")
	downloader := newSegmentedTestDownloader(t.TempDir(), 1, 1)
	err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content)))
	if err == nil || !strings.Contains(err.Error(), "range retries exhausted") {
		t.Fatalf("DownloadWithContext() error = %v, want exhausted range validation error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if invalidRequests != 1 {
		t.Fatalf("invalid range requests = %d, want 1", invalidRequests)
	}
	if _, err := os.Stat(dest + ".downloading"); err != nil {
		t.Fatalf("partial file should be retained after retry exhaustion: %v", err)
	}
}

func TestSegmentedDownloadDiscardsCorruptCheckpoint(t *testing.T) {
	content := testContent(2*testChunkSize + 73)
	dest := filepath.Join(t.TempDir(), "model.bin")
	cacheDir := t.TempDir()
	downloader := newSegmentedTestDownloader(cacheDir, 2, 0)

	var mu sync.Mutex
	requests := make(map[int64]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, probe := testRequestedRange(t, r, int64(len(content)))
		writeRangeHeaders(w, start, end, int64(len(content)), `"corrupt-cache"`)
		if !probe {
			mu.Lock()
			requests[start]++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()

	if err := os.WriteFile(dest+".downloading", make([]byte, len(content)), 0644); err != nil {
		t.Fatal(err)
	}
	id := downloader.GenerateDownloadID(server.URL, dest)
	if err := os.WriteFile(downloader.getCachePath(id), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := downloader.DownloadWithContext(context.Background(), server.URL, dest, "", int64(len(content))); err != nil {
		t.Fatalf("DownloadWithContext() error = %v", err)
	}
	assertFileContent(t, dest, content)
	mu.Lock()
	defer mu.Unlock()
	for _, start := range []int64{0, testChunkSize, 2 * testChunkSize} {
		if requests[start] != 1 {
			t.Fatalf("range %d requests = %d, want 1 after corrupt checkpoint reset", start, requests[start])
		}
	}
}

func newSegmentedTestDownloader(cacheDir string, concurrency, retries int) *HTTPDownloader {
	return NewHTTPDownloader(Options{
		ConcurrentDownloads: concurrency,
		MaxRetries:          retries,
		Timeout:             5,
		ChunkSize:           testChunkSize,
		ParallelThreshold:   1024 * 1024,
		CacheDir:            cacheDir,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func testContent(size int) []byte {
	content := make([]byte, size)
	for i := range content {
		content[i] = byte((i*31 + 17) % 251)
	}
	return content
}

func testRequestedRange(t *testing.T, r *http.Request, total int64) (start, end int64, probe bool) {
	t.Helper()
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
		t.Fatalf("invalid Range header %q: %v", r.Header.Get("Range"), err)
	}
	if start < 0 || end < start || end >= total {
		t.Fatalf("out-of-bounds Range header %q", r.Header.Get("Range"))
	}
	return start, end, start == 0 && end == 0
}

func writeRangeHeaders(w http.ResponseWriter, start, end, total int64, etag string) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	w.Header().Set("ETag", etag)
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(got) != len(want) || !equalBytes(got, want) {
		t.Fatalf("file content mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertMonotonicProgress(t *testing.T, progresses []float64) {
	t.Helper()
	if len(progresses) == 0 {
		t.Fatal("no progress callbacks")
	}
	for i := 1; i < len(progresses); i++ {
		if progresses[i] < progresses[i-1] {
			t.Fatalf("progress decreased at %d: %v", i, progresses)
		}
	}
	if progresses[len(progresses)-1] != 1 {
		t.Fatalf("final progress = %v, want 1", progresses[len(progresses)-1])
	}
}
