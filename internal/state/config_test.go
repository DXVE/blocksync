package state

import "testing"

func TestExcludesForPairIsDirectionIndependent(t *testing.T) {
	c := &Config{}
	c.SetExcludes(`C:\data\a`, `E:\backup\a`, []string{`*.tmp`})

	for _, pair := range [][2]string{
		{`C:\data\a`, `E:\backup\a`},
		{`E:\backup\a`, `C:\data\a`},
		{`c:\DATA\a`, `e:\backup\a\`},
	} {
		got := c.ExcludesFor(pair[0], pair[1])
		if len(got) != 1 || got[0] != `*.tmp` {
			t.Errorf("ExcludesFor(%q, %q) = %v", pair[0], pair[1], got)
		}
	}
	if got := c.ExcludesFor(`C:\data\b`, `E:\backup\a`); got != nil {
		t.Errorf("不同目录对应返回 nil，实际 %v", got)
	}
	if got := c.ExcludesFor(`C:\data\a`, ""); got != nil {
		t.Errorf("缺一侧应返回 nil，实际 %v", got)
	}
}

func TestSetExcludesKeepsOtherPairs(t *testing.T) {
	c := &Config{}
	c.SetExcludes(`C:\a`, `E:\a`, []string{"one"})
	c.SetExcludes(`C:\b`, `E:\b`, []string{"two"})
	c.SetExcludes(`E:\a`, `C:\a`, []string{"updated"})

	if len(c.ExcludeSets) != 2 {
		t.Fatalf("目录对数量 = %d, want 2", len(c.ExcludeSets))
	}
	if got := c.ExcludesFor(`C:\a`, `E:\a`); len(got) != 1 || got[0] != "updated" {
		t.Fatalf("同目录对应覆盖而不是新增，实际 %v", got)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	c := &Config{LocalDir: `C:\a`, CarrierDir: `E:\a`, SiteID: "site-1"}
	c.SetExcludes(`C:\a`, `E:\a`, []string{`\Recovery\`, `*\thumbs.db`})
	if err := SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.SiteID != "site-1" || len(got.ExcludesFor(`E:\a`, `C:\a`)) != 2 {
		t.Fatalf("配置往返失败: %+v", got)
	}
}
