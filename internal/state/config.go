// 本机配置（生效目录、按目录对排除规则、具名配置）的读写，存放于用户本地配置目录，不随项目目录复制。
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config 本机运行所需的路径、按目录对保存的排除规则与具名配置。
// LocalDir/CarrierDir 始终表示当前生效的一对目录，命令行同步只读取它们。
type Config struct {
	LocalDir      string       `json:"localDir"`
	CarrierDir    string       `json:"carrierDir"`
	ExcludeSets   []ExcludeSet `json:"excludeSets,omitempty"`
	Profiles      []Profile    `json:"profiles,omitempty"`
	ActiveProfile string       `json:"activeProfile,omitempty"`
}

// ExcludeSet 一组目录对的排除规则；方向无关，A/B 交换后仍是同一组。
type ExcludeSet struct {
	A        string   `json:"a"`
	B        string   `json:"b"`
	Excludes []string `json:"excludes"`
}

// Profile 一组具名的输入/输出目录；排除规则不重复存放，按目录对取自 ExcludeSets。
type Profile struct {
	Name       string `json:"name"`
	LocalDir   string `json:"localDir"`
	CarrierDir string `json:"carrierDir"`
}

// DefaultProfileName 无配置的旧版配置迁移时生成的配置名称。
const DefaultProfileName = "默认"

// FindProfile 按名称查找配置，返回下标与可写指针；不存在时返回 -1 与 nil。
func (c *Config) FindProfile(name string) (int, *Profile) {
	if c == nil {
		return -1, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return -1, nil
	}
	for i := range c.Profiles {
		if strings.EqualFold(strings.TrimSpace(c.Profiles[i].Name), name) {
			return i, &c.Profiles[i]
		}
	}
	return -1, nil
}

// MatchProfile 返回目录对（无序）对应的配置名称；没有匹配时返回空串。
func (c *Config) MatchProfile(a, b string) string {
	if c == nil || a == "" || b == "" {
		return ""
	}
	for _, p := range c.Profiles {
		if SamePair(p.LocalDir, p.CarrierDir, a, b) {
			return p.Name
		}
	}
	return ""
}

// NextProfileName 返回未占用的“配置N”名称，N 取当前最小的空闲序号。
func (c *Config) NextProfileName() string {
	for n := 1; ; n++ {
		name := fmt.Sprintf("配置%d", n)
		if i, _ := c.FindProfile(name); i < 0 {
			return name
		}
	}
}

// SetProfile 新增或替换配置；renameFrom 非空时先按该名称定位，用于改名。
// 生效配置跟随改名，规则由调用方负责去重校验。
func (c *Config) SetProfile(renameFrom string, p Profile) {
	p.Name = strings.TrimSpace(p.Name)
	i := -1
	if strings.TrimSpace(renameFrom) != "" {
		i, _ = c.FindProfile(renameFrom)
	}
	if i < 0 {
		i, _ = c.FindProfile(p.Name)
	}
	if i >= 0 {
		c.Profiles[i] = p
	} else {
		c.Profiles = append(c.Profiles, p)
	}
	if strings.TrimSpace(renameFrom) != "" && strings.EqualFold(c.ActiveProfile, strings.TrimSpace(renameFrom)) {
		c.ActiveProfile = p.Name
	}
}

// DeleteProfile 删除名称匹配的配置；被删的是生效配置时清空生效项，目录字段不动。
func (c *Config) DeleteProfile(name string) bool {
	i, _ := c.FindProfile(name)
	if i < 0 {
		return false
	}
	c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
	if strings.EqualFold(c.ActiveProfile, strings.TrimSpace(name)) {
		c.ActiveProfile = ""
	}
	return true
}

// ProfilePairInUse 判断目录对（无序）是否仍被除 exceptName 之外的配置引用。
func (c *Config) ProfilePairInUse(a, b, exceptName string) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Profiles {
		if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(exceptName)) {
			continue
		}
		if SamePair(p.LocalDir, p.CarrierDir, a, b) {
			return true
		}
	}
	return false
}

// RemoveExcludes 删除目录对的排除规则，返回是否存在并已删除。
func (c *Config) RemoveExcludes(a, b string) bool {
	if c == nil || a == "" || b == "" {
		return false
	}
	ka, kb := pairKeys(a, b)
	for i := range c.ExcludeSets {
		sa, sb := pairKeys(c.ExcludeSets[i].A, c.ExcludeSets[i].B)
		if sa == ka && sb == kb {
			c.ExcludeSets = append(c.ExcludeSets[:i], c.ExcludeSets[i+1:]...)
			return true
		}
	}
	return false
}

// MigrateExcludes 把排除规则从一对目录搬到另一对目录；
// 目录对未变、旧目录对没有规则、或旧目录对仍被其它配置引用时不做处理。
func (c *Config) MigrateExcludes(oldA, oldB, newA, newB, exceptName string) {
	if c == nil || oldA == "" || oldB == "" || SamePair(oldA, oldB, newA, newB) {
		return
	}
	if c.ProfilePairInUse(oldA, oldB, exceptName) {
		return
	}
	ex := c.ExcludesFor(oldA, oldB)
	if ex == nil {
		return
	}
	c.SetExcludes(newA, newB, ex)
	c.RemoveExcludes(oldA, oldB)
}

// EnsureProfile 在无配置时用当前目录对生成默认配置，并修正生效配置名称；返回是否改动。
// 已有配置但生效项为空或失效时改指第一条。
func (c *Config) EnsureProfile() bool {
	if c == nil {
		return false
	}
	if len(c.Profiles) == 0 {
		if c.LocalDir == "" && c.CarrierDir == "" {
			return false
		}
		c.Profiles = append(c.Profiles, Profile{Name: DefaultProfileName, LocalDir: c.LocalDir, CarrierDir: c.CarrierDir})
		c.ActiveProfile = DefaultProfileName
		return true
	}
	i, _ := c.FindProfile(c.ActiveProfile)
	if i < 0 {
		c.ActiveProfile = c.Profiles[0].Name
		return true
	}
	if c.ActiveProfile != c.Profiles[i].Name {
		c.ActiveProfile = c.Profiles[i].Name
		return true
	}
	return false
}

// SameDir 判断两个目录是否指向同一位置（忽略大小写、尾斜杠与相对写法）。
func SameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return normalizeDir(a) == normalizeDir(b)
}

// SamePair 判断两个目录对是否无序相同。
func SamePair(a1, b1, a2, b2 string) bool {
	if a1 == "" || b1 == "" || a2 == "" || b2 == "" {
		return false
	}
	x1, y1 := pairKeys(a1, b1)
	x2, y2 := pairKeys(a2, b2)
	return x1 == x2 && y1 == y2
}

// ExcludesFor 返回目录对的排除规则；未登记或任一侧为空时返回 nil。
func (c *Config) ExcludesFor(a, b string) []string {
	if c == nil || a == "" || b == "" {
		return nil
	}
	ka, kb := pairKeys(a, b)
	for i := range c.ExcludeSets {
		sa, sb := pairKeys(c.ExcludeSets[i].A, c.ExcludeSets[i].B)
		if sa == ka && sb == kb {
			return c.ExcludeSets[i].Excludes
		}
	}
	return nil
}

// SetExcludes 登记目录对的排除规则，保留其它目录对。
func (c *Config) SetExcludes(a, b string, excludes []string) {
	if excludes == nil {
		excludes = []string{}
	}
	ka, kb := pairKeys(a, b)
	for i := range c.ExcludeSets {
		sa, sb := pairKeys(c.ExcludeSets[i].A, c.ExcludeSets[i].B)
		if sa == ka && sb == kb {
			c.ExcludeSets[i].Excludes = excludes
			return
		}
	}
	c.ExcludeSets = append(c.ExcludeSets, ExcludeSet{A: a, B: b, Excludes: excludes})
}

// pairKeys 规范化两个目录名并排序，使 (a,b) 与 (b,a) 得到同一组键。
func pairKeys(a, b string) (string, string) {
	x, y := normalizeDir(a), normalizeDir(b)
	if x > y {
		x, y = y, x
	}
	return x, y
}

func normalizeDir(p string) string {
	p = strings.TrimSpace(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func ConfigPath() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("无法确定本机配置目录: %w", err)
		}
		base = d
	}
	return filepath.Join(base, "blocksync", "config.json"), nil
}

// LoadConfig 读取本机配置，不存在时返回 (nil, nil)。
func LoadConfig() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Config{}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("配置文件解析失败 %s: %w", p, err)
	}
	return c, nil
}

func SaveConfig(c *Config) error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
