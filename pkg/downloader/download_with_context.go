package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/AntNoHuabei/mediacraft/pkg/types"
)

// DownloadWithContext downloads one file and supports cancelation and resume.
func (hd *HTTPDownloader) DownloadWithContext(ctx context.Context, url string, destPath string, expectedSHA256 string, expectedFileSize int64, progressCallback ...func(float64, float64)) error {
	return hd.downloadWithRuntime(ctx, hd.runtimeOptions(), url, destPath, expectedSHA256, expectedFileSize, progressCallback...)
}

func (hd *HTTPDownloader) downloadWithRuntime(ctx context.Context, runtime runtimeOptions, url string, destPath string, expectedSHA256 string, expectedFileSize int64, progressCallback ...func(float64, float64)) error {
	downloadID := hd.generateDownloadID(url, destPath)
	partialPath := destPath + ".downloading"
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	if err := os.MkdirAll(hd.cacheDir, 0755); err != nil {
		return fmt.Errorf("create download cache directory: %w", err)
	}

	if len(progressCallback) > 0 && progressCallback[0] != nil {
		hd.progressCallbacks.Store(downloadID, progressCallback[0])
	}

	downloadCtx, cancel := context.WithCancel(ctx)
	control := &downloadControl{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	hd.ctxCancel.Store(downloadID, control)
	hd.runningTasks.Store(downloadID, struct{}{})
	defer func() {
		cancel()
		if control.discard.Load() {
			_ = os.Remove(partialPath)
			_ = hd.deleteCache(downloadID)
		}
		hd.ctxCancel.Delete(downloadID)
		hd.runningTasks.Delete(downloadID)
		hd.progressCallbacks.Delete(downloadID)
		close(control.done)
	}()

	hd.logger.Info("starting download", "url", url, "destPath", destPath)
	return RetryWithBackoff(downloadCtx, RetryOptions{
		MaxRetries: runtime.maxRetries,
		Logger:     hd.logger,
		URL:        url,
		DestPath:   destPath,
	}, func(attempt int) error {
		hd.logger.Info("starting download attempt",
			"url", url,
			"destPath", destPath,
			"attempt", attempt,
			"maxAttempts", runtime.maxRetries+1)
		return hd.downloadOnceWithContext(downloadCtx, runtime, downloadID, url, destPath, partialPath, expectedSHA256, expectedFileSize)
	})
}

func (hd *HTTPDownloader) downloadOnceWithContext(ctx context.Context, runtime runtimeOptions, downloadID, url, destPath, partialPath, expectedSHA256 string, expectedFileSize int64) error {
	startTime := time.Now()
	if expectedFileSize > 0 && expectedFileSize < runtime.parallelThreshold {
		hd.discardCheckpoint(downloadID, partialPath)
		return hd.downloadSimple(ctx, runtime.client, downloadID, url, destPath, partialPath, expectedSHA256, expectedFileSize, startTime)
	}

	remote, err := probeRemoteFile(ctx, runtime.client, url)
	if err != nil {
		return err
	}
	hd.logger.Debug("download remote probe",
		"url", url,
		"size", remote.size,
		"rangeable", remote.rangeable,
		"etag", remote.etag,
		"lastModified", remote.lastModified,
		"expectedSize", expectedFileSize,
		"parallelThreshold", runtime.parallelThreshold)
	if expectedFileSize > 0 && remote.size > 0 && remote.size != expectedFileSize {
		return &ErrCheckFileSizeVerificationFailed{Path: destPath, ExpectedSize: expectedFileSize, ActualSize: remote.size}
	}
	if remote.rangeable && remote.size >= runtime.parallelThreshold {
		err := hd.downloadSegmented(ctx, runtime, downloadID, url, destPath, partialPath, expectedSHA256, remote, startTime)
		var changed *remoteObjectChangedError
		if errors.As(err, &changed) && changed.statusCode == http.StatusOK {
			// Some CDNs return a full 200 response for a range request when their
			// validator does not match. A single stream avoids mixing that response
			// with checkpointed ranges; checksum verification still guards integrity.
			hd.discardCheckpoint(downloadID, partialPath)
			return hd.downloadSimple(ctx, runtime.client, downloadID, url, destPath, partialPath, expectedSHA256, expectedFileSize, startTime)
		}
		return err
	}

	size := expectedFileSize
	if size <= 0 {
		size = remote.size
	}
	hd.logger.Info("server does not support resumable ranges; using a single stream", "url", url)
	hd.discardCheckpoint(downloadID, partialPath)
	return hd.downloadSimple(ctx, runtime.client, downloadID, url, destPath, partialPath, expectedSHA256, size, startTime)
}

func (hd *HTTPDownloader) downloadSimple(ctx context.Context, client *http.Client, downloadID, url, destPath, partialPath, expectedSHA256 string, expectedFileSize int64, startTime time.Time) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &ErrServerReturnedError{URL: url, StatusCode: resp.StatusCode}
	}
	if expectedFileSize <= 0 && resp.ContentLength > 0 {
		expectedFileSize = resp.ContentLength
	}

	file, err := os.Create(partialPath)
	if err != nil {
		return err
	}

	var written int64
	lastReport := time.Time{}
	reportProgress := func(force bool) {
		now := time.Now()
		if !force && !lastReport.IsZero() && now.Sub(lastReport) < 250*time.Millisecond {
			return
		}
		progress := float64(0)
		if expectedFileSize > 0 {
			progress = float64(written) / float64(expectedFileSize)
			if progress > 1 {
				progress = 1
			}
		}
		hd.updateProgress(downloadID, types.DownloadProgress{
			ID: downloadID, URL: url, DestPath: destPath,
			Progress: progress, Size: expectedFileSize, Downloaded: written,
			Speed:  float64(written) / max(now.Sub(startTime).Seconds(), 0.001),
			Status: "downloading", StartTime: startTime.Unix(), CurrentTime: now.Unix(),
		})
		lastReport = now
	}
	reportProgress(true)
	progressReader := &downloadProgressReader{
		reader: resp.Body,
		onRead: func(n int64) {
			written += n
			reportProgress(false)
		},
	}
	_, copyErr := io.Copy(file, progressReader)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(partialPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(partialPath)
		return closeErr
	}
	if expectedFileSize > 0 && written != expectedFileSize {
		_ = os.Remove(partialPath)
		return &ErrCheckFileSizeVerificationFailed{Path: partialPath, ExpectedSize: expectedFileSize, ActualSize: written}
	}
	if expectedSHA256 != "" {
		if err := hd.VerifyChecksum(partialPath, expectedSHA256); err != nil {
			_ = os.Remove(partialPath)
			return err
		}
	}
	reportProgress(true)
	if err := os.Rename(partialPath, destPath); err != nil {
		return err
	}
	_ = hd.deleteCache(downloadID)

	hd.updateProgress(downloadID, types.DownloadProgress{
		ID:          downloadID,
		URL:         url,
		DestPath:    destPath,
		Progress:    1,
		Size:        written,
		Downloaded:  written,
		Speed:       float64(written) / max(time.Since(startTime).Seconds(), 0.001),
		Status:      "completed",
		StartTime:   startTime.Unix(),
		CurrentTime: time.Now().Unix(),
	})
	hd.lastPrintedProgress.Delete(downloadID)
	return nil
}

type downloadProgressReader struct {
	reader io.Reader
	onRead func(int64)
}

func (r *downloadProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.onRead != nil {
		r.onRead(int64(n))
	}
	return n, err
}
