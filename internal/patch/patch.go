// 补丁文件格式：头部 + 逐文件区间表 + 区间原始数据 + 应用后的新索引，以长度为 0 的路径作为条目结束标记，末尾附全文件 SHA256。
package patch

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	Magic   = "BLKPATCH1"
	Version = uint32(1)
	Ext     = ".blkpatch"

	headerFixed = len(Magic) + 4 + 8 + 32 + 32 + 4
)

type Action uint8

const (
	ActionAdd    Action = 1
	ActionModify Action = 2
	ActionDelete Action = 3
)

func (a Action) String() string {
	switch a {
	case ActionAdd:
		return "新增"
	case ActionModify:
		return "修改"
	case ActionDelete:
		return "删除"
	}
	return "未知"
}

type Range struct {
	Off uint64
	Len uint64
}

type Header struct {
	Version    uint32
	Generation uint64
	Parent     [32]byte
	Result     [32]byte
}

// EntrySpec 写入端描述一个文件变更。
type EntrySpec struct {
	Path             string
	Action           Action
	NewSize          uint64
	MtimeNano        int64
	LB, SB           uint32
	ResultFileDigest [32]byte
	Ranges           []Range
}

// PayloadLen 返回该条目应写入的数据字节数。
func (e *EntrySpec) PayloadLen() uint64 {
	var n uint64
	for _, r := range e.Ranges {
		n += r.Len
	}
	return n
}

type Writer struct {
	tmp, final string
	f          *os.File
	bw         *bufioWriter
	h          hash.Hash
	count      uint32
	done       bool
}

type bufioWriter struct {
	f *os.File
	n int64
}

func (b *bufioWriter) Write(p []byte) (int, error) {
	n, err := b.f.Write(p)
	b.n += int64(n)
	return n, err
}

func Create(tmp, final string, gen uint64, parent, result [32]byte) (*Writer, error) {
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	w := &Writer{tmp: tmp, final: final, f: f, bw: &bufioWriter{f: f}, h: sha256.New()}
	hdr := make([]byte, 0, headerFixed)
	hdr = append(hdr, Magic...)
	hdr = binary.LittleEndian.AppendUint32(hdr, Version)
	hdr = binary.LittleEndian.AppendUint64(hdr, gen)
	hdr = append(hdr, parent[:]...)
	hdr = append(hdr, result[:]...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 0) // 保留字段，条目以终止标记结束
	if err := w.write(hdr); err != nil {
		w.Abort()
		return nil, err
	}
	return w, nil
}

func (w *Writer) write(p []byte) error {
	w.h.Write(p)
	_, err := w.bw.Write(p)
	return err
}

// BeginEntry 写入条目头与区间表，返回用于流式写入区间数据的写入器。
func (w *Writer) BeginEntry(e *EntrySpec) (*EntryWriter, error) {
	if len(e.Path) == 0 || len(e.Path) > 60000 {
		return nil, fmt.Errorf("路径长度非法: %q", e.Path)
	}
	buf := make([]byte, 0, 128+len(e.Path)+16*len(e.Ranges))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(e.Path)))
	buf = append(buf, e.Path...)
	buf = append(buf, byte(e.Action))
	buf = binary.LittleEndian.AppendUint64(buf, e.NewSize)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.MtimeNano))
	buf = binary.LittleEndian.AppendUint32(buf, e.LB)
	buf = binary.LittleEndian.AppendUint32(buf, e.SB)
	buf = binary.LittleEndian.AppendUint32(buf, sha256.Size)
	buf = append(buf, e.ResultFileDigest[:]...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(e.Ranges)))
	for _, r := range e.Ranges {
		buf = binary.LittleEndian.AppendUint64(buf, r.Off)
		buf = binary.LittleEndian.AppendUint64(buf, r.Len)
	}
	if err := w.write(buf); err != nil {
		return nil, err
	}
	w.count++
	return &EntryWriter{w: w, remain: int64(e.PayloadLen())}, nil
}

type EntryWriter struct {
	w      *Writer
	remain int64
}

func (ew *EntryWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > ew.remain {
		return 0, fmt.Errorf("区间数据超出声明长度（剩余 %d 字节）", ew.remain)
	}
	if err := ew.w.write(p); err != nil {
		return 0, err
	}
	ew.remain -= int64(len(p))
	return len(p), nil
}

// End 校验数据长度并写入该文件应用后的新索引。
func (ew *EntryWriter) End(indexBlob []byte) error {
	if ew.remain != 0 {
		return fmt.Errorf("区间数据不足，还差 %d 字节", ew.remain)
	}
	buf := make([]byte, 0, 8+len(indexBlob))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(len(indexBlob)))
	buf = append(buf, indexBlob...)
	return ew.w.write(buf)
}

// Close 写终止标记与脚注，落盘后原子改名。
func (w *Writer) Close() error {
	if w.done {
		return nil
	}
	if err := w.write([]byte{0, 0}); err != nil {
		w.Abort()
		return err
	}
	sum := w.h.Sum(nil)
	if _, err := w.bw.Write(sum); err != nil {
		w.Abort()
		return err
	}
	if err := w.f.Sync(); err != nil {
		w.Abort()
		return err
	}
	if err := w.f.Close(); err != nil {
		_ = os.Remove(w.tmp)
		w.done = true
		return err
	}
	w.done = true
	return os.Rename(w.tmp, w.final)
}

func (w *Writer) Abort() {
	if w.done {
		return
	}
	w.done = true
	if w.f != nil {
		_ = w.f.Close()
	}
	_ = os.Remove(w.tmp)
}

// FileCount 返回已写入的条目数。
func (w *Writer) FileCount() uint32 { return w.count }

// EntryHeader 读取端解析出的条目信息。
type EntryHeader struct {
	Path             string
	Action           Action
	NewSize          uint64
	MtimeNano        int64
	LB, SB           uint32
	ResultFileDigest [32]byte
	Ranges           []Range
	PayloadOff       int64
	PayloadLen       int64
	Index            []byte
}

type Reader struct {
	Path    string
	f       *os.File
	Header  Header
	dataEnd int64
	size    int64
}

func Open(p string) (*Reader, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.Size() < int64(headerFixed)+2+sha256.Size {
		f.Close()
		return nil, errors.New("补丁文件过短或已损坏")
	}
	r := &Reader{Path: p, f: f, dataEnd: st.Size() - sha256.Size, size: st.Size()}
	if err := r.readHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func (r *Reader) readHeader() error {
	sec := io.NewSectionReader(r.f, 0, int64(headerFixed))
	var (
		b4 [4]byte
		b8 [8]byte
	)
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(sec, magic); err != nil {
		return err
	}
	if string(magic) != Magic {
		return errors.New("不是 blocksync 补丁文件")
	}
	if _, err := io.ReadFull(sec, b4[:]); err != nil {
		return err
	}
	r.Header.Version = binary.LittleEndian.Uint32(b4[:])
	if r.Header.Version != Version {
		return fmt.Errorf("不支持的补丁版本 %d", r.Header.Version)
	}
	if _, err := io.ReadFull(sec, b8[:]); err != nil {
		return err
	}
	r.Header.Generation = binary.LittleEndian.Uint64(b8[:])
	if _, err := io.ReadFull(sec, r.Header.Parent[:]); err != nil {
		return err
	}
	if _, err := io.ReadFull(sec, r.Header.Result[:]); err != nil {
		return err
	}
	if _, err := io.ReadFull(sec, b4[:]); err != nil {
		return err
	}
	return nil
}

func (r *Reader) Close() error { return r.f.Close() }

// Verify 流式校验整份补丁的脚注摘要。
func (r *Reader) Verify() error {
	sec := io.NewSectionReader(r.f, 0, r.dataEnd)
	h := sha256.New()
	if _, err := io.Copy(h, sec); err != nil {
		return fmt.Errorf("读取补丁失败: %w", err)
	}
	want := make([]byte, sha256.Size)
	if _, err := io.ReadFull(io.NewSectionReader(r.f, r.dataEnd, sha256.Size), want); err != nil {
		return fmt.Errorf("读取补丁脚注失败: %w", err)
	}
	if string(h.Sum(nil)) != string(want) {
		return errors.New("补丁校验失败：文件已损坏或被截断")
	}
	return nil
}

// Walk 依次解析每个文件条目，payload 用于读取该条目对应的区间数据。
func (r *Reader) Walk(fn func(e *EntryHeader, payload io.ReaderAt) error) error {
	sec := io.NewSectionReader(r.f, 0, r.dataEnd)
	off := int64(0)
	read := func(p []byte) error {
		n, err := io.ReadFull(sec, p)
		off += int64(n)
		return err
	}
	var (
		b1 [1]byte
		b2 [2]byte
		b4 [4]byte
		b8 [8]byte
	)
	if err := read(make([]byte, headerFixed)); err != nil {
		return err
	}
	for {
		if err := read(b2[:]); err != nil {
			return err
		}
		plen := int(binary.LittleEndian.Uint16(b2[:]))
		if plen == 0 {
			return nil
		}
		e := &EntryHeader{}
		pathBuf := make([]byte, plen)
		if err := read(pathBuf); err != nil {
			return err
		}
		e.Path = strings.ToValidUTF8(string(pathBuf), "\uFFFD")
		if err := read(b1[:]); err != nil {
			return err
		}
		e.Action = Action(b1[0])
		if err := read(b8[:]); err != nil {
			return err
		}
		e.NewSize = binary.LittleEndian.Uint64(b8[:])
		if err := read(b8[:]); err != nil {
			return err
		}
		e.MtimeNano = int64(binary.LittleEndian.Uint64(b8[:]))
		if err := read(b4[:]); err != nil {
			return err
		}
		e.LB = binary.LittleEndian.Uint32(b4[:])
		if err := read(b4[:]); err != nil {
			return err
		}
		e.SB = binary.LittleEndian.Uint32(b4[:])
		if err := read(b4[:]); err != nil {
			return err
		}
		if hl := binary.LittleEndian.Uint32(b4[:]); hl != sha256.Size {
			return fmt.Errorf("条目 %s 哈希长度不支持: %d", e.Path, hl)
		}
		if err := read(e.ResultFileDigest[:]); err != nil {
			return err
		}
		if err := read(b4[:]); err != nil {
			return err
		}
		nr := binary.LittleEndian.Uint32(b4[:])
		e.Ranges = make([]Range, nr)
		for k := uint32(0); k < nr; k++ {
			if err := read(b8[:]); err != nil {
				return err
			}
			e.Ranges[k].Off = binary.LittleEndian.Uint64(b8[:])
			if err := read(b8[:]); err != nil {
				return err
			}
			e.Ranges[k].Len = binary.LittleEndian.Uint64(b8[:])
		}
		e.PayloadOff = off
		for _, rg := range e.Ranges {
			e.PayloadLen += int64(rg.Len)
		}
		if e.PayloadLen > 0 {
			n, err := sec.Seek(e.PayloadLen, io.SeekCurrent)
			if err != nil {
				return err
			}
			// Seek 返回的是段内绝对位置，直接覆盖计数
			off = n
		}
		payload := io.NewSectionReader(r.f, e.PayloadOff, e.PayloadLen)
		if err := read(b8[:]); err != nil {
			return err
		}
		ilen := int64(binary.LittleEndian.Uint64(b8[:]))
		if ilen < 0 || ilen > 1<<30 {
			return fmt.Errorf("条目 %s 索引长度异常: %d", e.Path, ilen)
		}
		if ilen > 0 {
			e.Index = make([]byte, ilen)
			if err := read(e.Index); err != nil {
				return err
			}
		}
		if err := fn(e, payload); err != nil {
			return err
		}
	}
}

// NewRangeReader 依次读出若干区间的原始字节，用于生成补丁数据段。
func NewRangeReader(ra io.ReaderAt, ranges []Range) io.Reader {
	cp := make([]Range, len(ranges))
	copy(cp, ranges)
	return &rangeReader{ra: ra, ranges: cp}
}

type rangeReader struct {
	ra     io.ReaderAt
	ranges []Range
	ri     int
	off    uint64
}

func (r *rangeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for r.ri < len(r.ranges) {
		cur := r.ranges[r.ri]
		if r.off >= cur.Len {
			r.ri++
			r.off = 0
			continue
		}
		n := uint64(len(p))
		if n > cur.Len-r.off {
			n = cur.Len - r.off
		}
		m, err := r.ra.ReadAt(p[:n], int64(cur.Off+r.off))
		r.off += uint64(m)
		if m > 0 {
			return m, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if err == nil {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return 0, io.EOF
}

// Info 载波上的一个补丁。
type Info struct {
	Path   string
	Name   string
	Header Header
}

// List 列出目录中全部补丁，按生成代次升序。
func List(dir string) ([]Info, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), Ext) {
			continue
		}
		p := filepath.Join(dir, ent.Name())
		r, err := Open(p)
		if err != nil {
			continue
		}
		out = append(out, Info{Path: p, Name: ent.Name(), Header: r.Header})
		r.Close()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Header.Generation == out[j].Header.Generation {
			return out[i].Name < out[j].Name
		}
		return out[i].Header.Generation < out[j].Header.Generation
	})
	return out, nil
}
