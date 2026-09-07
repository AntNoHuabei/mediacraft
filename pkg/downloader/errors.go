package downloader

import "fmt"

// ErrDownloadCanceled 下载被取消
type ErrDownloadCanceled struct {
	Message string
}

func (e *ErrDownloadCanceled) Error() string {
	return fmt.Sprintf("下载已取消: %s", e.Message)
}

// ErrDownloadRequestFailed 下载请求失败
type ErrDownloadRequestFailed struct {
	URL     string
	Message string
}

func (e *ErrDownloadRequestFailed) Error() string {
	return fmt.Sprintf("下载请求失败: %s", e.Message)
}

// ErrResumeNotSupported 服务器不支持断点续传
type ErrResumeNotSupported struct {
	URL        string
	StatusCode int
}

func (e *ErrResumeNotSupported) Error() string {
	return fmt.Sprintf("服务器不支持断点续传，状态码: %d", e.StatusCode)
}

// ErrWriteFileFailed 写入文件失败
type ErrWriteFileFailed struct {
	Path    string
	Message string
}

func (e *ErrWriteFileFailed) Error() string {
	return fmt.Sprintf("写入文件失败: %s", e.Message)
}

// ErrReadResponseFailed 读取响应数据失败
type ErrReadResponseFailed struct {
	URL     string
	Message string
}

func (e *ErrReadResponseFailed) Error() string {
	return fmt.Sprintf("读取响应数据失败: %s", e.Message)
}

// ErrChecksumVerificationFailed 校验和验证失败
type ErrChecksumVerificationFailed struct {
	Path        string
	ExpectedSHA string
	ActualSHA   string
}

func (e *ErrChecksumVerificationFailed) Error() string {
	return fmt.Sprintf("校验和不匹配: 期望 %s, 实际 %s", e.ExpectedSHA, e.ActualSHA)
}

type ErrCheckFileSizeVerificationFailed struct {
	Path         string
	ExpectedSize int64
	ActualSize   int64
}

func (e *ErrCheckFileSizeVerificationFailed) Error() string {
	return fmt.Sprintf("文件大小不匹配: 期望 %d, 实际 %d", e.ExpectedSize, e.ActualSize)
}

// ErrFileOpenFailed 打开文件失败
type ErrFileOpenFailed struct {
	Path    string
	Message string
}

func (e *ErrFileOpenFailed) Error() string {
	return fmt.Sprintf("打开文件失败: %s", e.Message)
}

// ErrFileSeekFailed 定位文件位置失败
type ErrFileSeekFailed struct {
	Path    string
	Message string
}

func (e *ErrFileSeekFailed) Error() string {
	return fmt.Sprintf("定位文件位置失败: %s", e.Message)
}

// ErrCreateRequestFailed 创建请求失败
type ErrCreateRequestFailed struct {
	URL     string
	Message string
}

func (e *ErrCreateRequestFailed) Error() string {
	return fmt.Sprintf("创建请求失败: %s", e.Message)
}

// ErrGetFileInfoFailed 获取文件信息失败
type ErrGetFileInfoFailed struct {
	URL     string
	Message string
}

func (e *ErrGetFileInfoFailed) Error() string {
	return fmt.Sprintf("获取文件信息失败: %s", e.Message)
}

// ErrServerReturnedError 服务器返回错误状态码
type ErrServerReturnedError struct {
	URL        string
	StatusCode int
}

func (e *ErrServerReturnedError) Error() string {
	return fmt.Sprintf("服务器返回错误状态码: %d", e.StatusCode)
}

// ErrCannotGetFileSize 无法获取文件大小
type ErrCannotGetFileSize struct {
	URL string
}

func (e *ErrCannotGetFileSize) Error() string {
	return "无法获取文件大小"
}

// ErrCreateTargetFileFailed 创建目标文件失败
type ErrCreateTargetFileFailed struct {
	Path    string
	Message string
}

func (e *ErrCreateTargetFileFailed) Error() string {
	return fmt.Sprintf("创建目标文件失败: %s", e.Message)
}

// ErrTruncateFileFailed 截断文件失败
type ErrTruncateFileFailed struct {
	Path    string
	Message string
}

func (e *ErrTruncateFileFailed) Error() string {
	return fmt.Sprintf("截断文件失败: %s", e.Message)
}

// ErrLoadCacheFailed 加载缓存失败
type ErrLoadCacheFailed struct {
	ID      string
	Message string
}

func (e *ErrLoadCacheFailed) Error() string {
	return fmt.Sprintf("加载缓存失败: %s", e.Message)
}

// ErrCacheNotFound 未找到下载缓存
type ErrCacheNotFound struct {
	ID string
}

func (e *ErrCacheNotFound) Error() string {
	return "未找到下载缓存"
}

// ErrConcurrentDownloadFailed 并发下载失败
type ErrConcurrentDownloadFailed struct {
	URL     string
	Message string
}

func (e *ErrConcurrentDownloadFailed) Error() string {
	return fmt.Sprintf("并发下载失败: %s", e.Message)
}
