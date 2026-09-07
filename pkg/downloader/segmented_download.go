package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AntNoHuabei/mediacraft/pkg/types"
)

const (
	downloadCacheVersion     = 1
	defaultChunkSize         = int64(5 * 1024 * 1024)
	defaultParallelThreshold = int64(100 * 1024 * 1024)
)

type remoteFileInfo struct {
	size         int64
	rangeable    bool
	etag         string
	lastModified string
}

type byteRange struct {
	start int64
	end   int64
}

type chunkRetriesExhaustedError struct {
	err       error
	retryable bool
}

func (e *chunkRetriesExhaustedError) Error() string {
	return fmt.Sprintf("range retries exhausted: %v", e.err)
}

func (e *chunkRetriesExhaustedError) Unwrap() error {
	return e.err
}

type remoteObjectChangedError struct {
	statusCode int
}

func (e *remoteObjectChangedError) Error() string {
	return fmt.Sprintf("remote object changed during range download: status %d", e.statusCode)
}

type restartDownloadError struct {
	err error
}

func (e *restartDownloadError) Error() string {
	return e.err.Error()
}

func (e *restartDownloadError) Unwrap() error {
	return e.err
}

type checkpointState struct {
	mu    sync.Mutex
	id    string
	cache *DownloadCache
	hd    *HTTPDownloader
}

func probeRemoteFile(ctx context.Context, client *http.Client, url string) (remoteFileInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return remoteFileInfo{}, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return remoteFileInfo{}, err
	}
	defer resp.Body.Close()

	info := remoteFileInfo{
		etag:         resp.Header.Get("ETag"),
		lastModified: resp.Header.Get("Last-Modified"),
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil || start != 0 || end != 0 || resp.ContentLength != 1 {
			return remoteFileInfo{}, fmt.Errorf("range request returned invalid probe response: content-range=%q content-length=%d", resp.Header.Get("Content-Range"), resp.ContentLength)
		}
		info.size = total
		info.rangeable = true
		return info, nil
	case http.StatusOK:
		start, end, total, rangeErr := parseContentRange(resp.Header.Get("Content-Range"))
		if rangeErr == nil && start == 0 && resp.ContentLength == end-start+1 {
			info.size = total
			info.rangeable = true
			return info, nil
		}
		info.size = resp.ContentLength
		return info, nil
	default:
		return remoteFileInfo{}, &ErrServerReturnedError{URL: url, StatusCode: resp.StatusCode}
	}
}

func parseContentRange(value string) (start, end, total int64, err error) {
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes "), "/")
	if len(parts) != 2 || parts[1] == "*" {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	bounds := strings.Split(parts[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	start, err = strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	end, err = strconv.ParseInt(bounds[1], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	total, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	return start, end, total, nil
}

func (hd *HTTPDownloader) downloadSegmented(ctx context.Context, runtime runtimeOptions, downloadID, url, destPath, partialPath, expectedSHA256 string, remote remoteFileInfo, startTime time.Time) error {
	chunkSize := int64(runtime.chunkSize)
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	chunkCount := int((remote.size + chunkSize - 1) / chunkSize)
	checkpoint, file, completedBytes, err := hd.prepareCheckpoint(downloadID, url, destPath, partialPath, expectedSHA256, remote, chunkSize, chunkCount)
	if err != nil {
		return err
	}
	defer file.Close()

	var committed atomic.Int64
	committed.Store(completedBytes)
	var received atomic.Int64
	progressCtx, stopProgress := context.WithCancel(context.Background())
	progressDone := make(chan struct{})
	var progressMu sync.Mutex
	lastReceived := int64(0)
	lastReport := time.Now()
	lastSpeed := float64(0)
	report := func(status string, sampleSpeed bool) {
		progressMu.Lock()
		defer progressMu.Unlock()
		now := time.Now()
		if sampleSpeed {
			currentReceived := received.Load()
			elapsed := now.Sub(lastReport).Seconds()
			if elapsed > 0 {
				lastSpeed = float64(currentReceived-lastReceived) / elapsed
			}
			lastReceived = currentReceived
			lastReport = now
		}
		current := committed.Load()
		hd.updateProgress(downloadID, types.DownloadProgress{
			ID: downloadID, URL: url, DestPath: destPath,
			Progress: float64(current) / float64(remote.size),
			Size:     remote.size, Downloaded: current, Speed: lastSpeed, Status: status,
			StartTime: startTime.Unix(), CurrentTime: now.Unix(),
		})
	}
	report("downloading", false)
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressCtx.Done():
				return
			case <-ticker.C:
				report("downloading", true)
			}
		}
	}()
	defer func() {
		stopProgress()
		<-progressDone
	}()

	pending := make([]int, 0, chunkCount)
	for i := 0; i < chunkCount; i++ {
		if !bitmapSet(checkpoint.cache.Completed, i) {
			pending = append(pending, i)
		}
	}
	if len(pending) > 0 {
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		jobs := make(chan int)
		workerCount := runtime.maxConcurrent
		if workerCount <= 0 {
			workerCount = 1
		}
		if workerCount > len(pending) {
			workerCount = len(pending)
		}
		errCh := make(chan error, workerCount)
		tokens := runtime.rangeTokens
		if tokens == nil {
			tokens = make(chan struct{}, workerCount)
		}

		var wg sync.WaitGroup
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					rng := chunkRange(index, chunkSize, remote.size)
					err := RetryWithBackoff(attemptCtx, RetryOptions{
						MaxRetries: runtime.maxRetries,
						Logger:     hd.logger,
						URL:        url,
						DestPath:   destPath,
					}, func(_ int) error {
						select {
						case tokens <- struct{}{}:
						case <-attemptCtx.Done():
							return attemptCtx.Err()
						}
						err := hd.downloadRange(attemptCtx, runtime.client, url, file, rng, remote, expectedSHA256, &received)
						<-tokens
						return err
					})
					if err != nil {
						// A sibling worker cancels the shared attempt after reporting its
						// real error. Do not let that derived context.Canceled error hide
						// the network error that should drive the outer checkpoint retry.
						if errors.Is(err, context.Canceled) && ctx.Err() == nil {
							return
						}
						var changed *remoteObjectChangedError
						if errors.As(err, &changed) {
							err = &restartDownloadError{err: err}
						} else {
							err = &chunkRetriesExhaustedError{
								err:       err,
								retryable: IsRetryableDownloadError(context.Background(), err),
							}
						}
					}
					if err == nil {
						err = checkpoint.complete(index, rng.end-rng.start+1)
					}
					if err != nil {
						select {
						case errCh <- err:
							cancelAttempt()
						default:
						}
						return
					}
					committed.Add(rng.end - rng.start + 1)
					report("downloading", false)
				}
			}()
		}

		go func() {
			defer close(jobs)
			for _, index := range pending {
				select {
				case jobs <- index:
				case <-attemptCtx.Done():
					return
				}
			}
		}()
		wg.Wait()
		cancelAttempt()
		var firstErr error
	collectErrors:
		for {
			select {
			case err := <-errCh:
				if firstErr == nil || errors.Is(firstErr, context.Canceled) {
					firstErr = err
				}
			default:
				break collectErrors
			}
		}
		if firstErr != nil {
			return firstErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if expectedSHA256 != "" {
		if err := hd.VerifyChecksum(partialPath, expectedSHA256); err != nil {
			hd.discardCheckpoint(downloadID, partialPath)
			return err
		}
	}
	if err := os.Rename(partialPath, destPath); err != nil {
		return err
	}
	_ = hd.deleteCache(downloadID)
	committed.Store(remote.size)
	report("completed", false)
	hd.lastPrintedProgress.Delete(downloadID)
	return nil
}

func (hd *HTTPDownloader) prepareCheckpoint(downloadID, url, destPath, partialPath, expectedSHA256 string, remote remoteFileInfo, chunkSize int64, chunkCount int) (*checkpointState, *os.File, int64, error) {
	cache, err := hd.loadCache(downloadID)
	if err != nil {
		hd.logger.Warn("discarding unreadable download checkpoint", "id", downloadID, "error", err)
		hd.discardCheckpoint(downloadID, partialPath)
		cache = nil
	}
	valid := cache != nil && validCheckpoint(cache, url, destPath, expectedSHA256, remote, chunkSize, chunkCount)
	if valid {
		if info, statErr := os.Stat(partialPath); statErr != nil || info.Size() != remote.size {
			valid = false
		}
	}
	if !valid {
		hd.discardCheckpoint(downloadID, partialPath)
		cache = &DownloadCache{
			Version: downloadCacheVersion, URL: url, DestPath: destPath,
			TotalSize: remote.size, ExpectedSHA: expectedSHA256,
			LastModified: time.Now(), Status: "downloading",
			ETag: remote.etag, RemoteMTime: remote.lastModified,
			ChunkSize: chunkSize, Completed: make([]byte, (chunkCount+7)/8),
		}
	}
	hd.logger.Debug("download checkpoint prepared",
		"id", downloadID,
		"path", partialPath,
		"valid", valid,
		"totalSize", remote.size,
		"chunkSize", chunkSize,
		"chunkCount", chunkCount,
		"completedBytes", completedSize(cache.Completed, chunkSize, remote.size))
	flags := os.O_CREATE | os.O_RDWR
	if !valid {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partialPath, flags, 0644)
	if err != nil {
		return nil, nil, 0, err
	}
	if !valid {
		if err := file.Truncate(remote.size); err != nil {
			file.Close()
			return nil, nil, 0, &ErrTruncateFileFailed{Path: partialPath, Message: err.Error()}
		}
		if err := hd.saveCache(downloadID, cache); err != nil {
			file.Close()
			return nil, nil, 0, err
		}
	}
	return &checkpointState{id: downloadID, cache: cache, hd: hd}, file, completedSize(cache.Completed, chunkSize, remote.size), nil
}

func validCheckpoint(cache *DownloadCache, url, destPath, expectedSHA256 string, remote remoteFileInfo, chunkSize int64, chunkCount int) bool {
	if cache.Version != downloadCacheVersion || cache.URL != url || cache.DestPath != destPath ||
		cache.TotalSize != remote.size || !strings.EqualFold(cache.ExpectedSHA, expectedSHA256) ||
		cache.ChunkSize != chunkSize || len(cache.Completed) != (chunkCount+7)/8 {
		return false
	}
	if expectedSHA256 != "" {
		return true
	}
	if cache.ETag != "" || remote.etag != "" {
		return cache.ETag != "" && cache.ETag == remote.etag
	}
	if cache.RemoteMTime != "" || remote.lastModified != "" {
		return cache.RemoteMTime != "" && cache.RemoteMTime == remote.lastModified
	}
	return true
}

func (c *checkpointState) complete(index int, size int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bitmapSet(c.cache.Completed, index) {
		return nil
	}
	setBitmap(c.cache.Completed, index)
	c.cache.Downloaded += size
	c.cache.LastModified = time.Now()
	if err := c.hd.saveCache(c.id, c.cache); err != nil {
		clearBitmap(c.cache.Completed, index)
		c.cache.Downloaded -= size
		return err
	}
	return nil
}

func (hd *HTTPDownloader) downloadRange(ctx context.Context, client *http.Client, url string, file *os.File, rng byteRange, remote remoteFileInfo, expectedSHA256 string, received *atomic.Int64) error {
	ifRange := expectedSHA256 == ""
	hd.logger.Debug("download range request",
		"url", url,
		"start", rng.start,
		"end", rng.end,
		"ifRange", ifRange)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rng.start, rng.end))
	if ifRange && remote.etag != "" {
		req.Header.Set("If-Range", remote.etag)
	} else if ifRange && remote.lastModified != "" {
		req.Header.Set("If-Range", remote.lastModified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	hd.logger.Debug("download range response",
		"url", url,
		"start", rng.start,
		"end", rng.end,
		"status", resp.StatusCode,
		"contentLength", resp.ContentLength,
		"contentRange", resp.Header.Get("Content-Range"),
		"etag", resp.Header.Get("ETag"))
	if resp.StatusCode == http.StatusOK && strings.TrimSpace(resp.Header.Get("Content-Range")) == "" {
		if (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusRequestedRangeNotSatisfiable) &&
			ifRange && (remote.etag != "" || remote.lastModified != "") {
			return &remoteObjectChangedError{statusCode: resp.StatusCode}
		}
		return &ErrServerReturnedError{URL: url, StatusCode: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable &&
			ifRange && (remote.etag != "" || remote.lastModified != "") {
			return &remoteObjectChangedError{statusCode: resp.StatusCode}
		}
		return &ErrServerReturnedError{URL: url, StatusCode: resp.StatusCode}
	}
	start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
	expectedLength := rng.end - rng.start + 1
	if err != nil || start != rng.start || end != rng.end || total != remote.size || resp.ContentLength != expectedLength {
		return fmt.Errorf("range request returned invalid response for bytes=%d-%d: content-range=%q content-length=%d", rng.start, rng.end, resp.Header.Get("Content-Range"), resp.ContentLength)
	}
	w := &rangeFileWriter{file: file, offset: rng.start, received: received}
	written, err := io.CopyN(w, resp.Body, expectedLength)
	if err != nil {
		return err
	}
	if written != expectedLength {
		return io.ErrUnexpectedEOF
	}
	return nil
}

type rangeFileWriter struct {
	file     *os.File
	offset   int64
	received *atomic.Int64
}

func (w *rangeFileWriter) Write(p []byte) (int, error) {
	n, err := w.file.WriteAt(p, w.offset)
	w.offset += int64(n)
	w.received.Add(int64(n))
	return n, err
}

func chunkRange(index int, chunkSize, total int64) byteRange {
	start := int64(index) * chunkSize
	end := start + chunkSize - 1
	if end >= total {
		end = total - 1
	}
	return byteRange{start: start, end: end}
}

func bitmapSet(bitmap []byte, index int) bool {
	return bitmap[index/8]&(1<<uint(index%8)) != 0
}

func setBitmap(bitmap []byte, index int) {
	bitmap[index/8] |= 1 << uint(index%8)
}

func clearBitmap(bitmap []byte, index int) {
	bitmap[index/8] &^= 1 << uint(index%8)
}

func completedSize(bitmap []byte, chunkSize, total int64) int64 {
	var size int64
	for i := 0; i < len(bitmap)*8; i++ {
		if bitmapSet(bitmap, i) {
			rng := chunkRange(i, chunkSize, total)
			if rng.start < total {
				size += rng.end - rng.start + 1
			}
		}
	}
	return size
}

func (hd *HTTPDownloader) discardCheckpoint(downloadID, partialPath string) {
	_ = os.Remove(partialPath)
	if err := hd.deleteCache(downloadID); err != nil && !errors.Is(err, os.ErrNotExist) {
		hd.logger.Warn("failed to discard download checkpoint", "id", downloadID, "error", err)
	}
}
