//go:build windows

package mirror

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var procGetFinalPathNameByHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

const maxLongPath = 32767

// probePath 打开路径并返回解析联接后的最终路径与硬链接数；
// 路径不存在时 exists 为 false 且不返回错误，其余错误由调用方按中止处理。
func probePath(p string) (final string, links uint32, exists bool, err error) {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", 0, false, err
	}
	h, err := syscall.CreateFile(name, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if isNotExist(err) {
			return "", 0, false, nil
		}
		return "", 0, false, err
	}
	defer syscall.CloseHandle(h)

	buf := make([]uint16, maxLongPath)
	n, _, callErr := procGetFinalPathNameByHandleW.Call(uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if n == 0 {
		return "", 0, true, callErr
	}
	if int(n) >= len(buf) {
		return "", 0, true, fmt.Errorf("路径过长: %s", p)
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return "", 0, true, err
	}
	return stripLongPathPrefix(syscall.UTF16ToString(buf[:n])), info.NumberOfLinks, true, nil
}

func isNotExist(err error) bool {
	return errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) ||
		os.IsNotExist(err)
}

// stripLongPathPrefix 去掉 \\?\ 前缀，UNC 形式还原为 \\server\share。
func stripLongPathPrefix(p string) string {
	if strings.HasPrefix(p, `\\?\UNC\`) {
		return `\\` + p[len(`\\?\UNC\`):]
	}
	if strings.HasPrefix(p, `\\?\`) {
		return p[len(`\\?\`):]
	}
	return p
}
