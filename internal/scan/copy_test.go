package scan

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFullMatchesScanFull(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"partial-block", 1000},
		{"block-and-tail", int(testLB) + 123},
		{"multi-block", 3*int(testLB) + 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "src.bin")
			dst := filepath.Join(root, "sub", "dst.bin")
			data := rndBytes(tc.size, int64(tc.size+1))
			writeFile(t, src, data)

			srcInfo, err := os.Stat(src)
			if err != nil {
				t.Fatal(err)
			}
			got, err := CopyFull(src, dst, testLB, testSB)
			if err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(dst); err != nil || !bytes.Equal(b, data) {
				t.Fatalf("复制内容不一致: err=%v", err)
			}
			dstInfo, err := os.Stat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if !dstInfo.ModTime().Equal(srcInfo.ModTime()) {
				t.Fatalf("修改时间未还原: %v vs %v", dstInfo.ModTime(), srcInfo.ModTime())
			}
			want, err := ScanFull(src, testLB, testSB, 2)
			if err != nil {
				t.Fatal(err)
			}
			if !got.EqualContent(want.New) {
				t.Fatal("CopyFull 生成的索引与 ScanFull 不一致")
			}
		})
	}
}
