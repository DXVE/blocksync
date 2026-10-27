// 本机配置（源目录、目标目录、站点名）的读写，存放于用户本地配置目录，不随项目目录复制。
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

// Config 本机运行所需的路径、站点标识与按目录对保存的排除规则。
type Config struct {
	LocalDir    string       `json:"localDir"`
	CarrierDir  string       `json:"carrierDir"`
	SiteID      string       `json:"siteId"`
	ExcludeSets []ExcludeSet `json:"excludeSets,omitempty"`
}

// ExcludeSet 一组目录对的排除规则；方向无关，A/B 交换后仍是同一组。
type ExcludeSet struct {
	A        string   `json:"a"`
	B        string   `json:"b"`
	Excludes []string `json:"excludes"`
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
