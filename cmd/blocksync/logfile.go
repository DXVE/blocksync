package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"blocksync/internal/version"
)

// startLogTee 把本进程的标准输出与标准错误同时写进日志文件。
// 两路各建一条管道以保留 stdout/stderr 的区分（重定向语义不变），文件写入串行化。
// 返回的收尾函数写入退出码并关闭文件；path 为空时返回 (nil, nil)。
func startLogTee(path string) (stop func(code int), err error) {
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	// 头部直接写文件：此时标准输出尚未被接管
	fmt.Fprintf(f, "===== %s  blocksync %s  %s =====\n",
		time.Now().Format("2006-01-02 15:04:05"), version.Version, strings.Join(os.Args[1:], " "))

	locked := &lockedWriter{w: f}
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		_ = f.Close()
		return nil, err
	}
	os.Stdout, os.Stderr = outW, errW

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(io.MultiWriter(oldOut, locked), outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(io.MultiWriter(oldErr, locked), errR) }()

	return func(code int) {
		// 先让两路排空，再写收尾与关闭，避免尾部内容落在文件之外
		_ = outW.Close()
		_ = errW.Close()
		wg.Wait()
		fmt.Fprintf(f, "===== 结束：退出码 %d =====\n", code)
		_ = f.Close()
	}, nil
}

// lockedWriter 串行化两路输出对同一文件的写入，避免两次写入交错。
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
