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

// Excluded 判断相对路径是否命中排除规则。
// 规则按行解释：\ 视为分隔符；行首 \ 锚定根目录，否则匹配任意层级；
// 行尾 \ 表示目录，命中后整棵子树排除；通配符沿用 path.Match。
func Excluded(rel string, excludes []string) bool {
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	for _, raw := range excludes {
		if matchRule(rel, raw) {
			return true
		}
	}
	return false
}

func matchRule(rel, raw string) bool {
	r, ok := parseRule(raw)
	if !ok {
		return false
	}
	if !hasGlob(r.pat) && !r.anchored && !r.dirOnly {
		// 纯名字：任意层级出现同名条目即排除
		if rel == r.pat || strings.HasPrefix(rel, r.pat+"/") {
			return true
		}
		for _, seg := range strings.Split(rel, "/") {
			if seg == r.pat {
				return true
			}
		}
		return false
	}
	if r.dirOnly {
		for _, dir := range ancestors(rel) {
			if matchAtLevels(dir, r.pat, r.anchored) {
				return true
			}
		}
		return false
	}
	return matchAtLevels(rel, r.pat, r.anchored)
}

// excludeRule 一条解析后的排除规则。
type excludeRule struct {
	pat      string
	anchored bool
	dirOnly  bool
}

// parseRule 解析单条规则：\ 视作分隔符，行首锚定根目录，行尾表示目录。
func parseRule(raw string) (excludeRule, bool) {
	pat := strings.TrimSpace(raw)
	if pat == "" {
		return excludeRule{}, false
	}
	pat = strings.ReplaceAll(pat, `\`, "/")
	anchored := strings.HasPrefix(pat, "/")
	pat = strings.TrimPrefix(pat, "/")
	dirOnly := strings.HasSuffix(pat, "/")
	pat = strings.TrimSuffix(pat, "/")
	if pat == "" || pat == "." || pat == ".." {
		return excludeRule{}, false
	}
	if runtime.GOOS == "windows" {
		pat = strings.ToLower(pat)
	}
	return excludeRule{pat: pat, anchored: anchored, dirOnly: dirOnly}, true
}

// ValidatePattern 检查单条排除规则的写法是否合法，供界面保存前校验。
func ValidatePattern(raw string) error {
	r, ok := parseRule(raw)
	if !ok {
		return nil
	}
	if _, err := path.Match(r.pat, "x"); err != nil {
		return err
	}
	return nil
}

// ancestors 返回 rel 自身与各级上级目录。
func ancestors(rel string) []string {
	out := []string{rel}
	for i := 0; i < len(rel); i++ {
		if rel[i] == '/' {
			out = append(out, rel[:i])
		}
	}
	return out
}

// matchAtLevels 整体匹配路径；未锚定时对每一段后缀尝试，* 前缀可匹配根层。
func matchAtLevels(rel, pat string, anchored bool) bool {
	cands := []string{rel, "/" + rel}
	if !anchored {
		for i := 0; i < len(rel); i++ {
			if rel[i] == '/' {
				s := rel[i+1:]
				cands = append(cands, s, "/"+s)
			}
		}
	}
	for _, c := range cands {
		if ok, err := path.Match(pat, c); err == nil && ok {
			return true
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
