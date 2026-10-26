// 本机配置（源目录、目标目录、站点名）的读写，存放于用户本地配置目录，不随项目目录复制。
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config 本机运行所需的三项路径与站点标识。
type Config struct {
	LocalDir   string `json:"localDir"`
	CarrierDir string `json:"carrierDir"`
	SiteID     string `json:"siteId"`
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
