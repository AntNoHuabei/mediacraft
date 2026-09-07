//go:build windows

package runtime

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows"
)

// diskFreeBytes 返回 path 所在卷的可用字节数。
// Windows 下用 GetDiskFreeSpaceEx 精确探测；不支持/失败时返回 supported=false。
func diskFreeBytes(path string) (free int64, supported bool, err error) {
	dir := path
	if strings.TrimSpace(dir) == "" {
		return 0, false, errors.New("empty path")
	}
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false, err
	}
	var freeCaller, total, freeTotal uint64
	if err := windows.GetDiskFreeSpaceEx(dirPtr, &freeCaller, &total, &freeTotal); err != nil {
		return 0, false, err
	}
	return int64(freeCaller), true, nil
}
