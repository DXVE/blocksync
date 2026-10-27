package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"blocksync/internal/state"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "blocksync-bin-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "blocksync.exe")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "构建测试二进制失败: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		t.Fatalf("blocksync %s 失败: %v\n%s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String()
}

func runExpectFail(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err == nil {
		t.Fatalf("blocksync %s 本应失败但成功了\n%s", strings.Join(args, " "), buf.String())
	}
	return buf.String()
}

func rnd(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func writeFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyTree 复制目录内容，跳过 .blocksync（模拟一次性全量拷贝到对端）。
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".blocksync" || strings.HasPrefix(rel, ".blocksync"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func siteFile(t *testing.T, local string) struct {
	SiteID     string `json:"siteId"`
	Generation uint64 `json:"generation"`
	SetDigest  string `json:"setDigest"`
} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(local, ".blocksync", "site.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		SiteID     string `json:"siteId"`
		Generation uint64 `json:"generation"`
		SetDigest  string `json:"setDigest"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// assertTreesEqual 比较两个目录的文件内容（忽略 .blocksync）。
func assertTreesEqual(t *testing.T, a, b string) {
	t.Helper()
	read := func(root string) map[string][]byte {
		out := map[string][]byte{}
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." || rel == ".blocksync" || strings.HasPrefix(rel, ".blocksync"+string(filepath.Separator)) {
				if info.IsDir() && rel != "." {
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
			out[filepath.ToSlash(rel)] = b
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	ta, tb := read(a), read(b)
	if len(ta) != len(tb) {
		t.Fatalf("文件数量不同: %d vs %d\nA=%v\nB=%v", len(ta), len(tb), keys(ta), keys(tb))
	}
	for k, va := range ta {
		vb, ok := tb[k]
		if !ok {
			t.Fatalf("B 缺少文件 %s", k)
		}
		if !bytes.Equal(va, vb) {
			t.Fatalf("文件内容不同: %s", k)
		}
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

const (
	testLBArg = "--lb=65536"
	testSBArg = "--sb=4096"
)

func setup(t *testing.T) (root, a, b, c string) {
	t.Helper()
	root = t.TempDir()
	a = filepath.Join(root, "siteA", "vm")
	b = filepath.Join(root, "siteB", "vm")
	c = filepath.Join(root, "carrier")
	for _, d := range []string{a, b, c} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestEndToEndRoundTrip(t *testing.T) {
	_, a, b, c := setup(t)

	writeFile(t, filepath.Join(a, "disk-s001.vmdk"), rnd(1536*1024, 1))
	writeFile(t, filepath.Join(a, "disk-s002.vmdk"), rnd(700*1024, 2))
	writeFile(t, filepath.Join(a, "vm.vmx"), []byte("config.version = \"8\"\ndisplayName = \"dev\"\n"))

	run(t, "init", a, "--site", "A", testLBArg, testSBArg)
	copyTree(t, a, b)
	run(t, "init", b, "--site", "B", testLBArg, testSBArg)

	if siteFile(t, a).SetDigest != siteFile(t, b).SetDigest {
		t.Fatal("两端内容一致时状态摘要必须相同")
	}
	assertTreesEqual(t, a, b)

	// 无改动时 push 应报告无需搬运
	out := run(t, "push", a, c, testLBArg, testSBArg)
	if !strings.Contains(out, "无需搬运") {
		t.Fatalf("未改动时应报告无需搬运，实际输出:\n%s", out)
	}

	// 做几处小改动
	p1 := filepath.Join(a, "disk-s001.vmdk")
	d, _ := os.ReadFile(p1)
	d[1000] ^= 0xFF
	d[800000] ^= 0x0F
	writeFile(t, p1, d)

	p2 := filepath.Join(a, "disk-s002.vmdk")
	d2, _ := os.ReadFile(p2)
	writeFile(t, p2, append(d2, []byte("appended-tail")...))

	writeFile(t, filepath.Join(a, "vm.vmx"), []byte("config.version = \"8\"\ndisplayName = \"dev-2\"\n"))

	// dry-run 不应产生任何补丁
	run(t, "push", a, c, "--dry-run", testLBArg, testSBArg)
	if pats, _ := filepath.Glob(filepath.Join(c, "blocksync", "A", "*.blkpatch")); len(pats) != 0 {
		t.Fatalf("dry-run 不应写出补丁，实际 %v", pats)
	}

	run(t, "push", a, c, testLBArg, testSBArg)
	pats, _ := filepath.Glob(filepath.Join(c, "blocksync", "A", "*.blkpatch"))
	if len(pats) != 1 {
		t.Fatalf("应生成 1 个补丁，实际 %v", pats)
	}
	st, err := os.Stat(pats[0])
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 120*1024 {
		t.Fatalf("补丁体积应远小于数据量，实际 %d 字节", st.Size())
	}
	t.Logf("补丁体积 %d 字节（原始数据 %d 字节）", st.Size(), 1536*1024+700*1024)

	run(t, "apply", b, c, "--verify")
	assertTreesEqual(t, a, b)

	if siteFile(t, a).SetDigest != siteFile(t, b).SetDigest {
		t.Fatal("同步后两端摘要必须相同")
	}

	// 应用后再推一次应无变更
	out = run(t, "push", b, c, testLBArg, testSBArg)
	if !strings.Contains(out, "无需搬运") {
		t.Fatalf("对端应用后应无变更，实际输出:\n%s", out)
	}

	if out := run(t, "verify", b); !strings.Contains(out, "校验通过") {
		t.Fatalf("verify 应通过，实际:\n%s", out)
	}
}

func TestEndToEndDelete(t *testing.T) {
	_, a, b, c := setup(t)
	writeFile(t, filepath.Join(a, "keep.vmdk"), rnd(200*1024, 3))
	writeFile(t, filepath.Join(a, "drop.vmdk"), rnd(200*1024, 4))
	run(t, "init", a, "--site", "A", testLBArg, testSBArg)
	copyTree(t, a, b)
	run(t, "init", b, "--site", "B", testLBArg, testSBArg)

	if err := os.Remove(filepath.Join(a, "drop.vmdk")); err != nil {
		t.Fatal(err)
	}
	run(t, "push", a, c, testLBArg, testSBArg)
	run(t, "apply", b, c)

	if _, err := os.Stat(filepath.Join(b, "drop.vmdk")); !os.IsNotExist(err) {
		t.Fatal("删除未同步到对端")
	}
	assertTreesEqual(t, a, b)
}

func TestEndToEndChain(t *testing.T) {
	_, a, b, c := setup(t)
	writeFile(t, filepath.Join(a, "disk.vmdk"), rnd(300*1024, 5))
	run(t, "init", a, "--site", "A", testLBArg, testSBArg)
	copyTree(t, a, b)
	run(t, "init", b, "--site", "B", testLBArg, testSBArg)

	// 连续两次推送，一次应用应把两段补丁都吃掉
	p := filepath.Join(a, "disk.vmdk")
	d, _ := os.ReadFile(p)
	d[500] ^= 1
	writeFile(t, p, d)
	run(t, "push", a, c, testLBArg, testSBArg)

	d[90000] ^= 2
	writeFile(t, p, d)
	run(t, "push", a, c, testLBArg, testSBArg)

	pats, _ := filepath.Glob(filepath.Join(c, "blocksync", "A", "*.blkpatch"))
	if len(pats) != 2 {
		t.Fatalf("应有 2 个补丁，实际 %d", len(pats))
	}

	run(t, "apply", b, c)
	assertTreesEqual(t, a, b)
	if siteFile(t, a).SetDigest != siteFile(t, b).SetDigest {
		t.Fatal("链式应用后摘要必须一致")
	}
}

func TestEndToEndDivergenceRefused(t *testing.T) {
	_, a, b, c := setup(t)
	writeFile(t, filepath.Join(a, "disk.vmdk"), rnd(200*1024, 6))
	run(t, "init", a, "--site", "A", testLBArg, testSBArg)
	copyTree(t, a, b)
	run(t, "init", b, "--site", "B", testLBArg, testSBArg)

	p := filepath.Join(a, "disk.vmdk")
	d, _ := os.ReadFile(p)
	d[100] ^= 1
	writeFile(t, p, d)
	run(t, "push", a, c, testLBArg, testSBArg)

	d[200] ^= 2
	writeFile(t, p, d)
	run(t, "push", a, c, testLBArg, testSBArg)

	// 删掉第一段补丁，制造断链
	pats, _ := filepath.Glob(filepath.Join(c, "blocksync", "A", "*.blkpatch"))
	if len(pats) != 2 {
		t.Fatalf("应有 2 个补丁，实际 %d", len(pats))
	}
	if err := os.Remove(pats[0]); err != nil {
		t.Fatal(err)
	}

	out := runExpectFail(t, "apply", b, c)
	if !strings.Contains(out, "不衔接") {
		t.Fatalf("应报告补丁链不衔接，实际:\n%s", out)
	}
	// 目标端内容不能被改动
	if got, _ := os.ReadFile(filepath.Join(b, "disk.vmdk")); !bytes.Equal(got, rnd(200*1024, 6)) {
		t.Fatal("拒绝应用时不应改动目标文件")
	}
}

// .lck 目录不再阻断同步，也不参与传输（默认排除）。
func TestEndToEndLckIgnored(t *testing.T) {
	_, a, b, c := setup(t)
	writeFile(t, filepath.Join(a, "disk.vmdk"), rnd(64*1024, 7))
	run(t, "init", a, "--site", "A", testLBArg, testSBArg)
	copyTree(t, a, b)
	run(t, "init", b, "--site", "B", testLBArg, testSBArg)

	if err := os.MkdirAll(filepath.Join(a, "disk.vmdk.lck"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a, "disk.vmdk.lck", "disk.lck"), []byte("lock"))

	run(t, "push", a, c, testLBArg, testSBArg)
	run(t, "apply", b, c)
	if _, err := os.Stat(filepath.Join(b, "disk.vmdk.lck")); !os.IsNotExist(err) {
		t.Fatal(".lck 目录不应被同步到对端")
	}
	if got, _ := os.ReadFile(filepath.Join(b, "disk.vmdk")); !bytes.Equal(got, rnd(64*1024, 7)) {
		t.Fatal("数据文件内容不正确")
	}
}

func TestEndToEndWithoutInit(t *testing.T) {
	_, a, b, c := setup(t)
	writeFile(t, filepath.Join(a, "disk.vmdk"), rnd(300*1024, 31))
	writeFile(t, filepath.Join(a, "vm.vmx"), []byte("a = 1\n"))

	// 两端都不 init，直接 push/apply 应能完成首次全量投递
	run(t, "push", a, c, "--site", "A", testLBArg, testSBArg)
	run(t, "apply", b, c, "--site", "B")
	assertTreesEqual(t, a, b)
}

func TestVerifyDetectsAndRepairFixes(t *testing.T) {
	_, a, _, _ := setup(t)
	p := filepath.Join(a, "disk.vmdk")
	orig := rnd(256*1024, 41)
	writeFile(t, p, orig)
	run(t, "init", a, "--site", "A", testLBArg, testSBArg)

	if out := run(t, "verify", a); !strings.Contains(out, "校验通过") {
		t.Fatalf("初始校验应通过:\n%s", out)
	}

	d := append([]byte(nil), orig...)
	d[12345] ^= 0xFF
	writeFile(t, p, d)

	out := runExpectFail(t, "verify", a)
	if !strings.Contains(out, "内容不符") {
		t.Fatalf("应报告内容不符:\n%s", out)
	}

	run(t, "repair", a)
	if out := run(t, "verify", a); !strings.Contains(out, "校验通过") {
		t.Fatalf("repair 后校验应通过:\n%s", out)
	}
}

func TestEndToEndRandomRounds(t *testing.T) {
	_, a, b, c := setup(t)
	names := []string{"disk-s001.vmdk", "disk-s002.vmdk", "disk-s003.vmdk", "vm.vmx", "extra.cfg"}
	for i, n := range names {
		writeFile(t, filepath.Join(a, n), rnd(80000+i*61000, int64(100+i)))
	}
	run(t, "init", a, "--site", "A", testLBArg, testSBArg)
	copyTree(t, a, b)
	run(t, "init", b, "--site", "B", testLBArg, testSBArg)

	rng := rand.New(rand.NewSource(2024))
	for round := 0; round < 20; round++ {
		for _, n := range names {
			p := filepath.Join(a, n)
			d, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if len(d) == 0 {
				continue
			}
			switch rng.Intn(6) {
			case 0:
				d[rng.Intn(len(d))] ^= byte(1 + rng.Intn(255))
			case 1:
				off := rng.Intn(len(d))
				end := off + rng.Intn(3000)
				if end > len(d) {
					end = len(d)
				}
				copy(d[off:end], rnd(end-off, int64(round*len(names)+len(n))))
			case 2:
				d = append(d, rnd(rng.Intn(9000), int64(round))...)
			case 3:
				if len(d) > 20000 {
					d = d[:len(d)-rng.Intn(9000)]
				}
			case 4:
				writeFile(t, p, rnd(len(d), int64(round)+99))
				continue
			default:
				continue
			}
			writeFile(t, p, d)
		}

		run(t, "push", a, c, testLBArg, testSBArg)
		run(t, "apply", b, c, "--verify")
		assertTreesEqual(t, a, b)
		if siteFile(t, a).SetDigest != siteFile(t, b).SetDigest {
			t.Fatalf("第 %d 轮后两端摘要不一致", round)
		}
	}
}

// 目录对排除列表：两侧同时生效，被排除的文件不复制、目标侧已有的同名文件不删除。
func TestEndToEndPairExcludes(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	_, a, b, _ := setup(t)
	writeFile(t, filepath.Join(a, "keep.txt"), []byte("keep"))
	writeFile(t, filepath.Join(a, "cache.tmp"), []byte("source-excluded"))
	writeFile(t, filepath.Join(a, "sub", "cache.tmp"), []byte("source-excluded"))
	writeFile(t, filepath.Join(b, "cache.tmp"), []byte("target-kept"))
	writeFile(t, filepath.Join(b, "stale.tmp"), []byte("target-kept"))

	cfg := &state.Config{}
	cfg.SetExcludes(a, b, []string{"*.tmp"})
	if err := state.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	run(t, "sync", a, b, testLBArg, testSBArg, "--yes")

	if got, _ := os.ReadFile(filepath.Join(b, "keep.txt")); string(got) != "keep" {
		t.Fatalf("普通文件未同步: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "cache.tmp")); string(got) != "target-kept" {
		t.Fatalf("被排除文件被覆盖: %q", got)
	}
	if _, err := os.Stat(filepath.Join(b, "stale.tmp")); err != nil {
		t.Fatal("被排除的目标文件不应被删除")
	}
	if _, err := os.Stat(filepath.Join(b, "sub", "cache.tmp")); !os.IsNotExist(err) {
		t.Fatal("被排除的子目录文件不应被复制")
	}
}
