package mirror

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blocksync/internal/scan"
)

func countFiles(t *testing.T, root string) int {
	t.Helper()
	var n int
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == ".blocksync" || strings.HasPrefix(rel, ".blocksync"+string(filepath.Separator)) {
			return nil
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheckRootsRejectsOverlap(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	sub := filepath.Join(src, "inner")
	for _, d := range []string{src, dst, sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name    string
		src     string
		dst     string
		wantErr string
	}{
		{"相同目录", src, src, "相同"},
		{"输出是输入的上级", src, root, "包含输入目录"},
		{"输出是输入的下级", src, sub, "位于输入目录之内"},
	}
	for _, c := range cases {
		err := CheckRoots(c.src, c.dst)
		if err == nil {
			t.Fatalf("%s: 应拒绝", c.name)
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s: 错误信息应包含 %q，实际 %v", c.name, c.wantErr, err)
		}
	}
	if err := CheckRoots(src, dst); err != nil {
		t.Fatalf("互不包含的目录应通过: %v", err)
	}
}

// 输出目录是输入目录的上级时，镜像删除会把源文件当多余文件；必须在动手前拒绝。
func TestRunRefusesNestedTarget(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "a.bin"), rnd(1000, 1))
	write(t, filepath.Join(root, "other.bin"), rnd(100, 2))

	_, err := Run(Options{
		Source: src, Target: root, LB: tLB, SB: tSB,
		Excludes: scan.DefaultExcludes, AllowMassDelete: true,
	})
	if err == nil {
		t.Fatal("套在输入目录外的输出目录必须被拒绝")
	}
	if _, err := os.Stat(filepath.Join(src, "a.bin")); err != nil {
		t.Fatalf("源文件不应受影响: %v", err)
	}
}

// 相对路径（如 sync .\src .\dst）同样通过校验，不能被误判为越界。
func TestRunAcceptsRelativePaths(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"src", "dst"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "src", "a.bin"), rnd(4096, 8))

	t.Chdir(root)
	rep, err := Run(Options{Source: "src", Target: "dst", LB: tLB, SB: tSB, Excludes: scan.DefaultExcludes})
	if err != nil {
		t.Fatalf("相对路径不应被拒绝: %v", err)
	}
	if rep.FilesChanged != 1 {
		t.Fatalf("应写入 1 个文件，实际 %d", rep.FilesChanged)
	}
	if _, err := os.Stat(filepath.Join(root, "dst", "a.bin")); err != nil {
		t.Fatalf("目标文件未写入: %v", err)
	}
}

func TestMassDeleteRequiresOptIn(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(src, "keep.bin"), rnd(100, 3))
	for i := 0; i < 120; i++ {
		write(t, filepath.Join(dst, fmt.Sprintf("old-%03d.bin", i)), []byte("x"))
	}

	o := Options{Source: src, Target: dst, LB: tLB, SB: tSB, Excludes: scan.DefaultExcludes}
	if _, err := Run(o); err == nil || !strings.Contains(err.Error(), "安全上限") {
		t.Fatalf("应触发批量删除保护，实际 %v", err)
	}
	if n := countFiles(t, dst); n != 120 {
		t.Fatalf("被拒绝时目标目录不应改变，实际 %d 个文件", n)
	}

	o.AllowMassDelete = true
	if _, err := Run(o); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, dst); n != 1 {
		t.Fatalf("放行后应只剩源端文件，实际 %d 个", n)
	}
}

func TestConfirmCancelWritesNothing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(src, "a.bin"), rnd(500, 4))
	write(t, filepath.Join(dst, "old.bin"), rnd(20, 5))

	asked := 0
	_, err := Run(Options{
		Source: src, Target: dst, LB: tLB, SB: tSB,
		Excludes: scan.DefaultExcludes,
		Confirm: func(p Plan) (bool, error) {
			asked++
			if !p.NeedsConfirm() {
				t.Error("只在需要确认时才应询问")
			}
			if p.DeleteFiles != 1 || p.NewFiles != 1 {
				t.Errorf("计划统计不符: %+v", p)
			}
			return false, nil
		},
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("应返回取消，实际 %v", err)
	}
	if asked != 1 {
		t.Fatalf("应询问一次，实际 %d", asked)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.bin")); !os.IsNotExist(err) {
		t.Fatal("取消后不应写入任何文件")
	}
	if _, err := os.Stat(filepath.Join(dst, "old.bin")); err != nil {
		t.Fatal("取消后不应删除任何文件")
	}
}

// 日常运行（无删除、非首次）不应打扰用户。
func TestConfirmSkippedWhenRoutine(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(src, "a.bin"), rnd(4096, 6))
	if _, err := Run(Options{Source: src, Target: dst, LB: tLB, SB: tSB, Excludes: scan.DefaultExcludes}); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(src, "a.bin"), rnd(4096, 7))
	asked := 0
	if _, err := Run(Options{
		Source: src, Target: dst, LB: tLB, SB: tSB,
		Excludes: scan.DefaultExcludes,
		Confirm: func(Plan) (bool, error) {
			asked++
			return true, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if asked != 0 {
		t.Fatalf("常规增量运行不应询问，实际询问 %d 次", asked)
	}
}
