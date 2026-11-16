//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"blocksync/internal/state"
)

const (
	logKeepDays   = 7         // 日志文件保留天数
	logTotalLimit = 200 << 20 // 日志目录总大小上限
)

// logFileSink 把界面收到的行原样写入本次启动的日志文件。
// 写入发生在调用 appendLog 的协程里，进程被强杀时最多丢当前行；只在 mu 内调用。
type logFileSink struct {
	f    *os.File
	path string
	err  bool // 写入失败后不再重试，避免拖慢日志路径
}

// openLogFile 在本机日志目录建立本次启动的日志文件；失败返回 nil，界面照常工作。
func openLogFile(now time.Time) *logFileSink {
	dir, err := state.LogsDir()
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	pruneLogs(dir, now)
	p := filepath.Join(dir, "gui-"+now.Format("20060102-150405")+"-"+strconv.Itoa(os.Getpid())+".log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return &logFileSink{f: f, path: p}
}

// write 按行写入日志文件；每行一次 Write，不经过缓冲。
func (s *logFileSink) write(lines ...string) {
	if s == nil || s.f == nil || s.err {
		return
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	if _, err := s.f.WriteString(b.String()); err != nil {
		s.err = true
	}
}

func (s *logFileSink) close() {
	if s != nil && s.f != nil {
		_ = s.f.Close()
	}
}

// pruneLogs 删除超过保留期或超出总量上限的旧日志，只处理本程序生成的文件。
func pruneLogs(dir string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
		size int64
	}
	var items []item
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "gui-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, name), info.ModTime(), info.Size()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	var total int64
	for _, it := range items {
		total += it.size
		if now.Sub(it.mod) > logKeepDays*24*time.Hour || total > logTotalLimit {
			if os.Remove(it.path) == nil {
				total -= it.size
			}
		}
	}
}
