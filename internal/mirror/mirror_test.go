package mirror

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"blocksync/internal/scan"
)

const (
	tLB = int64(64 << 10)
	tSB = int64(4 << 10)
)

func rnd(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func write(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, src, dst string, dry bool) *Report {
	t.Helper()
	rep, err := Run(Options{
		Source: src, Target: dst,
		LB: tLB, SB: tSB,
		DryRun:   dry,
		Excludes: scan.DefaultExcludes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func equalTrees(t *testing.T, a, b string) {
	t.Helper()
	read := func(root string) map[string][]byte {
		m := map[string][]byte{}
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return nil
			}
			if rel == ".blocksync" || filepath.Base(filepath.Dir(rel)) == ".blocksync" || rel == ".blocksync"+string(filepath.Separator) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m[filepath.ToSlash(rel)] = b
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	ma, mb := read(a), read(b)
	if len(ma) != len(mb) {
		t.Fatalf("文件数不同: %d vs %d", len(ma), len(mb))
	}
	for k, va := range ma {
		vb, ok := mb[k]
		if !ok {
			t.Fatalf("目标缺少 %s", k)
		}
		if !bytes.Equal(va, vb) {
			t.Fatalf("内容不同: %s", k)
		}
	}
}

func TestFirstSyncCopiesEverything(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.vmdk"), rnd(200*1024, 1))
	write(t, filepath.Join(src, "sub", "b.vmx"), []byte("hello"))

	rep := run(t, src, dst, false)
	if rep.FilesChanged != 2 {
		t.Fatalf("首次应复制 2 个文件，实际 %d", rep.FilesChanged)
	}
	equalTrees(t, src, dst)
}

func TestSecondSyncReadsNothing(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.vmdk"), rnd(300*1024, 2))
	run(t, src, dst, false)

	rep := run(t, src, dst, false)
	if rep.FilesChanged != 0 {
		t.Fatalf("无变化时不应写文件，实际 %d", rep.FilesChanged)
	}
	if rep.BytesRead != 0 {
		t.Fatalf("两份索引都新鲜时不应读取任何数据，实际读了 %d 字节", rep.BytesRead)
	}
	if rep.BytesWritten != 0 {
		t.Fatalf("无变化时不应写入，实际 %d", rep.BytesWritten)
	}
}

func TestIncrementalWritesOnlyChangedBlocks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "a.vmdk")
	orig := rnd(512*1024, 3)
	write(t, p, orig)
	run(t, src, dst, false)

	mod := append([]byte(nil), orig...)
	copy(mod[100000:100100], rnd(100, 9))
	write(t, p, mod)

	rep := run(t, src, dst, false)
	if rep.FilesChanged != 1 {
		t.Fatalf("应只有 1 个文件变化，实际 %d", rep.FilesChanged)
	}
	if rep.BytesWritten > 2*uint64(tSB) {
		t.Fatalf("写入量应接近一个细块，实际 %d", rep.BytesWritten)
	}
	equalTrees(t, src, dst)
}

func TestDryRunWritesNothing(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.vmdk"), rnd(100*1024, 4))

	rep := run(t, src, dst, true)
	if rep.FilesChanged != 1 {
		t.Fatalf("dry-run 也应报告将改动 1 个文件，实际 %d", rep.FilesChanged)
	}
	if ents, _ := os.ReadDir(dst); len(ents) != 0 {
		t.Fatalf("dry-run 不应写入任何内容，实际目标目录有 %d 项", len(ents))
	}
}

func TestDeleteMirrors(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.vmdk"), rnd(50*1024, 5))
	write(t, filepath.Join(src, "drop.vmdk"), rnd(50*1024, 6))
	run(t, src, dst, false)

	if err := os.Remove(filepath.Join(src, "drop.vmdk")); err != nil {
		t.Fatal(err)
	}
	rep := run(t, src, dst, false)
	if rep.FilesDeleted != 1 {
		t.Fatalf("应删除 1 个文件，实际 %d", rep.FilesDeleted)
	}
	if _, err := os.Stat(filepath.Join(dst, "drop.vmdk")); !os.IsNotExist(err) {
		t.Fatal("目标端未删除")
	}
	equalTrees(t, src, dst)
}

func TestTargetModifiedExternally(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	orig := rnd(300*1024, 7)
	write(t, filepath.Join(src, "a.vmdk"), orig)
	write(t, filepath.Join(dst, "a.vmdk"), orig)
	run(t, src, dst, false)

	// 人为破坏目标端，索引随之过期
	bad := append([]byte(nil), orig...)
	copy(bad[5000:5100], rnd(100, 8))
	write(t, filepath.Join(dst, "a.vmdk"), bad)

	run(t, src, dst, false)
	equalTrees(t, src, dst)
}

func TestRoundTripBack(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	orig := rnd(256*1024, 10)
	write(t, filepath.Join(a, "disk.vmdk"), orig)
	run(t, a, b, false)

	// 在 a 侧改动并推到 b（模拟离开某个地点前）
	mod := append([]byte(nil), orig...)
	copy(mod[70000:70500], []byte("CHANGED-ON-THE-OTHER-SIDE"))
	write(t, filepath.Join(a, "disk.vmdk"), mod)
	if rep := run(t, a, b, false); rep.FilesChanged != 1 {
		t.Fatalf("应改动 1 个文件，实际 %d", rep.FilesChanged)
	}

	// 到另一端后反向同步：两侧索引都新鲜，应既不读也不写
	rep := run(t, b, a, false)
	if rep.FilesChanged != 0 || rep.BytesRead != 0 {
		t.Fatalf("反向同步应为空操作，实际改动 %d 个、读了 %d 字节", rep.FilesChanged, rep.BytesRead)
	}
	equalTrees(t, a, b)
}

func TestVerifyAndSizeShrink(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "a.vmdk")
	orig := rnd(400*1024, 11)
	write(t, p, orig)
	run(t, src, dst, false)

	write(t, p, orig[:250*1024])
	rep, err := Run(Options{Source: src, Target: dst, LB: tLB, SB: tSB, Verify: true, Excludes: scan.DefaultExcludes})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesChanged != 1 {
		t.Fatalf("截断应计为 1 个改动，实际 %d", rep.FilesChanged)
	}
	st, err := os.Stat(filepath.Join(dst, "a.vmdk"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 250*1024 {
		t.Fatalf("目标长度应为 250 KiB，实际 %d", st.Size())
	}
	equalTrees(t, src, dst)
}
