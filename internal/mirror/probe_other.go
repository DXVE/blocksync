//go:build !windows

package mirror

import (
	"os"
	"path/filepath"
)

// probePath 解析符号链接后的最终路径；非 Windows 平台不提供硬链接数。
func probePath(p string) (final string, links uint32, exists bool, err error) {
	if _, err := os.Lstat(p); err != nil {
		if os.IsNotExist(err) {
			return "", 0, false, nil
		}
		return "", 0, false, err
	}
	final, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", 0, false, err
	}
	return final, 0, true, nil
}
