//go:build windows

// BlockSync-GUI：只收集路径与选项，调用内置的 BlockSync-CLI 执行，并把输出回显到窗口。
package main

import (
	"bufio"
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

	"blocksync/internal/scan"
	"blocksync/internal/state"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")

	pRegisterClassExW        = user32.NewProc("RegisterClassExW")
	pCreateWindowExW         = user32.NewProc("CreateWindowExW")
	pDefWindowProcW          = user32.NewProc("DefWindowProcW")
	pDestroyWindow           = user32.NewProc("DestroyWindow")
	pShowWindow              = user32.NewProc("ShowWindow")
	pUpdateWindow            = user32.NewProc("UpdateWindow")
	pGetMessageW             = user32.NewProc("GetMessageW")
	pTranslateMessage        = user32.NewProc("TranslateMessage")
	pDispatchMessageW        = user32.NewProc("DispatchMessageW")
	pPostQuitMessage         = user32.NewProc("PostQuitMessage")
	pSendMessageW            = user32.NewProc("SendMessageW")
	pSetWindowTextW          = user32.NewProc("SetWindowTextW")
	pGetWindowTextW          = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW    = user32.NewProc("GetWindowTextLengthW")
	pSetTimer                = user32.NewProc("SetTimer")
	pKillTimer               = user32.NewProc("KillTimer")
	pMessageBoxW             = user32.NewProc("MessageBoxW")
	pEnableWindow            = user32.NewProc("EnableWindow")
	pSetProcessDPIAware      = user32.NewProc("SetProcessDPIAware")
	pSetDPIAwarenessCtx      = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForSystem         = user32.NewProc("GetDpiForSystem")
	pAdjustWindowRectEx      = user32.NewProc("AdjustWindowRectEx")
	pCreateMenu              = user32.NewProc("CreateMenu")
	pCreatePopupMenu         = user32.NewProc("CreatePopupMenu")
	pAppendMenuW             = user32.NewProc("AppendMenuW")
	pLoadCursorW             = user32.NewProc("LoadCursorW")
	pGetSystemMetrics        = user32.NewProc("GetSystemMetrics")
	pSetWindowPos            = user32.NewProc("SetWindowPos")
	pGetWindowRect           = user32.NewProc("GetWindowRect")
	pGetClientRect           = user32.NewProc("GetClientRect")
	pPostMessageW            = user32.NewProc("PostMessageW")
	pDialogBoxIndirectParamW = user32.NewProc("DialogBoxIndirectParamW")
	pEndDialog               = user32.NewProc("EndDialog")
	pGetDlgItem              = user32.NewProc("GetDlgItem")
	pSetFocus                = user32.NewProc("SetFocus")
	pGetForegroundWindow     = user32.NewProc("GetForegroundWindow")
	pSetForegroundWindow     = user32.NewProc("SetForegroundWindow")
	pBringWindowToTop        = user32.NewProc("BringWindowToTop")

	pCreateFontW  = gdi32.NewProc("CreateFontW")
	pDeleteObject = gdi32.NewProc("DeleteObject")

	pSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	pShellExecuteW        = shell32.NewProc("ShellExecuteW")
	pCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")

	pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
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

	bsPushbutton    = 0x00000000
	bsAutoCheckbox  = 0x00000003
	bsDefPushbutton = 0x00000001
	bmGetCheck      = 0x00F0
	bmSetCheck      = 0x00F1

	esWantReturn = 0x1000

	ssSunken      = 0x00000500
	ssCenterImage = 0x00000200
	ssNoPrefix    = 0x00000080
	ssEndEllipsis = 0x00004000

	wsPopup   = 0x80000000
	wsCaption = 0x00C00000
	wsSysMenu = 0x00080000

	pbsSmooth  = 0x0001
	pbsMarquee = 0x0008

	wmCreate     = 0x0001
	wmInitDialog = 0x0110
	wmDestroy    = 0x0002
	wmClose      = 0x0010
	wmSetFont    = 0x0030
	wmCommand    = 0x0111
	wmTimer      = 0x0113

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

	swpNoSize   = 0x0001
	swpNoZOrder = 0x0004

	dsSetFont    = 0x0040
	dsModalFrame = 0x0080
	atomButton   = 0x0080
	atomEdit     = 0x0081
	atomStatic   = 0x0082

	wmNotify            = 0x004E
	wmBringFront        = 0x0400 + 1 // WM_APP+1：由后台线程投递，请求把主窗口拉回前台
	iccListView         = 0x00000001 // ICC_LISTVIEW_CLASSES
	lvnSelChange        = 0xFFFFFF9B // LVN_SELCHANGE（-101）
	lvsReport           = 0x0001
	lvsSingleSel        = 0x0004
	lvsShowSelAlways    = 0x0008
	lvsExFullRowSelect  = 0x00000020
	lvisSelected        = 0x0002
	lvisFocused         = 0x0001
	lvniSelected        = 0x0002
	lvmFirst            = 0x1000
	lvmDeleteAllItems   = lvmFirst + 9
	lvmGetNextItem      = lvmFirst + 12
	lvmEnsureVisible    = lvmFirst + 19
	lvmSetItemState     = lvmFirst + 43
	lvmInsertItemW      = lvmFirst + 77
	lvmSetItemTextW     = lvmFirst + 116
	lvmInsertColumnW    = lvmFirst + 97
	lvmSetExtendedStyle = lvmFirst + 54
	lvifText            = 0x0001
	lvcfText            = 0x0004
	lvcfWidth           = 0x0002
	lvcfSubItem         = 0x0008
	lvcfmtLeft          = 0x0000
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

type initCommonControlsEx struct {
	dwSize uint32
	dwICC  uint32
}

type nmhdr struct {
	hwndFrom syscall.Handle
	idFrom   uintptr
	code     uint32
}

// lvColumn 对应 LVCOLUMNW；字段顺序与 C 结构一致。
type lvColumn struct {
	mask       uint32
	fmt        int32
	cx         int32
	pszText    *uint16
	cchTextMax int32
	iSubItem   int32
	iImage     int32
	iOrder     int32
}

// lvItem 对应 LVITEMW；字段顺序与 C 结构一致。
type lvItem struct {
	mask       uint32
	iItem      int32
	iSubItem   int32
	state      uint32
	stateMask  uint32
	pszText    *uint16
	cchTextMax int32
	iImage     int32
	lParam     uintptr
	iIndent    int32
	iGroupID   int32
	cColumns   uint32
	puColumns  uintptr
	piColFmt   *int32
	iGroup     int32
}

// appliedProfile 记录弹窗里“应用并关闭”选中的配置，供回到主界面后回填。
type appliedProfile struct {
	name, src, dst string
	ok             bool
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
	idMenuExc = 1017
	idCfgMgr  = 1018
	timerID   = 1

	idExEdit   = 2001
	idExOk     = 2002
	idExCancel = 2003
	idExSample = 2004

	idCfgList      = 3001
	idCfgName      = 3002
	idCfgSrc       = 3003
	idCfgDst       = 3004
	idCfgEx        = 3005
	idCfgBrowseSrc = 3006
	idCfgBrowseDst = 3007
	idCfgSample    = 3008
	idCfgNew       = 3010
	idCfgSave      = 3011
	idCfgDelete    = 3012
	idCfgApply     = 3013

	idNameEdit = 4001

	idDlgOk     = 1
	idDlgCancel = 2
)

var (
	hInstance syscall.Handle
	mainHwnd  syscall.Handle
	uiFont    syscall.Handle
	scale     = 1.0

	hEditSrc, hEditDst            syscall.Handle
	hBtnBrowseSrc, hBtnBrowseDst  syscall.Handle
	hBtnSwap                      syscall.Handle
	hChkVerify, hChkFull          syscall.Handle
	hBtnSync, hBtnDry, hBtnCancel syscall.Handle
	hBtnCfgMgr                    syscall.Handle
	hStatus, hProgress, hLog      syscall.Handle

	mu         sync.Mutex
	logQueue   []string
	progCur    int
	progTotal  int
	progKnown  bool
	finished   bool
	exitCode   int
	procExited bool
	readerDone bool
	running    bool
	cancelled  bool
	proc       *exec.Cmd
	logChars   int

	lastSrc, lastDst                                   string
	lastDry, lastVerify, lastFull, lastForce, lastMass bool

	reProgress = regexp.MustCompile(`\[(\d+)/(\d+)\]\s*(.*)`)
	reResult   = regexp.MustCompile(`完成(（测试比对）)?：扫描 (\d+) 个，改动 (\d+) 个，删除 (\d+) 个；读取 (\d+(?:\.\d+)? (?:B|KiB|MiB|GiB|TiB))，(?:预计需)?写入 (\d+(?:\.\d+)? (?:B|KiB|MiB|GiB|TiB))`)

	hExDlg, hExEdit syscall.Handle
	exDlgText       string
	exDlgOK         bool
	exDlgDone       bool
	exClassReady    bool

	hCfgDlg, hCfgList                  syscall.Handle
	hCfgName, hCfgSrc, hCfgDst, hCfgEx syscall.Handle
	hCfgBrowseSrc, hCfgBrowseDst       syscall.Handle
	cfgDlgCfg                          *state.Config
	cfgDlgSel                          string
	cfgDraftSrc, cfgDraftDst           string
	cfgDlgDirty                        bool
	cfgListBusy                        bool
	cfgApplied                         appliedProfile

	hSaveNameDlg, hSaveNameEdit syscall.Handle
	saveNameText                string
	saveNameOK                  bool
)

// excludeSample 排除列表的基础示例，首次打开对话框时预填。
var excludeSample = []string{
	`\System Volume Information\`,
	`\$Recycle.Bin\`,
	`\RECYCLE?\`,
	`\Recovery\`,
	`\Config.Msi\`,
	`\pagefile.sys`,
	`\hiberfil.sys`,
	`\swapfile.sys`,
	`desktop.ini`,
	`thumbs.db`,
	`.DS_Store`,
}

// confirmMarker 与 BlockSync-CLI 中的同名常量保持一致：出现该行时子进程在等待确认。
const confirmMarker = "需要确认后才会开始写入"

// seedExcludes 为尚无排除记录的目录对写入示例规则作为默认值；
// 已有记录（包括用户清空后留下的空列表）保持不动，不会被重新填充。
func seedExcludes(cfg *state.Config) bool {
	changed := false
	for _, p := range cfg.Profiles {
		if p.LocalDir == "" || p.CarrierDir == "" {
			continue
		}
		if cfg.ExcludesFor(p.LocalDir, p.CarrierDir) != nil {
			continue
		}
		cfg.SetExcludes(p.LocalDir, p.CarrierDir, append([]string(nil), excludeSample...))
		changed = true
	}
	return changed
}

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

func createChild(parent syscall.Handle, class, text string, style uint32, x, y, w, h int, id int) syscall.Handle {
	r, _, _ := pCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(u16(class))),
		uintptr(unsafe.Pointer(u16(text))),
		uintptr(style),
		uintptr(sz(x)), uintptr(sz(y)), uintptr(sz(w)), uintptr(sz(h)),
		uintptr(parent), uintptr(id), uintptr(hInstance), 0)
	hh := syscall.Handle(r)
	if uiFont != 0 && hh != 0 {
		pSendMessageW.Call(uintptr(hh), wmSetFont, uintptr(uiFont), 1)
	}
	return hh
}

func create(class, text string, style uint32, x, y, w, h int, id int) syscall.Handle {
	return createChild(mainHwnd, class, text, style, x, y, w, h, id)
}

func enable(h syscall.Handle, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	pEnableWindow.Call(uintptr(h), v)
}

func msgBoxOwner(owner syscall.Handle, text string, flags uint32) int {
	r, _, _ := pMessageBoxW.Call(uintptr(owner),
		uintptr(unsafe.Pointer(u16(text))),
		uintptr(unsafe.Pointer(u16("blocksync"))), uintptr(flags))
	return int(r)
}

func msgBox(text string, flags uint32) int {
	return msgBoxOwner(mainHwnd, text, flags)
}

// ---------- 配置（与命令行共用） ----------

// currentDirs 返回界面上的输入、输出目录。
func currentDirs() (string, string) {
	return strings.TrimSpace(getText(hEditSrc)), strings.TrimSpace(getText(hEditDst))
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

// massDeleteHint 返回失败日志中的超量删除提示行；没有则返回空串。
func massDeleteHint(logText string) string {
	for _, line := range strings.Split(logText, "\n") {
		if strings.Contains(line, "超过安全上限") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// ---------- 目录选择 ----------

// browseFolder 以主窗口为 owner 选择目录。
func browseFolder(title string) string {
	return browseFolderFor(mainHwnd, title)
}

// browseFolderFor 以指定窗口为 owner 选择目录；从模态对话框内调用时传对话框句柄，
// 避免主窗口在浏览框关闭后被错误地启用或激活。
func browseFolderFor(owner syscall.Handle, title string) string {
	buf := make([]uint16, syscall.MAX_PATH)
	bi := browseInfo{
		hwndOwner:      owner,
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

// bringToFront 把窗口拉回前台；已在前台时不重复激活，避免打断用户。
func bringToFront(h syscall.Handle) {
	if h == 0 {
		return
	}
	if r, _, _ := pGetForegroundWindow.Call(); syscall.Handle(r) == h {
		return
	}
	pSetForegroundWindow.Call(uintptr(h))
	pBringWindowToTop.Call(uintptr(h))
}

var iccOnce sync.Once

// initCommonControls 加载 comctl32 的列表视图类；重复调用无副作用。
func initCommonControls() {
	iccOnce.Do(func() {
		icc := initCommonControlsEx{dwSize: uint32(unsafe.Sizeof(initCommonControlsEx{})), dwICC: iccListView}
		pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
	})
}

// ---------- 执行 ----------

func appendLog(lines ...string) {
	mu.Lock()
	logQueue = append(logQueue, lines...)
	mu.Unlock()
}

// appendLogText 把一组行按顺序写入日志区；仅在界面线程调用。
func appendLogText(lines []string) {
	if len(lines) == 0 {
		return
	}
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

// flushPendingLog 立即写入队列中已有的行；仅在界面线程调用。
func flushPendingLog() {
	mu.Lock()
	lines := logQueue
	logQueue = nil
	mu.Unlock()
	appendLogText(lines)
}

// lockExists 只检查目录锁是否存在，不读取内容。
func lockExists(dir string) bool {
	_, err := os.Stat(filepath.Join(state.Dir(dir), "lock"))
	return err == nil
}

func startRun(dry, verify, full, force, allowMass bool) {
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
	if force {
		args = append(args, "--force")
	}
	if allowMass {
		args = append(args, "--allow-mass-delete")
	}
	lastSrc, lastDst = src, dst
	lastDry, lastVerify, lastFull, lastForce, lastMass = dry, verify, full, force, allowMass

	mu.Lock()
	progCur, progTotal, progKnown = 0, 0, false
	finished, exitCode = false, 0
	procExited, readerDone = false, false
	cancelled = false
	mu.Unlock()
	// 先把上一次运行遗留的行写入，再插入分隔行，保证先后顺序
	flushPendingLog()
	if logChars > 0 {
		if dry {
			appendLog("", "----- 测试比对 -----")
		} else {
			appendLog("", "----- 同步 -----")
		}
	}
	if dry {
		for _, d := range []string{src, dst} {
			if lockExists(d) {
				appendLog(fmt.Sprintf("提示：检测到目录锁 %s，测试比对只读执行，不受影响", filepath.Join(state.Dir(d), "lock")))
			}
		}
	}
	if dry {
		setText(hStatus, "正在比对…")
	} else {
		setText(hStatus, "正在同步…")
	}

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
				// 确认框在读取协程里弹出，关闭后由界面线程把主窗口拉回前台
				pPostMessageW.Call(uintptr(mainHwnd), wmBringFront, 0, 0)
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
		// 读端排空且进程已退出，才允许界面开始下一次运行
		mu.Lock()
		readerDone = true
		if procExited {
			finished = true
		}
		mu.Unlock()
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
		exitCode, procExited = code, true
		if readerDone {
			finished = true
		}
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
	enable(hBtnDry, !on)
	enable(hBtnBrowseSrc, !on)
	enable(hBtnBrowseDst, !on)
	enable(hBtnSwap, !on)
	enable(hBtnCfgMgr, !on)
	enable(hBtnCancel, on)
	enable(hEditSrc, !on)
	enable(hEditDst, !on)
	enable(hChkVerify, !on)
	enable(hChkFull, !on)
}

// buildMenu 创建菜单栏：保存配置与打开目录收进菜单，比对与同步留在主界面。
func buildMenu() syscall.Handle {
	menu, _, _ := pCreateMenu.Call()

	dir, _, _ := pCreatePopupMenu.Call()
	pAppendMenuW.Call(dir, mfString, idSave, uintptr(unsafe.Pointer(u16("保存为配置…"))))
	pAppendMenuW.Call(dir, mfSeparator, 0, 0)
	pAppendMenuW.Call(dir, mfString, idOpenSrc, uintptr(unsafe.Pointer(u16("打开原目录"))))
	pAppendMenuW.Call(dir, mfString, idOpen, uintptr(unsafe.Pointer(u16("打开输出目录"))))
	pAppendMenuW.Call(menu, mfPopup, dir, uintptr(unsafe.Pointer(u16("目录"))))

	settings, _, _ := pCreatePopupMenu.Call()
	pAppendMenuW.Call(settings, mfString, idMenuExc, uintptr(unsafe.Pointer(u16("排除列表…"))))
	pAppendMenuW.Call(menu, mfPopup, settings, uintptr(unsafe.Pointer(u16("设置"))))

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

// ---------- 排除列表对话框 ----------

// openExcludeDialog 读取当前目录对的排除列表，编辑确认后写回本机配置。
func openExcludeDialog() {
	if running {
		return
	}
	src, dst := currentDirs()
	if src == "" || dst == "" {
		msgBox("请先选择输入目录和输出目录。", mbOk|mbIconError)
		return
	}
	initial := strings.Join(excludeSample, "\r\n")
	if cfg, err := state.LoadConfig(); err == nil && cfg != nil {
		if list := cfg.ExcludesFor(src, dst); list != nil {
			initial = strings.Join(list, "\r\n")
		}
	}
	text, ok := showExcludeDialog(initial)
	bringToFront(mainHwnd)
	if !ok {
		return
	}
	list := splitExcludeLines(text)
	cfg, err := state.LoadConfig()
	if err != nil {
		msgBox("读取配置失败："+err.Error(), mbOk|mbIconError)
		return
	}
	if cfg == nil {
		cfg = &state.Config{}
	}
	cfg.SetExcludes(src, dst, list)
	if err := state.SaveConfig(cfg); err != nil {
		msgBox("保存排除列表失败："+err.Error(), mbOk|mbIconError)
		return
	}
	setText(hStatus, fmt.Sprintf("已保存 %d 条排除规则（当前目录对）", len(list)))
}

// splitExcludeLines 按行拆分并去掉空行。
func splitExcludeLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// validateExcludeText 校验每行规则的写法，返回第一条错误。
func validateExcludeText(text string) error {
	for i, line := range splitExcludeLines(text) {
		if err := scan.ValidatePattern(line); err != nil {
			return fmt.Errorf("第 %d 行“%s”不是有效规则：%v", i+1, line, err)
		}
	}
	return nil
}

// showExcludeDialog 用系统模态对话框编辑排除列表；返回文本与是否确认。
func showExcludeDialog(initial string) (string, bool) {
	exDlgText, exDlgOK = initial, false
	tpl := buildExcludeTemplate()
	ret, _, _ := pDialogBoxIndirectParamW.Call(
		uintptr(hInstance),
		uintptr(unsafe.Pointer(&tpl[0])),
		uintptr(mainHwnd),
		exDlgCallback(),
		0)
	runtime.KeepAlive(tpl)
	if ret == ^uintptr(0) {
		msgBox("无法打开排除列表对话框。", mbOk|mbIconError)
		return initial, false
	}
	if !exDlgOK {
		return initial, false
	}
	return exDlgText, true
}

var (
	exDlgCbOnce sync.Once
	exDlgCb     uintptr
)

// exDlgCallback 复用同一个对话框过程回调，避免重复注册占用回调槽位。
func exDlgCallback() uintptr {
	exDlgCbOnce.Do(func() { exDlgCb = syscall.NewCallback(exDlgProc) })
	return exDlgCb
}

// exDlgProc 排除列表对话框的窗口过程；激活、焦点与回车/ESC 由系统模态循环处理。
func exDlgProc(hwnd syscall.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmInitDialog:
		hExDlg = hwnd
		hExEdit = getDlgItem(hwnd, idExEdit)
		setText(hExEdit, exDlgText)
		pSendMessageW.Call(uintptr(hExEdit), emSetLimitTxt, 100000, 0)
		pSetFocus.Call(uintptr(hExEdit))
		// 对话框管理器在 WM_INITDIALOG 返回后才设置焦点，异步清掉默认全选
		pPostMessageW.Call(uintptr(hExEdit), emSetSel, 0, 0)
		centerOnOwner(hwnd)
		return 1

	case wmCommand:
		switch int(wParam & 0xFFFF) {
		case idExSample:
			setText(hExEdit, strings.Join(excludeSample, "\r\n"))
		case idDlgOk:
			text := getText(hExEdit)
			if err := validateExcludeText(text); err != nil {
				msgBoxOwner(hwnd, err.Error(), mbOk|mbIconError)
				return 1
			}
			exDlgText, exDlgOK = text, true
			pEndDialog.Call(uintptr(hwnd), 1)
		case idDlgCancel:
			pEndDialog.Call(uintptr(hwnd), 0)
		}
		return 1

	case wmClose:
		pEndDialog.Call(uintptr(hwnd), 0)
		return 1

	case wmDestroy:
		hExDlg, hExEdit = 0, 0
	}
	return 0
}

// buildExcludeTemplate 构造排除列表对话框的内存模板（DLGTEMPLATE），
// 尺寸使用对话框单位，字体与主界面一致（Segoe UI 9pt）。
func buildExcludeTemplate() []byte {
	var b []byte
	put16 := func(v uint16) { b = append(b, byte(v), byte(v>>8)) }
	put32 := func(v uint32) { b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	putText := func(s string) {
		u, err := syscall.UTF16FromString(s)
		if err != nil {
			u = []uint16{0}
		}
		for _, c := range u {
			b = append(b, byte(c), byte(c>>8))
		}
	}
	align4 := func() {
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}

	put32(dsSetFont | dsModalFrame | wsPopup | wsCaption | wsSysMenu)
	put32(0)
	put16(7)
	put16(0)
	put16(0)
	put16(300)
	put16(190)
	put16(0)
	put16(0)
	putText("排除列表")
	put16(9)
	putText("Segoe UI")

	item := func(style uint32, x, y, cx, cy int16, id uint16, class uint16, text string) {
		align4()
		put32(style)
		put32(0)
		put16(uint16(x))
		put16(uint16(y))
		put16(uint16(cx))
		put16(uint16(cy))
		put16(id)
		put16(0xFFFF)
		put16(class)
		putText(text)
		put16(0)
	}

	item(wsChild|wsVisible, 8, 7, 40, 9, 0xFFFF, atomStatic, "排除:")
	item(wsChild|wsVisible|wsTabStop|wsBorder|wsVScroll|esMultiline|esAutovscroll|esWantReturn,
		8, 17, 284, 124, idExEdit, atomEdit, "")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 8, 147, 52, 14, idExSample, atomButton, "填入示例")
	item(wsChild|wsVisible|wsTabStop|bsDefPushbutton, 176, 147, 52, 14, idDlgOk, atomButton, "确定")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 232, 147, 52, 14, idDlgCancel, atomButton, "取消")
	item(wsChild|wsVisible, 8, 166, 284, 8, 0xFFFF, atomStatic, `每行一条；\ 为分隔符；行首 \ 锚定根目录；行尾 \ 表示整个目录`)
	item(wsChild|wsVisible, 8, 176, 284, 8, 0xFFFF, atomStatic, "通配符：* 匹配不含分隔符的任意字符，? 匹配单个字符")
	align4()
	return b
}

func getDlgItem(hwnd syscall.Handle, id int) syscall.Handle {
	r, _, _ := pGetDlgItem.Call(uintptr(hwnd), uintptr(id))
	return syscall.Handle(r)
}

// centerOnOwner 把窗口移到主窗口中央。
func centerOnOwner(hwnd syscall.Handle) {
	var dr, mr rect
	pGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&dr)))
	pGetWindowRect.Call(uintptr(mainHwnd), uintptr(unsafe.Pointer(&mr)))
	w, h := int(dr.right-dr.left), int(dr.bottom-dr.top)
	x := int(mr.left) + (int(mr.right-mr.left)-w)/2
	y := int(mr.top) + (int(mr.bottom-mr.top)-h)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	pSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder)
}

// ---------- 保存配置命名对话框 ----------

// showSaveNameDialog 让用户确认配置名称，同时展示要保存的两个目录；取消不产生任何写入。
func showSaveNameDialog(name, src, dst string) (string, bool) {
	saveNameText, saveNameOK = name, false
	tpl := buildSaveNameTemplate(src, dst)
	ret, _, _ := pDialogBoxIndirectParamW.Call(
		uintptr(hInstance),
		uintptr(unsafe.Pointer(&tpl[0])),
		uintptr(mainHwnd),
		saveNameDlgCallback(),
		0)
	runtime.KeepAlive(tpl)
	if ret == ^uintptr(0) {
		msgBox("无法打开保存配置对话框。", mbOk|mbIconError)
		return name, false
	}
	return saveNameText, saveNameOK
}

var (
	saveNameCbOnce sync.Once
	saveNameCb     uintptr
)

// saveNameDlgCallback 复用同一个对话框过程回调，避免重复注册占用回调槽位。
func saveNameDlgCallback() uintptr {
	saveNameCbOnce.Do(func() { saveNameCb = syscall.NewCallback(saveNameDlgProc) })
	return saveNameCb
}

// saveNameDlgProc 命名对话框的窗口过程；名称预填并全选，直接输入即可替换。
func saveNameDlgProc(hwnd syscall.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmInitDialog:
		hSaveNameDlg = hwnd
		hSaveNameEdit = getDlgItem(hwnd, idNameEdit)
		setText(hSaveNameEdit, saveNameText)
		pSendMessageW.Call(uintptr(hSaveNameEdit), emSetLimitTxt, 120, 0)
		pSetFocus.Call(uintptr(hSaveNameEdit))
		// 对话框管理器在 WM_INITDIALOG 返回后才设置焦点，异步全选便于直接覆盖输入
		pPostMessageW.Call(uintptr(hSaveNameEdit), emSetSel, 0, ^uintptr(0))
		centerOnOwner(hwnd)
		return 1

	case wmCommand:
		switch int(wParam & 0xFFFF) {
		case idDlgOk:
			name := strings.TrimSpace(getText(hSaveNameEdit))
			if name == "" {
				msgBoxOwner(hwnd, "配置名称不能为空。", mbOk|mbIconError)
				return 1
			}
			saveNameText, saveNameOK = name, true
			pEndDialog.Call(uintptr(hwnd), 1)
		case idDlgCancel:
			pEndDialog.Call(uintptr(hwnd), 0)
		default:
			return 0
		}
		return 1

	case wmClose:
		pEndDialog.Call(uintptr(hwnd), 0)
		return 1

	case wmDestroy:
		hSaveNameDlg, hSaveNameEdit = 0, 0
	}
	return 0
}

// buildSaveNameTemplate 构造命名对话框的内存模板；目录行过长时尾部省略。
func buildSaveNameTemplate(src, dst string) []byte {
	var b []byte
	put16 := func(v uint16) { b = append(b, byte(v), byte(v>>8)) }
	put32 := func(v uint32) { b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	putText := func(s string) {
		u, err := syscall.UTF16FromString(s)
		if err != nil {
			u = []uint16{0}
		}
		for _, c := range u {
			b = append(b, byte(c), byte(c>>8))
		}
	}
	align4 := func() {
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}

	put32(dsSetFont | dsModalFrame | wsPopup | wsCaption | wsSysMenu)
	put32(0)
	put16(9)
	put16(0)
	put16(0)
	put16(260)
	put16(92)
	put16(0)
	put16(0)
	putText("保存配置")
	put16(9)
	putText("Segoe UI")

	item := func(style uint32, x, y, cx, cy int16, id uint16, class uint16, text string) {
		align4()
		put32(style)
		put32(0)
		put16(uint16(x))
		put16(uint16(y))
		put16(uint16(cx))
		put16(uint16(cy))
		put16(id)
		put16(0xFFFF)
		put16(class)
		putText(text)
		put16(0)
	}

	item(wsChild|wsVisible, 8, 10, 40, 9, 0xFFFF, atomStatic, "配置名称:")
	item(wsChild|wsVisible|wsTabStop|wsBorder|esAutohscroll, 52, 8, 200, 14, idNameEdit, atomEdit, "")
	item(wsChild|wsVisible, 8, 30, 24, 9, 0xFFFF, atomStatic, "输入:")
	item(wsChild|wsVisible|ssEndEllipsis|ssNoPrefix, 34, 30, 218, 9, 0xFFFF, atomStatic, src)
	item(wsChild|wsVisible, 8, 42, 24, 9, 0xFFFF, atomStatic, "输出:")
	item(wsChild|wsVisible|ssEndEllipsis|ssNoPrefix, 34, 42, 218, 9, 0xFFFF, atomStatic, dst)
	item(wsChild|wsVisible|ssNoPrefix, 8, 56, 244, 8, 0xFFFF, atomStatic, "同名配置若目录不同，确认后覆盖其目录；取消不保存任何内容")
	item(wsChild|wsVisible|wsTabStop|bsDefPushbutton, 144, 72, 52, 14, idDlgOk, atomButton, "保存")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 200, 72, 52, 14, idDlgCancel, atomButton, "取消")
	align4()
	return b
}

// ---------- 配置管理对话框 ----------

// configInitialSel 计算弹窗初始选中项：目录对命中配置时选中它；主界面已有目录但不对应
// 任何配置时返回草稿目录，让目录在表单里可见；都没有时退回生效配置或第一条。
func configInitialSel(cfg *state.Config, src, dst string) (sel, draftSrc, draftDst string) {
	if src != "" && dst != "" {
		if name := cfg.MatchProfile(src, dst); name != "" {
			return name, "", ""
		}
		return "", src, dst
	}
	if i, _ := cfg.FindProfile(cfg.ActiveProfile); i >= 0 {
		return cfg.ActiveProfile, "", ""
	}
	if len(cfg.Profiles) > 0 {
		return cfg.Profiles[0].Name, "", ""
	}
	return "", "", ""
}

// openConfigDialog 管理具名配置；在“应用并关闭”选中的配置会填入主界面两个目录框。
func openConfigDialog() {
	if running {
		return
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		msgBox("读取配置失败："+err.Error(), mbOk|mbIconError)
		return
	}
	if cfg == nil {
		cfg = &state.Config{}
	}
	changed := cfg.EnsureProfile()
	if seedExcludes(cfg) {
		changed = true
	}
	if changed {
		if err := state.SaveConfig(cfg); err != nil {
			msgBox("保存配置失败："+err.Error(), mbOk|mbIconError)
			return
		}
	}
	cfgDlgCfg = cfg
	src, dst := currentDirs()
	cfgDlgSel, cfgDraftSrc, cfgDraftDst = configInitialSel(cfg, src, dst)
	cfgDlgDirty = false
	cfgApplied = appliedProfile{}
	// 列表视图类必须在对话框创建前注册
	initCommonControls()
	shown := showConfigDialog()
	cfgDlgCfg = nil
	if shown && cfgApplied.ok {
		setText(hEditSrc, cfgApplied.src)
		setText(hEditDst, cfgApplied.dst)
		setText(hStatus, "已应用配置："+cfgApplied.name)
	}
	bringToFront(mainHwnd)
}

// showConfigDialog 用系统模态对话框管理配置；返回是否成功显示。
func showConfigDialog() bool {
	tpl := buildConfigTemplate()
	ret, _, _ := pDialogBoxIndirectParamW.Call(
		uintptr(hInstance),
		uintptr(unsafe.Pointer(&tpl[0])),
		uintptr(mainHwnd),
		cfgDlgCallback(),
		0)
	runtime.KeepAlive(tpl)
	if ret == ^uintptr(0) {
		msgBox("无法打开配置管理对话框。", mbOk|mbIconError)
		return false
	}
	return true
}

var (
	cfgDlgCbOnce sync.Once
	cfgDlgCb     uintptr
)

// cfgDlgCallback 复用同一个对话框过程回调，避免重复注册占用回调槽位。
func cfgDlgCallback() uintptr {
	cfgDlgCbOnce.Do(func() { cfgDlgCb = syscall.NewCallback(cfgDlgProc) })
	return cfgDlgCb
}

// cfgDlgProc 配置管理对话框的窗口过程；激活、焦点与 ESC 由系统模态循环处理。
func cfgDlgProc(hwnd syscall.Handle, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case wmInitDialog:
		hCfgDlg = hwnd
		hCfgList = getDlgItem(hwnd, idCfgList)
		hCfgName = getDlgItem(hwnd, idCfgName)
		hCfgSrc = getDlgItem(hwnd, idCfgSrc)
		hCfgDst = getDlgItem(hwnd, idCfgDst)
		hCfgEx = getDlgItem(hwnd, idCfgEx)
		hCfgBrowseSrc = getDlgItem(hwnd, idCfgBrowseSrc)
		hCfgBrowseDst = getDlgItem(hwnd, idCfgBrowseDst)
		if hCfgList == 0 {
			msgBoxOwner(hwnd, "列表控件不可用，只能编辑当前配置。", mbOk|mbIconWarning)
		}
		pSendMessageW.Call(uintptr(hCfgName), emSetLimitTxt, 260, 0)
		pSendMessageW.Call(uintptr(hCfgSrc), emSetLimitTxt, 1000, 0)
		pSendMessageW.Call(uintptr(hCfgDst), emSetLimitTxt, 1000, 0)
		pSendMessageW.Call(uintptr(hCfgEx), emSetLimitTxt, 100000, 0)
		cfgListInit()
		cfgRefreshList(cfgDlgSel)
		cfgFillForm(cfgDlgSel)
		centerOnOwner(hwnd)
		if hCfgList != 0 {
			pSetFocus.Call(uintptr(hCfgList))
		}
		return 1

	case wmNotify:
		if lParam == 0 || hCfgList == 0 {
			return 0
		}
		// 对话框过程只能把 lParam 声明为整型槽位，通知结构由系统投递
		n := (*nmhdr)(unsafe.Pointer(lParam))
		if n.hwndFrom == hCfgList && n.code == lvnSelChange {
			cfgSelectCurrent()
		}
		return 0

	case wmCommand:
		// 只接管本对话框的按钮，编辑框的 EN_CHANGE 等通知交回对话框管理器
		switch int(wParam & 0xFFFF) {
		case idCfgNew:
			cfgNewDraft()
		case idCfgSave:
			cfgSaveForm(hwnd, false)
		case idCfgDelete:
			cfgDeleteCurrent(hwnd)
		case idCfgApply:
			if cfgSaveForm(hwnd, true) {
				pEndDialog.Call(uintptr(hwnd), 1)
			}
		case idCfgSample:
			setText(hCfgEx, strings.Join(excludeSample, "\r\n"))
			pSetFocus.Call(uintptr(hCfgEx))
		case idCfgBrowseSrc:
			if d := browseFolderFor(hwnd, "选择输入目录"); d != "" {
				setText(hCfgSrc, d)
			}
			bringToFront(hwnd)
		case idCfgBrowseDst:
			if d := browseFolderFor(hwnd, "选择输出目录"); d != "" {
				setText(hCfgDst, d)
			}
			bringToFront(hwnd)
		case idDlgCancel:
			pEndDialog.Call(uintptr(hwnd), 0)
		default:
			return 0
		}
		return 1

	case wmClose:
		pEndDialog.Call(uintptr(hwnd), 0)
		return 1

	case wmDestroy:
		hCfgDlg, hCfgList = 0, 0
		hCfgName, hCfgSrc, hCfgDst, hCfgEx = 0, 0, 0, 0
		hCfgBrowseSrc, hCfgBrowseDst = 0, 0
	}
	return 0
}

// cfgListInit 建立列表列宽；列宽按控件实际像素宽度分配。
func cfgListInit() {
	if hCfgList == 0 {
		return
	}
	pSendMessageW.Call(uintptr(hCfgList), lvmSetExtendedStyle, lvsExFullRowSelect, lvsExFullRowSelect)
	var rc rect
	pGetClientRect.Call(uintptr(hCfgList), uintptr(unsafe.Pointer(&rc)))
	w := int(rc.right - rc.left)
	if w <= 0 {
		w = sz(560)
	}
	nameW := w * 22 / 100
	dirW := (w - nameW) / 2
	cfgAddColumn(0, nameW, "名称")
	cfgAddColumn(1, dirW, "输入目录")
	cfgAddColumn(2, w-nameW-dirW, "输出目录")
}

func cfgAddColumn(i, width int, title string) {
	p, _ := syscall.UTF16PtrFromString(title)
	col := lvColumn{
		mask:     lvcfText | lvcfWidth | lvcfSubItem,
		fmt:      lvcfmtLeft,
		cx:       int32(width),
		pszText:  p,
		iSubItem: int32(i),
	}
	pSendMessageW.Call(uintptr(hCfgList), lvmInsertColumnW, uintptr(i), uintptr(unsafe.Pointer(&col)))
	runtime.KeepAlive(p)
}

// cfgRefreshList 按配置顺序重建列表，并选中指定名称。
func cfgRefreshList(sel string) {
	if hCfgList == 0 {
		return
	}
	cfgListBusy = true
	pSendMessageW.Call(uintptr(hCfgList), lvmDeleteAllItems, 0, 0)
	for i, p := range cfgDlgCfg.Profiles {
		cfgAddRow(i, p)
	}
	if i, _ := cfgDlgCfg.FindProfile(sel); i >= 0 {
		cfgSelectRow(i)
	}
	cfgListBusy = false
}

func cfgAddRow(i int, p state.Profile) {
	t, _ := syscall.UTF16PtrFromString(p.Name)
	it := lvItem{mask: lvifText, iItem: int32(i), iSubItem: 0, pszText: t}
	pSendMessageW.Call(uintptr(hCfgList), lvmInsertItemW, 0, uintptr(unsafe.Pointer(&it)))
	runtime.KeepAlive(t)
	cfgSetSubItem(i, 1, p.LocalDir)
	cfgSetSubItem(i, 2, p.CarrierDir)
}

func cfgSetSubItem(row, col int, text string) {
	t, _ := syscall.UTF16PtrFromString(text)
	it := lvItem{
		mask:       lvifText,
		iItem:      int32(row),
		iSubItem:   int32(col),
		pszText:    t,
		cchTextMax: int32(len(text) + 1),
	}
	pSendMessageW.Call(uintptr(hCfgList), lvmSetItemTextW, uintptr(row), uintptr(unsafe.Pointer(&it)))
	runtime.KeepAlive(t)
}

// cfgSelectRow 选中指定行并滚动到可见位置；索引 -1 表示清空选择。
func cfgSelectRow(i int) {
	clear := lvItem{stateMask: lvisSelected | lvisFocused}
	pSendMessageW.Call(uintptr(hCfgList), lvmSetItemState, ^uintptr(0), uintptr(unsafe.Pointer(&clear)))
	if i < 0 {
		return
	}
	it := lvItem{state: lvisSelected | lvisFocused, stateMask: lvisSelected | lvisFocused}
	pSendMessageW.Call(uintptr(hCfgList), lvmSetItemState, uintptr(i), uintptr(unsafe.Pointer(&it)))
	pSendMessageW.Call(uintptr(hCfgList), lvmEnsureVisible, uintptr(i), 0)
}

// cfgSelectCurrent 列表选中项变化时切换表单内容；程序内刷新列表期间忽略。
func cfgSelectCurrent() {
	if cfgListBusy || cfgDlgCfg == nil {
		return
	}
	r, _, _ := pSendMessageW.Call(uintptr(hCfgList), lvmGetNextItem, ^uintptr(0), lvniSelected)
	i := int(r)
	if i < 0 || i >= len(cfgDlgCfg.Profiles) {
		return
	}
	name := cfgDlgCfg.Profiles[i].Name
	if !cfgDlgDirty && strings.EqualFold(name, cfgDlgSel) {
		return
	}
	cfgDlgSel = name
	cfgDlgDirty = false
	cfgFillForm(name)
}

// cfgFillForm 用指定配置填充表单；名称不存在时按新建草稿处理，首次打开可带出主界面目录。
func cfgFillForm(name string) {
	_, p := cfgDlgCfg.FindProfile(name)
	if p == nil {
		src, dst := cfgDraftSrc, cfgDraftDst
		cfgDraftSrc, cfgDraftDst = "", ""
		ex := cfgDlgCfg.ExcludesFor(src, dst)
		if ex == nil {
			ex = excludeSample
		}
		setText(hCfgName, cfgDlgCfg.NextProfileName())
		setText(hCfgSrc, src)
		setText(hCfgDst, dst)
		setText(hCfgEx, strings.Join(ex, "\r\n"))
		return
	}
	setText(hCfgName, p.Name)
	setText(hCfgSrc, p.LocalDir)
	setText(hCfgDst, p.CarrierDir)
	setText(hCfgEx, strings.Join(cfgDlgCfg.ExcludesFor(p.LocalDir, p.CarrierDir), "\r\n"))
}

// cfgNewDraft 清空表单进入新建状态，列表取消选中。
func cfgNewDraft() {
	cfgDlgSel = ""
	cfgDlgDirty = true
	cfgDraftSrc, cfgDraftDst = "", ""
	if hCfgList != 0 {
		cfgListBusy = true
		cfgSelectRow(-1)
		cfgListBusy = false
	}
	cfgFillForm("")
	pSetFocus.Call(uintptr(hCfgName))
	pPostMessageW.Call(uintptr(hCfgName), emSetSel, 0, ^uintptr(0))
}

// cfgSaveForm 校验表单并落盘；applying 为真时把该配置设为当前生效配置。
func cfgSaveForm(owner syscall.Handle, applying bool) bool {
	name := strings.TrimSpace(getText(hCfgName))
	src := strings.TrimSpace(getText(hCfgSrc))
	dst := strings.TrimSpace(getText(hCfgDst))
	exText := getText(hCfgEx)

	if name == "" {
		msgBoxOwner(owner, "配置名称不能为空。", mbOk|mbIconError)
		return false
	}
	if i, _ := cfgDlgCfg.FindProfile(name); i >= 0 && !strings.EqualFold(name, cfgDlgSel) {
		msgBoxOwner(owner, "已有同名配置：“"+name+"”，请换一个名称。", mbOk|mbIconError)
		return false
	}
	if src == "" || dst == "" {
		msgBoxOwner(owner, "请填写输入目录和输出目录。", mbOk|mbIconError)
		return false
	}
	if state.SameDir(src, dst) {
		msgBoxOwner(owner, "输入目录和输出目录不能是同一个。", mbOk|mbIconError)
		return false
	}
	if err := validateExcludeText(exText); err != nil {
		msgBoxOwner(owner, err.Error(), mbOk|mbIconError)
		return false
	}

	oldName := cfgDlgSel
	oldSrc, oldDst := "", ""
	if _, old := cfgDlgCfg.FindProfile(oldName); old != nil {
		oldSrc, oldDst = old.LocalDir, old.CarrierDir
	}
	cfgDlgCfg.SetProfile(oldName, state.Profile{Name: name, LocalDir: src, CarrierDir: dst})
	cfgDlgCfg.MigrateExcludes(oldSrc, oldDst, src, dst, name)
	cfgDlgCfg.SetExcludes(src, dst, splitExcludeLines(exText))
	if applying {
		cfgDlgCfg.LocalDir, cfgDlgCfg.CarrierDir, cfgDlgCfg.ActiveProfile = src, dst, name
	}
	if err := state.SaveConfig(cfgDlgCfg); err != nil {
		msgBoxOwner(owner, "保存失败："+err.Error(), mbOk|mbIconError)
		return false
	}
	cfgDlgSel = name
	cfgDlgDirty = false
	if applying {
		cfgApplied = appliedProfile{name: name, src: src, dst: dst, ok: true}
	}
	cfgRefreshList(name)
	cfgFillForm(name)
	setText(hStatus, "已保存配置："+name)
	return true
}

// cfgDeleteCurrent 删除列表选中的配置；生效项被删时只清空标记，不改主界面目录。
func cfgDeleteCurrent(owner syscall.Handle) {
	name := cfgDlgSel
	if name == "" {
		msgBoxOwner(owner, "请先在列表中选中要删除的配置。", mbOk|mbIconError)
		return
	}
	if msgBoxOwner(owner, "确定删除配置“"+name+"”？\r\n\r\n主界面上的目录不会改变。", mbYesNo|mbIconWarning) != idYes {
		return
	}
	cfgDlgCfg.DeleteProfile(name)
	if err := state.SaveConfig(cfgDlgCfg); err != nil {
		msgBoxOwner(owner, "保存失败："+err.Error(), mbOk|mbIconError)
		bringToFront(owner)
		return
	}
	setText(hStatus, "已删除配置："+name)
	cfgDlgSel = ""
	if len(cfgDlgCfg.Profiles) > 0 {
		cfgDlgSel = cfgDlgCfg.Profiles[0].Name
	}
	cfgDlgDirty = false
	cfgRefreshList(cfgDlgSel)
	cfgFillForm(cfgDlgSel)
	bringToFront(owner)
}

// buildConfigTemplate 构造配置管理对话框的内存模板（DLGTEMPLATE），
// 列表为 SysListView32，其余控件与排除列表对话框同一套系统样式。
func buildConfigTemplate() []byte {
	var b []byte
	put16 := func(v uint16) { b = append(b, byte(v), byte(v>>8)) }
	put32 := func(v uint32) { b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	putText := func(s string) {
		u, err := syscall.UTF16FromString(s)
		if err != nil {
			u = []uint16{0}
		}
		for _, c := range u {
			b = append(b, byte(c), byte(c>>8))
		}
	}
	align4 := func() {
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}

	put32(dsSetFont | dsModalFrame | wsPopup | wsCaption | wsSysMenu)
	put32(0)
	put16(19)
	put16(0)
	put16(0)
	put16(400)
	put16(306)
	put16(0)
	put16(0)
	putText("配置管理")
	put16(9)
	putText("Segoe UI")

	// 系统控件用原子类名；列表视图用类名字符串
	item := func(style uint32, x, y, cx, cy int16, id uint16, class uint16, text string) {
		align4()
		put32(style)
		put32(0)
		put16(uint16(x))
		put16(uint16(y))
		put16(uint16(cx))
		put16(uint16(cy))
		put16(id)
		put16(0xFFFF)
		put16(class)
		putText(text)
		put16(0)
	}
	itemClass := func(style uint32, x, y, cx, cy int16, id uint16, class, text string) {
		align4()
		put32(style)
		put32(0)
		put16(uint16(x))
		put16(uint16(y))
		put16(uint16(cx))
		put16(uint16(cy))
		put16(id)
		putText(class)
		putText(text)
		put16(0)
	}

	itemClass(wsChild|wsVisible|wsTabStop|wsBorder|lvsReport|lvsSingleSel|lvsShowSelAlways,
		8, 6, 384, 104, idCfgList, "SysListView32", "")

	item(wsChild|wsVisible, 8, 116, 28, 9, 0xFFFF, atomStatic, "名称:")
	item(wsChild|wsVisible|wsTabStop|wsBorder|esAutohscroll, 40, 114, 168, 14, idCfgName, atomEdit, "")

	item(wsChild|wsVisible, 8, 136, 34, 9, 0xFFFF, atomStatic, "输入目录:")
	item(wsChild|wsVisible|wsTabStop|wsBorder|esAutohscroll, 46, 134, 298, 14, idCfgSrc, atomEdit, "")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 348, 134, 44, 14, idCfgBrowseSrc, atomButton, "浏览…")

	item(wsChild|wsVisible, 8, 156, 34, 9, 0xFFFF, atomStatic, "输出目录:")
	item(wsChild|wsVisible|wsTabStop|wsBorder|esAutohscroll, 46, 154, 298, 14, idCfgDst, atomEdit, "")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 348, 154, 44, 14, idCfgBrowseDst, atomButton, "浏览…")

	item(wsChild|wsVisible, 8, 176, 40, 9, 0xFFFF, atomStatic, "排除列表:")
	item(wsChild|wsVisible|wsTabStop|wsBorder|wsVScroll|esMultiline|esAutovscroll|esWantReturn,
		8, 186, 384, 56, idCfgEx, atomEdit, "")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 8, 246, 56, 14, idCfgSample, atomButton, "填入示例")
	item(wsChild|wsVisible, 8, 264, 384, 8, 0xFFFF, atomStatic, `每行一条；\ 为分隔符；行首 \ 锚定根目录；行尾 \ 表示整个目录`)
	item(wsChild|wsVisible, 8, 274, 384, 8, 0xFFFF, atomStatic, "通配符：* 匹配不含分隔符的任意字符，? 匹配单个字符；同一对目录的规则在多个配置间共用")

	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 8, 288, 52, 15, idCfgNew, atomButton, "新建")
	item(wsChild|wsVisible|wsTabStop|bsDefPushbutton, 64, 288, 52, 15, idCfgSave, atomButton, "保存")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 120, 288, 52, 15, idCfgDelete, atomButton, "删除")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 240, 288, 76, 15, idCfgApply, atomButton, "应用并关闭")
	item(wsChild|wsVisible|wsTabStop|bsPushbutton, 324, 288, 52, 15, idDlgCancel, atomButton, "关闭")
	align4()
	return b
}

func flushUI() {
	mu.Lock()
	lines := logQueue
	logQueue = nil
	cur, total, known := progCur, progTotal, progKnown
	fin, code := finished, exitCode
	wasCancelled := cancelled
	mu.Unlock()

	appendLogText(lines)

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
					setText(hStatus, fmt.Sprintf("测试比对：需改动 %s 个文件、删除 %s 个；读取 %s，预计需写入 %s（未改动数据）", m[3], m[4], m[5], m[6]))
				} else {
					setText(hStatus, fmt.Sprintf("完成：改动 %s 个文件、删除 %s 个；读取 %s，写入 %s", m[3], m[4], m[5], m[6]))
				}
			} else if strings.Contains(joined, "未改动任何数据") {
				setText(hStatus, "测试比对：没有需要搬运的内容")
			} else if strings.Contains(joined, "已是最新") {
				setText(hStatus, "已是最新，没有需要搬运的内容")
			} else {
				setText(hStatus, "完成")
			}
		} else {
			if hint := massDeleteHint(joined); hint != "" && !lastMass {
				body := "本次要删除的文件数超过安全上限，同步已中止，未写入任何文件。\r\n\r\n" +
					hint + "\r\n\r\n" +
					"请先确认输入、输出目录没有填反。确认后将继续执行删除，继续吗？"
				if msgBox(body, mbYesNo|mbIconWarning) == idYes {
					startRun(lastDry, lastVerify, lastFull, lastForce, true)
					return
				}
			}
			if !lastDry && !lastForce && (lockExists(lastSrc) || lockExists(lastDst)) {
				var body strings.Builder
				body.WriteString("同步失败，且目录锁仍然存在：\r\n\r\n")
				for _, d := range []string{lastSrc, lastDst} {
					if lockExists(d) {
						body.WriteString(filepath.Join(state.Dir(d), "lock"))
						body.WriteString("\r\n")
					}
				}
				body.WriteString("\r\n可能是上次异常退出遗留，也可能另一个同步正在运行。\r\n强制忽略锁并重试一次吗？")
				if msgBox(body.String(), mbYesNo|mbIconWarning) == idYes {
					startRun(lastDry, lastVerify, lastFull, true, lastMass)
					return
				}
			}
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
	pSendMessageW.Call(uintptr(hChkVerify), bmSetCheck, 1, 0) // 复读校验默认勾选

	hBtnDry = create("BUTTON", "测试比对（只读）", wsChild|wsVisible|wsTabStop|bsPushbutton, 96, 116, 140, 30, idDry)
	hBtnSync = create("BUTTON", "开始同步", wsChild|wsVisible|wsTabStop|bsPushbutton, 246, 116, 140, 30, idSync)
	hBtnCancel = create("BUTTON", "取消", wsChild|wsVisible|wsTabStop|bsPushbutton, 396, 116, 76, 30, idCancel)
	hBtnCfgMgr = create("BUTTON", "配置管理…", wsChild|wsVisible|wsTabStop|bsPushbutton, 592, 116, 120, 30, idCfgMgr)

	hProgress = create("msctls_progress32", "", wsChild|wsVisible|pbsSmooth|pbsMarquee, 96, 156, 616, 16, idProg)
	pSendMessageW.Call(uintptr(hProgress), pbmSetRange32, 0, 100)

	label("日志", 16, 186, 60)
	hLog = create("EDIT", "",
		wsChild|wsVisible|wsBorder|wsVScroll|esMultiline|esAutovscroll|esReadonly,
		16, 208, 696, 292, idLog)
	pSendMessageW.Call(uintptr(hLog), emSetLimitTxt, 2000000, 0)

	create("STATIC", "提示：同步只写输出目录，不会改动输入目录；同步前请先关闭正在读写这些文件的程序。",
		wsChild|wsVisible, 16, 510, 700, 20, 0)

	// 状态栏贴住窗口底边：先画边框条，再画文字，避免文字背景盖住边框
	create("STATIC", "", wsChild|wsVisible|ssSunken, 0, 544, 728, 22, 0)
	hStatus = create("STATIC", "准备就绪", wsChild|wsVisible|ssCenterImage, 8, 545, 712, 20, idStatus)

	enable(hBtnCancel, false)

	cfg, _ := state.LoadConfig()
	if cfg != nil {
		// 旧版配置补一条“默认”；尚无排除规则的目录对填入示例规则
		changed := cfg.EnsureProfile()
		if seedExcludes(cfg) {
			changed = true
		}
		if changed {
			_ = state.SaveConfig(cfg)
		}
		// 回填当前生效目录；命令行 setup 改过目录时这里也能看到最新值
		if cfg.LocalDir != "" {
			setText(hEditSrc, cfg.LocalDir)
		}
		if cfg.CarrierDir != "" {
			setText(hEditDst, cfg.CarrierDir)
		}
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
			bringToFront(mainHwnd)
		case idBDst:
			if d := browseFolder("选择输出目录"); d != "" {
				setText(hEditDst, d)
			}
			bringToFront(mainHwnd)
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
			startRun(wParam&0xFFFF == idDry, verify, full, false, false)
		case idCancel:
			if running && proc != nil && proc.Process != nil {
				if msgBox("正在同步，确定要中止吗？\r\n\r\n已写入的部分不会回滚，重新运行会接着补齐。",
					mbYesNo|mbIconError) == idYes {
					_ = proc.Process.Kill()
					setText(hStatus, "正在中止…")
				}
			}
		case idSave:
			src, dst := currentDirs()
			if src == "" || dst == "" {
				msgBox("请先选择输入目录和输出目录。", mbOk|mbIconError)
				return 0
			}
			if state.SameDir(src, dst) {
				msgBox("输入目录和输出目录不能是同一个。", mbOk|mbIconError)
				return 0
			}
			cfg, err := state.LoadConfig()
			if err != nil {
				msgBox("读取配置失败："+err.Error(), mbOk|mbIconError)
				return 0
			}
			if cfg == nil {
				cfg = &state.Config{}
			}
			// 每次点击都重新取当前目录与建议名称：同目录对的配置用其名称，否则用下一个空闲序号
			suggest := cfg.MatchProfile(src, dst)
			if suggest == "" {
				suggest = cfg.NextProfileName()
			}
			name, ok := showSaveNameDialog(suggest, src, dst)
			bringToFront(mainHwnd)
			if !ok {
				return 0
			}
			// 同名即确认覆盖：把该配置的排除规则搬到新目录对，再写入当前目录
			if _, old := cfg.FindProfile(name); old != nil {
				body := fmt.Sprintf("配置“%s”已存在：\r\n\r\n旧：%s → %s\r\n新：%s → %s\r\n\r\n要用当前目录覆盖它吗？",
					old.Name, old.LocalDir, old.CarrierDir, src, dst)
				if msgBox(body, mbYesNo|mbIconWarning) != idYes {
					bringToFront(mainHwnd)
					return 0
				}
				cfg.MigrateExcludes(old.LocalDir, old.CarrierDir, src, dst, old.Name)
			}
			cfg.SetProfile("", state.Profile{Name: name, LocalDir: src, CarrierDir: dst})
			cfg.LocalDir, cfg.CarrierDir = src, dst
			cfg.ActiveProfile = name
			// 新目录对没有排除规则时写入示例规则
			seedExcludes(cfg)
			if err := state.SaveConfig(cfg); err != nil {
				msgBox("保存失败："+err.Error(), mbOk|mbIconError)
			} else {
				msgBox("已保存为配置“"+name+"”，下次打开会自动填好。", mbOk|mbIconInfo)
			}
			bringToFront(mainHwnd)
		case idMenuExc:
			openExcludeDialog()
		case idCfgMgr:
			openConfigDialog()
		case idOpen:
			openFolder(strings.TrimSpace(getText(hEditDst)))
		case idOpenSrc:
			openFolder(strings.TrimSpace(getText(hEditSrc)))
		}
		return 0

	case wmTimer:
		flushUI()
		return 0

	case wmBringFront:
		// 由后台线程在确认框关闭后投递，前台激活只能在界面线程做
		bringToFront(mainHwnd)
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
