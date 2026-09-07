package downloader

import (
	"testing"
)

func TestReconfigureUpdatesFutureDownloadSnapshots(t *testing.T) {
	dl := NewHTTPDownloader(Options{ConcurrentDownloads: 2, MaxRetries: 1, Timeout: 30, ChunkSize: 4, ParallelThreshold: 16})
	old := dl.runtimeOptions()

	dl.Reconfigure(Options{ConcurrentDownloads: 7, MaxRetries: 5, Timeout: 90, ChunkSize: 8, ParallelThreshold: 32})
	next := dl.runtimeOptions()

	if old.maxConcurrent != 2 || old.maxRetries != 1 || old.timeout != 30 || old.chunkSize != 4 || old.parallelThreshold != 16 {
		t.Fatalf("old snapshot changed: %#v", old)
	}
	if next.maxConcurrent != 7 || next.maxRetries != 5 || next.timeout != 90 || next.chunkSize != 8 || next.parallelThreshold != 32 {
		t.Fatalf("new snapshot = %#v", next)
	}
	if old.client == next.client {
		t.Fatal("expected reconfigure to replace the client for future tasks")
	}
}

func TestParallelThresholdDefaultsTo100MiB(t *testing.T) {
	dl := NewHTTPDownloader(Options{})
	if got := dl.runtimeOptions().parallelThreshold; got != 100*1024*1024 {
		t.Fatalf("parallel threshold = %d, want 100 MiB", got)
	}
}
