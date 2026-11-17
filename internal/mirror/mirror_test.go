package mirror

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// 源文件在复制途中变短时必须报错，不能把缓冲区残留内容写进目标。
func TestCopyRangesDetectsShortSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	if err := os.WriteFile(src, bytes.Repeat([]byte("A"), 3000), 0o644); err != nil {
		t.Fatal(err)
	}
	ranges := []scan.Range{{Off: 0, Len: 4096}, {Off: 4096, Len: 904}}
	if err := copyRanges(src, dst, ranges, 5000, 0); err == nil {
		t.Fatal("源文件短读应返回错误，实际静默写入了目标")
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

func TestFirstSyncReadsSourceOnce(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	data := rnd(400*1024, 21)
	write(t, filepath.Join(src, "a.vmdk"), data)

	rep := run(t, src, dst, false)
	if rep.BytesRead != uint64(len(data)) {
		t.Fatalf("首次同步应只读取一次源文件，实际读取 %d 字节", rep.BytesRead)
	}
	if rep.BytesWritten != uint64(len(data)) {
		t.Fatalf("首次同步写入量应为文件大小，实际 %d 字节", rep.BytesWritten)
	}
	equalTrees(t, src, dst)
}

func TestFirstSyncWithVerify(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	data := rnd(300*1024, 26)
	write(t, filepath.Join(src, "a.vmdk"), data)

	rep, err := Run(Options{Source: src, Target: dst, LB: tLB, SB: tSB, Verify: true, Excludes: scan.DefaultExcludes})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesChanged != 1 || rep.BytesWritten != uint64(len(data)) {
		t.Fatalf("首次同步应写入 1 个文件，实际改动 %d 个、写入 %d 字节", rep.FilesChanged, rep.BytesWritten)
	}
	equalTrees(t, src, dst)

	rep, err = Run(Options{Source: src, Target: dst, LB: tLB, SB: tSB, Verify: true, Excludes: scan.DefaultExcludes})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesChanged != 0 || rep.BytesRead != 0 || rep.BytesWritten != 0 {
		t.Fatalf("写后校验后的空操作不应读写，实际改动 %d、读 %d、写 %d", rep.FilesChanged, rep.BytesRead, rep.BytesWritten)
	}
}

func TestNoOpSyncKeepsIndexes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.vmdk"), rnd(300*1024, 22))
	run(t, src, dst, false)

	fixed := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	var files []string
	for _, root := range []string{src, dst} {
		idxRoot := filepath.Join(root, ".blocksync", "idx")
		err := filepath.Walk(idxRoot, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			files = append(files, p)
			return os.Chtimes(p, fixed, fixed)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		t.Fatal("未找到索引文件")
	}

	run(t, src, dst, false)
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(fixed) {
			t.Fatalf("无变化同步不应重写索引: %s", p)
		}
	}
}

func TestMissingSourceIndexDoesNotRewrite(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	data := rnd(300*1024, 23)
	write(t, filepath.Join(src, "a.vmdk"), data)
	run(t, src, dst, false)

	if err := os.RemoveAll(filepath.Join(src, ".blocksync", "idx")); err != nil {
		t.Fatal(err)
	}
	rep := run(t, src, dst, false)
	if rep.FilesChanged != 0 || rep.BytesWritten != 0 {
		t.Fatalf("源索引缺失不应改写目标，实际改动 %d 个、写入 %d 字节", rep.FilesChanged, rep.BytesWritten)
	}
	if rep.BytesRead != uint64(len(data)) {
		t.Fatalf("源索引缺失应只重读源文件，实际读取 %d 字节", rep.BytesRead)
	}
	if rep := run(t, src, dst, false); rep.BytesRead != 0 || rep.BytesWritten != 0 {
		t.Fatalf("重建索引后应恢复为空操作，实际读取 %d、写入 %d", rep.BytesRead, rep.BytesWritten)
	}
}

func TestMissingTargetIndexDoesNotRewrite(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	data := rnd(300*1024, 24)
	write(t, filepath.Join(src, "a.vmdk"), data)
	run(t, src, dst, false)

	if err := os.RemoveAll(filepath.Join(dst, ".blocksync", "idx")); err != nil {
		t.Fatal(err)
	}
	rep := run(t, src, dst, false)
	if rep.FilesChanged != 0 || rep.BytesWritten != 0 {
		t.Fatalf("目标索引缺失不应改写目标，实际改动 %d 个、写入 %d 字节", rep.FilesChanged, rep.BytesWritten)
	}
	if rep.BytesRead != uint64(len(data)) {
		t.Fatalf("目标索引缺失应只重读目标文件，实际读取 %d 字节", rep.BytesRead)
	}
	if rep := run(t, src, dst, false); rep.BytesRead != 0 || rep.BytesWritten != 0 {
		t.Fatalf("重建索引后应恢复为空操作，实际读取 %d、写入 %d", rep.BytesRead, rep.BytesWritten)
	}
}

func TestBothIndexesMissingDoesNotRewrite(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	data := rnd(300*1024, 27)
	write(t, filepath.Join(src, "a.vmdk"), data)
	run(t, src, dst, false)

	for _, root := range []string{src, dst} {
		if err := os.RemoveAll(filepath.Join(root, ".blocksync", "idx")); err != nil {
			t.Fatal(err)
		}
	}
	rep := run(t, src, dst, false)
	if rep.FilesChanged != 0 || rep.BytesWritten != 0 {
		t.Fatalf("两侧索引缺失但内容一致时不应改写目标，实际改动 %d 个、写入 %d 字节", rep.FilesChanged, rep.BytesWritten)
	}
	if rep.BytesRead != 2*uint64(len(data)) {
		t.Fatalf("两侧索引缺失应各重读一次，实际读取 %d 字节", rep.BytesRead)
	}
	if rep := run(t, src, dst, false); rep.BytesRead != 0 || rep.BytesWritten != 0 {
		t.Fatalf("重建索引后应恢复为空操作，实际读取 %d、写入 %d", rep.BytesRead, rep.BytesWritten)
	}
}

func TestIndexesNotRewrittenAfterUpdate(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "a.vmdk")
	orig := rnd(300*1024, 25)
	write(t, p, orig)
	run(t, src, dst, false)

	mod := append([]byte(nil), orig...)
	copy(mod[1000:1100], []byte("changed"))
	write(t, p, mod)
	if rep := run(t, src, dst, false); rep.FilesChanged != 1 {
		t.Fatalf("应改动 1 个文件，实际 %d", rep.FilesChanged)
	}

	fixed := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	var files []string
	for _, root := range []string{src, dst} {
		idxRoot := filepath.Join(root, ".blocksync", "idx")
		err := filepath.Walk(idxRoot, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			files = append(files, p)
			return os.Chtimes(p, fixed, fixed)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if rep := run(t, src, dst, false); rep.FilesChanged != 0 || rep.BytesRead != 0 || rep.BytesWritten != 0 {
		t.Fatalf("增量同步后的空操作不应读写，实际改动 %d、读 %d、写 %d", rep.FilesChanged, rep.BytesRead, rep.BytesWritten)
	}
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(fixed) {
			t.Fatalf("增量同步后的空操作不应重写索引: %s", p)
		}
	}
}

// 哈希计算行后紧跟同号结果行，结果行后紧跟同号完成行；dry-run 也一样。
func TestProgressLinesGetDoneMarker(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	write(t, filepath.Join(src, "a.bin"), rnd(200<<10, 1))
	write(t, filepath.Join(src, "b.bin"), rnd(100<<10, 2))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	check := func(dry bool) {
		var out bytes.Buffer
		if _, err := Run(Options{
			Source: src, Target: dst, LB: tLB, SB: tSB,
			DryRun: dry, Excludes: scan.DefaultExcludes, Out: &out,
		}); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		results, calcs := 0, 0
		for i := 0; i+1 < len(lines); i++ {
			m := ProgressPattern.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			status := strings.TrimSpace(m[3])
			n := ProgressPattern.FindStringSubmatch(lines[i+1])
			switch {
			case strings.HasPrefix(status, CalcMarker):
				if n == nil || n[1] != m[1] || strings.HasPrefix(strings.TrimSpace(n[3]), DoneMarker) ||
					strings.HasPrefix(strings.TrimSpace(n[3]), CalcMarker) {
					t.Fatalf("哈希计算行后应紧跟同号结果行，实际输出:\n%s", out.String())
				}
				calcs++
			case strings.HasPrefix(status, DoneMarker):
			default:
				if n == nil || !strings.HasPrefix(strings.TrimSpace(n[3]), DoneMarker) || n[1] != m[1] {
					t.Fatalf("进度行后应紧跟同号完成行，实际输出:\n%s", out.String())
				}
				results++
			}
		}
		if results != 2 || calcs != 2 {
			t.Fatalf("dry=%v 时应有 2 个结果行与 2 个哈希计算行，实际 %d / %d，输出:\n%s", dry, results, calcs, out.String())
		}
	}
	check(true)
	check(false)
}

// 两份索引都新鲜时不应输出哈希计算行：没有任何整文件读取。
func TestNoCalcLineWhenIndexesFresh(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.vmdk"), rnd(120<<10, 3))
	run(t, src, dst, false)

	var out bytes.Buffer
	if _, err := Run(Options{
		Source: src, Target: dst, LB: tLB, SB: tSB,
		Excludes: scan.DefaultExcludes, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), CalcMarker) {
		t.Fatalf("索引全部可用时不应输出哈希计算行，实际输出:\n%s", out.String())
	}
}

// 逐文件行的状态、原因与说明按约定排版；变短与清空并入更新。
func TestProgressLineFormat(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "a.bin")
	data := rnd(200<<10, 31)
	write(t, p, data)

	runOut := func() string {
		var out bytes.Buffer
		if _, err := Run(Options{
			Source: src, Target: dst, LB: tLB, SB: tSB,
			Excludes: scan.DefaultExcludes, Out: &out,
		}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	first := runOut()
	if want := "[1/1] " + CalcMarker + "  a.bin  " + scan.FormatSize(uint64(len(data))); !strings.Contains(first, want) {
		t.Fatalf("缺少哈希计算行 %q，实际输出:\n%s", want, first)
	}
	if want := "[1/1] 新增  a.bin  " + scan.FormatSize(uint64(len(data))); !strings.Contains(first, want) {
		t.Fatalf("新增行应只写文件体积 %q，实际输出:\n%s", want, first)
	}

	mod := append([]byte(nil), data...)
	copy(mod[1000:1100], []byte("changed"))
	write(t, p, mod)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	second := runOut()
	if want := "[1/1] 更新  a.bin  （源文件修改时间变化）  变化 "; !strings.Contains(second, want) {
		t.Fatalf("更新行应在路径后给出原因与变化量 %q，实际输出:\n%s", want, second)
	}
	if strings.Contains(second, "变化块") || strings.Contains(second, "文件共") {
		t.Fatalf("旧的说明写法不应再出现，实际输出:\n%s", second)
	}
}

// 变短且剩余内容一致时只需截断，状态仍归入更新。
func TestTruncateLineFormat(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "b.bin")
	data := rnd(200<<10, 32)
	write(t, p, data)
	run(t, src, dst, false)

	if err := os.WriteFile(p, data[:100<<10], 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Run(Options{
		Source: src, Target: dst, LB: tLB, SB: tSB,
		Excludes: scan.DefaultExcludes, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if want := "[1/1] 更新  b.bin  （源文件长度变化）  截断到 100.0 KiB"; !strings.Contains(out.String(), want) {
		t.Fatalf("变短应归入更新并给出截断说明 %q，实际输出:\n%s", want, out.String())
	}
}

// 空文件不会实际读盘，不输出哈希计算行。
func TestEmptyFileNoCalcLine(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "empty.bin"), nil)

	var out bytes.Buffer
	if _, err := Run(Options{
		Source: src, Target: dst, LB: tLB, SB: tSB,
		Excludes: scan.DefaultExcludes, Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), CalcMarker) {
		t.Fatalf("空文件不应输出哈希计算行，实际输出:\n%s", out.String())
	}
}

// 汇总的读取量要包含区间复制与写入后校验的读回。
func TestBytesReadIncludesRangesAndVerify(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	p := filepath.Join(src, "a.vmdk")
	data := rnd(200<<10, 33)
	write(t, p, data)
	run(t, src, dst, false)

	mod := append([]byte(nil), data...)
	copy(mod[1000:1100], []byte("changed"))
	write(t, p, mod)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Options{
		Source: src, Target: dst, LB: tLB, SB: tSB, Verify: true,
		Excludes: scan.DefaultExcludes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.BytesRead < 2*uint64(len(mod)) {
		t.Fatalf("读取应计入源扫描、区间复制与校验复读，实际 %d 字节", rep.BytesRead)
	}
	if rep.BytesWritten > 2*uint64(tSB) {
		t.Fatalf("写入量应接近一个细块，实际 %d", rep.BytesWritten)
	}
}
