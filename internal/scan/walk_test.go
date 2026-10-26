package scan

import (
	"os"
	"path/filepath"
	"testing"
)

// Windows 系统目录默认排除：目标盘为盘符根时不应因权限拒绝而中止。
func TestExcludedSystemDirs(t *testing.T) {
	cases := []string{
		"$RECYCLE.BIN/S-1-5-21-1-2-3-1001/desktop.ini",
		"$RECYCLE.BIN",
		"System Volume Information/tracking.db",
		"$WinREAgent/logs/backup.xml",
		"Config.Msi/abc.rbf",
	}
	for _, rel := range cases {
		if !Excluded(rel, DefaultExcludes) {
			t.Fatalf("应排除 %s", rel)
		}
	}
	if Excluded("Documents/data.bin", DefaultExcludes) {
		t.Fatal("普通文件不应被排除")
	}
}

// Walk 遇到被排除的系统目录时应整棵跳过，不返回内容也不报错。
func TestWalkSkipsSystemDirs(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(root, "keep.bin"))
	mk(filepath.Join(root, "$RECYCLE.BIN", "S-1-5-21-1", "desktop.ini"))
	mk(filepath.Join(root, "System Volume Information", "tracking.db"))

	files, err := Walk(root, DefaultExcludes)
	if err != nil {
		t.Fatalf("不应因系统目录报错: %v", err)
	}
	if len(files) != 1 || files[0].Rel != "keep.bin" {
		t.Fatalf("只应返回 keep.bin，实际 %+v", files)
	}
}
