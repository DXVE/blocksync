package state

import "testing"

// OtherSide 供单目录命令找回目录对的另一侧；具名配置优先，其次生效目录对。
func TestOtherSide(t *testing.T) {
	c := &Config{LocalDir: `C:\data\a`, CarrierDir: `E:\backup\a`}
	if got := c.OtherSide(`c:\DATA\a\`); got != `E:\backup\a` {
		t.Errorf("生效目录对应返回另一侧，实际 %q", got)
	}
	if got := c.OtherSide(`E:\backup\a`); got != `C:\data\a` {
		t.Errorf("反向也应返回另一侧，实际 %q", got)
	}
	if got := c.OtherSide(`D:\other`); got != "" {
		t.Errorf("未登记的目录应返回空串，实际 %q", got)
	}

	c.Profiles = []Profile{{Name: "b", LocalDir: `D:\data\b`, CarrierDir: `F:\backup\b`}}
	if got := c.OtherSide(`F:\backup\b`); got != `D:\data\b` {
		t.Errorf("具名配置中的目录应返回其配对的另一侧，实际 %q", got)
	}
	if got := (*Config)(nil).OtherSide(`C:\data\a`); got != "" {
		t.Errorf("空配置应返回空串，实际 %q", got)
	}
}

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
	c := &Config{LocalDir: `C:\a`, CarrierDir: `E:\a`}
	c.SetExcludes(`C:\a`, `E:\a`, []string{`\Recovery\`, `*\thumbs.db`})
	if err := SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ExcludesFor(`E:\a`, `C:\a`)) != 2 {
		t.Fatalf("配置往返失败: %+v", got)
	}
}

func TestSetProfileAddsUpdatesAndRenames(t *testing.T) {
	c := &Config{ActiveProfile: "A"}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	if len(c.Profiles) != 1 || c.Profiles[0].LocalDir != `C:\a` {
		t.Fatalf("新增配置失败: %+v", c.Profiles)
	}

	c.SetProfile("A", Profile{Name: "B", LocalDir: `C:\b`, CarrierDir: `E:\b`})
	if len(c.Profiles) != 1 || c.Profiles[0].Name != "B" || c.ActiveProfile != "B" {
		t.Fatalf("改名后 profiles=%+v active=%q", c.Profiles, c.ActiveProfile)
	}

	c.SetProfile("B", Profile{Name: "B", LocalDir: `C:\c`, CarrierDir: `E:\c`})
	if len(c.Profiles) != 1 || c.Profiles[0].LocalDir != `C:\c` {
		t.Fatalf("同名保存应覆盖而不是新增: %+v", c.Profiles)
	}
}

func TestDeleteProfileClearsActive(t *testing.T) {
	c := &Config{}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetProfile("", Profile{Name: "B", LocalDir: `C:\b`, CarrierDir: `E:\b`})
	c.ActiveProfile = "A"

	if !c.DeleteProfile("a") {
		t.Fatal("DeleteProfile 应按名称忽略大小写命中")
	}
	if len(c.Profiles) != 1 || c.Profiles[0].Name != "B" {
		t.Fatalf("删除后 profiles=%+v", c.Profiles)
	}
	if c.ActiveProfile != "" {
		t.Fatalf("删除生效配置后 active 应清空，实际 %q", c.ActiveProfile)
	}
	if c.DeleteProfile("A") {
		t.Fatal("重复删除应返回 false")
	}
}

func TestProfilePairInUseIgnoresSelf(t *testing.T) {
	c := &Config{}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetProfile("", Profile{Name: "B", LocalDir: `C:\b`, CarrierDir: `E:\b`})

	if !c.ProfilePairInUse(`E:\a`, `C:\a`, "B") {
		t.Fatal("其它配置引用该目录对应命中")
	}
	if c.ProfilePairInUse(`E:\a`, `C:\a`, "A") {
		t.Fatal("排除自身后不应命中")
	}
	if c.ProfilePairInUse(`C:\x`, `E:\x`, "") {
		t.Fatal("未使用的目录对应返回 false")
	}
}

func TestRemoveExcludes(t *testing.T) {
	c := &Config{}
	c.SetExcludes(`C:\a`, `E:\a`, []string{"one"})
	c.SetExcludes(`C:\b`, `E:\b`, []string{"two"})

	if !c.RemoveExcludes(`E:\a`, `C:\a`) {
		t.Fatal("应按无序目录对删除")
	}
	if got := c.ExcludesFor(`C:\a`, `E:\a`); got != nil {
		t.Fatalf("删除后应查不到规则，实际 %v", got)
	}
	if len(c.ExcludeSets) != 1 || len(c.ExcludesFor(`C:\b`, `E:\b`)) != 1 {
		t.Fatalf("不应影响其它目录对: %+v", c.ExcludeSets)
	}
	if c.RemoveExcludes(`C:\a`, `E:\a`) {
		t.Fatal("重复删除应返回 false")
	}
}

func TestEnsureProfileMigratesLegacyDirs(t *testing.T) {
	c := &Config{LocalDir: `C:\a`, CarrierDir: `E:\a`}
	if !c.EnsureProfile() {
		t.Fatal("旧配置应生成默认配置")
	}
	if len(c.Profiles) != 1 || c.Profiles[0].Name != DefaultProfileName || c.ActiveProfile != DefaultProfileName {
		t.Fatalf("迁移结果 profiles=%+v active=%q", c.Profiles, c.ActiveProfile)
	}
	if c.Profiles[0].LocalDir != `C:\a` || c.Profiles[0].CarrierDir != `E:\a` {
		t.Fatalf("迁移应沿用原目录: %+v", c.Profiles[0])
	}
	if c.EnsureProfile() {
		t.Fatal("重复调用不应再改动")
	}

	empty := &Config{}
	if empty.EnsureProfile() || len(empty.Profiles) != 0 {
		t.Fatal("没有目录时不应生成配置")
	}

	c.ActiveProfile = "已被删除"
	if !c.EnsureProfile() || c.ActiveProfile != DefaultProfileName {
		t.Fatalf("失效的生效项应改指第一条，实际 %q", c.ActiveProfile)
	}
}

func TestNextProfileNameSkipsTaken(t *testing.T) {
	c := &Config{}
	if got := c.NextProfileName(); got != "配置1" {
		t.Fatalf("空配置应给出 配置1，实际 %q", got)
	}
	c.SetProfile("", Profile{Name: "配置1"})
	c.SetProfile("", Profile{Name: "配置3", LocalDir: `C:\c`, CarrierDir: `E:\c`})
	if got := c.NextProfileName(); got != "配置2" {
		t.Fatalf("应补空闲序号，实际 %q", got)
	}
	c.SetProfile("", Profile{Name: "配置2"})
	if got := c.NextProfileName(); got != "配置4" {
		t.Fatalf("连续占用后应取下一个，实际 %q", got)
	}
	var nilCfg *Config
	if got := nilCfg.NextProfileName(); got != "配置1" {
		t.Fatalf("nil 配置应退役到 配置1，实际 %q", got)
	}
}

func TestMatchProfileByPair(t *testing.T) {
	c := &Config{}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetProfile("", Profile{Name: "B", LocalDir: `C:\b`, CarrierDir: `F:\b`})

	if got := c.MatchProfile(`e:\a`, `c:\a\`); got != "A" {
		t.Fatalf("交换方向或大小写差异应命中同一配置，实际 %q", got)
	}
	if got := c.MatchProfile(`C:\b`, `F:\b`); got != "B" {
		t.Fatalf("MatchProfile 命中第二条失败，实际 %q", got)
	}
	if got := c.MatchProfile(`C:\x`, `E:\x`); got != "" {
		t.Fatalf("未匹配应返回空串，实际 %q", got)
	}
	if got := c.MatchProfile(`C:\a`, ""); got != "" {
		t.Fatalf("缺一侧应返回空串，实际 %q", got)
	}
}

func TestMigrateExcludesFollowsProfileDirs(t *testing.T) {
	c := &Config{}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetExcludes(`C:\a`, `E:\a`, []string{`\Cache\`})

	c.MigrateExcludes(`C:\a`, `E:\a`, `D:\a`, `F:\a`, "A")
	if got := c.ExcludesFor(`C:\a`, `E:\a`); got != nil {
		t.Fatalf("旧目录对的规则应被搬走，实际 %v", got)
	}
	if got := c.ExcludesFor(`D:\a`, `F:\a`); len(got) != 1 || got[0] != `\Cache\` {
		t.Fatalf("新目录对应带上规则，实际 %v", got)
	}
}

func TestMigrateExcludesKeepsRulesSharedByOtherProfile(t *testing.T) {
	c := &Config{}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetProfile("", Profile{Name: "B", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetExcludes(`C:\a`, `E:\a`, []string{`\Cache\`})

	c.MigrateExcludes(`C:\a`, `E:\a`, `D:\a`, `F:\a`, "A")
	if got := c.ExcludesFor(`C:\a`, `E:\a`); len(got) != 1 {
		t.Fatalf("仍被 B 使用时应保留旧规则，实际 %v", got)
	}
	if got := c.ExcludesFor(`D:\a`, `F:\a`); got != nil {
		t.Fatalf("不应复制出第二份规则，实际 %v", got)
	}
}

func TestMigrateExcludesSkipsUnchangedPair(t *testing.T) {
	c := &Config{}
	c.SetProfile("", Profile{Name: "A", LocalDir: `C:\a`, CarrierDir: `E:\a`})
	c.SetExcludes(`C:\a`, `E:\a`, []string{`\Cache\`})

	c.MigrateExcludes(`C:\a`, `E:\a`, `E:\a`, `C:\a`, "A")
	if len(c.ExcludeSets) != 1 || len(c.ExcludesFor(`C:\a`, `E:\a`)) != 1 {
		t.Fatalf("同一目录对不应产生改动: %+v", c.ExcludeSets)
	}
}

func TestConfigRoundTripKeepsProfiles(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	c := &Config{}
	c.SetProfile("", Profile{Name: "工作", LocalDir: `C:\w`, CarrierDir: `E:\w`})
	c.SetProfile("", Profile{Name: "备份", LocalDir: `C:\b`, CarrierDir: `F:\b`})
	c.ActiveProfile = "备份"
	c.SetExcludes(`C:\b`, `F:\b`, []string{`\Cache\`})

	if err := SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 2 || got.ActiveProfile != "备份" {
		t.Fatalf("配置往返后 profiles=%+v active=%q", got.Profiles, got.ActiveProfile)
	}
	if len(got.ExcludesFor(`F:\b`, `C:\b`)) != 1 {
		t.Fatalf("排除规则应随配置一起保存: %+v", got.ExcludeSets)
	}
}
