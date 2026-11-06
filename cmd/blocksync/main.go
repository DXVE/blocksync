// blocksync 命令行入口：sync/setup/status/verify/repair 五个子命令。
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"blocksync/internal/index"
	"blocksync/internal/mirror"
	"blocksync/internal/scan"
	"blocksync/internal/state"
	"blocksync/internal/version"
)

const usageText = `blocksync ` + version.Version + ` —— 大目录/大文件的块级增量同步

版本: ` + version.Version + `

用法:

  blocksync setup                       交互录入本机两个目录，首次必做
  blocksync setup --show                查看当前配置

  blocksync sync <输入目录> <输出目录> [--dry-run] [--full] [--verify]
      把输入目录增量镜像到输出目录：只写变化的块，未变化的文件不读取。
      反向同步交换两个参数的顺序即可。

  blocksync status [<目录>]             查看该目录的索引与状态
  blocksync verify [<目录>]             全量复算，检查内容与索引是否一致
  blocksync repair [<目录>]             按磁盘实际内容重建索引
  blocksync version                     显示版本号

日常流程（同样可在图形界面中点选完成）:
  blocksync setup                       首次：录入并保存两个目录
  blocksync sync "D:\data\project" "E:\backup\project"
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
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd := os.Args[1]
	var err error
	switch cmd {
	case "repair":
		err = cmdRepair(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "sync":
		err = cmdSync(os.Args[2:])
	case "setup":
		err = cmdSetup(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("blocksync " + version.Version)
		return
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

// reorder 把选项挪到位置参数之前，使 `blocksync sync A B --dry-run` 这种写法可用。
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
			if err := cmdRepair([]string{cfg.LocalDir}); err != nil {
				return cfg, err
			}
		}
	}
	return cfg, nil
}

// loadConfigOrWarn 读取本机配置；失败时提示一行并返回 nil，调用方退回内置规则。
func loadConfigOrWarn() *state.Config {
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "提示：本机配置读取失败（%v），本次只用内置排除规则\n", err)
		return nil
	}
	return cfg
}

// loadExcludes 汇总排除规则：内置默认 → 目录对的界面配置。
// other 为同一次同步的另一侧目录；只有单侧目录时传空串。
func loadExcludes(local, other string) []string {
	ex := append([]string(nil), scan.DefaultExcludes...)
	if other == "" {
		return ex
	}
	if cfg := loadConfigOrWarn(); cfg != nil {
		ex = append(ex, cfg.ExcludesFor(local, other)...)
	}
	return ex
}

// loadExcludesForDir 供单目录命令（repair/verify）使用：从配置查出该目录所属
// 目录对的另一侧，取到与 sync 相同的排除规则。
func loadExcludesForDir(dir string) []string {
	ex := append([]string(nil), scan.DefaultExcludes...)
	cfg := loadConfigOrWarn()
	if cfg == nil {
		return ex
	}
	if other := cfg.OtherSide(dir); other != "" {
		ex = append(ex, cfg.ExcludesFor(dir, other)...)
	}
	return ex
}

// warnLegacyExcludes 提示旧版按目录存放的排除文件已停用；只检查存在性，不读取内容。
func warnLegacyExcludes(dir string) {
	p := filepath.Join(state.Dir(dir), "config.json")
	if _, err := os.Stat(p); err == nil {
		fmt.Fprintf(os.Stderr, "提示：%s 已停用，排除规则统一从本机配置读取\n", p)
	}
}

// ---------- repair ----------

// cmdRepair 按磁盘实际内容重建块索引，并清理已不存在文件的残留索引。
func cmdRepair(args []string) error {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	workers := fs.Int("workers", 0, "")
	lb := fs.Int64("lb", index.DefaultLBSize, "")
	sb := fs.Int64("sb", index.DefaultSBSize, "")
	force := fs.Bool("force", false, "")
	if err := parseFlags(fs, args, "workers", "lb", "sb"); err != nil {
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

	store := index.NewStore(state.Dir(local))
	excludes := loadExcludesForDir(local)
	files, err := scan.Walk(local, excludes)
	if err != nil {
		return fmt.Errorf("扫描目录失败: %w", err)
	}
	w := scan.Workers(*workers)

	var total uint64
	for i, f := range files {
		abs := filepath.Join(local, filepath.FromSlash(f.Rel))
		fmt.Fprintf(os.Stderr, "[%d/%d] 建立索引 %s (%s)\n", i+1, len(files), f.Rel, scan.FormatSize(uint64(f.Size)))
		res, err := scan.ScanFull(abs, *lb, *sb, w)
		if err != nil {
			return fmt.Errorf("扫描 %s 失败: %w", f.Rel, err)
		}
		if err := store.Save(f.Rel, res.New); err != nil {
			return err
		}
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

	fmt.Printf("完成：%d 个文件，共 %s\n", len(files), scan.FormatSize(total))
	return nil
}

// ---------- sync ----------

// askPlan 在实际写入前展示计划并要求确认；非交互输入按取消处理。
func askPlan(p mirror.Plan) (bool, error) {
	fmt.Print("\n" + p.Summary())
	fmt.Println("写入只发生在输出目录，输入目录保持只读。")
	fmt.Println(mirror.ConfirmMarker + "。")
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

	if fs.NArg() != 2 {
		return errors.New("用法: blocksync sync <输入目录> <输出目录>")
	}
	src := filepath.Clean(fs.Arg(0))
	dst := filepath.Clean(fs.Arg(1))

	if err := mirror.CheckRoots(src, dst); err != nil {
		return err
	}
	warnLegacyExcludes(src)

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
		Excludes:        loadExcludes(src, dst),
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
			scan.FormatSize(rep.BytesRead), scan.FormatSize(rep.BytesWritten))
		return nil
	}
	fmt.Printf("\n完成：扫描 %d 个，改动 %d 个，删除 %d 个；读取 %s，写入 %s\n",
		rep.FilesScanned, rep.FilesChanged, rep.FilesDeleted,
		scan.FormatSize(rep.BytesRead), scan.FormatSize(rep.BytesWritten))
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
		return nil
	}
	_, err := runSetup()
	return err
}

// ---------- status ----------

// cmdStatus 输出目录的索引覆盖情况，供确认索引是否完整。
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
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
			return errors.New("本机尚未配置，请直接运行 blocksync setup")
		}
		local = cfg.LocalDir
	}
	if st, err := os.Stat(local); err != nil || !st.IsDir() {
		return fmt.Errorf("目录不存在: %s", local)
	}
	store := index.NewStore(state.Dir(local))
	list, _ := store.List()
	var indexed uint64
	for _, rel := range list {
		if size, err := store.Size(rel); err == nil {
			indexed += size
		}
	}
	fmt.Printf("目录      : %s\n", local)
	fmt.Printf("索引文件  : %d 个，覆盖 %s\n", len(list), scan.FormatSize(indexed))
	return nil
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
	excludes := loadExcludesForDir(local)
	files, err := scan.Walk(local, excludes)
	if err != nil {
		return fmt.Errorf("扫描目录失败: %w", err)
	}
	w := scan.Workers(workers)
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
		switch d := scan.Compare(res.New, old); {
		case d < 0:
			fmt.Printf("内容不符  %s（文件长度与索引记录不同）\n", f.Rel)
			bad++
		case d > 0:
			fmt.Printf("内容不符  %s（%d 个细块不一致）\n", f.Rel, d)
			bad++
		}
	}
	idxList, _ := store.List()
	for _, rel := range idxList {
		// 被排除规则跳过的文件没有索引是正常的，不算残留
		if scan.Excluded(rel, excludes) {
			continue
		}
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
