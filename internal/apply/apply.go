// 把补丁条目原地写入本地文件：按区间定位写、按新尺寸截断、还原修改时间。
package apply

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blocksync/internal/index"
	"blocksync/internal/patch"
)

const writeChunk = 4 << 20

// SafeJoin 拼接相对路径并阻止跳出根目录。
func SafeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("空路径")
	}
	clean := filepath.FromSlash(rel)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("非法绝对路径: %s", rel)
	}
	abs := filepath.Join(root, clean)
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法路径: %s", rel)
	}
	return abs, nil
}

// Entry 应用单个文件条目，返回实际写入字节数。
func Entry(local string, store *index.Store, e *patch.EntryHeader, payload io.ReaderAt) (int64, error) {
	abs, err := SafeJoin(local, e.Path)
	if err != nil {
		return 0, err
	}

	switch e.Action {
	case patch.ActionDelete:
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		return 0, store.Delete(e.Path)

	case patch.ActionAdd, patch.ActionModify:
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return 0, err
		}
		var written int64
		buf := make([]byte, writeChunk)
		var pOff int64
		for _, r := range e.Ranges {
			var done uint64
			for done < r.Len {
				n := uint64(len(buf))
				if n > r.Len-done {
					n = r.Len - done
				}
				if _, err := payload.ReadAt(buf[:n], pOff+int64(done)); err != nil && !errors.Is(err, io.EOF) {
					f.Close()
					return written, fmt.Errorf("读取补丁数据失败: %w", err)
				}
				if _, err := f.WriteAt(buf[:n], int64(r.Off+done)); err != nil {
					f.Close()
					return written, fmt.Errorf("写入目标失败: %w", err)
				}
				done += n
				written += int64(n)
			}
			pOff += int64(r.Len)
		}
		if err := f.Truncate(int64(e.NewSize)); err != nil {
			f.Close()
			return written, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return written, err
		}
		if err := f.Close(); err != nil {
			return written, err
		}
		if e.MtimeNano != 0 {
			t := time.Unix(0, e.MtimeNano)
			_ = os.Chtimes(abs, t, t)
		}
		if len(e.Index) > 0 {
			if err := store.SaveRaw(e.Path, e.Index); err != nil {
				return written, err
			}
		}
		return written, nil
	}
	return 0, fmt.Errorf("未知的条目动作 %d", e.Action)
}
