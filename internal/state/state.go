// 站点状态与单实例锁；状态摘要只由内容决定，保证两个站点在内容一致时摘要一致。
package state

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const StateDirName = ".blocksync"

// Site 站点持久化状态。
type Site struct {
	SiteID      string `json:"siteId"`
	Generation  uint64 `json:"generation"`
	SetDigest   string `json:"setDigest"`
	LastPushed  string `json:"lastPushed,omitempty"`
	LastApplied string `json:"lastApplied,omitempty"`
}

func Dir(local string) string {
	return filepath.Join(local, StateDirName)
}

func sitePath(local string) string {
	return filepath.Join(Dir(local), "site.json")
}

func defaultSiteID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "site"
	}
	return h
}

// Load 读取站点状态，缺失时返回带默认 SiteID 的空状态。
func Load(local string) (*Site, error) {
	b, err := os.ReadFile(sitePath(local))
	if errors.Is(err, os.ErrNotExist) {
		return &Site{SiteID: defaultSiteID()}, nil
	}
	if err != nil {
		return nil, err
	}
	s := &Site{}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("site.json 解析失败: %w", err)
	}
	if s.SiteID == "" {
		s.SiteID = defaultSiteID()
	}
	return s, nil
}

// Save 原子写入站点状态。
func Save(local string, s *Site) error {
	if err := os.MkdirAll(Dir(local), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := sitePath(local) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, sitePath(local))
}

// DigestBytes 把十六进制摘要转回定长字节；空串表示全零。
func DigestBytes(hexStr string) ([32]byte, error) {
	var out [32]byte
	if hexStr == "" {
		return out, nil
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("状态摘要格式非法: %q", hexStr)
	}
	copy(out[:], b)
	return out, nil
}

func DigestHex(d [32]byte) string { return hex.EncodeToString(d[:]) }

// Lock 建立单实例锁，返回释放函数。
func Lock(local string, force bool) (func(), error) {
	if err := os.MkdirAll(Dir(local), 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(Dir(local), "lock")
	mk := func() (*os.File, error) {
		return os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	f, err := mk()
	if err != nil {
		if !force {
			return nil, fmt.Errorf("目录正被另一个 blocksync 进程使用（%s）。确认无其他实例后可加 --force", p)
		}
		_ = os.Remove(p)
		if f, err = mk(); err != nil {
			return nil, fmt.Errorf("无法获取锁文件 %s: %w", p, err)
		}
	}
	fmt.Fprintf(f, "pid=%d time=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}
