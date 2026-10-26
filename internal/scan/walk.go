// 目录枚举与排除规则；排除项同时按相对路径和文件名匹配。
package scan

import (
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// DefaultExcludes 只排除工具自身状态目录与 Windows 系统文件，其余内容一律忠实同步。
var DefaultExcludes = []string{
	".blocksync",
	"$RECYCLE.BIN",
	"System Volume Information",
	"$WinREAgent",
	"Config.Msi",
	"desktop.ini",
	"Thumbs.db",
	"pagefile.sys",
	"hiberfil.sys",
	"swapfile.sys",
}

// Entry 一个待同步的普通文件。
type Entry struct {
	Rel       string
	Size      int64
	MtimeNano int64
}

func hasGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// Excluded 判断相对路径是否命中排除规则；Windows 路径不区分大小写。
func Excluded(rel string, excludes []string) bool {
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	base := path.Base(rel)
	for _, pat := range excludes {
		if pat == "" {
			continue
		}
		pat = strings.TrimSuffix(pat, "/")
		if runtime.GOOS == "windows" {
			pat = strings.ToLower(pat)
		}
		if ok, err := path.Match(pat, rel); err == nil && ok {
			return true
		}
		if ok, err := path.Match(pat, base); err == nil && ok {
			return true
		}
		if !hasGlob(pat) {
			if rel == pat || strings.HasPrefix(rel, pat+"/") {
				return true
			}
			// 目录名出现在任意层级时整棵子树都排除
			for _, seg := range strings.Split(rel, "/") {
				if seg == pat {
					return true
				}
			}
		}
	}
	return false
}

// Walk 返回 root 下所有需要同步的普通文件，按相对路径排序。
func Walk(root string, excludes []string) ([]Entry, error) {
	out, _, err := WalkStats(root, excludes)
	return out, err
}

// WalkStats 在 Walk 的基础上返回被跳过的条目（排除项与非普通文件），
// 供调用方说明“为什么只扫描了这几个”。
func WalkStats(root string, excludes []string) ([]Entry, []string, error) {
	var out []Entry
	var skipped []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if Excluded(rel, excludes) {
				skipped = append(skipped, rel+"/")
				return fs.SkipDir
			}
			return nil
		}
		if Excluded(rel, excludes) {
			skipped = append(skipped, rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			skipped = append(skipped, rel)
			return nil
		}
		out = append(out, Entry{Rel: rel, Size: info.Size(), MtimeNano: info.ModTime().UnixNano()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	sort.Strings(skipped)
	return out, skipped, err
}
