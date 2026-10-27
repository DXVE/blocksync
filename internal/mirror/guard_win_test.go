//go:build windows

package mirror

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"blocksync/internal/scan"
)

func runErr(src, dst string) error {
	_, err := Run(Options{Source: src, Target: dst, LB: tLB, SB: tSB, Excludes: scan.DefaultExcludes})
	return err
}

// 输出目录里指向输入目录的联接必须被拒绝，且不得改动输入目录。
func TestRejectJunctionIntoSource(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	sub := filepath.Join(src, "sub", "a.txt")
	write(t, sub, []byte("SOURCE-NEW-CONTENT"))
	link := filepath.Join(dst, "sub")
	if err := exec.Command("cmd", "/c", "mklink", "/J", link, src).Run(); err != nil {
		t.Skipf("当前环境无法创建目录联接: %v", err)
	}
	defer os.Remove(link)

	err := runErr(src, dst)
	if err == nil || !strings.Contains(err.Error(), "输入目录内") {
		t.Fatalf("应拒绝经联接写入输入目录，实际 err=%v", err)
	}
	if got, _ := os.ReadFile(sub); string(got) != "SOURCE-NEW-CONTENT" {
		t.Fatalf("输入目录内容被改动: %q", got)
	}
	if _, err := os.Stat(filepath.Join(src, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("输入目录里出现了不该创建的文件")
	}
}

// 目标文件是源目录中另一个文件的硬链接时，写入会改动同一份数据，必须拒绝。
func TestRejectHardlinkedTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.bin"), rnd(8<<10, 1))
	target := filepath.Join(src, "b.bin")
	write(t, target, rnd(8<<10, 2))
	if err := os.Link(target, filepath.Join(dst, "a.bin")); err != nil {
		t.Skipf("当前环境无法创建硬链接: %v", err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	err = runErr(src, dst)
	if err == nil || !strings.Contains(err.Error(), "硬链接") {
		t.Fatalf("应拒绝写入硬链接目标，实际 err=%v", err)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("源文件内容被硬链接写入改动")
	}
}
