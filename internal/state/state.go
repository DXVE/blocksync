// 状态目录与单实例锁；索引按文件存放在该目录下。
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const StateDirName = ".blocksync"

func Dir(local string) string {
	return filepath.Join(local, StateDirName)
}

// Lock 建立单实例锁，返回释放函数。
func Lock(local string, force bool) (func(), error) {
	if err := os.MkdirAll(Dir(local), 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(Dir(local), "lock")
	mk := func() (*os.File, error) {
		return os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	f, err := mk()
	if err != nil {
		if !force {
			return nil, fmt.Errorf("目录正被另一个 blocksync 进程使用（%s）。确认无其他实例后可加 --force", p)
		}
		_ = os.Remove(p)
		if f, err = mk(); err != nil {
			return nil, fmt.Errorf("无法获取锁文件 %s: %w", p, err)
		}
	}
	fmt.Fprintf(f, "pid=%d time=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}
