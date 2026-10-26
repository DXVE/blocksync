package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

func newPair(t *testing.T) (src, dst string) {
	t.Helper()
	root := t.TempDir()
	src, dst = filepath.Join(root, "src"), filepath.Join(root, "dst")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return src, dst
}

// 目标文件被外部改过、长度与索引不一致时，仍要按块比对，不能整文件重写。
func TestSizeMismatchKeepsBlockLevelWrite(t *testing.T) {
	src, dst := newPair(t)
	orig := rnd(256*1024, 51)
	write(t, filepath.Join(src, "disk.vmdk"), orig)
	run(t, src, dst, false)

	// 目标文件多出 64 KiB，原有内容未变
	grown := append(append([]byte(nil), orig...), rnd(64*1024, 52)...)
	write(t, filepath.Join(dst, "disk.vmdk"), grown)

	rep := run(t, src, dst, false)
	if rep.BytesWritten >= uint64(len(orig)) {
		t.Fatalf("长度不同不应整文件重写：写入 %d，文件 %d", rep.BytesWritten, len(orig))
	}
	equalTrees(t, src, dst)
}

// 源文件变长时同样只写变化的块。
func TestLongerSourceWritesOnlyChangedBlocks(t *testing.T) {
	src, dst := newPair(t)
	orig := rnd(256*1024, 61)
	write(t, filepath.Join(src, "disk.vmdk"), orig)
	run(t, src, dst, false)

	grown := append(append([]byte(nil), orig...), rnd(64*1024, 62)...)
	write(t, filepath.Join(src, "disk.vmdk"), grown)

	rep := run(t, src, dst, false)
	if rep.BytesWritten >= uint64(len(grown)) {
		t.Fatalf("长度变化不应整文件重写：写入 %d，文件 %d", rep.BytesWritten, len(grown))
	}
	equalTrees(t, src, dst)
}

// 试运行不落盘索引：随后的正式同步需要重新计算，结果仍然正确。
func TestDryRunDoesNotPersistIndexes(t *testing.T) {
	src, dst := newPair(t)
	data := rnd(128*1024, 71)
	write(t, filepath.Join(src, "disk.vmdk"), data)
	run(t, src, dst, false)

	changed := append([]byte(nil), data...)
	changed[1000] ^= 0xFF
	write(t, filepath.Join(src, "disk.vmdk"), changed)

	dry := run(t, src, dst, true)
	if dry.BytesRead == 0 {
		t.Fatal("试运行应读取源文件以计算差异")
	}
	rep := run(t, src, dst, false)
	if rep.BytesRead == 0 {
		t.Fatal("试运行不应保存索引，正式同步应重新计算")
	}
	if rep.BytesWritten == 0 || rep.BytesWritten >= uint64(len(changed)) {
		t.Fatalf("应只写变化块，实际 %d", rep.BytesWritten)
	}
	equalTrees(t, src, dst)
}

// 空文件必须被创建；源文件截断到 0 时目标也要跟着截断。
func TestEmptyFileAndTruncation(t *testing.T) {
	src, dst := newPair(t)
	write(t, filepath.Join(src, "zero.bin"), nil)
	write(t, filepath.Join(src, "data.bin"), rnd(64*1024, 31))
	run(t, src, dst, false)

	if st, err := os.Stat(filepath.Join(dst, "zero.bin")); err != nil || st.Size() != 0 {
		t.Fatalf("空文件应被创建：%v", err)
	}

	write(t, filepath.Join(src, "data.bin"), nil)
	run(t, src, dst, false)
	if st, err := os.Stat(filepath.Join(dst, "data.bin")); err != nil || st.Size() != 0 {
		t.Fatalf("源文件截断后目标应同样为 0 字节：%v", err)
	}
	equalTrees(t, src, dst)
}
