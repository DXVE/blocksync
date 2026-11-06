// 并行块哈希与差异区间计算；一次读取同时求两级哈希，按细块哈希比较定位变化。
package scan

import (
	"bytes"
	"crypto/sha256"
	"os"
	"runtime"
	"sync"

	"blocksync/internal/index"
)

// Range 源文件中的一段变更区间。
type Range struct {
	Off uint64
	Len uint64
}

// Result 一次扫描的结果。
type Result struct {
	New          *index.File
	Ranges       []Range
	ScannedBytes uint64
}

type lbHashes struct {
	idx int64
	lb  [index.HashLen]byte
	sb  []byte // 该粗块内全部细块哈希
}

// ValidateBlocks 校验块参数合法性。
func ValidateBlocks(lb, sb int64) bool {
	return lb > 0 && sb > 0 && lb > sb && lb%sb == 0
}

// Workers 返回并行哈希的线程数：n 非正时取 CPU 核数。
func Workers(n int) int {
	if n > 0 {
		return n
	}
	return runtime.NumCPU()
}

func mapLBs(f *os.File, size, lb, sb int64, workers int, fn func(*lbHashes)) error {
	perLB := lb / sb
	nLB := index.BlockCount(size, lb)
	if nLB == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if int64(workers) > nLB {
		workers = int(nLB)
	}

	var (
		errOnce  sync.Once
		firstErr error
	)
	setErr := func(e error) { errOnce.Do(func() { firstErr = e }) }

	jobs := make(chan int64, workers*2)
	results := make(chan *lbHashes, workers*2)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, lb)
			for i := range jobs {
				off := i * lb
				n := lb
				if off+n > size {
					n = size - off
				}
				if n <= 0 {
					continue
				}
				if _, err := f.ReadAt(buf[:n], off); err != nil {
					setErr(err)
					continue
				}
				h := &lbHashes{idx: i, lb: sha256.Sum256(buf[:n]), sb: make([]byte, perLB*index.HashLen)}
				for j := int64(0); j < perLB; j++ {
					so := j * sb
					if so >= n {
						break
					}
					sn := sb
					if so+sn > n {
						sn = n - so
					}
					sh := sha256.Sum256(buf[so : so+sn])
					copy(h.sb[j*index.HashLen:], sh[:])
				}
				results <- h
			}
		}()
	}

	go func() {
		for i := int64(0); i < nLB; i++ {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	for h := range results {
		fn(h)
	}
	if firstErr != nil {
		return firstErr
	}
	_, err := f.Stat()
	return err
}

// ScanFull 单遍扫描整个文件，产出完整索引，并把整个文件视为变更。
func ScanFull(p string, lb, sb int64, workers int) (*Result, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	size := st.Size()
	nf := index.New(uint32(lb), uint32(sb), uint64(size), st.ModTime().UnixNano())
	if size == 0 {
		return &Result{New: nf}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	perLB := lb / sb
	err = mapLBs(f, size, lb, sb, workers, func(h *lbHashes) {
		copy(nf.LBHashes[h.idx*index.HashLen:], h.lb[:])
		base := h.idx * perLB * index.HashLen
		end := base + int64(len(h.sb))
		if end > int64(len(nf.SBHashes)) {
			end = int64(len(nf.SBHashes))
		}
		if base < end {
			copy(nf.SBHashes[base:end], h.sb)
		}
	})
	if err != nil {
		return nil, err
	}
	return &Result{
		New:          nf,
		Ranges:       []Range{{Off: 0, Len: uint64(size)}},
		ScannedBytes: uint64(size),
	}, nil
}

// ScanDiff 与旧索引比对，返回变更区间与新的完整索引。
func ScanDiff(p string, lb, sb int64, old *index.File, workers int) (*Result, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	size := st.Size()
	nf := index.New(uint32(lb), uint32(sb), uint64(size), st.ModTime().UnixNano())
	if size == 0 {
		return &Result{New: nf}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	perLB := lb / sb
	nLB := nf.NLB()
	compatible := old != nil && old.LB == uint32(lb) && old.SB == uint32(sb)
	lbRanges := make([][]Range, nLB)
	var changedBytes uint64

	err = mapLBs(f, size, lb, sb, workers, func(h *lbHashes) {
		copy(nf.LBHashes[h.idx*index.HashLen:], h.lb[:])
		base := h.idx * perLB * index.HashLen
		end := base + int64(len(h.sb))
		if end > int64(len(nf.SBHashes)) {
			end = int64(len(nf.SBHashes))
		}
		if base < end {
			copy(nf.SBHashes[base:end], h.sb)
		}

		lbOff := h.idx * lb
		nThis := size - lbOff
		if nThis > lb {
			nThis = lb
		}
		var rs []Range
		var runStart, runEnd int64 = -1, 0
		flush := func() {
			if runStart >= 0 {
				rs = append(rs, Range{Off: uint64(runStart), Len: uint64(runEnd - runStart)})
				runStart = -1
			}
		}
		for j := int64(0); j < perLB; j++ {
			so := j * sb
			if so >= nThis {
				break
			}
			sn := sb
			if so+sn > nThis {
				sn = nThis - so
			}
			sbIdx := h.idx*perLB + j
			changed := true
			if compatible && sbIdx < old.NSB() {
				if bytes.Equal(old.SBHash(sbIdx), h.sb[j*index.HashLen:(j+1)*index.HashLen]) {
					changed = false
				}
			}
			if changed {
				if runStart < 0 {
					runStart = lbOff + so
				}
				runEnd = lbOff + so + sn
			} else {
				flush()
			}
		}
		flush()
		lbRanges[h.idx] = rs
		for _, r := range rs {
			changedBytes += r.Len
		}
	})
	if err != nil {
		return nil, err
	}

	res := &Result{New: nf, ScannedBytes: uint64(size)}
	if changedBytes == uint64(size) && size > 0 {
		res.Ranges = []Range{{Off: 0, Len: uint64(size)}}
		return res, nil
	}
	for _, rs := range lbRanges {
		res.Ranges = append(res.Ranges, rs...)
	}
	return res, nil
}

// RangesAgainst 用两份索引算出 a 相对 b 的变更区间，允许两者长度不同：
// 超出 b 覆盖范围的块视为变更，b 多余的部分由调用方截断。
func RangesAgainst(a, b *index.File) []Range {
	if a == nil {
		return nil
	}
	if b == nil || a.LB != b.LB || a.SB != b.SB {
		return RangesAll(a.Size)
	}
	sbSize := int64(a.SB)
	n := a.NSB()
	bNSB := b.NSB()
	var ranges []Range
	var runStart, runEnd int64 = -1, 0
	flush := func() {
		if runStart >= 0 {
			ranges = append(ranges, Range{Off: uint64(runStart), Len: uint64(runEnd - runStart)})
			runStart = -1
		}
	}
	for i := int64(0); i < n; i++ {
		off := i * sbSize
		ln := sbSize
		if off+ln > int64(a.Size) {
			ln = int64(a.Size) - off
		}
		if ln <= 0 {
			break
		}
		if i < bNSB && bytes.Equal(a.SBHash(i), b.SBHash(i)) {
			flush()
			continue
		}
		if runStart < 0 {
			runStart = off
		}
		runEnd = off + ln
	}
	flush()
	return ranges
}

// RangesAll 返回覆盖整个文件的单个区间。
func RangesAll(size uint64) []Range {
	if size == 0 {
		return nil
	}
	return []Range{{Off: 0, Len: size}}
}

// Compare 用完整索引与已存索引逐块比较，返回不一致的细块数量。
func Compare(a, b *index.File) (diffSB int64) {
	if a == nil || b == nil || a.LB != b.LB || a.SB != b.SB || a.Size != b.Size {
		return -1
	}
	n := a.NSB()
	if b.NSB() < n {
		n = b.NSB()
	}
	for i := int64(0); i < n; i++ {
		if !bytes.Equal(a.SBHash(i), b.SBHash(i)) {
			diffSB++
		}
	}
	if a.NSB() != b.NSB() {
		return -1
	}
	return diffSB
}
