// Package types 定义跨包共享的基础数据结构。
//
// DownloadProgress 是下载器的统一进度结构，在本模块内自包含定义，
// 使下载器可以独立编译，不依赖任何外部/私有模块。
package types

// DownloadProgress 下载进度结构体。
type DownloadProgress struct {
	ID            string  // 下载任务唯一标识
	URL           string  // 下载地址
	DestPath      string  // 目标文件路径
	Progress      float64 // 进度百分比 0-100
	Speed         float64 // 下载速度（字节/秒）
	Size          int64   // 总字节数（0 表示未知）
	Downloaded    int64   // 已下载字节数
	Status        string  // 状态描述（downloading/paused/completed/error 等）
	Error         string  // 错误信息（失败时）
	StartTime     int64   // 开始时间（Unix 毫秒）
	CurrentTime   int64   // 当前时间（Unix 毫秒）
	EstimatedTime int64   // 预计剩余时间（毫秒，未知为 0）
}
