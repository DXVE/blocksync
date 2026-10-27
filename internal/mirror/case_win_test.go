//go:build windows

package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaseOnlyNameIsNotDeleted(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "Readme.txt"), []byte("source"))
	write(t, filepath.Join(dst, "readme.txt"), []byte("old"))

	if rep := run(t, src, dst, false); rep.FilesChanged != 1 {
		t.Fatalf("应更新 1 个文件，实际 %d", rep.FilesChanged)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != ".blocksync" {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 {
		t.Fatalf("目标应只保留一个文件，实际 %v", names)
	}
	if got, err := os.ReadFile(filepath.Join(dst, names[0])); err != nil || string(got) != "source" {
		t.Fatalf("目标内容不正确: err=%v 内容=%q", err, got)
	}
	if rep := run(t, src, dst, false); rep.FilesChanged != 0 {
		t.Fatalf("第二次同步不应再有改动，实际 %d", rep.FilesChanged)
	}
}

func TestCaseOnlyNameSameContent(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "Readme.txt"), []byte("same"))
	write(t, filepath.Join(dst, "readme.txt"), []byte("same"))

	if rep := run(t, src, dst, false); rep.FilesChanged != 0 {
		t.Fatalf("内容一致时不应改动，实际 %d", rep.FilesChanged)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != ".blocksync" {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 {
		t.Fatalf("目标应只保留一个文件，实际 %v", names)
	}
	if rep := run(t, src, dst, false); rep.FilesChanged != 0 || rep.BytesRead != 0 {
		t.Fatalf("重建索引后应恢复为空操作，实际改动 %d、读取 %d", rep.FilesChanged, rep.BytesRead)
	}
}
