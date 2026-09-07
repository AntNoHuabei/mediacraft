//go:build !windows

package runtime

import (
	"errors"
	"syscall"
)

// diskFreeBytes 返回 path 所在文件系统的可用字节数。
// Unix 用 statfs 探测；不可用平台返回 supported=false（跳过校验）。
func diskFreeBytes(path string) (free int64, supported bool, err error) {
	if path == "" {
		return 0, false, errors.New("empty path")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, false, err
	}
	// Bsize 与 Bavail/Bfree 字段在不同 Unix 有类型差异，统一经 Bsize 换算。
	free = int64(stat.Bavail) * int64(stat.Bsize)
	if free < 0 {
		free = int64(stat.Bfree) * int64(stat.Bsize)
	}
	return free, true, nil
}
