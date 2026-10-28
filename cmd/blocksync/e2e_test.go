package main

import (
	"bytes"
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

// readTree 读取目录内容（忽略 .blocksync），用于内容快照与比较。
func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
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

// assertTreesEqual 比较两个目录的文件内容（忽略 .blocksync）。
func assertTreesEqual(t *testing.T, a, b string) {
	t.Helper()
	ta, tb := readTree(t, a), readTree(t, b)
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

func setup(t *testing.T) (root, a, b string) {
	t.Helper()
	root = t.TempDir()
	a = filepath.Join(root, "siteA", "vm")
	b = filepath.Join(root, "siteB", "vm")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestEndToEndRoundTrip(t *testing.T) {
	_, a, b := setup(t)

	writeFile(t, filepath.Join(a, "disk-s001.vmdk"), rnd(1536*1024, 1))
	writeFile(t, filepath.Join(a, "disk-s002.vmdk"), rnd(700*1024, 2))
	writeFile(t, filepath.Join(a, "vm.vmx"), []byte("config.version = \"8\"\ndisplayName = \"dev\"\n"))

	run(t, "sync", a, b, "--yes", testLBArg, testSBArg)
	assertTreesEqual(t, a, b)

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

	// 测试比对不应改动任何数据文件
	before := readTree(t, b)
	out := run(t, "sync", a, b, "--dry-run", "--yes", testLBArg, testSBArg)
	if !strings.Contains(out, "测试比对") {
		t.Fatalf("应输出测试比对结果，实际:\n%s", out)
	}
	after := readTree(t, b)
	if len(before) != len(after) {
		t.Fatalf("测试比对改动了文件数量: %d -> %d", len(before), len(after))
	}
	for k, v := range before {
		if !bytes.Equal(v, after[k]) {
			t.Fatalf("测试比对改动了文件: %s", k)
		}
	}

	run(t, "sync", a, b, "--yes", "--verify", testLBArg, testSBArg)
	assertTreesEqual(t, a, b)

	if out := run(t, "verify", b); !strings.Contains(out, "校验通过") {
		t.Fatalf("verify 应通过，实际:\n%s", out)
	}
}

func TestEndToEndDelete(t *testing.T) {
	_, a, b := setup(t)
	writeFile(t, filepath.Join(a, "keep.vmdk"), rnd(200*1024, 3))
	writeFile(t, filepath.Join(a, "drop.vmdk"), rnd(200*1024, 4))
	run(t, "sync", a, b, "--yes", testLBArg, testSBArg)

	if err := os.Remove(filepath.Join(a, "drop.vmdk")); err != nil {
		t.Fatal(err)
	}
	run(t, "sync", a, b, "--yes", testLBArg, testSBArg)

	if _, err := os.Stat(filepath.Join(b, "drop.vmdk")); !os.IsNotExist(err) {
		t.Fatal("删除未同步到对端")
	}
	assertTreesEqual(t, a, b)
}

// .lck 目录按普通内容同步，默认排除不包含它。
func TestEndToEndLckSynced(t *testing.T) {
	_, a, b := setup(t)
	writeFile(t, filepath.Join(a, "disk.vmdk"), rnd(64*1024, 7))

	if err := os.MkdirAll(filepath.Join(a, "disk.vmdk.lck"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a, "disk.vmdk.lck", "disk.lck"), []byte("lock"))

	run(t, "sync", a, b, "--yes", testLBArg, testSBArg)
	if got, err := os.ReadFile(filepath.Join(b, "disk.vmdk.lck", "disk.lck")); err != nil || string(got) != "lock" {
		t.Fatalf(".lck 目录应随内容同步，实际 err=%v 内容=%q", err, got)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "disk.vmdk")); !bytes.Equal(got, rnd(64*1024, 7)) {
		t.Fatal("数据文件内容不正确")
	}
}

// 未预先跑 repair 时，sync 应能自行建立索引并完成首次同步。
func TestEndToEndWithoutRepair(t *testing.T) {
	_, a, b := setup(t)
	writeFile(t, filepath.Join(a, "disk.vmdk"), rnd(300*1024, 31))
	writeFile(t, filepath.Join(a, "vm.vmx"), []byte("a = 1\n"))

	run(t, "sync", a, b, "--yes", testLBArg, testSBArg)
	assertTreesEqual(t, a, b)
}

func TestVerifyDetectsAndRepairFixes(t *testing.T) {
	_, a, _ := setup(t)
	p := filepath.Join(a, "disk.vmdk")
	orig := rnd(256*1024, 41)
	writeFile(t, p, orig)
	run(t, "repair", a, testLBArg, testSBArg)

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

	run(t, "repair", a, testLBArg, testSBArg)
	if out := run(t, "verify", a); !strings.Contains(out, "校验通过") {
		t.Fatalf("repair 后校验应通过:\n%s", out)
	}
}

func TestEndToEndRandomRounds(t *testing.T) {
	_, a, b := setup(t)
	names := []string{"disk-s001.vmdk", "disk-s002.vmdk", "disk-s003.vmdk", "vm.vmx", "extra.cfg"}
	for i, n := range names {
		writeFile(t, filepath.Join(a, n), rnd(80000+i*61000, int64(100+i)))
	}
	run(t, "sync", a, b, "--yes", testLBArg, testSBArg)
	assertTreesEqual(t, a, b)

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

		run(t, "sync", a, b, "--yes", "--verify", testLBArg, testSBArg)
		assertTreesEqual(t, a, b)
	}
}

// 目录对排除列表：两侧同时生效，被排除的文件不复制、目标侧已有的同名文件不删除。
func TestEndToEndPairExcludes(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	_, a, b := setup(t)
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

func TestSyncRequiresTwoPaths(t *testing.T) {
	_, a, b := setup(t)
	runExpectFail(t, "sync", "--yes")
	runExpectFail(t, "sync", a, "--yes")
	runExpectFail(t, "sync", a, b, filepath.Join(a, "extra"), "--yes")
}

func TestLegacyDirExcludesIgnored(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	_, a, b := setup(t)
	writeFile(t, filepath.Join(a, "keep.txt"), []byte("keep"))
	writeFile(t, filepath.Join(a, "legacy.txt"), []byte("legacy"))
	writeFile(t, filepath.Join(a, ".blocksync", "config.json"), []byte(`{"excludes":["legacy.txt"]}`))

	run(t, "sync", a, b, testLBArg, testSBArg, "--yes")
	if _, err := os.Stat(filepath.Join(b, "legacy.txt")); err != nil {
		t.Fatal("旧的按目录排除文件应已停用，文件应被同步")
	}
}
