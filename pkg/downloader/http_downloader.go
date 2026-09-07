package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AntNoHuabei/mediacraft/pkg/types"
)

type DownloadCache struct {
	Version      int       `json:"version"`
	URL          string    `json:"url"`
	DestPath     string    `json:"destPath"`
	Downloaded   int64     `json:"downloaded"`
	TotalSize    int64     `json:"totalSize"`
	ExpectedSHA  string    `json:"expectedSHA"`
	LastModified time.Time `json:"lastModified"`
	Status       string    `json:"status"`
	ETag         string    `json:"etag,omitempty"`
	RemoteMTime  string    `json:"remoteLastModified,omitempty"`
	ChunkSize    int64     `json:"chunkSize,omitempty"`
	Completed    []byte    `json:"completed,omitempty"`
}

type downloadControl struct {
	cancel  context.CancelFunc
	done    chan struct{}
	discard atomic.Bool
}

type DownloadTask struct {
	ID             string
	URL            string
	DestPath       string
	ExpectedSHA256 string
	FileSize       int64
}

type HTTPDownloader struct {
	configMu            sync.RWMutex
	client              *http.Client
	maxConcurrent       int
	maxRetries          int
	timeout             int
	chunkSize           int
	parallelThreshold   int64
	useProxy            bool
	proxyURL            string
	logger              *slog.Logger
	cacheDir            string
	progress            sync.Map
	ctxCancel           sync.Map
	runningTasks        sync.Map
	taskMutex           sync.Mutex
	lastPrintedProgress sync.Map
	progressCallbacks   sync.Map
}

type Options struct {
	ConcurrentDownloads int
	MaxRetries          int
	Timeout             int
	ChunkSize           int
	ParallelThreshold   int64
	Logger              *slog.Logger
	CacheDir            string
}

type runtimeOptions struct {
	client            *http.Client
	maxConcurrent     int
	maxRetries        int
	timeout           int
	chunkSize         int
	parallelThreshold int64
	rangeTokens       chan struct{}
}

func newHTTPClient(timeout int) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		Proxy:                 http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = time.Duration(timeout) * time.Second
	}
	return client
}

func normalizeRuntimeOptions(options Options) runtimeOptions {
	maxConcurrent := options.ConcurrentDownloads
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}
	parallelThreshold := options.ParallelThreshold
	if parallelThreshold <= 0 {
		parallelThreshold = defaultParallelThreshold
	}
	return runtimeOptions{
		client:            newHTTPClient(options.Timeout),
		maxConcurrent:     maxConcurrent,
		maxRetries:        options.MaxRetries,
		timeout:           options.Timeout,
		chunkSize:         options.ChunkSize,
		parallelThreshold: parallelThreshold,
	}
}

func NewHTTPDownloader(options Options) *HTTPDownloader {
	var logHelper *slog.Logger
	if options.Logger != nil {
		logHelper = options.Logger
	} else {
		logHelper = slog.Default()
	}

	cacheDir := options.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "mediacraft", "download_cache")
	}

	runtime := normalizeRuntimeOptions(options)

	return &HTTPDownloader{
		client:            runtime.client,
		maxConcurrent:     runtime.maxConcurrent,
		maxRetries:        runtime.maxRetries,
		timeout:           runtime.timeout,
		chunkSize:         runtime.chunkSize,
		parallelThreshold: runtime.parallelThreshold,
		logger:            logHelper,
		cacheDir:          cacheDir,
	}
}

// Reconfigure updates settings used by downloads started after this call.
// Active downloads keep the immutable snapshot they acquired at start.
func (hd *HTTPDownloader) Reconfigure(options Options) {
	if hd == nil {
		return
	}
	next := normalizeRuntimeOptions(options)
	hd.configMu.Lock()
	oldClient := hd.client
	hd.client = next.client
	hd.maxConcurrent = next.maxConcurrent
	hd.maxRetries = next.maxRetries
	hd.timeout = next.timeout
	hd.chunkSize = next.chunkSize
	hd.parallelThreshold = next.parallelThreshold
	hd.configMu.Unlock()
	if transport, ok := oldClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (hd *HTTPDownloader) runtimeOptions() runtimeOptions {
	hd.configMu.RLock()
	defer hd.configMu.RUnlock()
	return runtimeOptions{
		client:            hd.client,
		maxConcurrent:     hd.maxConcurrent,
		maxRetries:        hd.maxRetries,
		timeout:           hd.timeout,
		chunkSize:         hd.chunkSize,
		parallelThreshold: hd.parallelThreshold,
	}
}

func (hd *HTTPDownloader) VerifyChecksum(filePath, expectedSHA256 string) error {
	return hd.verifyChecksumRange(filePath, expectedSHA256, 0)
}

func (hd *HTTPDownloader) verifyChecksumRange(filePath, expectedSHA256 string, skipBytes int64) error {
	file, err := os.Open(filePath)
	if err != nil {
		return &ErrFileOpenFailed{Path: filePath, Message: err.Error()}
	}
	defer file.Close()

	if skipBytes > 0 {
		if _, err := file.Seek(skipBytes, io.SeekStart); err != nil {
			return &ErrFileSeekFailed{Path: filePath, Message: err.Error()}
		}
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return &ErrReadResponseFailed{URL: filePath, Message: err.Error()}
	}

	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualSHA256, expectedSHA256) {
		return &ErrChecksumVerificationFailed{Path: filePath, ExpectedSHA: expectedSHA256, ActualSHA: actualSHA256}
	}

	hd.logger.Info("校验和验证通过", "path", filePath)
	return nil
}

func (hd *HTTPDownloader) CancelDownload(id string) error {
	hd.logger.Info("取消下载", "id", id)

	// Stop all writers before removing resumable state.
	if value, ok := hd.ctxCancel.Load(id); ok {
		if control, ok := value.(*downloadControl); ok {
			control.discard.Store(true)
			control.cancel()
			<-control.done
			hd.logger.Info("下载已取消", "id", id)
		}
	}

	// 清理 .downloading 临时文件
	if value, ok := hd.progress.Load(id); ok {
		if p, ok := value.(types.DownloadProgress); ok && p.DestPath != "" {
			downloadingPath := p.DestPath + ".downloading"
			if err := os.Remove(downloadingPath); err != nil && !os.IsNotExist(err) {
				hd.logger.Warn("删除下载临时文件失败", "path", downloadingPath, "error", err)
			}
		}
	}

	if err := hd.deleteCache(id); err != nil && !os.IsNotExist(err) {
		hd.logger.Warn("删除下载检查点失败", "id", id, "error", err)
	}
	hd.progress.Delete(id)
	hd.runningTasks.Delete(id)
	hd.lastPrintedProgress.Delete(id)
	hd.progressCallbacks.Delete(id)
	return nil
}

// CancelAllDownloads cancels every active transfer before storage migration.
func (hd *HTTPDownloader) CancelAllDownloads() error {
	if hd == nil {
		return nil
	}
	type activeDownload struct {
		id      string
		control *downloadControl
	}
	active := make([]activeDownload, 0)
	hd.ctxCancel.Range(func(key, value any) bool {
		if id, ok := key.(string); ok {
			if control, ok := value.(*downloadControl); ok {
				active = append(active, activeDownload{id: id, control: control})
			}
		}
		return true
	})
	for _, item := range active {
		item.control.discard.Store(true)
		item.control.cancel()
	}
	var errs []error
	for _, item := range active {
		<-item.control.done
		if err := hd.CancelDownload(item.id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (hd *HTTPDownloader) GetProgress(id string) (types.DownloadProgress, error) {
	if value, ok := hd.progress.Load(id); ok {
		if progress, ok := value.(types.DownloadProgress); ok {
			return progress, nil
		}
	}
	return types.DownloadProgress{}, fmt.Errorf("未找到下载进度")
}

func (hd *HTTPDownloader) resolveFileSize(ctx context.Context, client *http.Client, url string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
				return resp.ContentLength
			}
		}
	}

	reqProbe, errProbe := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errProbe != nil {
		return 0
	}
	reqProbe.Header.Set("Range", "bytes=0-0")
	if r2, e2 := client.Do(reqProbe); e2 == nil {
		defer r2.Body.Close()
		if r2.StatusCode == http.StatusPartialContent {
			cr := r2.Header.Get("Content-Range")
			if idx := strings.LastIndex(cr, "/"); idx != -1 && idx+1 < len(cr) {
				if v, perr := strconv.ParseInt(strings.TrimSpace(cr[idx+1:]), 10, 64); perr == nil && v > 0 {
					return v
				}
			}
		} else if r2.StatusCode == http.StatusOK {
			if cl := r2.Header.Get("Content-Length"); cl != "" {
				if v, perr := strconv.ParseInt(strings.TrimSpace(cl), 10, 64); perr == nil && v > 0 {
					return v
				}
			}
		}
	}
	return 0
}

// StartDownloadsWithProgress keeps the original percentage-only callback API.
func (hd *HTTPDownloader) StartDownloadsWithProgress(tasks []DownloadTask, progressCallback func(progress float64, speed float64)) error {
	return hd.StartDownloadsWithProgressBytes(tasks, func(progress float64, speed float64, _, _ int64) {
		if progressCallback != nil {
			progressCallback(progress, speed)
		}
	})
}

// StartDownloadsWithProgressBytes reports aggregate byte counters in addition
// to the percentage progress. This lets callers combine this download with a
// prerequisite transfer such as a runtime installation.
func (hd *HTTPDownloader) StartDownloadsWithProgressBytes(tasks []DownloadTask, progressCallback func(progress float64, speed float64, downloadedBytes int64, totalBytes int64)) error {
	if len(tasks) == 0 {
		return fmt.Errorf("任务列表为空")
	}

	runtime := hd.runtimeOptions()
	rangeConcurrency := runtime.maxConcurrent
	if rangeConcurrency <= 0 {
		rangeConcurrency = 5
	}
	runtime.rangeTokens = make(chan struct{}, rangeConcurrency)

	concurrency := rangeConcurrency
	if concurrency > len(tasks) {
		concurrency = len(tasks)
	}

	for i := range tasks {
		if tasks[i].FileSize <= 0 {
			if size := hd.resolveFileSize(context.Background(), runtime.client, tasks[i].URL); size > 0 {
				tasks[i].FileSize = size
			}
		}
	}

	totalSize := int64(0)
	for _, task := range tasks {
		totalSize += task.FileSize
	}

	hd.logger.Info("开始批量下载", "tasks", len(tasks), "concurrency", concurrency, "totalSize", totalSize)

	var wg sync.WaitGroup
	errChan := make(chan error, len(tasks))

	var totalDownloaded atomic.Int64
	var lastNotifiedProgress atomic.Int64
	lastNotifiedProgress.Store(-1)
	var notifyMu sync.Mutex

	notifyProgress := func(speed float64) {
		notifyMu.Lock()
		defer notifyMu.Unlock()
		downloaded := totalDownloaded.Load()
		var progress float64
		if totalSize > 0 {
			progress = float64(downloaded) / float64(totalSize) * 100
		}
		currentProgress := int64(progress)
		last := lastNotifiedProgress.Load()
		if currentProgress != last {
			lastNotifiedProgress.Store(currentProgress)
			if progressCallback != nil {
				progressCallback(progress, speed, downloaded, totalSize)
			}
		}
	}

	for i, task := range tasks {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()

			ctx := context.Background()

			hd.logger.Info("线程开始任务", "threadID", threadID, "url", task.URL, "destPath", task.DestPath)

			if _, err := os.Stat(task.DestPath); err == nil {
				hd.logger.Info("文件已存在，跳过下载", "path", task.DestPath)
				totalDownloaded.Add(task.FileSize)
				notifyProgress(0)
				return
			}

			var prevDownloaded atomic.Int64
			err := hd.downloadWithRuntime(ctx, runtime, task.URL, task.DestPath, task.ExpectedSHA256, task.FileSize, func(progress float64, speed float64) {
				currentTaskDownloaded := int64(float64(task.FileSize) * progress)
				prev := prevDownloaded.Swap(currentTaskDownloaded)
				totalDownloaded.Add(currentTaskDownloaded - prev)
				notifyProgress(speed)
			})
			if err != nil {
				hd.logger.Error("任务失败", "url", task.URL, "error", err)
				errChan <- err
			} else {
				hd.logger.Info("线程完成任务", "threadID", threadID, "url", task.URL)
				finalTaskDownloaded := task.FileSize
				prev := prevDownloaded.Swap(finalTaskDownloaded)
				totalDownloaded.Add(finalTaskDownloaded - prev)
				notifyProgress(0)
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	var errs []error
	for err := range errChan {
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("有 %d 个任务失败", len(errs))
	}

	hd.logger.Info("批量下载完成", "tasks", len(tasks))
	return nil
}

func (hd *HTTPDownloader) GetRunningTasks() int {
	count := 0
	hd.runningTasks.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// GenerateDownloadID 生成下载ID
func (hd *HTTPDownloader) GenerateDownloadID(url, destPath string) string {
	hasher := sha256.New()
	hasher.Write([]byte(url + destPath))
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}

// generateDownloadID 生成下载ID（内部使用）
func (hd *HTTPDownloader) generateDownloadID(url, destPath string) string {
	return hd.GenerateDownloadID(url, destPath)
}

func (hd *HTTPDownloader) getCachePath(id string) string {
	return filepath.Join(hd.cacheDir, id+".json")
}

func (hd *HTTPDownloader) saveCache(id string, cache *DownloadCache) error {
	cachePath := hd.getCachePath(id)
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), filepath.Base(cachePath)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, cachePath)
}

func (hd *HTTPDownloader) loadCache(id string) (*DownloadCache, error) {
	cachePath := hd.getCachePath(id)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var cache DownloadCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}

	return &cache, nil
}

func (hd *HTTPDownloader) deleteCache(id string) error {
	cachePath := hd.getCachePath(id)
	err := os.Remove(cachePath)
	matches, _ := filepath.Glob(cachePath + ".*.tmp")
	for _, path := range matches {
		_ = os.Remove(path)
	}
	return err
}

func (hd *HTTPDownloader) updateProgress(id string, progress types.DownloadProgress) {
	hd.progress.Store(id, progress)

	// 打印下载进度，每个百分比打印一次
	if hd.logger != nil && progress.Status == "downloading" {
		currentProgress := int(progress.Progress)
		// 获取上一次打印的进度
		lastProgressVal, _ := hd.lastPrintedProgress.Load(id)
		lastProgress := 0
		if lastProgressVal != nil {
			lastProgress = lastProgressVal.(int)
		}

		// 只有当进度超过上一次打印的进度时才打印
		if currentProgress > lastProgress {
			hd.logger.Info("下载进度",
				"progress", currentProgress,
				"downloaded", formatBytes(progress.Downloaded),
				"total", formatBytes(progress.Size),
				"speed", fmt.Sprintf("%.2f MB/s", progress.Speed/1024/1024))
			hd.lastPrintedProgress.Store(id, currentProgress)
		}
	}

	// 调用进度回调函数
	if callback, ok := hd.progressCallbacks.Load(id); ok {
		if cb, ok := callback.(func(float64, float64)); ok {
			cb(progress.Progress, progress.Speed)
		}
	}
}

// formatBytes 格式化字节数为人类可读的格式
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
