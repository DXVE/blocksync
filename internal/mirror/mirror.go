// 目录到目录的增量镜像：两侧各存一份块索引，只在必要时读源文件，只写变化的区间。
package mirror

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"blocksync/internal/index"
	"blocksync/internal/scan"
	"blocksync/internal/state"
)

const copyChunk = 4 << 20

// Options 一次同步的参数。
type Options struct {
	Source   string
	Target   string
	LB, SB   int64
	Workers  int
	Full     bool
	DryRun   bool
	Verify   bool
	Excludes []string
	Out      io.Writer

	// AllowMassDelete 允许一次删除超过安全上限的数量的文件。
	AllowMassDelete bool
	// Confirm 首次同步或需要删除时征求用户同意；为 nil 表示不询问。
	Confirm func(Plan) (bool, error)
}

// Plan 写入前对本次同步的估算，只依据文件列表与元数据，不读取文件内容。
type Plan struct {
	Source, Target string
	NewFiles       int
	NewBytes       int64
	ResizedFiles   int
	ResizedBytes   int64
	CompareFiles   int
	DeleteFiles    int
	DeleteBytes    int64
	FirstRun       bool
}

// ErrCancelled 用户在确认阶段取消，未写入任何文件。
var ErrCancelled = errors.New("已取消：未写入任何文件")

// NeedsConfirm 首次同步或包含删除时才需要用户确认。
func (p Plan) NeedsConfirm() bool { return p.FirstRun || p.DeleteFiles > 0 }

// Summary 生成给人看的计划摘要。
func (p Plan) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "准备从 %s 同步到 %s\n", p.Source, p.Target)
	if p.FirstRun {
		b.WriteString("  目标目录还没有索引，本次为首次同步\n")
	}
	if p.NewFiles > 0 {
		fmt.Fprintf(&b, "  新增 %d 个文件（约 %s）\n", p.NewFiles, sizeStr(uint64(p.NewBytes)))
	}
	if p.ResizedFiles > 0 {
		fmt.Fprintf(&b, "  大小变化 %d 个文件（共 %s）\n", p.ResizedFiles, sizeStr(uint64(p.ResizedBytes)))
	}
	if p.CompareFiles > 0 {
		fmt.Fprintf(&b, "  需比对 %d 个文件（大小相同，写入量取决于块比对）\n", p.CompareFiles)
	}
	if p.DeleteFiles > 0 {
		fmt.Fprintf(&b, "  删除 %d 个文件（共 %s）\n", p.DeleteFiles, sizeStr(uint64(p.DeleteBytes)))
	}
	return b.String()
}

// Report 同步结果统计。
type Report struct {
	FilesScanned int
	FilesChanged int
	FilesDeleted int
	BytesRead    uint64
	BytesWritten uint64
	// Skipped 输入目录中按排除规则跳过的条目（目录以 / 结尾）。
	Skipped []string
}

func (o *Options) workers() int {
	if o.Workers > 0 {
		return o.Workers
	}
	return runtime.NumCPU()
}

// Run 把 Source 目录镜像到 Target 目录。
func Run(o Options) (*Report, error) {
	if !scan.ValidateBlocks(o.LB, o.SB) {
		return nil, fmt.Errorf("块大小非法：粗块 %d 必须大于细块 %d 且能被其整除", o.LB, o.SB)
	}
	for _, p := range []string{o.Source, o.Target} {
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return nil, fmt.Errorf("目录不存在: %s", p)
		}
	}
	absSrc, _ := filepath.Abs(o.Source)
	absDst, _ := filepath.Abs(o.Target)
	if absSrc == absDst {
		return nil, errors.New("输入目录与输出目录相同")
	}
	if err := CheckRoots(o.Source, o.Target); err != nil {
		return nil, err
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	srcRoot, err := canonicalPath(o.Source)
	if err != nil {
		return nil, err
	}
	dstRoot, err := canonicalPath(o.Target)
	if err != nil {
		return nil, err
	}

	srcStore := index.NewStore(state.Dir(o.Source))
	dstStore := index.NewStore(state.Dir(o.Target))

	srcFiles, skipped, err := scan.WalkStats(o.Source, o.Excludes)
	if err != nil {
		return nil, fmt.Errorf("扫描输入目录失败: %w", err)
	}
	dstFiles, err := scan.Walk(o.Target, o.Excludes)
	if err != nil {
		return nil, fmt.Errorf("扫描输出目录失败: %w", err)
	}

	rep := &Report{Skipped: skipped}
	inSrc := make(map[string]bool, len(srcFiles))
	dstMap := make(map[string]scan.Entry, len(dstFiles))
	for _, f := range srcFiles {
		inSrc[scan.PathKey(f.Rel)] = true
	}
	for _, f := range dstFiles {
		dstMap[scan.PathKey(f.Rel)] = f
	}
	var dels []scan.Entry
	for _, f := range dstFiles {
		if !inSrc[scan.PathKey(f.Rel)] {
			dels = append(dels, f)
		}
	}
	w := o.workers()

	plan := buildPlan(o, srcFiles, dstMap, dels, dstStore)
	if !o.DryRun {
		if err := o.checkMassDelete(len(dels), len(srcFiles)); err != nil {
			return nil, err
		}
		if o.Confirm != nil && plan.NeedsConfirm() {
			ok, err := o.Confirm(plan)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, ErrCancelled
			}
		}
	}

	total := len(srcFiles) + len(dels)
	for i, f := range srcFiles {
		rep.FilesScanned++
		absSrcFile := filepath.Join(o.Source, filepath.FromSlash(f.Rel))
		absDstFile := filepath.Join(o.Target, filepath.FromSlash(f.Rel))

		srcIdx, err := srcStore.Load(f.Rel)
		if err != nil {
			fmt.Fprintf(out, "  警告: %s 源索引损坏，将重建\n", f.Rel)
			srcIdx = nil
		}
		dstIdx, err := dstStore.Load(f.Rel)
		if err != nil {
			dstIdx = nil
		}
		dstEntry, dstExists := dstMap[scan.PathKey(f.Rel)]
		dstFresh := false

		// 目标侧索引必须与目标文件现状一致，否则重扫，避免按过期哈希算区间
		if dstExists {
			dstFresh = dstIdx != nil &&
				dstIdx.Size == uint64(dstEntry.Size) &&
				dstIdx.MtimeNano == dstEntry.MtimeNano &&
				dstIdx.LB == uint32(o.LB) && dstIdx.SB == uint32(o.SB)
			if !dstFresh {
				dres, err := scan.ScanFull(absDstFile, o.LB, o.SB, w)
				if err != nil {
					return nil, fmt.Errorf("读取目标文件 %s 失败: %w", f.Rel, err)
				}
				dstIdx = dres.New
				rep.BytesRead += dres.ScannedBytes
			}
		} else {
			dstIdx = nil
		}

		srcFresh := !o.Full && srcIdx != nil &&
			srcIdx.Size == uint64(f.Size) && srcIdx.MtimeNano == f.MtimeNano &&
			srcIdx.LB == uint32(o.LB) && srcIdx.SB == uint32(o.SB)

		// 目标已有该文件却仍要重算时，标出原因，便于判断索引为何没被复用
		srcWhy := ""
		if !srcFresh && dstExists {
			switch {
			case o.Full:
				srcWhy = "全量比对"
			case srcIdx == nil:
				srcWhy = "源索引不存在"
			case srcIdx.Size != uint64(f.Size):
				srcWhy = "源文件长度变化"
			case srcIdx.MtimeNano != f.MtimeNano:
				srcWhy = "源文件修改时间变化"
			case srcIdx.LB != uint32(o.LB) || srcIdx.SB != uint32(o.SB):
				srcWhy = "块大小不同"
			default:
				srcWhy = "源索引不可用"
			}
		}

		copied := false
		var (
			ranges []scan.Range
			newIdx *index.File
		)
		if !o.DryRun && !dstExists && !srcFresh {
			// 新文件且源索引不可信：一遍读取完成哈希与复制，避免再读一次源文件。
			if err := o.guardWriteTarget(absDstFile, srcRoot, dstRoot); err != nil {
				return nil, err
			}
			if err := ensureDistinct(absSrcFile, absDstFile); err != nil {
				return nil, err
			}
			nf, err := scan.CopyFull(absSrcFile, absDstFile, o.LB, o.SB)
			if err != nil {
				return nil, fmt.Errorf("写入 %s 失败: %w", f.Rel, err)
			}
			newIdx = nf
			copied = true
			rep.BytesRead += nf.Size
		} else if srcFresh {
			// 源索引可信：新索引直接沿用，区间只靠哈希推算，不读文件内容。
			// 长度不同也要逐块比对，不能整文件重写。
			newIdx = srcIdx
			ranges = scan.RangesAgainst(srcIdx, dstIdx)
		} else {
			res, err := scan.ScanDiff(absSrcFile, o.LB, o.SB, dstIdx, w)
			if err != nil {
				return nil, fmt.Errorf("比对 %s 失败: %w", f.Rel, err)
			}
			ranges = res.Ranges
			newIdx = res.New
			rep.BytesRead += res.ScannedBytes
		}

		changedBytes := uint64(0)
		if copied {
			changedBytes = newIdx.Size
		} else {
			for _, r := range ranges {
				changedBytes += r.Len
			}
		}
		// 目标缺失或长度不同时，即使没有变化块也要写一次：覆盖空文件与截断到 0 的情况
		needWrite := changedBytes > 0 || !dstExists || (dstIdx != nil && dstIdx.Size != uint64(f.Size))

		status, detail := "未变", ""
		if needWrite {
			rep.FilesChanged++
			switch {
			case !dstExists:
				status = "新增"
				detail = fmt.Sprintf("（%s）", sizeStr(uint64(f.Size)))
			case changedBytes > 0:
				status = "更新"
				detail = fmt.Sprintf("变化块 %s（文件共 %s）", sizeStr(changedBytes), sizeStr(uint64(f.Size)))
			case f.Size == 0:
				status = "清空"
			default:
				status = "截断"
				detail = "截断到 " + sizeStr(uint64(f.Size))
			}
		}
		fmt.Fprintf(out, "[%d/%d] %s  %s", i+1, total, status, f.Rel)
		if detail != "" {
			fmt.Fprintf(out, "  %s", detail)
		}
		if srcWhy != "" {
			fmt.Fprintf(out, "（%s）", srcWhy)
		}
		fmt.Fprintln(out)

		if needWrite {
			rep.BytesWritten += changedBytes
			if !o.DryRun {
				if !copied {
					if err := o.guardWriteTarget(absDstFile, srcRoot, dstRoot); err != nil {
						return nil, err
					}
					if err := ensureDistinct(absSrcFile, absDstFile); err != nil {
						return nil, err
					}
					if err := copyRanges(absSrcFile, absDstFile, ranges, int64(newIdx.Size), f.MtimeNano); err != nil {
						return nil, fmt.Errorf("写入 %s 失败: %w", f.Rel, err)
					}
				}
				if o.Verify {
					got, err := scan.ScanFull(absDstFile, o.LB, o.SB, w)
					if err != nil {
						return nil, fmt.Errorf("校验 %s 失败: %w", f.Rel, err)
					}
					if !got.New.EqualContent(newIdx) {
						return nil, fmt.Errorf("校验失败：%s 写入后内容与源不一致", f.Rel)
					}
				}
			}
		}
		if o.DryRun {
			continue
		}

		// 只在索引缺失、失效或文件实际写入时保存；未变化文件不重写索引。
		if !srcFresh {
			if err := saveIndex(srcStore, f.Rel, newIdx, f.MtimeNano); err != nil {
				return nil, err
			}
		}
		if needWrite || (dstExists && !dstFresh) {
			dstMtime := f.MtimeNano
			if st, err := os.Stat(absDstFile); err == nil {
				dstMtime = st.ModTime().UnixNano()
			}
			if err := saveIndex(dstStore, f.Rel, newIdx, dstMtime); err != nil {
				return nil, err
			}
		}
	}

	// 输出目录里多出来的文件按镜像语义删除
	for j, f := range dels {
		abs := filepath.Join(o.Target, filepath.FromSlash(f.Rel))
		rep.FilesDeleted++
		fmt.Fprintf(out, "[%d/%d] 删除  %s（源端已不存在）\n", len(srcFiles)+j+1, total, f.Rel)
		if o.DryRun {
			continue
		}
		if err := o.guardTarget(abs, srcRoot, dstRoot); err != nil {
			return nil, err
		}
		if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("删除 %s 失败: %w", f.Rel, err)
		}
		_ = dstStore.Delete(f.Rel)
	}
	// 两侧索引都收缩到源文件集合，避免残留条目
	if !o.DryRun {
		for _, st := range []*index.Store{srcStore, dstStore} {
			list, err := st.List()
			if err != nil {
				continue
			}
			for _, rel := range list {
				if !inSrc[scan.PathKey(rel)] {
					_ = st.Delete(rel)
				}
			}
		}
	}
	return rep, nil
}

// ---------- 源目录保护 ----------

// CheckRoots 校验两个目录既不重合也不互相包含。任何“输出目录套着输入目录”
// 的配置都会让镜像删除波及源文件，因此在动手前一律拒绝。
func CheckRoots(src, dst string) error {
	s, err := canonicalPath(src)
	if err != nil {
		return err
	}
	d, err := canonicalPath(dst)
	if err != nil {
		return err
	}
	switch {
	case s == d:
		return fmt.Errorf("输入目录与输出目录相同：%s", src)
	case isWithin(s, d):
		return fmt.Errorf("输出目录位于输入目录之内（%s 在 %s 里），会破坏输入目录", dst, src)
	case isWithin(d, s):
		return fmt.Errorf("输出目录包含输入目录（%s 在 %s 里），镜像删除会删掉输入目录中的文件", src, dst)
	}
	return nil
}

// canonicalPath 规范化路径：拿句柄解析联接后的最终位置，Windows 下统一大小写，
// 使两个指向同一位置的不同写法可以比较；解析失败直接报错。
func canonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return resolvePath(abs)
}

// resolvePath 返回路径的最终位置；末级不存在时以最近的已存在上级为基准拼接剩余部分。
func resolvePath(abs string) (string, error) {
	cur := filepath.Clean(abs)
	rest := ""
	for {
		final, _, exists, err := probePath(cur)
		if err != nil {
			return "", fmt.Errorf("解析路径 %s 失败: %w", abs, err)
		}
		if exists {
			return normalizePath(filepath.Join(final, rest)), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("无法解析路径: %s", abs)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func normalizePath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func isWithin(root, p string) bool {
	if p == root {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	return strings.HasPrefix(p, root)
}

// guardTarget 在每次删除前独立校验：不得落在输入目录，且必须在输出目录内。
func (o *Options) guardTarget(abs, srcRoot, dstRoot string) error {
	return guardLocation(abs, srcRoot, dstRoot, false)
}

// guardWriteTarget 在每次写入前校验位置，并拒绝有多个硬链接的目标文件：
// 写入这类文件会改动同一份数据的其他路径。
func (o *Options) guardWriteTarget(abs, srcRoot, dstRoot string) error {
	return guardLocation(abs, srcRoot, dstRoot, true)
}

// guardLocation 按句柄解析出的最终路径校验位置；解析失败一律中止，不再按字符串路径放行。
func guardLocation(abs, srcRoot, dstRoot string, forWrite bool) error {
	final, links, exists, err := probePath(abs)
	if err != nil {
		return fmt.Errorf("无法确认 %s 的实际位置: %w", abs, err)
	}
	if exists {
		final = normalizePath(final)
	} else if final, err = resolvePath(abs); err != nil {
		return err
	}
	if isWithin(srcRoot, final) {
		return fmt.Errorf("已阻止写入 %s：该路径位于输入目录内，疑似输入/输出目录嵌套", abs)
	}
	if !isWithin(dstRoot, final) {
		return fmt.Errorf("已阻止写入 %s：该路径不在输出目录内", abs)
	}
	if forWrite && exists && links > 1 {
		return fmt.Errorf("已阻止写入 %s：该文件有 %d 个硬链接，写入会改动同一份数据的其他路径", abs, links)
	}
	return nil
}

// ensureDistinct 阻止把与源文件同一份数据（硬链接等）的目标文件当作可写对象。
func ensureDistinct(srcPath, dstPath string) error {
	si, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	di, err := os.Stat(dstPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if os.SameFile(si, di) {
		return fmt.Errorf("源文件与目标文件是同一份数据，已阻止写入：%s", dstPath)
	}
	return nil
}

// buildPlan 只按文件列表与元数据估算本次同步，不读取文件内容。
func buildPlan(o Options, srcFiles []scan.Entry, dstMap map[string]scan.Entry, dels []scan.Entry, dstStore *index.Store) Plan {
	p := Plan{Source: o.Source, Target: o.Target}
	for _, f := range srcFiles {
		d, ok := dstMap[f.Rel]
		switch {
		case !ok:
			p.NewFiles++
			p.NewBytes += f.Size
		case d.Size != f.Size:
			p.ResizedFiles++
			p.ResizedBytes += f.Size
		default:
			p.CompareFiles++
		}
	}
	for _, f := range dels {
		p.DeleteBytes += f.Size
	}
	p.DeleteFiles = len(dels)
	if _, err := os.Stat(dstStore.Root()); errors.Is(err, os.ErrNotExist) {
		p.FirstRun = true
	}
	return p
}

// checkMassDelete 在删除数量异常时中止，用于拦截方向写反或根目录选错。
func (o *Options) checkMassDelete(deletes, total int) error {
	if o.AllowMassDelete || deletes == 0 {
		return nil
	}
	limit := total / 100
	if limit < 100 {
		limit = 100
	}
	if deletes > limit {
		return fmt.Errorf("本次将删除 %d 个文件（输入目录共 %d 个），超过安全上限 %d。请先确认方向与两个目录无误，再加 --allow-mass-delete 重新执行", deletes, total, limit)
	}
	return nil
}

func saveIndex(store *index.Store, rel string, f *index.File, mtime int64) error {
	g := f.Clone()
	g.MtimeNano = mtime
	return store.Save(rel, g)
}

// copyRanges 按区间从源文件拷到目标文件，最后截断到源长度并还原修改时间。
func copyRanges(srcPath, dstPath string, ranges []scan.Range, size int64, mtimeNano int64) error {
	sf, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer sf.Close()
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	df, err := os.OpenFile(dstPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, copyChunk)
	for _, r := range ranges {
		var done uint64
		for done < r.Len {
			n := uint64(len(buf))
			if n > r.Len-done {
				n = r.Len - done
			}
			if _, err := sf.ReadAt(buf[:n], int64(r.Off+done)); err != nil && !errors.Is(err, io.EOF) {
				df.Close()
				return err
			}
			if _, err := df.WriteAt(buf[:n], int64(r.Off+done)); err != nil {
				df.Close()
				return err
			}
			done += n
		}
	}
	if err := df.Truncate(size); err != nil {
		df.Close()
		return err
	}
	if err := df.Sync(); err != nil {
		df.Close()
		return err
	}
	if err := df.Close(); err != nil {
		return err
	}
	if mtimeNano != 0 {
		t := time.Unix(0, mtimeNano)
		_ = os.Chtimes(dstPath, t, t)
	}
	return nil
}

func sizeStr(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
