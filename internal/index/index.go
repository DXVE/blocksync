// 每文件块索引的编解码与磁盘存储；索引按文件独立存放，体积约为文件大小的 1/1024。
package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultLBSize = 4 << 20  // 4 MiB 粗块
	DefaultSBSize = 32 << 10 // 32 KiB 细块
	HashLen       = 32

	indexMagic   = "BLKIDX2\x00"
	indexVersion = uint32(2)
	indexExt     = ".idx"

	// 头部布局：magic(8) version(4) LB(4) SB(4) hashLen(4) size(8) mtime(8) nLB(8) nSB(8)
	fixedHeader = 56
	sizeOffset  = 24 // 头部内文件长度的起始位置，供只读长度时使用
)

// File 单个文件的块索引。
type File struct {
	LB        uint32
	SB        uint32
	Size      uint64
	MtimeNano int64
	LBHashes  []byte // nLB * HashLen
	SBHashes  []byte // nSB * HashLen
}

// BlockCount 返回按 bs 切分 size 得到的块数。
func BlockCount(size, bs int64) int64 {
	if size <= 0 || bs <= 0 {
		return 0
	}
	return (size + bs - 1) / bs
}

// New 分配一个大小匹配的索引。
func New(lb, sb uint32, size uint64, mtime int64) *File {
	f := &File{LB: lb, SB: sb, Size: size, MtimeNano: mtime}
	f.LBHashes = make([]byte, BlockCount(int64(size), int64(lb))*HashLen)
	f.SBHashes = make([]byte, BlockCount(int64(size), int64(sb))*HashLen)
	return f
}

func (f *File) NLB() int64 { return int64(len(f.LBHashes) / HashLen) }
func (f *File) NSB() int64 { return int64(len(f.SBHashes) / HashLen) }

func (f *File) LBHash(i int64) []byte { return f.LBHashes[i*HashLen : (i+1)*HashLen] }
func (f *File) SBHash(i int64) []byte { return f.SBHashes[i*HashLen : (i+1)*HashLen] }

// encodeLen 返回序列化后的字节数。
func (f *File) encodeLen() int {
	return fixedHeader + len(f.LBHashes) + len(f.SBHashes)
}

// Encode 序列化，字节布局与机器字节序无关。
func (f *File) Encode() []byte {
	var b bytes.Buffer
	b.Grow(f.encodeLen())
	_ = f.writeTo(&b)
	return b.Bytes()
}

// writeTo 按固定布局顺序写出，避免为整份索引再分配一份缓冲。
func (f *File) writeTo(w io.Writer) error {
	var head [fixedHeader]byte
	copy(head[:8], indexMagic)
	binary.LittleEndian.PutUint32(head[8:], indexVersion)
	binary.LittleEndian.PutUint32(head[12:], f.LB)
	binary.LittleEndian.PutUint32(head[16:], f.SB)
	binary.LittleEndian.PutUint32(head[20:], HashLen)
	binary.LittleEndian.PutUint64(head[24:], f.Size)
	binary.LittleEndian.PutUint64(head[32:], uint64(f.MtimeNano))
	binary.LittleEndian.PutUint64(head[40:], uint64(f.NLB()))
	binary.LittleEndian.PutUint64(head[48:], uint64(f.NSB()))
	for _, part := range [][]byte{head[:], f.LBHashes, f.SBHashes} {
		if _, err := w.Write(part); err != nil {
			return err
		}
	}
	return nil
}

// Decode 反序列化并校验块数与长度一致。
func Decode(b []byte) (*File, error) {
	if len(b) < fixedHeader {
		return nil, errors.New("索引数据过短")
	}
	if string(b[:8]) != indexMagic {
		return nil, errors.New("索引魔数不匹配")
	}
	pos := 8
	ver := binary.LittleEndian.Uint32(b[pos:])
	pos += 4
	if ver != indexVersion {
		return nil, fmt.Errorf("不支持的索引版本 %d", ver)
	}
	f := &File{}
	f.LB = binary.LittleEndian.Uint32(b[pos:])
	pos += 4
	f.SB = binary.LittleEndian.Uint32(b[pos:])
	pos += 4
	hashLen := binary.LittleEndian.Uint32(b[pos:])
	pos += 4
	if hashLen != HashLen {
		return nil, fmt.Errorf("不支持的哈希长度 %d", hashLen)
	}
	f.Size = binary.LittleEndian.Uint64(b[pos:])
	pos += 8
	f.MtimeNano = int64(binary.LittleEndian.Uint64(b[pos:]))
	pos += 8
	nLB := int64(binary.LittleEndian.Uint64(b[pos:]))
	pos += 8
	nSB := int64(binary.LittleEndian.Uint64(b[pos:]))
	pos += 8
	if want := BlockCount(int64(f.Size), int64(f.LB)); want != nLB {
		return nil, fmt.Errorf("LB 块数不符：索引 %d，期望 %d", nLB, want)
	}
	if want := BlockCount(int64(f.Size), int64(f.SB)); want != nSB {
		return nil, fmt.Errorf("SB 块数不符：索引 %d，期望 %d", nSB, want)
	}
	need := int64(fixedHeader) + (nLB+nSB)*HashLen
	if int64(len(b)) != need {
		return nil, fmt.Errorf("索引长度不符：实际 %d，期望 %d", len(b), need)
	}
	f.LBHashes = append([]byte(nil), b[pos:pos+int(nLB*HashLen)]...)
	pos += int(nLB * HashLen)
	f.SBHashes = append([]byte(nil), b[pos:pos+int(nSB*HashLen)]...)
	return f, nil
}

// ContentDigest 只对内容与块参数求摘要，不含 mtime，供写后校验与索引比对使用。
func (f *File) ContentDigest() [HashLen]byte {
	var head [16]byte
	binary.LittleEndian.PutUint32(head[0:], f.LB)
	binary.LittleEndian.PutUint32(head[4:], f.SB)
	binary.LittleEndian.PutUint64(head[8:], f.Size)
	h := sha256.New()
	h.Write(head[:])
	h.Write(f.LBHashes)
	h.Write(f.SBHashes)
	var out [HashLen]byte
	copy(out[:], h.Sum(nil))
	return out
}

// EqualContent 判断两份索引内容是否一致。
func (f *File) EqualContent(g *File) bool {
	if f == nil || g == nil {
		return f == g
	}
	if f.LB != g.LB || f.SB != g.SB || f.Size != g.Size {
		return false
	}
	if len(f.LBHashes) != len(g.LBHashes) || len(f.SBHashes) != len(g.SBHashes) {
		return false
	}
	return f.ContentDigest() == g.ContentDigest()
}

// Store 负责 idx 目录下按相对路径镜像存放的索引文件。
type Store struct {
	root string
}

func NewStore(stateDir string) *Store {
	return &Store{root: filepath.Join(stateDir, "idx")}
}

func (s *Store) Root() string { return s.root }

func (s *Store) target(rel string) string {
	return filepath.Join(s.root, filepath.FromSlash(rel)) + indexExt
}

// Load 读取索引，不存在时返回 (nil, nil)。
func (s *Store) Load(rel string) (*File, error) {
	b, err := os.ReadFile(s.target(rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, err := Decode(b)
	if err != nil {
		return nil, fmt.Errorf("索引损坏 %s: %w", rel, err)
	}
	return f, nil
}

// Save 原子写入索引：先顺序写临时文件，再改名替换。
func (s *Store) Save(rel string, f *File) error {
	p := s.target(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := f.writeTo(fh); err != nil {
		fh.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fh.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// Size 只读取头部里的文件长度，不解码整份索引。
func (s *Store) Size(rel string) (uint64, error) {
	fh, err := os.Open(s.target(rel))
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	var head [sizeOffset + 8]byte
	if _, err := io.ReadFull(fh, head[:]); err != nil {
		return 0, fmt.Errorf("索引损坏 %s: %w", rel, err)
	}
	if string(head[:8]) != indexMagic {
		return 0, fmt.Errorf("索引损坏 %s: 索引魔数不匹配", rel)
	}
	return binary.LittleEndian.Uint64(head[sizeOffset:]), nil
}

func (s *Store) Delete(rel string) error {
	err := os.Remove(s.target(rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List 返回所有已建索引的相对路径（斜杠分隔）。
func (s *Store) List() ([]string, error) {
	var out []string
	if _, err := os.Stat(s.root); errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	err := filepath.Walk(s.root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), indexExt) {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		rel = strings.TrimSuffix(filepath.ToSlash(rel), indexExt)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}
