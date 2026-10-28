package scan

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

const (
	testLB = int64(64 << 10)
	testSB = int64(4 << 10)
)

func writeFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func rndBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

// applyRanges 把变更区间套到旧内容上，得到应与新文件一致的结果；尾部按新长度截断。
func applyRanges(old, cur []byte, ranges []Range) []byte {
	out := append([]byte(nil), old...)
	for _, r := range ranges {
		end := int(r.Off + r.Len)
		if end > len(out) {
			out = append(out, make([]byte, end-len(out))...)
		}
		copy(out[r.Off:end], cur[r.Off:end])
	}
	if len(out) > len(cur) {
		out = out[:len(cur)]
	}
	return out
}

func TestScanFullStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	writeFile(t, p, rndBytes(300*1024, 1))

	a, err := ScanFull(p, testLB, testSB, 4)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ScanFull(p, testLB, testSB, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !a.New.EqualContent(b.New) {
		t.Fatal("同一文件两次扫描结果应一致（与线程数无关）")
	}
	if a.ChangedBytes != uint64(300*1024) {
		t.Fatalf("全量扫描应把整个文件视为变更，得到 %d", a.ChangedBytes)
	}
}

func TestScanDiffNoChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	writeFile(t, p, rndBytes(200*1024, 2))
	base, err := ScanFull(p, testLB, testSB, 4)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ScanDiff(p, testLB, testSB, base.New, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ranges) != 0 || res.ChangedBytes != 0 {
		t.Fatalf("无改动时不应产生区间，得到 %d 段 %d 字节", len(res.Ranges), res.ChangedBytes)
	}
	if !res.New.EqualContent(base.New) {
		t.Fatal("无改动时索引应保持不变")
	}
}

func TestScanDiffMutations(t *testing.T) {
	const size = 300 * 1024
	cases := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"单字节", func(b []byte) []byte { b[100] ^= 0xFF; return b }},
		{"细块边界跨越", func(b []byte) []byte {
			copy(b[testSB-4:testSB+4], []byte("BOUNDARY"))
			return b
		}},
		{"粗块中间", func(b []byte) []byte {
			copy(b[testLB+1234:testLB+1300], rndBytes(66, 9))
			return b
		}},
		{"大片连续", func(b []byte) []byte { return append(b, rndBytes(50*1024, 11)...) }},
		{"尾部截断", func(b []byte) []byte { return b[:size-7] }},
		{"整文件重写", func(b []byte) []byte { return rndBytes(size, 12) }},
		{"全零区域", func(b []byte) []byte {
			for i := 0; i < 40*1024; i++ {
				b[i] = 0
			}
			return b
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "f.bin")
			orig := rndBytes(size, 3)
			writeFile(t, p, orig)

			base, err := ScanFull(p, testLB, testSB, 4)
			if err != nil {
				t.Fatal(err)
			}

			cur := tc.mutate(append([]byte(nil), orig...))
			writeFile(t, p, cur)

			res, err := ScanDiff(p, testLB, testSB, base.New, 4)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Ranges) == 0 {
				t.Fatal("应检测到变更")
			}
			got := applyRanges(orig, cur, res.Ranges)
			if !bytes.Equal(got, cur) {
				t.Fatalf("按区间应用后与目标不一致：得到 %d 字节，期望 %d 字节", len(got), len(cur))
			}
			if res.ChangedBytes >= uint64(len(cur)) && tc.name != "整文件重写" {
				t.Fatalf("变更量应小于文件大小，得到 %d / %d", res.ChangedBytes, len(cur))
			}
			// 新索引必须与目标文件独立扫描的结果一致
			full, err := ScanFull(p, testLB, testSB, 2)
			if err != nil {
				t.Fatal(err)
			}
			if !res.New.EqualContent(full.New) {
				t.Fatal("差异扫描产出的索引与全量扫描不一致")
			}
		})
	}
}

func TestScanDiffRepeatedRounds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	cur := rndBytes(256*1024, 21)
	writeFile(t, p, cur)
	base, err := ScanFull(p, testLB, testSB, 4)
	if err != nil {
		t.Fatal(err)
	}
	rnd := rand.New(rand.NewSource(99))
	for round := 0; round < 40; round++ {
		old := cur
		cur = append([]byte(nil), old...)
		switch round % 4 {
		case 0:
			off := rnd.Intn(len(cur))
			cur[off] ^= 0x5A
		case 1:
			off := rnd.Intn(len(cur) - 100)
			copy(cur[off:off+100], rndBytes(100, int64(round)))
		case 2:
			cur = append(cur, rndBytes(rnd.Intn(5000), int64(round))...)
		case 3:
			if len(cur) > 10000 {
				cur = cur[:len(cur)-rnd.Intn(3000)]
			}
		}
		writeFile(t, p, cur)

		res, err := ScanDiff(p, testLB, testSB, base.New, 4)
		if err != nil {
			t.Fatalf("第 %d 轮: %v", round, err)
		}
		if got := applyRanges(old, cur, res.Ranges); !bytes.Equal(got, cur) {
			t.Fatalf("第 %d 轮按区间应用后与目标不一致", round)
		}
		base.New = res.New
	}
}

func TestExcluded(t *testing.T) {
	ex := DefaultExcludes
	cases := map[string]bool{
		"a.vmdk":               false,
		"a.vmdk.lck":           false,
		"vmware.log":           false,
		"vm.vmem":              false,
		"nested/vm.vswp":       false,
		".blocksync/idx/a.idx": true,
		".blocksync":           true,
		"sub/.blocksync/x":     true,
		"desktop.ini":          true,
		"Thumbs.db":            true,
		"$RECYCLE.BIN/x/y":     true,
	}
	for rel, want := range cases {
		if got := Excluded(rel, ex); got != want {
			t.Fatalf("%s: 期望 %v，得到 %v", rel, want, got)
		}
	}
}
