package scan

import "testing"

func TestExcludeRuleSyntax(t *testing.T) {
	rules := []string{
		`\System Volume Information\`,
		`\$Recycle.Bin\`,
		`\RECYCLE?\`,
		`\Recovery\`,
		`*\thumbs.db`,
		`*\.git\`,
	}
	cases := []struct {
		rel  string
		want bool
	}{
		{"System Volume Information/tracking.db", true},
		{"system volume information/tracking.db", true},
		{"sub/System Volume Information/tracking.db", false},
		{"$RECYCLE.BIN/S-1-5-21/desktop.ini", true},
		{"$Recycle.Bin", true},
		{"RECYCLE1/x", true},
		{"RECYCLE12/x", false},
		{"Recovery/log.txt", true},
		{"sub/Recovery/log.txt", false},
		{"thumbs.db", true},
		{"a/thumbs.db", true},
		{"a/b/Thumbs.db", true},
		{"thumbs.db.bak", false},
		{".git/config", true},
		{"repo/.git/config", true},
		{"repo/.github/config", false},
		{"keep.txt", false},
	}
	for _, c := range cases {
		if got := Excluded(c.rel, rules); got != c.want {
			t.Errorf("Excluded(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// 纯名字与旧写法保持原语义：任意层级同名条目、通配符按 basename 生效。
func TestExcludeLegacyPatterns(t *testing.T) {
	cases := []struct {
		rel, pat string
		want     bool
	}{
		{"cache", "cache", true},
		{"a/cache", "cache", true},
		{"a/cache/x.bin", "cache", true},
		{"cached/x.bin", "cache", false},
		{"a/x.tmp", "*.tmp", true},
		{"a/b/x.tmp", "*.tmp", true},
		{"x.tmp.bak", "*.tmp", false},
		{"a/b.txt", "b.txt", true},
		{"a/b.txt", "*.log", false},
	}
	for _, c := range cases {
		if got := Excluded(c.rel, []string{c.pat}); got != c.want {
			t.Errorf("Excluded(%q, %q) = %v, want %v", c.rel, c.pat, got, c.want)
		}
	}
}

func TestExcludeInvalidPatternIgnored(t *testing.T) {
	if Excluded("a/b.txt", []string{"["}) {
		t.Fatal("非法模式应被忽略")
	}
	if !Excluded(".blocksync/x", DefaultExcludes) {
		t.Fatal(".blocksync 必须被默认排除")
	}
}

func TestValidatePattern(t *testing.T) {
	if err := ValidatePattern(`\System Volume Information\`); err != nil {
		t.Fatalf("合法规则被拒: %v", err)
	}
	if err := ValidatePattern("["); err == nil {
		t.Fatal("非法规则应返回错误")
	}
}
