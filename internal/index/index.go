// 每文件块索引的编解码与磁盘存储；索引按文件独立存放，体积约为文件大小的 1/1024。
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultLBSize = 4 << 20  // 4 MiB 粗块
	DefaultSBSize = 32 << 10 // 32 KiB 细块
	HashLen       = 32

	indexMagic   = "BLKIDX1\x00"
	indexVersion = uint32(1)
	indexExt     = ".idx"
)

// File 单个文件的块索引。
type File struct {
	LB         uint32
	SB         uint32
	Size       uint64
	MtimeNano  int64
	FileDigest [HashLen]byte // 全文件 SHA256，0 表示未计算
	LBHashes   []byte        // nLB * HashLen
	SBHashes   []byte        // nSB * HashLen
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

func (f *File) Clone() *File {
	g := *f
	g.LBHashes = append([]byte(nil), f.LBHashes...)
	g.SBHashes = append([]byte(nil), f.SBHashes...)
	return &g
}

// Encode 序列化，字节布局与机器字节序无关。
func (f *File) Encode() []byte {
	b := make([]byte, 0, 64+len(f.LBHashes)+len(f.SBHashes))
	b = append(b, indexMagic...)
	b = binary.LittleEndian.AppendUint32(b, indexVersion)
	b = binary.LittleEndian.AppendUint32(b, f.LB)
	b = binary.LittleEndian.AppendUint32(b, f.SB)
	b = binary.LittleEndian.AppendUint32(b, HashLen)
	b = binary.LittleEndian.AppendUint64(b, f.Size)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.MtimeNano))
	b = append(b, f.FileDigest[:]...)
	b = binary.LittleEndian.AppendUint64(b, uint64(f.NLB()))
	b = binary.LittleEndian.AppendUint64(b, uint64(f.NSB()))
	b = append(b, f.LBHashes...)
	b = append(b, f.SBHashes...)
	return b
}

// Decode 反序列化并校验块数与长度一致。
func Decode(b []byte) (*File, error) {
	const fixed = 8 + 4 + 4 + 4 + 4 + 8 + 8 + HashLen + 8 + 8
	if len(b) < fixed {
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
	copy(f.FileDigest[:], b[pos:pos+HashLen])
	pos += HashLen
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
	need := int64(fixed) + (nLB+nSB)*HashLen
	if int64(len(b)) != need {
		return nil, fmt.Errorf("索引长度不符：实际 %d，期望 %d", len(b), need)
	}
	f.LBHashes = append([]byte(nil), b[pos:pos+int(nLB*HashLen)]...)
	pos += int(nLB * HashLen)
	f.SBHashes = append([]byte(nil), b[pos:pos+int(nSB*HashLen)]...)
	return f, nil
}

// ContentDigest 只对内容与块参数求摘要，不含 mtime，供跨站点状态比对使用。
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

// SetEntry 站点状态摘要中的一项。
type SetEntry struct {
	Path   string
	Size   uint64
	Digest [HashLen]byte
}

// SetDigest 对整组文件的 (路径, 大小, 内容摘要) 求摘要，与顺序和 mtime 无关。
func SetDigest(entries []SetEntry) [HashLen]byte {
	sorted := append([]SetEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	var num [16]byte
	for _, e := range sorted {
		h.Write([]byte(e.Path))
		h.Write([]byte{0})
		binary.LittleEndian.PutUint64(num[0:], e.Size)
		h.Write(num[:8])
		h.Write(e.Digest[:])
	}
	var out [HashLen]byte
	copy(out[:], h.Sum(nil))
	return out
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

// Save 原子写入索引。
func (s *Store) Save(rel string, f *File) error {
	p := s.target(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, f.Encode(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// SaveRaw 直接写入已编码的索引数据。
func (s *Store) SaveRaw(rel string, blob []byte) error {
	p := s.target(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
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
