// blocksync 命令行入口：init/push/apply/status/verify/repair 六个子命令。
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"blocksync/internal/apply"
	"blocksync/internal/index"
	"blocksync/internal/mirror"
	"blocksync/internal/patch"
	"blocksync/internal/scan"
	"blocksync/internal/state"
)

const usageText = `blocksync —— 大目录/大文件的块级增量同步

用法（省略目录参数时使用 blocksync setup 保存的两个目录）:

  blocksync setup                       交互录入本机两个目录，首次必做
  blocksync setup --show                查看当前配置

  blocksync sync [<输入目录> <输出目录>] [--dry-run] [--full] [--verify]
      把输入目录增量镜像到输出目录：只写变化的块，未变化的文件不读取。
  blocksync sync --reverse
      反向同步（移动硬盘 -> 本机），使用已保存的配置。

  blocksync status [<目录>]             查看该目录的索引与状态
  blocksync verify [<目录>]             全量复算，检查内容与索引是否一致
  blocksync repair [<目录>]             按磁盘实际内容重建索引

日常流程（同样可在图形界面中点选完成）:
  blocksync setup                       首次：录入并保存两个目录
  blocksync sync                        离开前：本机 -> 移动硬盘
  blocksync sync --reverse --verify     到达后：移动硬盘 -> 本机，写后校验
  blocksync setup --show                查看已保存的配置

通用选项:
  --dry-run     测试比对：只报告将搬运多少与具体文件，不写任何数据文件
  --verify      写入后复读校验：把写下去的内容读回来重算哈希
  --full        忽略修改时间，全量比对：强制重读所有文件内容
  --workers N   哈希线程数，默认等于 CPU 核数
  --lb BYTES    粗块大小，默认 4194304（4 MiB）
  --sb BYTES    细块大小，默认 32768（32 KiB），两端必须一致
  --force       忽略上次异常退出遗留的目录锁
  --yes         跳过首次同步与删除前的确认（供脚本调用）
  --allow-mass-delete  删除数量超过安全上限时仍继续（确认无误再用）

源目录保护: 输入目录与输出目录相同、或互相包含时直接拒绝；每次写入与删除
都会复核路径，确保不落在输入目录内。首次同步或需要删除文件时，会先列出计划
并要求确认。

进阶（两个目录无法同时访问时分两步走）:
  blocksync init / push / apply，详见 README
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd := os.Args[1]
	var err error
	switch cmd {
	case "init":
		err = cmdRebuild(os.Args[2:], "init")
	case "repair":
		err = cmdRebuild(os.Args[2:], "repair")
	case "push":
		err = cmdPush(os.Args[2:])
	case "apply":
		err = cmdApply(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "sync":
		err = cmdSync(os.Args[2:])
	case "setup":
		err = cmdSetup(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usageText)
		return
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func workersOrDefault(n int) int {
	if n > 0 {
		return n
	}
	return runtime.NumCPU()
}

// reorder 把选项挪到位置参数之前，使 `blocksync push 目录 载波 --dry-run` 这种写法可用。
func reorder(args []string, takesValue map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name, _, hasVal := strings.Cut(a, "=")
			trimmed := strings.TrimLeft(name, "-")
			flags = append(flags, a)
			if takesValue[trimmed] && !hasVal && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func parseFlags(fs *flag.FlagSet, args []string, takesValue ...string) error {
	m := make(map[string]bool, len(takesValue))
	for _, v := range takesValue {
		m[v] = true
	}
	return fs.Parse(reorder(args, m))
}

// ---------- 本机配置与交互录入 ----------

var stdin = bufio.NewReader(os.Stdin)

func askLine(prompt, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, err := stdin.ReadString('\n')
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "\"")
	if err != nil && line == "" {
		fmt.Println()
		return "", errors.New("输入已结束，配置未完成")
	}
	if line == "" {
		line = def
	}
	return line, nil
}

func askYes(prompt string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Printf("%s [%s]: ", prompt, hint)
	line, err := stdin.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if err != nil && line == "" {
		fmt.Println()
		return def
	}
	if line == "" {
		return def
	}
	return line == "y" || line == "yes" || line == "是"
}

func askDir(prompt, def string, create bool) (string, error) {
	for {
		v, err := askLine(prompt, def)
		if err != nil {
			return "", err
		}
		if v == "" {
			fmt.Println("  不能为空，请重新输入")
			continue
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			fmt.Printf("  路径无法解析：%v\n", err)
			continue
		}
		if st, err := os.Stat(abs); err == nil {
			if !st.IsDir() {
				fmt.Println("  该路径不是目录，请重新输入")
				continue
			}
			return abs, nil
		}
		if !create {
			fmt.Printf("  目录不存在：%s\n", abs)
			continue
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			fmt.Printf("  无法创建目录：%v\n", err)
			continue
		}
		fmt.Printf("  已创建目录 %s\n", abs)
		return abs, nil
	}
}

// autoDetectCarrier 在各盘符下查找已存在的 blocksync 目录，用于移动硬盘盘符变化时自动纠正。
func autoDetectCarrier() (string, bool) {
	var found []string
	for c := 'D'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if st, err := os.Stat(filepath.Join(root, "blocksync")); err == nil && st.IsDir() {
			found = append(found, root)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return "", false
}

// runSetup 交互录入并保存本机配置，返回最终配置。
func runSetup() (*state.Config, error) {
	cur, err := state.LoadConfig()
	if err != nil {
		fmt.Printf("（读取旧配置失败，将重新录入：%v）\n", err)
	}
	cfg := &state.Config{}
	if cur != nil {
		*cfg = *cur
	}

	fmt.Println()
	fmt.Println("===== blocksync 首次配置 =====")
	fmt.Println("提示：可以把文件夹直接拖到本窗口再回车，路径两侧的引号会自动去掉。")
	fmt.Println()

	local, err := askDir("源目录（要同步出去的文件夹）", cfg.LocalDir, false)
	if err != nil {
		return nil, err
	}
	carrier, err := askDir("移动硬盘同步目录（填盘符根目录即可，例如 E:\\）", cfg.CarrierDir, true)
	if err != nil {
		return nil, err
	}
	cfg.LocalDir, cfg.CarrierDir = local, carrier
	if err := state.SaveConfig(cfg); err != nil {
		return nil, err
	}
	p, _ := state.ConfigPath()
	fmt.Println()
	fmt.Printf("配置已保存：%s\n", p)
	fmt.Printf("  源目录      ：%s\n", cfg.LocalDir)
	fmt.Printf("  目标目录    ：%s\n", cfg.CarrierDir)

	if _, err := os.Stat(filepath.Join(state.Dir(cfg.LocalDir), "idx")); err != nil {
		fmt.Println()
		if askYes("该目录还没有索引，现在建立吗（只读扫描一遍，几分钟）", true) {
			fmt.Println()
			if err := cmdRebuild([]string{cfg.LocalDir, "--site", cfg.SiteID}, "init"); err != nil {
				return cfg, err
			}
		}
	}
	return cfg, nil
}

// resolveTargets 优先使用命令行给出的两个目录，否则读取本机配置，配置缺失时现场录入。
func resolveTargets(fs *flag.FlagSet, siteName *string) (local, carrier string, err error) {
	args := fs.Args()
	if len(args) >= 2 {
		local = filepath.Clean(args[0])
		carrier = filepath.Clean(args[1])
		return local, carrier, nil
	}

	cfg, err := state.LoadConfig()
	if err != nil {
		return "", "", err
	}
	if cfg == nil || cfg.LocalDir == "" || cfg.CarrierDir == "" {
		if cfg, err = runSetup(); err != nil {
			return "", "", err
		}
	}
	if *siteName == "" {
		*siteName = cfg.SiteID
	}
	carrier = cfg.CarrierDir
	if st, err := os.Stat(carrier); err != nil || !st.IsDir() {
		if alt, ok := autoDetectCarrier(); ok {
			fmt.Printf("提示：配置的载波目录 %s 不可用，已自动改用 %s\n", carrier, alt)
			carrier = alt
			cfg.CarrierDir = alt
			_ = state.SaveConfig(cfg)
		}
	}
	return cfg.LocalDir, carrier, nil
}

func loadExcludes(local string) []string {
	ex := append([]string(nil), scan.DefaultExcludes...)
	p := filepath.Join(state.Dir(local), "config.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return ex
	}
	var cfg struct {
		Excludes []string `json:"excludes"`
	}
	if json.Unmarshal(b, &cfg) == nil {
		ex = append(ex, cfg.Excludes...)
	}
	return ex
}

func fmtSize(n uint64) string {
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

// ---------- init / repair ----------

func cmdRebuild(args []string, mode string) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	workers := fs.Int("workers", 0, "")
	lb := fs.Int64("lb", index.DefaultLBSize, "")
	sb := fs.Int64("sb", index.DefaultSBSize, "")
	force := fs.Bool("force", false, "")
	siteName := fs.String("site", "", "")
	if err := parseFlags(fs, args, "workers", "lb", "sb", "site"); err != nil {
		return err
	}
	local := ""
	if fs.NArg() >= 1 {
		local = filepath.Clean(fs.Arg(0))
	} else {
		cfg, err := state.LoadConfig()
		if err != nil {
			return err
		}
		if cfg == nil || cfg.LocalDir == "" {
			if cfg, err = runSetup(); err != nil {
				return err
			}
		}
		local = cfg.LocalDir
		if *siteName == "" {
			*siteName = cfg.SiteID
		}
	}
	if !scan.ValidateBlocks(*lb, *sb) {
		return fmt.Errorf("块大小非法：粗块 %d 必须大于细块 %d 且能被其整除", *lb, *sb)
	}
	if st, err := os.Stat(local); err != nil || !st.IsDir() {
		return fmt.Errorf("目录不存在: %s", local)
	}
	if _, err := os.Stat(state.Dir(local)); err != nil {
		if err := os.MkdirAll(state.Dir(local), 0o755); err != nil {
			return err
		}
	}
	unlock, err := state.Lock(local, *force)
	if err != nil {
		return err
	}
	defer unlock()

	site, err := state.Load(local)
	if err != nil {
		return err
	}
	if *siteName != "" && site.SiteID != *siteName {
		site.SiteID = *siteName
		if err := state.Save(local, site); err != nil {
			return err
		}
	}
	store := index.NewStore(state.Dir(local))
	excludes := loadExcludes(local)
	files, err := scan.Walk(local, excludes)
	if err != nil {
		return fmt.Errorf("扫描目录失败: %w", err)
	}
	w := workersOrDefault(*workers)

	var entries []index.SetEntry
	var total uint64
	for i, f := range files {
		abs := filepath.Join(local, filepath.FromSlash(f.Rel))
		fmt.Fprintf(os.Stderr, "[%d/%d] 建立索引 %s (%s)\n", i+1, len(files), f.Rel, fmtSize(uint64(f.Size)))
		res, err := scan.ScanFull(abs, *lb, *sb, w)
		if err != nil {
			return fmt.Errorf("扫描 %s 失败: %w", f.Rel, err)
		}
		if err := store.Save(f.Rel, res.New); err != nil {
			return err
		}
		entries = append(entries, index.SetEntry{Path: f.Rel, Size: res.New.Size, Digest: res.New.ContentDigest()})
		total += res.New.Size
	}
	// 清理磁盘上已不存在的旧索引
	disk := map[string]bool{}
	for _, f := range files {
		disk[f.Rel] = true
	}
	idxList, _ := store.List()
	for _, rel := range idxList {
		if !disk[rel] {
			_ = store.Delete(rel)
		}
	}

	site.Generation++
	site.SetDigest = state.DigestHex(index.SetDigest(entries))
	if err := state.Save(local, site); err != nil {
		return err
	}
	fmt.Printf("完成：%d 个文件，共 %s\n", len(files), fmtSize(total))
	fmt.Printf("站点 %s，代次 %d，状态摘要 %s\n", site.SiteID, site.Generation, short(site.SetDigest))
	fmt.Println("请在对端站点也执行一次 init（内容一致时两侧摘要必然相同）")
	return nil
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// ---------- push ----------

type pendingFile struct {
	rel     string
	action  patch.Action
	spec    patch.EntrySpec
	idx     *index.File
	changed uint64
}

func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	full := fs.Bool("full", false, "")
	force := fs.Bool("force", false, "")
	workers := fs.Int("workers", 0, "")
	lb := fs.Int64("lb", index.DefaultLBSize, "")
	sb := fs.Int64("sb", index.DefaultSBSize, "")
	siteName := fs.String("site", "", "")
	if err := parseFlags(fs, args, "workers", "lb", "sb", "site"); err != nil {
		return err
	}
	local, carrier, err := resolveTargets(fs, siteName)
	if err != nil {
		return err
	}
	if !scan.ValidateBlocks(*lb, *sb) {
		return fmt.Errorf("块大小非法：粗块 %d 必须大于细块 %d 且能被其整除", *lb, *sb)
	}
	if st, err := os.Stat(local); err != nil || !st.IsDir() {
		return fmt.Errorf("本地目录不存在: %s", local)
	}
	if st, err := os.Stat(carrier); err != nil || !st.IsDir() {
		return fmt.Errorf("载波目录不存在: %s", carrier)
	}
	unlock, err := state.Lock(local, *force)
	if err != nil {
		return err
	}
	defer unlock()

	site, err := state.Load(local)
	if err != nil {
		return err
	}
	if *siteName != "" && site.SiteID != *siteName {
		site.SiteID = *siteName
		if err := state.Save(local, site); err != nil {
			return err
		}
	}
	store := index.NewStore(state.Dir(local))
	excludes := loadExcludes(local)
	files, err := scan.Walk(local, excludes)
	if err != nil {
		return fmt.Errorf("扫描目录失败: %w", err)
	}
	w := workersOrDefault(*workers)

	var (
		setEntries []index.SetEntry
		pending    []pendingFile
		newIdx     = map[string]*index.File{}
		totalBytes uint64
		scanned    int
	)
	for i, f := range files {
		abs := filepath.Join(local, filepath.FromSlash(f.Rel))
		old, err := store.Load(f.Rel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: %s 索引损坏，将重建（%v）\n", f.Rel, err)
			old = nil
		}
		if old != nil && !*full && old.Size == uint64(f.Size) && old.MtimeNano == f.MtimeNano {
			setEntries = append(setEntries, index.SetEntry{Path: f.Rel, Size: old.Size, Digest: old.ContentDigest()})
			continue
		}

		scanned++
		fmt.Fprintf(os.Stderr, "[%d/%d] 比对 %s (%s)\n", i+1, len(files), f.Rel, fmtSize(uint64(f.Size)))
		action := patch.ActionModify
		var res *scan.Result
		if old == nil {
			action = patch.ActionAdd
			res, err = scan.ScanFull(abs, *lb, *sb, w)
		} else {
			res, err = scan.ScanDiff(abs, *lb, *sb, old, w)
		}
		if err != nil {
			return fmt.Errorf("扫描 %s 失败: %w", f.Rel, err)
		}
		newIdx[f.Rel] = res.New
		setEntries = append(setEntries, index.SetEntry{Path: f.Rel, Size: res.New.Size, Digest: res.New.ContentDigest()})

		if len(res.Ranges) > 0 {
			rs := make([]patch.Range, len(res.Ranges))
			for k, r := range res.Ranges {
				rs[k] = patch.Range{Off: r.Off, Len: r.Len}
			}
			pending = append(pending, pendingFile{
				rel:    f.Rel,
				action: action,
				spec: patch.EntrySpec{
					Path: f.Rel, Action: action,
					NewSize:   res.New.Size,
					MtimeNano: res.New.MtimeNano,
					LB:        res.New.LB, SB: res.New.SB,
					Ranges: rs,
				},
				idx:     res.New,
				changed: res.ChangedBytes,
			})
			totalBytes += res.ChangedBytes
		}
	}

	// 磁盘上已删除的文件
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Rel] = true
	}
	idxList, _ := store.List()
	for _, rel := range idxList {
		if seen[rel] || scan.Excluded(rel, excludes) {
			continue
		}
		if _, err := os.Stat(filepath.Join(local, filepath.FromSlash(rel))); errors.Is(err, os.ErrNotExist) {
			pending = append(pending, pendingFile{
				rel:    rel,
				action: patch.ActionDelete,
				spec:   patch.EntrySpec{Path: rel, Action: patch.ActionDelete},
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].rel < pending[j].rel })

	if len(pending) == 0 {
		fmt.Printf("无需搬运：%d 个文件均未变化（比对扫描 %d 个）\n", len(files), scanned)
		return nil
	}

	fmt.Printf("待搬运：%d 个文件，%s\n", len(pending), fmtSize(totalBytes))
	for _, p := range pending {
		if p.action == patch.ActionDelete {
			fmt.Printf("  %-8s %s\n", p.action, p.rel)
			continue
		}
		fmt.Printf("  %-8s %s  变化块 %s（文件共 %s）\n", p.action, p.rel, fmtSize(p.changed), fmtSize(p.idx.Size))
	}
	if *dry {
		fmt.Println("--dry-run：未写入任何文件")
		return nil
	}

	gen := site.Generation + 1
	newDigest := index.SetDigest(setEntries)
	patchDir := filepath.Join(carrier, "blocksync", site.SiteID)
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-g%04d%s", time.Now().Format("20060102-150405"), gen, patch.Ext)
	final := filepath.Join(patchDir, name)
	tmp := final + ".tmp"
	parentDigest, err := state.DigestBytes(site.SetDigest)
	if err != nil {
		return err
	}
	pw, err := patch.Create(tmp, final, gen, parentDigest, newDigest)
	if err != nil {
		return err
	}

	for _, p := range pending {
		if p.action == patch.ActionDelete {
			ew, err := pw.BeginEntry(&p.spec)
			if err != nil {
				pw.Abort()
				return err
			}
			if err := ew.End(nil); err != nil {
				pw.Abort()
				return err
			}
			continue
		}
		abs := filepath.Join(local, filepath.FromSlash(p.rel))
		sf, err := os.Open(abs)
		if err != nil {
			pw.Abort()
			return err
		}
		ew, err := pw.BeginEntry(&p.spec)
		if err != nil {
			sf.Close()
			pw.Abort()
			return err
		}
		n, err := io.Copy(ew, patch.NewRangeReader(sf, p.spec.Ranges))
		if err == nil && uint64(n) != p.spec.PayloadLen() {
			err = fmt.Errorf("读取源数据不足：%d / %d", n, p.spec.PayloadLen())
		}
		if err == nil {
			err = ew.End(p.idx.Encode())
		}
		sf.Close()
		if err != nil {
			pw.Abort()
			return fmt.Errorf("生成 %s 的补丁失败: %w", p.rel, err)
		}
	}
	if err := pw.Close(); err != nil {
		return err
	}

	for rel, f := range newIdx {
		if err := store.Save(rel, f); err != nil {
			return err
		}
	}
	for _, p := range pending {
		if p.action == patch.ActionDelete {
			_ = store.Delete(p.rel)
		}
	}
	site.Generation = gen
	site.SetDigest = state.DigestHex(newDigest)
	site.LastPushed = name
	if err := state.Save(local, site); err != nil {
		return err
	}

	st, _ := os.Stat(final)
	var sz int64
	if st != nil {
		sz = st.Size()
	}
	fmt.Printf("已写入补丁 %s（%s，共 %d 个文件条目）\n", name, fmtSize(uint64(sz)), pw.FileCount())
	return nil
}

// ---------- apply ----------

func siteDirs(carrier string) ([]string, error) {
	root := filepath.Join(carrier, "blocksync")
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	return out, nil
}

func findChain(infos []patch.Info, cur [32]byte) []patch.Info {
	used := make([]bool, len(infos))
	var chain []patch.Info
	for {
		found := -1
		for i, in := range infos {
			if used[i] {
				continue
			}
			if in.Header.Parent == cur {
				found = i
				break
			}
		}
		if found < 0 {
			return chain
		}
		used[found] = true
		chain = append(chain, infos[found])
		cur = infos[found].Header.Result
	}
}

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	verify := fs.Bool("verify", false, "")
	force := fs.Bool("force", false, "")
	keep := fs.Int("keep", 2, "应用后每个对端保留的补丁个数")
	siteName := fs.String("site", "", "")
	if err := parseFlags(fs, args, "keep", "site"); err != nil {
		return err
	}
	local, carrier, err := resolveTargets(fs, siteName)
	if err != nil {
		return err
	}
	if st, err := os.Stat(local); err != nil || !st.IsDir() {
		return fmt.Errorf("本地目录不存在: %s", local)
	}
	if st, err := os.Stat(carrier); err != nil || !st.IsDir() {
		return fmt.Errorf("载波目录不存在: %s", carrier)
	}
	unlock, err := state.Lock(local, *force)
	if err != nil {
		return err
	}
	defer unlock()

	site, err := state.Load(local)
	if err != nil {
		return err
	}
	if *siteName != "" {
		site.SiteID = *siteName
	}
	store := index.NewStore(state.Dir(local))
	cur, err := state.DigestBytes(site.SetDigest)
	if err != nil {
		return err
	}

	dirs, err := siteDirs(carrier)
	if err != nil {
		return err
	}
	var infos []patch.Info
	for _, d := range dirs {
		if filepath.Base(d) == site.SiteID {
			continue
		}
		l, err := patch.List(d)
		if err != nil {
			return err
		}
		infos = append(infos, l...)
	}
	if len(infos) == 0 {
		fmt.Println("载波上没有待应用的补丁")
		return nil
	}
	chain := findChain(infos, cur)
	if len(chain) == 0 {
		fmt.Println("载波上的补丁与本机状态不衔接：")
		for _, in := range infos {
			fmt.Printf("  %s  代次 %d  基线 %s\n", in.Name, in.Header.Generation, short(state.DigestHex(in.Header.Parent)))
		}
		return fmt.Errorf("本机状态摘要为 %s。常见原因：上一次离开时漏了 push，或两端各自改过文件造成分叉", short(state.DigestHex(cur)))
	}

	if *dry {
		var total int64
		var nfiles int
		for _, in := range chain {
			r, err := patch.Open(in.Path)
			if err != nil {
				return err
			}
			var b int64
			var n int
			_ = r.Walk(func(e *patch.EntryHeader, _ io.ReaderAt) error {
				b += e.PayloadLen
				n++
				return nil
			})
			r.Close()
			fmt.Printf("  %s  代次 %d  %d 个文件  %s\n", in.Name, in.Header.Generation, n, fmtSize(uint64(b)))
			total += b
			nfiles += n
		}
		fmt.Printf("将应用 %d 个补丁、%d 个文件条目，共写入 %s（--dry-run 未改动任何文件）\n",
			len(chain), nfiles, fmtSize(uint64(total)))
		return nil
	}

	for _, in := range chain {
		r, err := patch.Open(in.Path)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "校验并应用 %s ...\n", in.Name)
		if err := r.Verify(); err != nil {
			r.Close()
			return fmt.Errorf("%s: %w", in.Name, err)
		}
		var written int64
		var nfiles int
		err = r.Walk(func(e *patch.EntryHeader, payload io.ReaderAt) error {
			n, err := apply.Entry(local, store, e, payload)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Path, err)
			}
			written += n
			nfiles++
			if *verify && len(e.Index) > 0 && e.Action != patch.ActionDelete {
				if err := verifyAgainstIndex(local, e); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			r.Close()
			return err
		}
		site.Generation = r.Header.Generation
		site.SetDigest = state.DigestHex(r.Header.Result)
		site.LastApplied = in.Name
		r.Close()
		if err := state.Save(local, site); err != nil {
			return err
		}
		fmt.Printf("已应用 %s：%d 个文件，写入 %s\n", in.Name, nfiles, fmtSize(uint64(written)))
	}

	if *keep >= 0 {
		prune(carrier, site.SiteID, *keep, dirs)
	}
	fmt.Printf("完成：站点 %s 代次 %d，状态摘要 %s\n", site.SiteID, site.Generation, short(site.SetDigest))
	return nil
}

func verifyAgainstIndex(local string, e *patch.EntryHeader) error {
	f, err := index.Decode(e.Index)
	if err != nil {
		return fmt.Errorf("校验 %s: 补丁内索引损坏: %w", e.Path, err)
	}
	abs := filepath.Join(local, filepath.FromSlash(e.Path))
	res, err := scan.ScanFull(abs, int64(f.LB), int64(f.SB), runtime.NumCPU())
	if err != nil {
		return fmt.Errorf("校验 %s 失败: %w", e.Path, err)
	}
	if !res.New.EqualContent(f) {
		return fmt.Errorf("校验失败：%s 应用后内容与补丁声明不一致", e.Path)
	}
	return nil
}

func prune(carrier, ownSite string, keep int, dirs []string) {
	for _, d := range dirs {
		if filepath.Base(d) == ownSite {
			continue
		}
		list, err := patch.List(d)
		if err != nil || len(list) <= keep {
			continue
		}
		for _, in := range list[:len(list)-keep] {
			_ = os.Remove(in.Path)
		}
	}
}

// ---------- sync ----------

// 界面据此行判断需要弹出确认框，修改时两侧必须保持一致。
const confirmMarker = "需要确认后才会开始写入"

// askPlan 在实际写入前展示计划并要求确认；非交互输入按取消处理。
func askPlan(p mirror.Plan) (bool, error) {
	fmt.Print("\n" + p.Summary())
	fmt.Println("写入只发生在输出目录，输入目录保持只读。")
	fmt.Println(confirmMarker + "。")
	fmt.Print("确认继续？[y/N]: ")
	line, err := stdin.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	// 非终端输入不会回显回车，补一个换行，避免后续输出与提示行挤在一起
	if st, e := os.Stdin.Stat(); e == nil && st.Mode()&os.ModeCharDevice == 0 {
		fmt.Println()
	}
	if err != nil && line == "" {
		return false, errors.New("未获得确认：当前不是交互终端，确认无误后请加 --yes")
	}
	return line == "y" || line == "yes" || line == "是", nil
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	reverse := fs.Bool("reverse", false, "反向：移动硬盘 -> 本机")
	dry := fs.Bool("dry-run", false, "测试比对：只报告，不写任何文件")
	full := fs.Bool("full", false, "忽略修改时间，全量比对")
	verify := fs.Bool("verify", false, "写入后复读校验")
	force := fs.Bool("force", false, "跳过上次遗留的目录锁")
	quiet := fs.Bool("quiet", false, "不输出逐文件进度")
	yes := fs.Bool("yes", false, "跳过首次同步与删除前的确认")
	allowMassDelete := fs.Bool("allow-mass-delete", false, "允许一次删除超过安全上限的文件数")
	workers := fs.Int("workers", 0, "")
	lb := fs.Int64("lb", index.DefaultLBSize, "")
	sb := fs.Int64("sb", index.DefaultSBSize, "")
	if err := parseFlags(fs, args, "workers", "lb", "sb"); err != nil {
		return err
	}

	var src, dst string
	if fs.NArg() >= 2 {
		src = filepath.Clean(fs.Arg(0))
		dst = filepath.Clean(fs.Arg(1))
	} else {
		cfg, err := state.LoadConfig()
		if err != nil {
			return err
		}
		if cfg == nil || cfg.LocalDir == "" || cfg.CarrierDir == "" {
			if cfg, err = runSetup(); err != nil {
				return err
			}
		}
		src, dst = cfg.LocalDir, cfg.CarrierDir
		if *reverse {
			src, dst = dst, src
		}
	}

	if err := mirror.CheckRoots(src, dst); err != nil {
		return err
	}

	if !*dry {
		unlockSrc, err := state.Lock(src, *force)
		if err != nil {
			return err
		}
		defer unlockSrc()
		unlockDst, err := state.Lock(dst, *force)
		if err != nil {
			return err
		}
		defer unlockDst()
	}

	if !*quiet {
		fmt.Printf("输入：%s\n输出：%s\n", src, dst)
	}
	progress := os.Stderr
	if *quiet {
		progress = nil
	}
	var confirm func(mirror.Plan) (bool, error)
	if !*yes {
		confirm = askPlan
	}
	rep, err := mirror.Run(mirror.Options{
		Source:          src,
		Target:          dst,
		LB:              *lb,
		SB:              *sb,
		Workers:         *workers,
		Full:            *full,
		DryRun:          *dry,
		Verify:          *verify,
		Excludes:        loadExcludes(src),
		Out:             progress,
		AllowMassDelete: *allowMassDelete,
		Confirm:         confirm,
	})
	if err != nil {
		return err
	}
	if len(rep.Skipped) > 0 {
		fmt.Printf("按排除规则跳过 %d 项：%s\n", len(rep.Skipped), skippedLine(rep.Skipped))
	}
	if rep.FilesChanged == 0 && rep.FilesDeleted == 0 {
		if *dry {
			fmt.Printf("测试比对：%d 个文件均无变化，未改动任何数据\n", rep.FilesScanned)
		} else {
			fmt.Printf("已是最新：%d 个文件均无变化\n", rep.FilesScanned)
		}
		return nil
	}
	if *dry {
		fmt.Printf("\n完成（测试比对）：扫描 %d 个，改动 %d 个，删除 %d 个；读取 %s，预计需写入 %s（未改动任何数据）\n",
			rep.FilesScanned, rep.FilesChanged, rep.FilesDeleted,
			fmtSize(rep.BytesRead), fmtSize(rep.BytesWritten))
		return nil
	}
	fmt.Printf("\n完成：扫描 %d 个，改动 %d 个，删除 %d 个；读取 %s，写入 %s\n",
		rep.FilesScanned, rep.FilesChanged, rep.FilesDeleted,
		fmtSize(rep.BytesRead), fmtSize(rep.BytesWritten))
	return nil
}

// ---------- setup ----------

// skippedLine 把被跳过的条目拼成一行，最多列出 6 项。
func skippedLine(list []string) string {
	const max = 6
	names := list
	if len(names) > max {
		names = names[:max]
	}
	out := strings.Join(names, "、")
	if len(list) > max {
		out += " …"
	}
	return out
}

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	show := fs.Bool("show", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *show {
		cfg, err := state.LoadConfig()
		if err != nil {
			return err
		}
		if cfg == nil {
			return errors.New("本机尚未配置，请直接运行 blocksync setup")
		}
		p, _ := state.ConfigPath()
		fmt.Printf("配置文件  : %s\n", p)
		fmt.Printf("源目录    : %s\n", cfg.LocalDir)
		fmt.Printf("目标目录  : %s\n", cfg.CarrierDir)
		fmt.Printf("站点名    : %s\n", cfg.SiteID)
		return nil
	}
	_, err := runSetup()
	return err
}

// ---------- status ----------

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	siteName := fs.String("site", "", "")
	if err := parseFlags(fs, args, "site"); err != nil {
		return err
	}
	local, carrierArg := "", ""
	if fs.NArg() >= 1 {
		local = filepath.Clean(fs.Arg(0))
	}
	if fs.NArg() >= 2 {
		carrierArg = filepath.Clean(fs.Arg(1))
	}
	if local == "" {
		cfg, err := state.LoadConfig()
		if err != nil {
			return err
		}
		if cfg == nil || cfg.LocalDir == "" {
			return errors.New("本机尚未配置，请先运行 blocksync setup")
		}
		local = cfg.LocalDir
		if carrierArg == "" {
			carrierArg = cfg.CarrierDir
		}
	}
	site, err := state.Load(local)
	if err != nil {
		return err
	}
	if *siteName != "" {
		site.SiteID = *siteName
	}
	store := index.NewStore(state.Dir(local))
	list, _ := store.List()
	var indexed uint64
	for _, rel := range list {
		if f, err := store.Load(rel); err == nil && f != nil {
			indexed += f.Size
		}
	}
	fmt.Printf("站点      : %s\n", site.SiteID)
	fmt.Printf("代次      : %d\n", site.Generation)
	fmt.Printf("状态摘要  : %s\n", orDash(site.SetDigest))
	fmt.Printf("索引文件  : %d 个，覆盖 %s\n", len(list), fmtSize(indexed))
	if site.LastPushed != "" {
		fmt.Printf("最近推送  : %s\n", site.LastPushed)
	}
	if site.LastApplied != "" {
		fmt.Printf("最近应用  : %s\n", site.LastApplied)
	}
	if carrierArg == "" {
		return nil
	}
	carrier := carrierArg
	dirs, err := siteDirs(carrier)
	if err != nil {
		return err
	}
	cur, _ := state.DigestBytes(site.SetDigest)
	var total int64
	var infos []patch.Info
	for _, d := range dirs {
		if filepath.Base(d) == site.SiteID {
			continue
		}
		l, _ := patch.List(d)
		infos = append(infos, l...)
		for _, in := range l {
			if st, err := os.Stat(in.Path); err == nil {
				total += st.Size()
			}
		}
	}
	fmt.Printf("载波补丁  : %d 个，共 %s\n", len(infos), fmtSize(uint64(total)))
	chain := findChain(infos, cur)
	if len(chain) > 0 {
		fmt.Printf("可直接应用: %d 个（最新代次 %d）\n", len(chain), chain[len(chain)-1].Header.Generation)
	} else if len(infos) > 0 {
		fmt.Println("可直接应用: 0（补丁与本机基线不衔接，可能漏推或已分叉）")
	} else {
		fmt.Println("可直接应用: 0（没有对端补丁）")
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "(未初始化)"
	}
	return s
}

// ---------- verify ----------

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	workers := fs.Int("workers", 0, "")
	quiet := fs.Bool("quiet", false, "")
	if err := parseFlags(fs, args, "workers"); err != nil {
		return err
	}
	if fs.NArg() >= 1 {
		local := filepath.Clean(fs.Arg(0))
		return verifyDir(local, *workers, *quiet)
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil || cfg.LocalDir == "" {
		return errors.New("本机尚未配置，请先运行 blocksync setup")
	}
	return verifyDir(cfg.LocalDir, *workers, *quiet)
}

func verifyDir(local string, workers int, quiet bool) error {
	store := index.NewStore(state.Dir(local))
	excludes := loadExcludes(local)
	files, err := scan.Walk(local, excludes)
	if err != nil {
		return fmt.Errorf("扫描目录失败: %w", err)
	}
	w := workersOrDefault(workers)
	disk := map[string]bool{}
	bad := 0
	for i, f := range files {
		disk[f.Rel] = true
		abs := filepath.Join(local, filepath.FromSlash(f.Rel))
		fmt.Fprintf(os.Stderr, "[%d/%d] 校验 %s\n", i+1, len(files), f.Rel)
		old, err := store.Load(f.Rel)
		if err != nil {
			fmt.Printf("索引损坏  %s: %v\n", f.Rel, err)
			bad++
			continue
		}
		if old == nil {
			fmt.Printf("缺少索引  %s\n", f.Rel)
			bad++
			continue
		}
		res, err := scan.ScanFull(abs, int64(old.LB), int64(old.SB), w)
		if err != nil {
			fmt.Printf("扫描失败  %s: %v\n", f.Rel, err)
			bad++
			continue
		}
		if d := scan.Compare(res.New, old); d != 0 {
			fmt.Printf("内容不符  %s（%d 个细块不一致）\n", f.Rel, d)
			bad++
		}
	}
	idxList, _ := store.List()
	for _, rel := range idxList {
		if !disk[rel] {
			fmt.Printf("多余索引  %s\n", rel)
			bad++
		}
	}
	if bad == 0 {
		fmt.Printf("校验通过：%d 个文件与索引一致\n", len(files))
		return nil
	}
	return fmt.Errorf("发现 %d 处不一致，可执行 blocksync repair 重建索引", bad)
}
