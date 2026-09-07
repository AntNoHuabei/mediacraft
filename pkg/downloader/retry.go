package downloader

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

var (
	downloadRetryInitialInterval     = time.Second
	downloadRetryMaxInterval         = 30 * time.Second
	downloadRetryRandomizationFactor = 0.2
)

// RetryOptions configures retry behavior for one logical download.
type RetryOptions struct {
	MaxRetries  int
	Logger      *slog.Logger
	URL         string
	DestPath    string
	PartialPath string
}

// RetryWithBackoff retries transient download failures using exponential backoff.
func RetryWithBackoff(ctx context.Context, opts RetryOptions, operation func(attempt int) error) error {
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	exp := backoff.NewExponentialBackOff()
	exp.InitialInterval = downloadRetryInitialInterval
	exp.Multiplier = 2
	exp.RandomizationFactor = downloadRetryRandomizationFactor
	exp.MaxInterval = downloadRetryMaxInterval
	exp.MaxElapsedTime = 0
	exp.Reset()

	retryBackoff := backoff.WithContext(backoff.WithMaxRetries(exp, uint64(maxRetries)), ctx)
	attempt := 1
	maxAttempts := maxRetries + 1

	for {
		err := operation(attempt)
		if err == nil {
			return nil
		}

		if shouldDiscardPartialDownload(err) && opts.PartialPath != "" {
			if removeErr := os.Remove(opts.PartialPath); removeErr != nil && !os.IsNotExist(removeErr) {
				logger.Warn("删除损坏的下载临时文件失败",
					"url", opts.URL,
					"destPath", opts.DestPath,
					"partialPath", opts.PartialPath,
					"error", removeErr)
			}
		}

		if !IsRetryableDownloadError(ctx, err) {
			logger.Warn("下载失败，不再重试",
				"url", opts.URL,
				"destPath", opts.DestPath,
				"attempt", attempt,
				"maxAttempts", maxAttempts,
				"error", err)
			return err
		}

		wait := retryBackoff.NextBackOff()
		if wait == backoff.Stop {
			logger.Warn("下载重试次数已耗尽",
				"url", opts.URL,
				"destPath", opts.DestPath,
				"attempt", attempt,
				"maxAttempts", maxAttempts,
				"error", err)
			return err
		}

		logger.Warn("下载失败，将重试",
			"url", opts.URL,
			"destPath", opts.DestPath,
			"attempt", attempt,
			"maxAttempts", maxAttempts,
			"retryIn", wait.String(),
			"error", err)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}

		attempt++
	}
}

// IsRetryableDownloadError returns true for transient failures that are safe to retry.
func IsRetryableDownloadError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var restart *restartDownloadError
	if errors.As(err, &restart) {
		return true
	}
	var exhausted *chunkRetriesExhaustedError
	if errors.As(err, &exhausted) {
		return exhausted.retryable
	}

	// HTTP 状态码错误(从 ErrServerReturnedError 或包装的错误中提取)
	var serverErr *ErrServerReturnedError
	if errors.As(err, &serverErr) {
		return serverErr.StatusCode == http.StatusTooManyRequests ||
			serverErr.StatusCode == http.StatusRequestTimeout ||
			serverErr.StatusCode >= http.StatusInternalServerError
	}

	// 文件大小不匹配
	var sizeErr *ErrCheckFileSizeVerificationFailed
	if errors.As(err, &sizeErr) {
		return true
	}

	// 校验和不匹配
	var checksumErr *ErrChecksumVerificationFailed
	if errors.As(err, &checksumErr) {
		return true
	}

	// 标准 io 错误
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrShortWrite) {
		return true
	}

	// 网络错误(timeout/temporary)
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	// URL 错误（HTTP 客户端包装的错误）
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return true
		}
		// 递归检查内部错误
		return IsRetryableDownloadError(ctx, urlErr.Err)
	}

	// 兜底：从错误文本中提取可重试的信号
	return isRetryableErrorText(err.Error())
}

// isRetryableErrorText checks error message text for retryable signals.
func isRetryableErrorText(message string) bool {
	lower := strings.ToLower(message)

	if strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "wsarecv") ||
		strings.Contains(lower, "connection aborted") ||
		strings.Contains(lower, "connection timed out") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "unexpected eof") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "goaway") ||
		strings.Contains(lower, "http2: server sent") ||
		strings.Contains(lower, "stream error") ||
		strings.Contains(lower, "internal_error") {
		return true
	}

	// 提取 HTTP 状态码：匹配 "status code <NNN>" 或 "statuscode=<NNN>" 或 ": <NNN>" 格式
	if idx := strings.Index(lower, "status code"); idx != -1 {
		tail := lower[idx:]
		for _, code := range []string{"429", "408", "500", "502", "503", "504"} {
			if strings.Contains(tail, code) {
				return true
			}
		}
	}

	return false
}

func shouldDiscardPartialDownload(err error) bool {
	var checksumErr *ErrChecksumVerificationFailed
	if errors.As(err, &checksumErr) {
		return true
	}
	if strings.Contains(strings.ToLower(err.Error()), "range request returned invalid") {
		return true
	}
	return false
}
