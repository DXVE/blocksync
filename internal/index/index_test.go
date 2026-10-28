package index

import (
	"crypto/sha256"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	sizes := []uint64{0, 1, 4095, 4096, 65536, 65537, 1<<20 + 12345}
	for _, size := range sizes {
		f := New(4096, 1024, size, 123456789)
		for i := int64(0); i < f.NSB(); i++ {
			h := sha256.Sum256([]byte{byte(i), byte(i >> 8)})
			copy(f.SBHash(i), h[:])
		}
		for i := int64(0); i < f.NLB(); i++ {
			h := sha256.Sum256([]byte{byte(i), 0xFF})
			copy(f.LBHash(i), h[:])
		}
		g, err := Decode(f.Encode())
		if err != nil {
			t.Fatalf("size=%d: %v", size, err)
		}
		if g.LB != f.LB || g.SB != f.SB || g.Size != f.Size || g.MtimeNano != f.MtimeNano {
			t.Fatalf("size=%d: 头字段不一致", size)
		}
		if !g.EqualContent(f) {
			t.Fatalf("size=%d: 内容摘要不一致", size)
		}
		if g.NSB() != f.NSB() || g.NLB() != f.NLB() {
			t.Fatalf("size=%d: 块数不一致", size)
		}
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	f := New(4096, 1024, 10000, 1)
	enc := f.Encode()

	cases := map[string][]byte{
		"截断":   enc[:len(enc)-1],
		"魔数错":  append([]byte("XXXXXXX\x00"), enc[8:]...),
		"多余字节": append(append([]byte(nil), enc...), 0),
	}
	for name, b := range cases {
		if _, err := Decode(b); err == nil {
			t.Fatalf("%s: 应被拒绝", name)
		}
	}
}

func TestContentDigestIgnoresMtime(t *testing.T) {
	a := New(4096, 1024, 8192, 111)
	b := New(4096, 1024, 8192, 999)
	if !a.EqualContent(b) {
		t.Fatal("mtime 不同不应影响内容摘要")
	}
	copy(b.SBHash(0), []byte("changed"))
	if a.EqualContent(b) {
		t.Fatal("块内容不同应影响内容摘要")
	}
}

func TestStoreSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	f := New(4096, 1024, 5000, 7)
	if err := s.Save("sub/dir/a.vmdk", f); err != nil {
		t.Fatal(err)
	}
	g, err := s.Load("sub/dir/a.vmdk")
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || !g.EqualContent(f) {
		t.Fatal("读取结果与写入不一致")
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0] != "sub/dir/a.vmdk" {
		t.Fatalf("List 结果异常: %v %v", list, err)
	}
	if err := s.Delete("sub/dir/a.vmdk"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Load("sub/dir/a.vmdk"); g != nil {
		t.Fatal("删除后仍能读到索引")
	}
}

// 旧版格式（BLKIDX1、含 FileDigest）应被拒绝，交由调用方重扫重建。
func TestDecodeRejectsOldFormat(t *testing.T) {
	old := append([]byte("BLKIDX1\x00"), make([]byte, 120)...)
	if _, err := Decode(old); err == nil {
		t.Fatal("旧版索引应被拒绝")
	}
}
