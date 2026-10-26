package patch

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadVerify(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.bin")
	src := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		t.Fatal(err)
	}

	final := filepath.Join(dir, "p"+Ext)
	var parent, result [32]byte
	parent[0] = 0xAA
	result[0] = 0xBB

	w, err := Create(final+".tmp", final, 7, parent, result)
	if err != nil {
		t.Fatal(err)
	}
	spec := &EntrySpec{
		Path:      "dir/a.vmdk",
		Action:    ActionModify,
		NewSize:   32,
		MtimeNano: 1234567890,
		LB:        4096,
		SB:        1024,
		Ranges:    []Range{{Off: 4, Len: 6}, {Off: 20, Len: 3}},
	}
	ew, err := w.BeginEntry(spec)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.Copy(ew, NewRangeReader(f, spec.Ranges)); err != nil {
		t.Fatal(err)
	}
	if err := ew.End([]byte("INDEX-BLOB")); err != nil {
		t.Fatal(err)
	}

	del := &EntrySpec{Path: "old.vmdk", Action: ActionDelete}
	ew2, err := w.BeginEntry(del)
	if err != nil {
		t.Fatal(err)
	}
	if err := ew2.End(nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(final)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Header.Generation != 7 || r.Header.Parent != parent || r.Header.Result != result {
		t.Fatal("头部字段不一致")
	}
	if err := r.Verify(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}

	var n int
	err = r.Walk(func(e *EntryHeader, payload io.ReaderAt) error {
		n++
		switch e.Path {
		case "dir/a.vmdk":
			if e.Action != ActionModify || e.NewSize != 32 || e.MtimeNano != 1234567890 {
				t.Fatal("条目头字段不一致")
			}
			if len(e.Ranges) != 2 || e.PayloadLen != 9 {
				t.Fatalf("区间解析异常: %+v len=%d", e.Ranges, e.PayloadLen)
			}
			got := make([]byte, e.PayloadLen)
			if _, err := payload.ReadAt(got, 0); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			want := append(append([]byte(nil), src[4:10]...), src[20:23]...)
			if !bytes.Equal(got, want) {
				t.Fatalf("区间数据不一致: %q != %q", got, want)
			}
			if string(e.Index) != "INDEX-BLOB" {
				t.Fatalf("内嵌索引不一致: %q", e.Index)
			}
		case "old.vmdk":
			if e.Action != ActionDelete || e.PayloadLen != 0 || len(e.Index) != 0 {
				t.Fatal("删除条目解析异常")
			}
		default:
			t.Fatalf("出现未知条目 %s", e.Path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("条目数应为 2，得到 %d", n)
	}
}

func TestCorruptionRejected(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "p"+Ext)
	w, err := Create(final+".tmp", final, 1, [32]byte{}, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	spec := &EntrySpec{Path: "a", Action: ActionModify, NewSize: 4, LB: 4096, SB: 1024, Ranges: []Range{{Off: 0, Len: 4}}}
	ew, _ := w.BeginEntry(spec)
	ew.Write([]byte("DATA"))
	ew.End(nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	orig, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}

	flipped := append([]byte(nil), orig...)
	flipped[len(Magic)+4] ^= 0xFF // 改代次字段，头部结构仍合法，只有摘要会不符
	if err := os.WriteFile(final, flipped, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(final)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err == nil {
		t.Fatal("翻转字节后校验应失败")
	}
	r.Close()

	truncated := orig[:len(orig)-40]
	if err := os.WriteFile(final, truncated, 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := Open(final); err == nil {
		errVerify := r.Verify()
		r.Close()
		if errVerify == nil {
			t.Fatal("截断后校验应失败")
		}
	}
}

func TestListSorted(t *testing.T) {
	dir := t.TempDir()
	for i, gen := range []uint64{3, 1, 2} {
		final := filepath.Join(dir, string(rune('a'+i))+Ext)
		w, err := Create(final+".tmp", final, gen, [32]byte{}, [32]byte{})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("应列出 3 个补丁，得到 %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Header.Generation > list[i].Header.Generation {
			t.Fatal("未被按代次升序排列")
		}
	}
}
