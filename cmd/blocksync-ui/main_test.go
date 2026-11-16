//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// newTestLogEdit 创建与日志区同样式的只读多行编辑框，并装入空模型；用例锁在该 OS 线程上。
// 窗口绑定创建它的线程，跨线程发消息会阻塞，因此整个用例不换线程。
func newTestLogEdit(t *testing.T) (syscall.Handle, func()) {
	t.Helper()
	runtime.LockOSThread()
	hInst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("EDIT"))), 0,
		wsPopup|wsVisible|esMultiline|esAutovscroll|esReadonly, 0, 0, 400, 300, 0, 0, hInst, 0)
	if h == 0 {
		runtime.UnlockOSThread()
		t.Skip("无法创建编辑框，跳过界面用例")
	}
	oldLog, oldRun, oldView, oldSink := hLog, runLog, logView, logSink
	hLog, runLog, logSink = syscall.Handle(h), nil, nil
	logView = &logModel{limit: 500000}
	return hLog, func() {
		pDestroyWindow.Call(h)
		hLog, runLog, logView, logSink = oldLog, oldRun, oldView, oldSink
		runtime.UnlockOSThread()
	}
}

// 回归用例：用户点过日志、插入点停在中间后，新行仍必须落在末尾。
func TestAppendLogTextAppendsAtEnd(t *testing.T) {
	logEdit, cleanup := newTestLogEdit(t)
	defer cleanup()

	appendLogText([]string{"第一行", "第二行"})
	pSendMessageW.Call(uintptr(logEdit), emSetSel, 2, 2) // 模拟点击后插入点落在第一行内

	appendLogText([]string{"错误: 写入失败"})

	got := getText(logEdit)
	if !strings.HasSuffix(strings.TrimSpace(got), "错误: 写入失败") {
		t.Fatalf("新行应追加到末尾，实际日志为:\n%s", got)
	}
	if !strings.HasPrefix(got, "第一行\r\n第二行\r\n") {
		t.Fatalf("原有内容应保持不变，实际日志为:\n%s", got)
	}
}

// 回归用例：超过上限时从头部整行丢弃最旧内容，控件与模型保持一致。
func TestAppendLogTextDropsOldestLines(t *testing.T) {
	logEdit, cleanup := newTestLogEdit(t)
	defer cleanup()

	logView.limit = 20
	for _, l := range []string{"旧一", "旧二", "旧三", "旧四", "旧五", "新六"} {
		appendLogText([]string{l})
	}

	got := getText(logEdit)
	if strings.Contains(got, "旧一") {
		t.Fatalf("超限后最旧的行应被丢弃，实际日志为:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "新六") {
		t.Fatalf("最新行应在末尾，实际日志为:\n%s", got)
	}
	if uintptr(logView.chars) != logLen() {
		t.Fatalf("模型字符数 %d 与控件字符数 %d 不一致", logView.chars, logLen())
	}
}

// 模型单独校验：连续写入始终不超上限，且保留最新行。
func TestLogModelTrimKeepsNewest(t *testing.T) {
	m := &logModel{limit: 30}
	for i := 1; i <= 10; i++ {
		m.add([]string{fmt.Sprintf("第%d行", i)})
		if m.chars > m.limit {
			t.Fatalf("字符数 %d 超过上限 %d", m.chars, m.limit)
		}
	}
	if want := "第10行\r\n"; !strings.HasSuffix(m.text(), want) {
		t.Fatalf("最新行应保留，实际模型尾部为:\n%s", m.text())
	}
}

// 读端不得像 Scanner 那样在超长行或读错误处静默结束。
func TestReadLogStreamKeepsLongLines(t *testing.T) {
	long := strings.Repeat("x", 2<<20)
	var got []string
	err := readLogStream(strings.NewReader("a\r\n"+long+"\n末尾无换行"), func(l string) {
		got = append(got, l)
	})
	if err != nil {
		t.Fatalf("正常读到 EOF 不应报错: %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != long || got[2] != "末尾无换行" {
		t.Fatalf("应交付 3 行（含超长行与末尾不完整行），实际 %d 行", len(got))
	}
}

func TestReadLogStreamReportsError(t *testing.T) {
	boom := errors.New("管道坏了")
	if err := readLogStream(errReader{err: boom}, func(string) {}); !errors.Is(err, boom) {
		t.Fatalf("读取错误应原样返回，得到 %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// 日志文件：写入内容可读回，超过保留期的旧文件在启动时清理。
func TestLogFileSinkWritesAndPrunes(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)

	logDir := filepath.Join(base, "blocksync", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(logDir, "gui-20200101-000000-1.log")
	if err := os.WriteFile(stale, []byte("旧内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	s := openLogFile(time.Now())
	if s == nil {
		t.Fatal("应能建立日志文件")
	}
	s.write("错误: 写入失败", "完成：扫描 287 个")
	s.close()

	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if got := string(data); !strings.Contains(got, "错误: 写入失败") || !strings.Contains(got, "完成：扫描 287 个") {
		t.Fatalf("日志文件内容不完整:\n%s", got)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("超过保留期的旧日志应在启动时被清理")
	}
}

// appendLog 必须同时写入界面队列与日志文件。
func TestAppendLogWritesFile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)

	oldSink, oldQueue := logSink, logQueue
	logSink = openLogFile(time.Now())
	if logSink == nil {
		t.Fatal("应能建立日志文件")
	}
	path := logSink.path
	defer func() {
		logSink.close()
		logSink, logQueue = oldSink, oldQueue
	}()

	appendLog("错误: 写入失败")
	logSink.close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "错误: 写入失败") {
		t.Fatalf("日志文件应包含追加的内容，实际为:\n%s", data)
	}
}

func TestMassDeleteHint(t *testing.T) {
	logText := "输入：A\n输出：B\n错误: 本次将删除 120 个文件（输入目录共 500 个），超过安全上限 100。请先确认方向与两个目录无误，再加 --allow-mass-delete 重新执行"
	got := massDeleteHint(logText)
	if got == "" {
		t.Fatal("应识别出超量删除提示")
	}
	if massDeleteHint("错误: 目录不存在: X") != "" {
		t.Fatal("普通错误不应触发超量删除提示")
	}
}

func TestValidateExcludeTextLineNumber(t *testing.T) {
	err := validateExcludeText("cache\n\n[abc\n")
	if err == nil {
		t.Fatal("非法通配符未被发现")
	}
	if !strings.Contains(err.Error(), "第 3 行") {
		t.Fatalf("行号应按原始行计算，得到: %v", err)
	}
	if err := validateExcludeText("cache\n\n\n*.tmp\n"); err != nil {
		t.Fatalf("合法规则被误报: %v", err)
	}
}

// 「完成」行与进度行同号，不能推进进度条。
func TestProgressOfIgnoresDoneLines(t *testing.T) {
	if _, _, ok := progressOf("[2/7] 完成  a.vmdk"); ok {
		t.Fatal("完成行不应推进进度")
	}
	if c, n, ok := progressOf("[2/7] 更新  a.vmdk  变化块 1 MiB"); !ok || c != 2 || n != 7 {
		t.Fatalf("进度行解析错误: c=%d n=%d ok=%v", c, n, ok)
	}
	if _, _, ok := progressOf("完成：扫描 7 个，改动 4 个"); ok {
		t.Fatal("汇总行不应推进进度")
	}
}

// 折叠只影响界面：appendLogFile 写文件但不入界面队列。
func TestAppendLogFileSkipsQueue(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)

	oldSink, oldQueue := logSink, logQueue
	logQueue = nil
	logSink = openLogFile(time.Now())
	if logSink == nil {
		t.Fatal("应能建立日志文件")
	}
	path := logSink.path
	defer func() {
		logSink.close()
		logSink, logQueue = oldSink, oldQueue
	}()

	appendLogFile("未变行只进文件")
	if len(logQueue) != 0 {
		t.Fatalf("只写文件的接口不应进入界面队列: %v", logQueue)
	}
	logSink.close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	if !strings.Contains(string(data), "未变行只进文件") {
		t.Fatalf("日志文件应包含该行，实际为:\n%s", data)
	}
}

// 逐文件行要能解出表格字段：状态、路径、变化量与完成标记。
func TestParseTableRow(t *testing.T) {
	cases := []struct {
		line   string
		status string
		path   string
		amount string
		done   bool
	}{
		{"[2/7] 更新  24H2-Windows11-cl1/x-s001.vmdk  变化块 327.3 MiB（文件共 1.2 GiB）（源文件修改时间变化）",
			"更新", "24H2-Windows11-cl1/x-s001.vmdk", "327.3 MiB / 1.2 GiB", false},
		{"[4/7] 新增  sub/new.vmdk  （23.1 MiB）", "新增", "sub/new.vmdk", "23.1 MiB", false},
		{"[5/7] 无变化  vmware.log（源索引不存在）", "无变化", "vmware.log", "—", false},
		{"[6/7] 截断  big.bin  截断到 12.0 MiB", "截断", "big.bin", "12.0 MiB", false},
		{"[7/7] 删除  old.bin（源端已不存在）", "删除", "old.bin", "—", false},
		{"[7/7] 完成  old.bin", "完成", "old.bin", "", true},
	}
	for _, c := range cases {
		row, ok := parseTableRow(c.line)
		if !ok {
			t.Fatalf("应解析为逐文件行: %s", c.line)
		}
		if row.status != c.status || row.path != c.path || row.amount != c.amount || row.done != c.done {
			t.Fatalf("解析结果不符: %+v", row)
		}
	}
	if _, ok := parseTableRow("完成：扫描 7 个，改动 4 个"); ok {
		t.Fatal("汇总行不是逐文件行")
	}
}

func TestFormatTableRow(t *testing.T) {
	row := tableRow{idx: "[2/7]", status: "更新", path: "a.vmdk", amount: "1.0 MiB / 2.0 MiB"}
	if got, want := formatTableRow(row, "a.vmdk", true), "[2/7]\t更新\ta.vmdk\t1.0 MiB / 2.0 MiB\t完成"; got != want {
		t.Fatalf("行格式错误: %q", got)
	}
	if got := formatTableRow(row, "a.vmdk", false); strings.HasSuffix(got, "完成") {
		t.Fatalf("未完成行不应带完成列: %q", got)
	}
}

// 超宽时中间省略：保留头部与尾部，总宽不超限。
func TestFitPathMiddleEllipsis(t *testing.T) {
	widthOf := func(s string) int { return len([]rune(s)) * 10 }
	name := "24H2-Windows11-cl1-split-000001-s001.vmdk"
	got := fitPath(name, 200, widthOf)
	if !strings.Contains(got, "…") {
		t.Fatalf("超宽时应省略: %q", got)
	}
	if widthOf(got) > 200 {
		t.Fatalf("省略后仍超宽: %q", got)
	}
	if !strings.HasSuffix(got, "s001.vmdk") || !strings.HasPrefix(got, "24H2") {
		t.Fatalf("应保留首尾: %q", got)
	}
	if fitPath("a.vmdk", 200, widthOf) != "a.vmdk" {
		t.Fatal("放得下时不应改动")
	}
}

// 制表位换算：写入带 tab 的行后，各字段起点应落在设计列位上。
func TestLogTabColumnsAlign(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	face := u16("Segoe UI")
	fh := int(-12)
	font, _, _ := pCreateFontW.Call(uintptr(fh), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))

	oldFont, oldDC, oldLog, oldView := uiFont, hMeasureDC, hLog, logView
	uiFont = syscall.Handle(font)
	hMeasureDC, _, _ = pCreateCompatDC.Call(0)
	pSelectObject.Call(hMeasureDC, uintptr(uiFont))

	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("EDIT"))), 0,
		wsPopup|wsVisible|esMultiline|esAutovscroll|esAutohscroll|esReadonly, 0, 0, 760, 292, 0, 0, hInst, 0)
	if h == 0 {
		t.Skip("无法创建编辑框")
	}
	hLog, logView = syscall.Handle(h), &logModel{limit: 500000}
	defer func() {
		pDestroyWindow.Call(h)
		pDeleteDC.Call(hMeasureDC)
		pDeleteObject.Call(uintptr(uiFont))
		uiFont, hMeasureDC, hLog, logView = oldFont, oldDC, oldLog, oldView
	}()

	setupLogColumns()
	const line = "[1/7]\t无变化\taaaaaaaaaa\t1.0 MiB / 2.0 GiB\t完成"
	setText(hLog, line)

	// EM_POSFROMCHAR 取每个字段首字符的 x 坐标（低 16 位）
	xAt := func(idx int) int {
		r, _, _ := pSendMessageW.Call(uintptr(hLog), emPosFromChar, uintptr(idx), 0)
		return int(r & 0xFFFF)
	}
	var rc rect
	pGetClientRect.Call(uintptr(hLog), uintptr(unsafe.Pointer(&rc)))
	w := int(rc.right - rc.left)
	want := []int{int(float64(w) * 0.10), int(float64(w) * 0.20), int(float64(w) * 0.71)}
	// 字段首字符在行内的索引：无=4, a=src 起点, 1=数量起点, 完=完成起点
	pos := []int{len([]rune("[1/7]\t")), len([]rune("[1/7]\t无变化\t")), len([]rune("[1/7]\t无变化\taaaaaaaaaa\t")), len([]rune("[1/7]\t无变化\taaaaaaaaaa\t1.0 MiB / 2.0 GiB\t"))}
	for i, p := range pos[:3] {
		got := xAt(p)
		if got < want[i]-3 || got > want[i]+3 {
			t.Fatalf("第 %d 列起点 %d，期望 %d（控件宽 %d）", i+2, got, want[i], w)
		}
	}
	wantDone := int(float64(w) * 0.93)
	if got := xAt(pos[3]); got < wantDone-3 || got > wantDone+3 {
		t.Fatalf("完成列起点 %d，期望 %d", got, wantDone)
	}
}

// 进度行在完成行到达前不输出；完成行到达后合并成一行；中止时输出不带完成列的行。
func TestLogTablePairsDoneLine(t *testing.T) {
	var got []string
	emit := func(s string) { got = append(got, s) }
	var table logTable

	r1, _ := parseTableRow("[1/2] 更新  a.vmdk  变化块 1.0 MiB（文件共 2.0 MiB）")
	table.start(r1, emit)
	if len(got) != 0 {
		t.Fatalf("完成行到达前不应输出，实际 %q", got)
	}
	table.finish("[1/2]", "[1/2] 完成  a.vmdk", emit)
	if len(got) != 1 || !strings.HasPrefix(got[0], "[1/2]\t更新\ta.vmdk\t1.0 MiB / 2.0 MiB") || !strings.HasSuffix(got[0], "\t完成") {
		t.Fatalf("应输出带完成列的合并行，实际 %q", got)
	}

	r2, _ := parseTableRow("[2/2] 无变化  b.log")
	table.start(r2, emit)
	table.flush(emit, false)
	if len(got) != 2 || strings.HasSuffix(got[1], "完成") {
		t.Fatalf("中止时应输出不带完成列的行，实际 %q", got)
	}

	// 没有对应缓存行的完成行按原样输出，不影响后续
	table.finish("[9/9]", "[9/9] 完成  x", emit)
	if len(got) != 3 || got[2] != "[9/9] 完成  x" {
		t.Fatalf("孤立完成行应原样输出，实际 %q", got)
	}
}
