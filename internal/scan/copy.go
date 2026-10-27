// 单遍复制整个文件并生成块索引：一次读取同时计算哈希与写入目标。
package scan

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"time"

	"blocksync/internal/index"
)

// CopyFull 把 src 复制到 dst，并在同一遍读取中生成索引；返回的索引与 ScanFull 结果一致。
func CopyFull(src, dst string, lb, sb int64) (*index.File, error) {
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	size := st.Size()
	nf := index.New(uint32(lb), uint32(sb), uint64(size), st.ModTime().UnixNano())
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	sf, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer sf.Close()
	df, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer df.Close()

	perLB := lb / sb
	buf := make([]byte, lb)
	for off := int64(0); off < size; off += lb {
		n := lb
		if off+n > size {
			n = size - off
		}
		if _, err := io.ReadFull(sf, buf[:n]); err != nil {
			return nil, err
		}
		lbIdx := off / lb
		h := sha256.Sum256(buf[:n])
		copy(nf.LBHashes[lbIdx*index.HashLen:], h[:])
		base := lbIdx * perLB * index.HashLen
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
			pos := base + j*index.HashLen
			if pos+index.HashLen <= int64(len(nf.SBHashes)) {
				copy(nf.SBHashes[pos:], sh[:])
			}
		}
		if _, err := df.WriteAt(buf[:n], off); err != nil {
			return nil, err
		}
	}
	if err := df.Truncate(size); err != nil {
		return nil, err
	}
	if err := df.Sync(); err != nil {
		return nil, err
	}
	if err := df.Close(); err != nil {
		return nil, err
	}
	t := time.Unix(0, nf.MtimeNano)
	_ = os.Chtimes(dst, t, t)
	return nf, nil
}
