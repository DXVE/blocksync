//go:build windows

// blocksync 图形界面：只收集路径与选项，调用同目录的 blocksync.exe 执行，并把输出回显到窗口。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	pRegisterClassExW     = user32.NewProc("RegisterClassExW")
	pCreateWindowExW      = user32.NewProc("CreateWindowExW")
	pDefWindowProcW       = user32.NewProc("DefWindowProcW")
	pDestroyWindow        = user32.NewProc("DestroyWindow")
	pShowWindow           = user32.NewProc("ShowWindow")
	pUpdateWindow         = user32.NewProc("UpdateWindow")
	pGetMessageW          = user32.NewProc("GetMessageW")
	pTranslateMessage     = user32.NewProc("TranslateMessage")
	pDispatchMessageW     = user32.NewProc("DispatchMessageW")
	pPostQuitMessage      = user32.NewProc("PostQuitMessage")
	pSendMessageW         = user32.NewProc("SendMessageW")
	pSetWindowTextW       = user32.NewProc("SetWindowTextW")
	pGetWindowTextW       = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	pSetTimer             = user32.NewProc("SetTimer")
	pKillTimer            = user32.NewProc("KillTimer")
	pMessageBoxW          = user32.NewProc("MessageBoxW")
	pEnableWindow         = user32.NewProc("EnableWindow")
	pSetProcessDPIAware   = user32.NewProc("SetProcessDPIAware")
	pSetDPIAwarenessCtx   = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForSystem      = user32.NewProc("GetDpiForSystem")
	pAdjustWindowRectEx   = user32.NewProc("AdjustWindowRectEx")
	pCreateMenu           = user32.NewProc("CreateMenu")
	pCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	pAppendMenuW          = user32.NewProc("AppendMenuW")
	pLoadCursorW          = user32.NewProc("LoadCursorW")
	pGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	pSetWindowPos         = user32.NewProc("SetWindowPos")

	pCreateFontW  = gdi32.NewProc("CreateFontW")
	pDeleteObject = gdi32.NewProc("DeleteObject")

	pSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	pShellExecuteW        = shell32.NewProc("ShellExecuteW")
	pCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
)

const (
	wsChild   = 0x40000000
	wsVisible = 0x10000000
	wsTabStop = 0x00010000
	wsBorder  = 0x00800000
	wsVScroll = 0x00200000
	wsHScroll = 0x00100000

	esMultiline   = 0x0004
	esAutovscroll = 0x0040
	esAutohscroll = 0x0080
	esReadonly    = 0x0800

	bsPushbutton   = 0x00000000
	bsAutoCheckbox = 0x00000003
	bmGetCheck     = 0x00F0

	pbsSmooth  = 0x0001
	pbsMarquee = 0x0008

	wmCreate  = 0x0001
	wmDestroy = 0x0002
	wmClose   = 0x0010
	wmSetFont = 0x0030
	wmCommand = 0x0111
	wmTimer   = 0x0113

	emSetSel      = 0x00B1
	emReplaceSel  = 0x00C2
	emSetLimitTxt = 0x00C5
	emScrollCaret = 0x00B7

	pbmSetPos     = 0x0402
	pbmSetRange32 = 0x0406
	pbmSetMarquee = 0x040A

	mbOk          = 0x00000000
	mbYesNo       = 0x00000004
	mbIconError   = 0x00000010
	mbIconWarning = 0x00000030
	mbIconInfo    = 0x00000040
	idYes         = 6

	mfString    = 0x00000000
	mfPopup     = 0x00000010
	mfSeparator = 0x00000800

	swShow = 5
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type msgT struct {
	hwnd    syscall.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type rect struct{ left, top, right, bottom int32 }

type browseInfo struct {
	hwndOwner      syscall.Handle
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

const (
	idEditSrc = 1001
	idEditDst = 1002
	idBSrc    = 1003
	idBDst    = 1004
	idChkVer  = 1005
	idChkFull = 1006
	idSync    = 1007
	idDry     = 1008
	idCancel  = 1009
	idSave    = 1010
	idOpen    = 1011
	idStatus  = 1012
	idProg    = 1013
	idLog     = 1014
	idSwap    = 1015
	idOpenSrc = 1016
	timerID   = 1
)

var (
	hInstance syscall.Handle
	mainHwnd  syscall.Handle
	uiFont    syscall.Handle
	scale     = 1.0

	hEditSrc, hEditDst           syscall.Handle
	hBtnBrowseSrc, hBtnBrowseDst syscall.Handle
	hBtnSwap                     syscall.Handle
	hChkVerify, hChkFull         syscall.Handle
	hBtnSync, hBtnCancel         syscall.Handle
	hStatus, hProgress, hLog     syscall.Handle

	mu        sync.Mutex
	logQueue  []string
	progCur   int
	progTotal int
	progKnown bool
	finished  bool
	exitCode  int
	running   bool
	cancelled bool
	proc      *exec.Cmd
	logChars  int

	reProgress = regexp.MustCompile(`\[(\d+)/(\d+)\]\s*(.*)`)
	reResult   = regexp.MustCompile(`完成(（试运行）)?：扫描 (\d+) 个，改动 (\d+) 个，删除 (\d+) 个；读取 (\d+(?:\.\d+)? (?:B|KiB|MiB|GiB|TiB))，(?:预计需)?写入 (\d+(?:\.\d+)? (?:B|KiB|MiB|GiB|TiB))`)
)

// confirmMarker 与命令行 blocksync.exe 中的同名常量保持一致：出现该行时子进程在等待确认。
const confirmMarker = "需要确认后才会开始写入"

func sz(v int) int { return int(float64(v)*scale + 0.5) }

func u16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

func getText(h syscall.Handle) string {
	n, _, _ := pGetWindowTextLengthW.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+2)
	pGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func setText(h syscall.Handle, s string) {
	pSetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(u16(s))))
}

func create(class, text string, style uint32, x, y, w, h int, id int) syscall.Handle {
	r, _, _ := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(u16(class))),
		uintptr(unsafe.Pointer(u16(text))),
		uintptr(style),
		uintptr(sz(x)), uintptr(sz(y)), uintptr(sz(w)), uintptr(sz(h)),
		uintptr(mainHwnd), uintptr(id), uintptr(hInstance), 0)
	hh := syscall.Handle(r)
	if uiFont != 0 && hh != 0 {
		pSendMessageW.Call(uintptr(hh), wmSetFont, uintptr(uiFont), 1)
	}
	return hh
}

func enable(h syscall.Handle, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	pEnableWindow.Call(uintptr(h), v)
}

func msgBox(text string, flags uint32) int {
	r, _, _ := pMessageBoxW.Call(uintptr(mainHwnd),
		uintptr(unsafe.Pointer(u16(text))),
		uintptr(unsafe.Pointer(u16("blocksync"))), uintptr(flags))
	return int(r)
}

// ---------- 配置（与命令行共用） ----------

type config struct {
	LocalDir   string `json:"localDir"`
	CarrierDir string `json:"carrierDir"`
}

func configPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "blocksync", "config.json")
}

func loadConfig() config {
	var c config
	if b, err := os.ReadFile(configPath()); err == nil {
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func saveConfig(c config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// ensureExe 把内置主程序释放到临时目录，返回路径与清理函数。
// 只使用内置副本，避免与外部 exe 版本不一致。
func ensureExe() (string, func(), error) {
	if len(embeddedCLI) == 0 {
		return "", nil, errors.New("当前界面程序未内置主程序，请用 build.cmd 重新构建")
	}
	f, err := os.CreateTemp("", "blocksync-*.exe")
	if err != nil {
		return "", nil, fmt.Errorf("创建临时文件失败: %w", err)
	}
	p := f.Name()
	cleanup := func() { _ = os.Remove(p) }
	if _, err := f.Write(embeddedCLI); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("释放主程序失败: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return p, cleanup, nil
}

// ---------- 目录选择 ----------

func browseFolder(title string) string {
	buf := make([]uint16, syscall.MAX_PATH)
	bi := browseInfo{
		hwndOwner:      mainHwnd,
		pszDisplayName: &buf[0],
		lpszTitle:      u16(title),
		ulFlags:        0x0001 | 0x0040, // BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE
	}
	pidl, _, _ := pSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer pCoTaskMemFree.Call(pidl)
	out := make([]uint16, syscall.MAX_PATH)
	ok, _, _ := pSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&out[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(out)
}

// ---------- 执行 ----------

func appendLog(lines ...string) {
	mu.Lock()
	logQueue = append(logQueue, lines...)
	mu.Unlock()
}

func startRun(dry, verify, full bool) {
	src := strings.TrimSpace(getText(hEditSrc))
	dst := strings.TrimSpace(getText(hEditDst))
	if src == "" || dst == "" {
		msgBox("请先选择输入目录和输出目录。", mbOk|mbIconError)
		return
	}
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		msgBox("输入目录和输出目录不能是同一个。", mbOk|mbIconError)
		return
	}
	exe, cleanup, err := ensureExe()
	if err != nil {
		msgBox(err.Error(), mbOk|mbIconError)
		return
	}

	args := []string{"sync", src, dst}
	if dry {
		args = append(args, "--dry-run")
	}
	if verify {
		args = append(args, "--verify")
	}
	if full {
		args = append(args, "--full")
	}

	mu.Lock()
	logQueue = nil
	progCur, progTotal, progKnown = 0, 0, false
	finished, exitCode = false, 0
	cancelled = false
	mu.Unlock()
	setText(hLog, "")
	logChars = 0
	setText(hStatus, "正在同步…")

	r, w, err := os.Pipe()
	if err != nil {
		cleanup()
		msgBox(err.Error(), mbOk|mbIconError)
		return
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		cleanup()
		w.Close()
		r.Close()
		msgBox(err.Error(), mbOk|mbIconError)
		return
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin = inR
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		w.Close()
		r.Close()
		cleanup()
		msgBox("启动失败："+err.Error(), mbOk|mbIconError)
		return
	}
	w.Close()
	inR.Close()
	proc = cmd
	running = true
	setRunningUI(true)

	go func() {
		defer r.Close()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var recent []string
		for sc.Scan() {
			line := sc.Text()
			appendLog(line)
			if strings.Contains(line, "已取消") {
				mu.Lock()
				cancelled = true
				mu.Unlock()
			}
			if strings.Contains(line, confirmMarker) {
				// 子进程打印该行后停在标准输入等待答复
				body := confirmBody(recent)
				if body == "" {
					body = fmt.Sprintf("输入目录：%s\n输出目录：%s", src, dst)
				}
				answer := "n\n"
				if msgBox(body+"\n\n确认继续吗？", mbYesNo|mbIconWarning) == idYes {
					answer = "y\n"
				}
				_, _ = inW.Write([]byte(answer))
				continue
			}
			recent = append(recent, line)
			if len(recent) > 12 {
				recent = recent[len(recent)-12:]
			}
			if m := reProgress.FindStringSubmatch(line); m != nil {
				c, _ := strconv.Atoi(m[1])
				t, _ := strconv.Atoi(m[2])
				mu.Lock()
				progCur, progTotal, progKnown = c, t, t > 0
				mu.Unlock()
			}
		}
	}()

	go func() {
		err := cmd.Wait()
		inW.Close()
		cleanup() // 子进程已退出，删除临时释放的主程序
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		mu.Lock()
		finished, exitCode = true, code
		mu.Unlock()
	}()
}

// confirmBody 从日志尾部拼出确认框内容：去掉命令回显与标记行，保持自上而下的顺序。
func confirmBody(lines []string) string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "输入：") || strings.HasPrefix(t, "输出：") || strings.Contains(l, confirmMarker) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func setRunningUI(on bool) {
	enable(hBtnSync, !on)
	enable(hBtnBrowseSrc, !on)
	enable(hBtnBrowseDst, !on)
	enable(hBtnSwap, !on)
	enable(hBtnCancel, on)
	enable(hEditSrc, !on)
	enable(hEditDst, !on)
	enable(hChkVerify, !on)
	enable(hChkFull, !on)
}

// buildMenu 创建菜单栏：试运行与两个工具项收进菜单，主界面只留同步按钮。
func buildMenu() syscall.Handle {
	menu, _, _ := pCreateMenu.Call()

	act, _, _ := pCreatePopupMenu.Call()
	pAppendMenuW.Call(act, mfString, idDry, uintptr(unsafe.Pointer(u16("试运行（计算哈希）"))))
	pAppendMenuW.Call(menu, mfPopup, act, uintptr(unsafe.Pointer(u16("操作"))))

	dir, _, _ := pCreatePopupMenu.Call()
	pAppendMenuW.Call(dir, mfString, idSave, uintptr(unsafe.Pointer(u16("记住这两个目录"))))
	pAppendMenuW.Call(dir, mfSeparator, 0, 0)
	pAppendMenuW.Call(dir, mfString, idOpenSrc, uintptr(unsafe.Pointer(u16("打开原目录"))))
	pAppendMenuW.Call(dir, mfString, idOpen, uintptr(unsafe.Pointer(u16("打开输出目录"))))
	pAppendMenuW.Call(menu, mfPopup, dir, uintptr(unsafe.Pointer(u16("目录"))))

	return syscall.Handle(menu)
}

// openFolder 用资源管理器打开指定目录。
func openFolder(path string) {
	if path == "" {
		return
	}
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(u16("open"))),
		uintptr(unsafe.Pointer(u16(path))), 0, 0, swShow)
}

func flushUI() {
	mu.Lock()
	lines := logQueue
	logQueue = nil
	cur, total, known := progCur, progTotal, progKnown
	fin, code := finished, exitCode
	wasCancelled := cancelled
	mu.Unlock()

	if len(lines) > 0 {
		text := strings.Join(lines, "\r\n") + "\r\n"
		pSendMessageW.Call(uintptr(hLog), emSetSel, ^uintptr(0), ^uintptr(0))
		pSendMessageW.Call(uintptr(hLog), emReplaceSel, 0, uintptr(unsafe.Pointer(u16(text))))
		pSendMessageW.Call(uintptr(hLog), emScrollCaret, 0, 0)
		logChars += len(text)
		if logChars > 500000 {
			setText(hLog, "")
			setText(hLog, text)
			logChars = len(text)
		}
	}

	if known && total > 0 && running {
		pSendMessageW.Call(uintptr(hProgress), pbmSetMarquee, 0, 0)
		pSendMessageW.Call(uintptr(hProgress), pbmSetRange32, 0, uintptr(total))
		pSendMessageW.Call(uintptr(hProgress), pbmSetPos, uintptr(cur), 0)
		setText(hStatus, fmt.Sprintf("已处理 %d / %d 个文件", cur, total))
	}

	if fin && running {
		running = false
		proc = nil
		setRunningUI(false)
		pSendMessageW.Call(uintptr(hProgress), pbmSetMarquee, 0, 0)
		// 结束时把进度条停在“成功满格 / 中止清零”，避免停在运行中的文件计数上
		pSendMessageW.Call(uintptr(hProgress), pbmSetRange32, 0, 100)
		done := uintptr(0)
		if code == 0 && !wasCancelled {
			done = 100
		}
		pSendMessageW.Call(uintptr(hProgress), pbmSetPos, done, 0)
		joined := strings.Join(lines, "\n")
		if wasCancelled {
			setText(hStatus, "已取消，未写入任何文件")
		} else if code == 0 {
			if m := reResult.FindStringSubmatch(joined); m != nil {
				if m[1] != "" {
					setText(hStatus, fmt.Sprintf("试运行：需改动 %s 个文件、删除 %s 个；读取 %s，预计需写入 %s（未改动数据）", m[3], m[4], m[5], m[6]))
				} else {
					setText(hStatus, fmt.Sprintf("完成：改动 %s 个文件、删除 %s 个；读取 %s，写入 %s", m[3], m[4], m[5], m[6]))
				}
			} else if strings.Contains(joined, "未改动任何数据") {
				setText(hStatus, "试运行：没有需要搬运的内容")
			} else if strings.Contains(joined, "已是最新") {
				setText(hStatus, "已是最新，没有需要搬运的内容")
			} else {
				setText(hStatus, "完成")
			}
		} else {
			setText(hStatus, fmt.Sprintf("失败（退出码 %d），详见下面的日志", code))
		}
	}
}

// ---------- 界面构建 ----------

func onCreate() {
	fh := -int(12.0*scale + 0.5) // 9pt @ 96dpi = 12px
	face := u16("Segoe UI")
	h, _, _ := pCreateFontW.Call(uintptr(fh), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
	uiFont = syscall.Handle(h)

	label := func(text string, x, y, w int) {
		create("STATIC", text, wsChild|wsVisible, x, y, w, 22, 0)
	}

	label("输入目录", 16, 18, 74)
	hEditSrc = create("EDIT", "", wsChild|wsVisible|wsTabStop|wsBorder|esAutohscroll, 96, 16, 480, 26, idEditSrc)
	hBtnBrowseSrc = create("BUTTON", "浏览…", wsChild|wsVisible|wsTabStop|bsPushbutton, 584, 16, 56, 26, idBSrc)

	label("输出目录", 16, 52, 74)
	hEditDst = create("EDIT", "", wsChild|wsVisible|wsTabStop|wsBorder|esAutohscroll, 96, 50, 480, 26, idEditDst)
	hBtnBrowseDst = create("BUTTON", "浏览…", wsChild|wsVisible|wsTabStop|bsPushbutton, 584, 50, 56, 26, idBDst)

	hBtnSwap = create("BUTTON", "交换", wsChild|wsVisible|wsTabStop|bsPushbutton, 648, 16, 64, 60, idSwap)

	hChkVerify = create("BUTTON", "写入后复读校验", wsChild|wsVisible|wsTabStop|bsAutoCheckbox, 96, 86, 124, 22, idChkVer)
	hChkFull = create("BUTTON", "忽略修改时间，全量比对", wsChild|wsVisible|wsTabStop|bsAutoCheckbox, 236, 86, 180, 22, idChkFull)

	hBtnSync = create("BUTTON", "开始同步", wsChild|wsVisible|wsTabStop|bsPushbutton, 96, 116, 190, 30, idSync)
	hBtnCancel = create("BUTTON", "取消", wsChild|wsVisible|wsTabStop|bsPushbutton, 296, 116, 76, 30, idCancel)

	hStatus = create("STATIC", "准备就绪", wsChild|wsVisible, 96, 156, 616, 20, idStatus)
	hProgress = create("msctls_progress32", "", wsChild|wsVisible|pbsSmooth|pbsMarquee, 96, 180, 616, 16, idProg)
	pSendMessageW.Call(uintptr(hProgress), pbmSetRange32, 0, 100)

	label("日志", 16, 206, 60)
	hLog = create("EDIT", "",
		wsChild|wsVisible|wsBorder|wsVScroll|wsHScroll|esMultiline|esAutovscroll|esAutohscroll|esReadonly,
		16, 228, 696, 292, idLog)
	pSendMessageW.Call(uintptr(hLog), emSetLimitTxt, 2000000, 0)

	create("STATIC", "提示：同步只写输出目录，不会改动输入目录；同步前请先关闭正在读写这些文件的程序。",
		wsChild|wsVisible, 16, 532, 700, 20, 0)

	enable(hBtnCancel, false)

	cfg := loadConfig()
	if cfg.LocalDir != "" {
		setText(hEditSrc, cfg.LocalDir)
	}
	if cfg.CarrierDir != "" {
		setText(hEditDst, cfg.CarrierDir)
	}
	pSetTimer.Call(uintptr(mainHwnd), timerID, 120, 0)
}

func wndProc(hwnd syscall.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmCreate:
		mainHwnd = hwnd
		onCreate()
		return 0

	case wmCommand:
		switch int(wParam & 0xFFFF) {
		case idBSrc:
			if d := browseFolder("选择输入目录"); d != "" {
				setText(hEditSrc, d)
			}
		case idBDst:
			if d := browseFolder("选择输出目录"); d != "" {
				setText(hEditDst, d)
			}
		case idSwap:
			a, b := getText(hEditSrc), getText(hEditDst)
			setText(hEditSrc, b)
			setText(hEditDst, a)
			setText(hStatus, "已交换输入与输出目录")
		case idSync, idDry:
			if running {
				return 0
			}
			v, _, _ := pSendMessageW.Call(uintptr(hChkVerify), bmGetCheck, 0, 0)
			f, _, _ := pSendMessageW.Call(uintptr(hChkFull), bmGetCheck, 0, 0)
			verify := v != 0
			full := f != 0
			startRun(wParam&0xFFFF == idDry, verify, full)
		case idCancel:
			if running && proc != nil && proc.Process != nil {
				if msgBox("正在同步，确定要中止吗？\r\n\r\n已写入的部分不会回滚，重新运行会接着补齐。",
					mbYesNo|mbIconError) == idYes {
					_ = proc.Process.Kill()
					setText(hStatus, "正在中止…")
				}
			}
		case idSave:
			c := config{LocalDir: strings.TrimSpace(getText(hEditSrc)), CarrierDir: strings.TrimSpace(getText(hEditDst))}
			if err := saveConfig(c); err != nil {
				msgBox("保存失败："+err.Error(), mbOk|mbIconError)
			} else {
				msgBox("已记住这两个目录，下次打开会自动填好。", mbOk|mbIconInfo)
			}
		case idOpen:
			openFolder(strings.TrimSpace(getText(hEditDst)))
		case idOpenSrc:
			openFolder(strings.TrimSpace(getText(hEditSrc)))
		}
		return 0

	case wmTimer:
		flushUI()
		return 0

	case wmClose:
		mu.Lock()
		isRunning := running
		mu.Unlock()
		if isRunning {
			if msgBox("同步尚未结束，确定退出吗？", mbYesNo|mbIconError) != idYes {
				return 0
			}
			if proc != nil && proc.Process != nil {
				_ = proc.Process.Kill()
			}
		}
		pKillTimer.Call(uintptr(hwnd), timerID)
		pDestroyWindow.Call(uintptr(hwnd))
		return 0

	case wmDestroy:
		if uiFont != 0 {
			pDeleteObject.Call(uintptr(uiFont))
		}
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(m), wParam, lParam)
	return r
}

func initDPI() {
	if r, _, _ := pSetDPIAwarenessCtx.Call(^uintptr(3)); r == 0 { // PER_MONITOR_AWARE_V2
		pSetProcessDPIAware.Call()
	}
	dpi := 96
	if r, _, _ := pGetDpiForSystem.Call(); r > 0 {
		dpi = int(r)
	}
	scale = float64(dpi) / 96.0
}

func main() {
	// 窗口与消息循环必须始终在同一个 OS 线程上，否则 GetMessage 会收不到本窗口的消息
	runtime.LockOSThread()

	initDPI()

	hi, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	hInstance = syscall.Handle(hi)
	cursor, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW

	className := u16("blocksyncUIWindow")
	title := u16("blocksync - 文件增量同步")

	cb := syscall.NewCallback(wndProc)
	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   cb,
		hInstance:     syscall.Handle(hInstance),
		hCursor:       syscall.Handle(cursor),
		hbrBackground: 16, // COLOR_BTNFACE + 1
		lpszClassName: className,
	}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	const style = 0x00C00000 | 0x00080000 | 0x00020000 | 0x02000000 // CAPTION|SYSMENU|MINIMIZEBOX|CLIPCHILDREN
	r := rect{0, 0, int32(sz(728)), int32(sz(566))}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), uintptr(style), 1, 0) // bMenu=TRUE 计入菜单栏高度
	wide, high := int(r.right-r.left), int(r.bottom-r.top)

	scrW, _, _ := pGetSystemMetrics.Call(0)
	scrH, _, _ := pGetSystemMetrics.Call(1)
	x := (int(scrW) - wide) / 2
	y := (int(scrH) - high) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	hMenu := buildMenu()
	hwnd, _, _ := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		uintptr(style), uintptr(x), uintptr(y), uintptr(wide), uintptr(high),
		0, uintptr(hMenu), uintptr(hInstance), 0)
	mainHwnd = syscall.Handle(hwnd)
	if mainHwnd == 0 {
		msgBox("创建窗口失败", mbOk|mbIconError)
		return
	}
	pShowWindow.Call(uintptr(mainHwnd), swShow)
	pUpdateWindow.Call(uintptr(mainHwnd))

	var m msgT
	for {
		ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
