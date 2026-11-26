package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
)

const timeChipGapMs = 10 * 60 * 1000 // 10 minutes between timeline time separator pills

// App renders the Scheme C · Zen Popover TailChat native GPU interface using mygo/ui.
type App struct {
	state *State
	win   *mygo.Window

	// Main view interactive fields (accessed on UI thread).
	composer           string
	composerRef        mygoui.Handle
	searchOpen         bool
	focusSearchPending bool
	searchInput        string
	searchRef          mygoui.Handle
	concealedCopy      bool

	// Lightweight Devices popover state (Option A · Inline Accordion).
	devicesPopoverOpen      bool
	suppressStatusPillClick bool
	devicesErr              string
	expandedDeviceID   string
	deviceDirectInput  string
	deviceDirectErr    string
	deviceDirectSaving bool

	// Dedicated 2-tab QR-first Pairing modal state.
	pairOpen        bool
	pairTab         int // 0 = 扫码配对, 1 = 输入配对码加入
	pairCode        string
	pairToken       *model.PairingToken
	pairQRCode      string
	pairQRBitmap    *mygoui.Bitmap
	pairCopiedUntil time.Time
	joinCodeInput   string
	pairErr         string
	joining         bool
	pairLoading     bool
	pairGenSeq      uint64

	// Settings & About modal + Clear History confirmation state.
	settingsOpen       bool
	settingsTab        int // 0 = General, 1 = Transfer, 2 = Network, 3 = About
	settingsDraft      SettingsDraft
	settingsErr        string
	settingsSaving     bool
	clearConfirmOpen   bool
	pendingDirMu       sync.Mutex
	pendingDownloadDir *string

	// Pending results from background UI operations (applied safely on UI thread).
	asyncMu               sync.Mutex
	pendingJoinDone       bool
	pendingJoinErr        string
	pendingDevicesErr     *string
	pendingPeerDirectDone bool
	pendingPeerDirectDev  string
	pendingPeerDirectErr  string
	pendingSettingsDone   bool
	pendingSettingsErr    string
	pendingSettingsClose  bool
	pendingPair           *pairGenResult

	// Lazily decoded embedded TailChat app icon for About tab.
	appIconOnce sync.Once
	appIconSrc  mygoui.ImageSource

	// Media preview cache, inline audio player, lightbox, and update modal state.
	media           *MediaCache
	audio           *AudioController
	lightboxOpen    bool
	lightboxMsgID   string
	lightboxFile    model.FileBlob
	updateModalOpen bool

	// Custom header window controls & split titlebar mode (used on Windows/Linux).
	windowButtonsMu sync.RWMutex
	windowButtons   mygo.WindowButtons
	splitTitleBar   bool

	// Light/dark appearance state (systemDark updated by desktop host; live preview while Settings is open).
	appearanceMu        sync.RWMutex
	systemDark          bool
	lastThemePref       string
	lastThemeDark       bool
	hasAppliedTheme     bool
	onAppearanceChanged func(pref string, dark bool)

	// Platform integration hooks (overridable in headless tests).
	runAsync           func(fn func())
	winMinimize        func()
	winToggleMaximize  func()
	winIsMaximized     func() bool
	winClose           func()
	openFileDialog     func() ([]string, error)
	chooseDirDialog    func(defaultDir string) (string, error)
	readClipboardImage func() []byte
	readClipboardFiles func() ([]string, error)
	readClipboardText  func() string
	writeClipboardText func(text string, concealed bool) error
	openLocalPath      func(path string) error
	showInFolder       func(path string) error
	openExternalURL    func(url string) error
}

// NewApp constructs a TailChat UI controller bound to state.
func NewApp(state *State) *App {
	a := &App{
		state:    state,
		media:    NewMediaCache(nil),
		audio:    NewAudioController(nil),
		runAsync: func(fn func()) { go fn() },
	}
	a.openFileDialog = func() ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:   a.win,
			Title:    tr(a.state.Locale()).SelectFilesTitle,
			Multiple: true,
		})
	}
	a.chooseDirDialog = func(defaultDir string) (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:            a.win,
			Title:             tr(a.state.Locale()).ChooseDirTitle,
			DefaultPath:       strings.TrimSpace(defaultDir),
			Directory:         true,
			CreateDirectories: true,
		})
		if err != nil {
			return "", err
		}
		if len(paths) == 0 {
			return "", nil
		}
		return paths[0], nil
	}
	a.readClipboardImage = func() []byte {
		return mygo.Clipboard.ReadImage()
	}
	a.readClipboardFiles = func() ([]string, error) {
		return mygo.Clipboard.ReadFiles()
	}
	a.readClipboardText = func() string {
		return mygo.Clipboard.ReadText()
	}
	a.writeClipboardText = func(text string, concealed bool) error {
		if concealed {
			return mygo.Clipboard.Write(transfer.TextData(text), mygo.ClipboardOptions{Concealed: true})
		}
		mygo.Clipboard.WriteText(text)
		return nil
	}
	a.openLocalPath = func(path string) error {
		return mygo.Shell.OpenPath(path)
	}
	a.showInFolder = func(path string) error {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("empty file path")
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
		mygo.Shell.ShowItemInFolder(path)
		return nil
	}
	a.openExternalURL = func(url string) error {
		return mygo.Shell.OpenExternal(url)
	}
	return a
}

// SetWindow attaches the host desktop window to this App.
func (a *App) SetWindow(w *mygo.Window) {
	a.win = w
	if w != nil {
		a.media.SetInvalidate(w.Invalidate)
		a.audio.SetInvalidate(w.Invalidate)
		a.winMinimize = w.Minimize
		a.winToggleMaximize = w.ToggleMaximize
		a.winIsMaximized = w.IsMaximized
		a.winClose = w.Close
	}
}

// SetCustomWindowButtons configures custom header window control buttons (used on Linux).
func (a *App) SetCustomWindowButtons(wb mygo.WindowButtons) {
	a.windowButtonsMu.Lock()
	a.windowButtons = mygo.WindowButtons{
		Start: append([]string(nil), wb.Start...),
		End:   append([]string(nil), wb.End...),
	}
	a.windowButtonsMu.Unlock()
	a.invalidate()
}

func (a *App) customWindowButtons() mygo.WindowButtons {
	a.windowButtonsMu.RLock()
	wb := a.windowButtons
	a.windowButtonsMu.RUnlock()
	return wb
}

// SetSplitTitleBar toggles the two-tier header layout (32px dedicated window bar + 48px app toolbar)
// used on Windows and Linux so window controls never misalign with or crowd the application toolbar.
func (a *App) SetSplitTitleBar(enabled bool) {
	a.windowButtonsMu.Lock()
	changed := a.splitTitleBar != enabled
	a.splitTitleBar = enabled
	a.windowButtonsMu.Unlock()
	if changed {
		a.invalidate()
	}
}

func (a *App) isSplitTitleBar() bool {
	a.windowButtonsMu.RLock()
	v := a.splitTitleBar
	a.windowButtonsMu.RUnlock()
	return v
}

// SetSystemDark updates the OS dark mode flag used when theme preference is "auto".
func (a *App) SetSystemDark(dark bool) {
	a.appearanceMu.Lock()
	changed := a.systemDark != dark
	a.systemDark = dark
	a.appearanceMu.Unlock()
	if changed {
		a.invalidate()
	}
}

// SystemDark reports the last observed OS dark mode state.
func (a *App) SystemDark() bool {
	a.appearanceMu.RLock()
	dark := a.systemDark
	a.appearanceMu.RUnlock()
	return dark
}

// SetOnAppearanceChanged registers a callback invoked whenever effective theme preference or dark state changes.
func (a *App) SetOnAppearanceChanged(fn func(pref string, dark bool)) {
	a.appearanceMu.Lock()
	a.onAppearanceChanged = fn
	a.appearanceMu.Unlock()
}

func (a *App) effectiveTheme(snap ViewSnapshot) (pref string, dark bool) {
	pref = model.NormalizeTheme(snap.Config.Theme)
	if a.settingsOpen {
		pref = a.settingsDraft.SelectedTheme()
	}
	return pref, ResolveDarkMode(pref, a.SystemDark())
}

// Close stops active inline media playback and releases cached GPU/decoder resources.
func (a *App) Close() {
	if a.audio != nil {
		a.audio.Stop()
	}
	if a.media != nil {
		a.media.Close()
	}
}

type pairGenResult struct {
	seq       uint64
	code      string
	tok       *model.PairingToken
	bmp       *mygoui.Bitmap
	errText   string
	showToast bool
}

func (a *App) invalidate() {
	if a.win != nil {
		a.win.Invalidate()
		return
	}
	if a.state != nil {
		a.state.notifyInvalidate()
	}
}

// drainPendingAsync applies results staged by background goroutines onto UI-thread fields.
func (a *App) drainPendingAsync() {
	a.asyncMu.Lock()
	joinDone := a.pendingJoinDone
	joinErr := a.pendingJoinErr
	if joinDone {
		a.pendingJoinDone = false
		a.pendingJoinErr = ""
	}
	devErr := a.pendingDevicesErr
	a.pendingDevicesErr = nil
	peerDirectDone := a.pendingPeerDirectDone
	peerDirectDev := a.pendingPeerDirectDev
	peerDirectErr := a.pendingPeerDirectErr
	if peerDirectDone {
		a.pendingPeerDirectDone = false
		a.pendingPeerDirectDev = ""
		a.pendingPeerDirectErr = ""
	}
	settingsDone := a.pendingSettingsDone
	settingsErr := a.pendingSettingsErr
	settingsClose := a.pendingSettingsClose
	if settingsDone {
		a.pendingSettingsDone = false
		a.pendingSettingsErr = ""
		a.pendingSettingsClose = false
	}
	pairRes := a.pendingPair
	a.pendingPair = nil
	a.asyncMu.Unlock()

	if joinDone {
		a.joining = false
		if joinErr != "" {
			a.pairErr = joinErr
		} else {
			a.pairErr = ""
			a.joinCodeInput = ""
			a.pairOpen = false
		}
	}

	if devErr != nil {
		a.devicesErr = *devErr
	}

	if peerDirectDone {
		a.deviceDirectSaving = false
		if peerDirectErr != "" {
			a.deviceDirectErr = peerDirectErr
		} else {
			a.deviceDirectErr = ""
			if a.expandedDeviceID == peerDirectDev {
				a.expandedDeviceID = ""
				a.deviceDirectInput = ""
			}
		}
	}

	if settingsDone {
		a.settingsSaving = false
		if settingsErr != "" {
			a.settingsErr = settingsErr
		} else {
			a.settingsErr = ""
			if settingsClose {
				a.settingsOpen = false
			}
		}
	}

	if pairRes != nil && pairRes.seq == a.pairGenSeq {
		a.pairLoading = false
		if pairRes.errText != "" {
			a.pairErr = pairRes.errText
			a.pairQRCode = ""
			a.pairQRBitmap = nil
		} else {
			a.pairErr = ""
			a.pairCode = pairRes.code
			a.pairToken = pairRes.tok
			a.pairQRCode = pairRes.code
			a.pairQRBitmap = pairRes.bmp
			a.pairCopiedUntil = time.Time{}
			if pairRes.showToast {
				a.state.SetToast(tr(a.state.Locale()).RefreshedQRToast, false)
			}
		}
	}
}

// OpenPairingModal opens the dedicated 2-tab QR-first Pairing modal (also usable from tray menu and Devices popover).
func (a *App) OpenPairingModal() {
	a.openPairingFlow(0)
}

func (a *App) openPairingFlow(tab int) {
	a.pairErr = ""
	a.devicesErr = ""
	a.devicesPopoverOpen = false
	a.settingsOpen = false
	a.clearConfirmOpen = false
	a.pairTab = tab
	a.pairOpen = true
	if tab == 0 {
		a.ensurePairingCode()
	}
	a.invalidate()
}

func (a *App) isPairingCodeExpired() bool {
	if a.pairCode == "" {
		return true
	}
	if a.pairToken != nil && a.pairToken.ExpiresAt > 0 {
		return time.Until(time.UnixMilli(a.pairToken.ExpiresAt)) <= 0
	}
	return false
}

func (a *App) clearPairingCode() {
	a.pairCode = ""
	a.pairToken = nil
	a.pairQRCode = ""
	a.pairQRBitmap = nil
	a.pairCopiedUntil = time.Time{}
}

func (a *App) ensurePairingCode() {
	if a.pairLoading {
		return
	}
	if a.pairCode == "" || a.isPairingCodeExpired() {
		a.startPairingCodeGeneration(false, false)
		return
	}
	_ = a.pairingQRBitmap()
}

func (a *App) generatePairingCode(showToast bool) {
	a.startPairingCodeGeneration(false, showToast)
}

func (a *App) refreshPairingCode() {
	cancelFirst := a.pairCode != ""
	a.clearPairingCode()
	a.startPairingCodeGeneration(cancelFirst, true)
}

func (a *App) startPairingCodeGeneration(cancelFirst, showToast bool) {
	a.pairGenSeq++
	seq := a.pairGenSeq
	a.pairLoading = true
	a.pairErr = ""

	a.dispatchAsync(func() {
		if cancelFirst {
			_ = a.state.CancelPairing()
		}
		code, tok, err := a.state.CreatePairingCode(context.Background(), 300)
		res := &pairGenResult{
			seq:       seq,
			showToast: showToast,
		}
		if err != nil {
			res.errText = err.Error()
		} else {
			tokCopy := tok
			res.code = code
			res.tok = &tokCopy
			if bmp, qrErr := renderPairingQRBitmap(code, defaultPairingQRSize); qrErr != nil {
				res.errText = qrErr.Error()
			} else {
				res.bmp = bmp
			}
		}
		a.asyncMu.Lock()
		a.pendingPair = res
		a.asyncMu.Unlock()
		a.invalidate()
	})
	a.drainPendingAsync()
}

func (a *App) pairingQRBitmap() *mygoui.Bitmap {
	if a.pairCode == "" {
		a.pairQRCode = ""
		a.pairQRBitmap = nil
		return nil
	}
	if a.pairQRBitmap != nil && a.pairQRCode == a.pairCode {
		return a.pairQRBitmap
	}
	bmp, err := renderPairingQRBitmap(a.pairCode, defaultPairingQRSize)
	if err != nil {
		a.pairErr = err.Error()
		return nil
	}
	a.pairQRCode = a.pairCode
	a.pairQRBitmap = bmp
	return bmp
}

// View builds the full Zen Popover native UI tree for each frame.
func (a *App) View(c *mygoui.Context) {
	if a.win == nil {
		svc := c.Services()
		a.state.SetInvalidate(svc.Invalidate)
		a.media.SetInvalidate(svc.Invalidate)
		a.audio.SetInvalidate(svc.Invalidate)
	}

	a.drainPendingAsync()

	snap := a.state.Snapshot()
	themePref, dark := a.effectiveTheme(snap)
	c.SetTheme(zenTheme(dark))
	p := zenPal(c)

	a.appearanceMu.Lock()
	var notifyAppearance func(string, bool)
	if !a.hasAppliedTheme || a.lastThemePref != themePref || a.lastThemeDark != dark {
		a.hasAppliedTheme = true
		a.lastThemePref = themePref
		a.lastThemeDark = dark
		notifyAppearance = a.onAppearanceChanged
	}
	a.appearanceMu.Unlock()
	if notifyAppearance != nil {
		notifyAppearance(themePref, dark)
	}

	// Global window shortcuts.
	c.OnShortcut(mygoui.Cmd, mygoui.KeyF, func() {
		a.devicesPopoverOpen = false
		a.searchOpen = true
		a.focusSearchPending = true
		a.searchRef.Focus()
	})
	if c.Shortcut(0, mygoui.KeyEscape) && (a.searchOpen || a.searchInput != "") && !a.anyOverlayOpen() {
		a.searchOpen = false
		if a.searchInput != "" {
			a.searchInput = ""
			a.setSearchQuery("")
		}
	}
	if c.Shortcut(mygoui.Cmd, mygoui.KeyEnter) && strings.TrimSpace(a.composer) != "" && !a.anyOverlayOpen() {
		a.submitComposer()
	}

	root := mygoui.Column(c).Fill().Background(p.Bg)

	// Handle direct OS file drops and native transfer drops onto the chat window.
	if dropped := root.DroppedFiles(); len(dropped) > 0 {
		a.sendFiles(dropped)
	}
	if drop, ok := mygoui.DropData(root, transfer.DropOptions{
		Formats:    []transfer.Format{transfer.FileList, transfer.URIList, transfer.Text},
		Operations: transfer.Copy,
	}); ok {
		a.handleDrop(drop)
	}

	dragOver := root.FileDragOver()
	if dragOver {
		root.Border(2, p.Accent)
	}

	var devicesBtn, headerActions mygoui.Element

	root.Children(func() {
		devicesBtn, headerActions = a.renderHeader(c, snap)

		if snap.Update.BannerVisible() {
			a.renderUpdateBanner(c, snap)
		}

		if a.searchOpen || a.searchInput != "" {
			a.renderSearchDrawer(c, snap.Locale)
		}

		if dragOver {
			a.renderDropOverlayHint(c, snap.Locale)
		}

		a.renderTimeline(c, snap)
		a.renderComposer(c, snap.Locale, dragOver)
	})

	a.renderDevicesPopover(c, devicesBtn, headerActions, snap)
	a.renderPairingModal(c, snap)
	a.renderSettingsModal(c, snap)
	a.renderClearConfirmModal(c, snap)
	a.renderLightboxModal(c, snap)
	a.renderUpdateModal(c, snap)
	if snap.ToastText != "" {
		a.renderToastOverlay(c, snap)
	}
}

func (a *App) anyOverlayOpen() bool {
	return a.devicesPopoverOpen || a.pairOpen || a.settingsOpen || a.clearConfirmOpen || a.lightboxOpen || a.updateModalOpen
}

func headerStatusColors(p *zenPalette, snap ViewSnapshot) (dotColor, textColor, pillBg, pillBorder mygoui.Color) {
	dotColor = p.Offline
	textColor = p.InkMuted
	pillBg = p.InputFill
	pillBorder = p.Border
	if snap.OnlineCount > 0 {
		switch {
		case snap.HasLANDirect:
			dotColor = p.LANDirect
			textColor = p.LANDirect
			pillBg = p.LANDirectSoft
			pillBorder = p.LANDirect.Alpha(0.28)
		case snap.HasCustomDirect:
			dotColor = p.CustomDirect
			textColor = p.CustomDirect
			pillBg = p.CustomDirectSoft
			pillBorder = p.CustomDirectBorder
		case snap.HasP2PDirect:
			dotColor = p.Direct
			textColor = p.DirectText
			pillBg = p.DirectSoft
			pillBorder = p.Direct.Alpha(0.28)
		default:
			dotColor = p.DERP
			textColor = p.DERPText
			pillBg = p.DERPSoft
			pillBorder = p.DERP.Alpha(0.28)
		}
	}
	return dotColor, textColor, pillBg, pillBorder
}

func (a *App) toggleDevicesPopover() {
	a.devicesPopoverOpen = !a.devicesPopoverOpen
	if a.devicesPopoverOpen {
		a.pairOpen = false
		a.settingsOpen = false
		a.clearConfirmOpen = false
		a.devicesErr = ""
		a.deviceDirectErr = ""
	} else {
		a.expandedDeviceID = ""
		a.deviceDirectInput = ""
		a.deviceDirectErr = ""
	}
}

func (a *App) renderHeaderActions(c *mygoui.Context, snap ViewSnapshot) (mygoui.Element, mygoui.Element) {
	t := tr(snap.Locale)
	var devicesBtn mygoui.Element
	headerActions := mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Shrink(0).Children(func() {
		searchActive := a.searchOpen || a.searchInput != ""
		if zenCircleBtn(c, "btn-search", iconSearch, t.SearchMessages, searchActive).Clicked() {
			a.devicesPopoverOpen = false
			a.searchOpen = !a.searchOpen
			if a.searchOpen {
				a.focusSearchPending = true
			} else if a.searchInput != "" {
				a.searchInput = ""
				a.setSearchQuery("")
			}
		}

		devicesBtn = zenCircleBtn(c, "btn-devices", iconSmartphone, t.PairedDevices, a.devicesPopoverOpen)
		if devicesBtn.Clicked() {
			a.toggleDevicesPopover()
		}

		if zenCircleBtn(c, "btn-settings", iconSettings, t.SettingsAndDERP, a.settingsOpen).Clicked() {
			a.settingsDraft = NewSettingsDraft(snap.Config)
			a.settingsErr = ""
			a.settingsSaving = false
			a.settingsTab = 0
			a.clearConfirmOpen = false
			a.pendingDirMu.Lock()
			a.pendingDownloadDir = nil
			a.pendingDirMu.Unlock()
			a.devicesPopoverOpen = false
			a.pairOpen = false
			a.settingsOpen = true
		}

		if zenPairHeaderBtn(c, t.Pair, t.PairNewDevice, a.pairOpen).Clicked() {
			a.openPairingFlow(0)
		}
	})
	return devicesBtn, headerActions
}

func (a *App) renderHeader(c *mygoui.Context, snap ViewSnapshot) (mygoui.Element, mygoui.Element) {
	p := zenPal(c)
	bar := c.TitleBar()
	wb := a.customWindowButtons()
	dotColor, subColor, pillBg, pillBorder := headerStatusColors(p, snap)

	if a.isSplitTitleBar() {
		return a.renderSplitHeader(c, snap, bar, wb, dotColor, subColor, pillBg, pillBorder)
	}

	leftBase := float32(18)
	if len(wb.Start) > 0 {
		leftBase = 14
	}
	leftPad := max(bar.Left+10, leftBase)
	rightPad := max(bar.Right+10, 14)
	headerHeight := max(bar.Height, 60)

	var devicesBtn, headerActions mygoui.Element

	mygoui.Row(c).
		Height(headerHeight).
		Padding(0, rightPad, 0, leftPad).
		Gap(10).
		AlignItems(mygoui.Center).
		Background(p.Surface).
		BorderWidth(0, 0, 1, 0).
		BorderColor(p.Border).
		DragWindow().
		Children(func() {
			if len(wb.Start) > 0 {
				a.renderWindowButtons(c, snap.Locale, wb.Start, "start", false)
			}

			// Title & live connectivity status stack.
			mygoui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
				mygoui.Text(c, "TailChat").FontSize(15).Bold().TextColor(p.Ink).NoWrap()
				mygoui.Row(c).Gap(5).AlignItems(mygoui.Center).Children(func() {
					mygoui.Box(c).Size(6, 6).Radius(99).Background(dotColor)
					mygoui.Text(c, snap.ConnectivityTxt).
						FontSize(11).
						FontWeight(550).
						TextColor(subColor).
						Grow(1).
						MinWidth(0).
						SingleLine()
				})
			})

			// Right action bar: Search, Devices, Settings, and dedicated Pair CTA pill.
			devicesBtn, headerActions = a.renderHeaderActions(c, snap)

			if len(wb.End) > 0 {
				mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
					mygoui.Box(c).Size(1, 18).Background(p.Border).Margin(0, 4, 0, 0)
					a.renderWindowButtons(c, snap.Locale, wb.End, "end", false)
				})
			}
		})

	return devicesBtn, headerActions
}

// renderSplitHeader renders the Windows/Linux two-tier header (Plan A):
// Row 1 is a dedicated 32px draggable window titlebar with brand + OS/custom window controls;
// Row 2 is a full-width 48px application toolbar with clickable connectivity pill + action buttons.
func (a *App) renderSplitHeader(
	c *mygoui.Context,
	snap ViewSnapshot,
	bar mygoui.TitleBar,
	wb mygo.WindowButtons,
	dotColor, subColor, pillBg, pillBorder mygoui.Color,
) (mygoui.Element, mygoui.Element) {
	t := tr(snap.Locale)
	p := zenPal(c)

	titleHeight := max(bar.Height, 32)
	leftBase := float32(14)
	if len(wb.Start) > 0 {
		leftBase = 10
	}
	leftPad := max(bar.Left+10, leftBase)
	rightPad := max(bar.Right+8, 10)

	var devicesBtn, headerActions mygoui.Element

	mygoui.Column(c).FillWidth().Children(func() {
		// Row 1: Dedicated 32px draggable window titlebar.
		mygoui.Row(c.Key("window-titlebar")).
			Height(titleHeight).
			FillWidth().
			Padding(0, rightPad, 0, leftPad).
			Gap(8).
			AlignItems(mygoui.Center).
			Background(p.TitleBarBg).
			BorderWidth(0, 0, 1, 0).
			BorderColor(p.Border).
			DragWindow().
			Children(func() {
				if len(wb.Start) > 0 {
					a.renderWindowButtons(c, snap.Locale, wb.Start, "start", true)
				}

				mygoui.Row(c).Gap(7).AlignItems(mygoui.Center).Shrink(0).Children(func() {
					if iconSrc := a.appIcon(); iconSrc != nil {
						mygoui.Row(c).
							Size(16, 16).
							Radius(4).
							Clip().
							Shrink(0).
							Children(func() {
								mygoui.Image(c, iconSrc).Size(16, 16).Fit(mygoui.Cover)
							})
					}
					mygoui.Text(c, "TailChat").
						FontSize(12).
						FontWeight(600).
						TextColor(p.InkSecondary).
						NoWrap()
				})

				mygoui.Spacer(c)

				if len(wb.End) > 0 {
					a.renderWindowButtons(c, snap.Locale, wb.End, "end", true)
				}
			})

		// Row 2: Full-width 48px application toolbar (status pill + Search/Devices/Settings/Pair).
		mygoui.Row(c.Key("app-toolbar")).
			Height(48).
			FillWidth().
			Padding(0, 14, 0, 14).
			Gap(10).
			AlignItems(mygoui.Center).
			Background(p.Surface).
			BorderWidth(0, 0, 1, 0).
			BorderColor(p.Border).
			Children(func() {
				mygoui.Row(c).Grow(1).MinWidth(0).AlignItems(mygoui.Center).Children(func() {
					statusBtn := mygoui.ButtonBase(c.Key("btn-status-pill"))
					bg := pillBg
					border := pillBorder
					if statusBtn.Pressed() || a.devicesPopoverOpen {
						border = subColor.Alpha(0.45)
					} else if statusBtn.Hovered() {
						border = subColor.Alpha(0.38)
					}
					statusBtn.
						Padding(4, 10).
						Radius(99).
						Gap(6).
						Background(bg).
						Border(1, border).
						AlignItems(mygoui.Center).
						MaxWidthPercent(100).
						MinWidth(0).
						Cursor(mygoui.CursorPointer).
						Label(snap.ConnectivityTxt).
						Tooltip(t.PairedDevices).
						Children(func() {
							mygoui.Box(c).Size(6, 6).Radius(99).Shrink(0).Background(dotColor)
							mygoui.Text(c, snap.ConnectivityTxt).
								FontSize(12).
								FontWeight(550).
								TextColor(subColor).
								MinWidth(0).
								SingleLine()
						})
					if statusBtn.Pressed() && a.devicesPopoverOpen {
						a.suppressStatusPillClick = true
					}
					if statusBtn.Clicked() {
						if a.suppressStatusPillClick {
							a.suppressStatusPillClick = false
						} else {
							a.toggleDevicesPopover()
						}
					} else if !statusBtn.Pressed() {
						a.suppressStatusPillClick = false
					}
				})

				devicesBtn, headerActions = a.renderHeaderActions(c, snap)
			})
	})

	return devicesBtn, headerActions
}

func (a *App) renderWindowButtons(c *mygoui.Context, loc Locale, buttons []string, side string, compact bool) {
	t := tr(loc)
	maximized := a.winIsMaximized != nil && a.winIsMaximized()

	mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Shrink(0).Children(func() {
		for _, kind := range buttons {
			switch kind {
			case "minimize":
				if zenWindowControlBtn(c, "win-btn-"+side+"-minimize", iconWinMinimize, t.WinMinimize, false, compact).Clicked() {
					if a.winMinimize != nil {
						a.winMinimize()
					}
				}
			case "maximize":
				svg := iconWinMaximize
				label := t.WinMaximize
				if maximized {
					svg = iconWinRestore
					label = t.WinRestore
				}
				if zenWindowControlBtn(c, "win-btn-"+side+"-maximize", svg, label, false, compact).Clicked() {
					if a.winToggleMaximize != nil {
						a.winToggleMaximize()
					}
				}
			case "close":
				if zenWindowControlBtn(c, "win-btn-"+side+"-close", iconX, t.WinClose, true, compact).Clicked() {
					if a.winClose != nil {
						a.winClose()
					}
				}
			}
		}
	})
}

func (a *App) renderSearchDrawer(c *mygoui.Context, loc Locale) {
	t := tr(loc)
	p := zenPal(c)
	mygoui.Row(c).
		Padding(8, 16).
		Background(p.Surface).
		BorderWidth(0, 0, 1, 0).
		BorderColor(p.Border).
		AlignItems(mygoui.Center).
		Children(func() {
			mygoui.Row(c).
				Grow(1).
				Radius(10).
				Background(p.InputFill).
				Border(1, p.Border).
				Padding(6, 10).
				Gap(7).
				AlignItems(mygoui.Center).
				Children(func() {
					mygoui.Icon(c, iconSearch).Size(14, 14).TextColor(p.InkMuted)
					in := mygoui.TextInputBase(c.Key("search"), &a.searchInput).
						Placeholder(t.SearchPlaceholder).
						Label(t.SearchMessages).
						AutoFocus().
						Bind(&a.searchRef).
						Grow(1).
						FontSize(12.5).
						TextColor(p.Ink)
					if a.focusSearchPending {
						a.focusSearchPending = false
						a.searchRef.Focus()
					}
					if in.Changed() || in.Submitted() {
						a.setSearchQuery(a.searchInput)
					}
					if a.searchInput != "" {
						clearBtn := mygoui.ButtonBase(c.Key("search-clear")).
							Size(18, 18).
							Radius(99).
							Background(p.TimeChipBg).
							Justify(mygoui.Center).
							AlignItems(mygoui.Center).
							Cursor(mygoui.CursorPointer).
							Label(t.ClearSearch).
							Children(func() {
								mygoui.Icon(c, iconX).Size(11, 11).TextColor(p.InkSecondary)
							})
						if clearBtn.Clicked() {
							a.searchInput = ""
							a.setSearchQuery("")
							a.searchRef.Focus()
						}
					}
				})
		})
}

func (a *App) renderDropOverlayHint(c *mygoui.Context, loc Locale) {
	p := zenPal(c)
	mygoui.Row(c).
		Padding(8, 16).
		Background(p.AccentSoft).
		BorderWidth(0, 0, 1, 0).
		BorderColor(p.AccentBorder).
		Justify(mygoui.Center).
		Children(func() {
			mygoui.Text(c, tr(loc).DropOverlayHint).FontSize(12).Bold().TextColor(p.Accent)
		})
}

func (a *App) renderToastOverlay(c *mygoui.Context, snap ViewSnapshot) {
	t := tr(snap.Locale)
	p := zenPal(c)
	bg := p.ToastBg
	border := p.ToastBorder
	if snap.ToastIsErr {
		bg = p.DangerSolid
		border = p.DangerBorder
	}
	safeTop, _, _, _ := c.SafeArea()
	topOffset := safeTop + 68
	if a.isSplitTitleBar() {
		topOffset = safeTop + 88
	}
	mygoui.Overlay(c, func() {
		mygoui.Row(c.Key("toast-overlay")).
			Absolute().
			Top(topOffset).
			Left(0).
			Right(0).
			Padding(0, 16).
			Justify(mygoui.Center).
			PassThrough().
			Children(func() {
				mygoui.Row(c).
					Padding(6, 12).
					Gap(8).
					Radius(99).
					Background(bg).
					Border(1, border).
					AlignItems(mygoui.Center).
					MaxWidthPercent(92).
					Shadow(0, 8, 20, 0, p.ShadowModal).
					Children(func() {
						mygoui.Text(c, snap.ToastText).FontSize(12).TextColor(p.SelfBubbleInk).Wrap()
						dismiss := mygoui.ButtonBase(c.Key("toast-dismiss")).
							Size(18, 18).
							Radius(99).
							Background(mygoui.RGBA(255, 255, 255, 0.14)).
							Justify(mygoui.Center).
							AlignItems(mygoui.Center).
							Cursor(mygoui.CursorPointer).
							Label(t.DismissToast).
							Children(func() {
								mygoui.Icon(c, iconX).Size(11, 11).TextColor(p.SelfBubbleInk)
							})
						if dismiss.Clicked() {
							a.state.ClearToast()
						}
					})
			})
	})
}

func (a *App) renderTimeline(c *mygoui.Context, snap ViewSnapshot) {
	p := zenPal(c)
	msgs := snap.Messages
	if snap.SearchQuery != "" {
		msgs = snap.SearchResults
	}

	mygoui.Scroll(c.Key("timeline-scroll")).Grow(1).Children(func() {
		mygoui.Column(c).Padding(16, 16).Gap(14).FillWidth().Children(func() {
			visibleCount := 0
			for _, msg := range msgs {
				if msg.Kind != model.MessageKindTombstone {
					visibleCount++
				}
			}

			if visibleCount == 0 {
				a.renderEmptyState(c, snap.Locale, snap.SearchQuery)
				return
			}

			var prevTS int64
			for _, msg := range msgs {
				if msg.Kind == model.MessageKindTombstone {
					continue
				}
				if msg.CreatedAt > 0 && (prevTS == 0 || msg.CreatedAt-prevTS > timeChipGapMs) {
					if chip := FormatTimeChipLocale(snap.Locale, msg.CreatedAt); chip != "" {
						mygoui.Row(c).
							AlignSelf(mygoui.Center).
							Padding(3, 10).
							Radius(99).
							Background(p.TimeChipBg).
							Children(func() {
								mygoui.Text(c, chip).FontSize(11).TextColor(p.InkMuted)
							})
					}
				}
				if msg.CreatedAt > 0 {
					prevTS = msg.CreatedAt
				}
				a.renderMessageBubble(c, snap, msg)
			}
		})
	})
}

func (a *App) renderEmptyState(c *mygoui.Context, loc Locale, searchQuery string) {
	t := tr(loc)
	p := zenPal(c)
	title := t.EmptyTitle
	subtitle := t.EmptySubtitle
	icon := iconLaptop
	if searchQuery != "" {
		title = fmt.Sprintf(t.SearchEmptyTitleF, searchQuery)
		subtitle = t.SearchEmptySub
		icon = iconSearch
	}

	mygoui.Column(c).
		AlignSelf(mygoui.Center).
		MaxWidth(360).
		Margin(32, 0, 0, 0).
		Padding(22, 24).
		Gap(10).
		Radius(16).
		Background(p.Surface).
		Border(1, p.Border).
		Shadow(0, 3, 12, 0, p.ShadowSoft).
		AlignItems(mygoui.Center).
		Children(func() {
			mygoui.Row(c).
				Size(42, 42).
				Radius(12).
				Background(p.AccentSoft).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Children(func() {
					mygoui.Icon(c, icon).Size(20, 20).TextColor(p.Accent)
				})
			mygoui.Text(c, title).FontSize(14).Bold().TextColor(p.Ink)
			mygoui.Text(c, subtitle).FontSize(12).TextColor(p.InkSecondary).Wrap()
		})
}

func (a *App) renderMessageBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	t := tr(snap.Locale)
	p := zenPal(c)
	isSelf := snap.Identity.DeviceID != "" && msg.AuthorDeviceID == snap.Identity.DeviceID
	ts := FormatTimestamp(msg.CreatedAt)

	winW, _ := c.Size()
	maxBubbleW := (winW - 32) * 0.84
	if maxBubbleW < 180 {
		maxBubbleW = 180
	}
	maxTextW := maxBubbleW - 28 // bubble horizontal padding (14 * 2)
	if maxTextW < 140 {
		maxTextW = 140
	}

	group := mygoui.Column(c.Key("msg-"+msg.MsgID)).
		Gap(4).
		MinWidth(0).
		MaxWidth(maxBubbleW)
	if isSelf {
		group.AlignSelf(mygoui.End).AlignItems(mygoui.End)
	} else {
		group.AlignSelf(mygoui.Start).AlignItems(mygoui.Start)
	}

	group.Children(func() {
		// Sender label row.
		mygoui.Row(c).Padding(0, 4).Gap(5).AlignItems(mygoui.Center).Children(func() {
			if isSelf {
				label := t.ThisDevice
				if ts != "" {
					label += " · " + ts
				}
				mygoui.Text(c, label).FontSize(11).TextColor(p.InkMuted)
			} else {
				mygoui.Icon(c, deviceIconForAuthor(msg.AuthorDeviceID, snap.Devices)).
					Size(12, 12).
					TextColor(p.InkMuted)
				author := msg.AuthorName
				if author == "" {
					author = shortID(msg.AuthorDeviceID)
				}
				if ts != "" {
					author += " · " + ts
				}
				mygoui.Text(c, author).FontSize(11).TextColor(p.InkMuted)
			}
		})

		// Bubble body.
		switch msg.Kind {
		case model.MessageKindText:
			if isSelf {
				mygoui.Column(c).
					MinWidth(0).
					MaxWidth(maxBubbleW).
					Padding(10, 14).
					Radius(16, 5, 16, 16).
					Background(p.SelfBubbleBg).
					Shadow(0, 2, 8, 0, p.ShadowCard).
					Children(func() {
						mygoui.Text(c, msg.TextBody).
							MinWidth(0).
							MaxWidth(maxTextW).
							FontSize(13.5).
							TextColor(p.SelfBubbleInk).
							Selectable()
					})
			} else {
				mygoui.Column(c).
					MinWidth(0).
					MaxWidth(maxBubbleW).
					Padding(10, 14).
					Radius(5, 16, 16, 16).
					Background(p.Surface).
					Border(1, p.Border).
					Shadow(0, 2, 8, 0, p.ShadowSoft).
					Children(func() {
						mygoui.Text(c, msg.TextBody).
							MinWidth(0).
							MaxWidth(maxTextW).
							FontSize(13.5).
							TextColor(p.Ink).
							Selectable()
					})
			}

		case model.MessageKindFile:
			a.renderFileBubble(c, snap, msg)
		}

		// Subtle micro-action row under each bubble.
		mygoui.Row(c).Gap(4).Padding(0, 2).AlignItems(mygoui.Center).Children(func() {
			if msg.Kind == model.MessageKindText {
				copyLabel := t.Copy
				if a.concealedCopy {
					copyLabel = t.PrivateCopy
				}
				if zenMicroBtn(c, "copy-"+msg.MsgID, iconCopy, copyLabel, false).Clicked() {
					a.copyText(msg.TextBody, a.concealedCopy)
				}
			}
			if zenMicroBtn(c, "del-"+msg.MsgID, iconTrash, t.Delete, true).Clicked() {
				msgID := msg.MsgID
				a.dispatchAsync(func() {
					_ = a.state.DeleteMessage(context.Background(), msgID)
				})
			}
		})
	})
}

func (a *App) renderFileBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	t := tr(snap.Locale)
	p := zenPal(c)
	fb := msg.File
	if fb == nil {
		mygoui.Column(c).
			Padding(10, 14).
			Radius(16).
			Background(p.Surface).
			Border(1, p.Border).
			Children(func() {
				mygoui.Text(c, t.FilePrefix+shortID(msg.BlobHash)).FontSize(13).TextColor(p.InkMuted)
			})
		return
	}

	task, hasTask := snap.Transfers[fb.BlobHash]
	isLocal := fb.LocalPath != "" || (hasTask && task.State == model.TransferDone)
	showProgress := hasTask && (task.State == model.TransferRunning ||
		task.State == model.TransferQueued ||
		task.State == model.TransferPaused ||
		task.State == model.TransferFailed)

	switch ClassifyMedia(fb.FileName, fb.MimeType) {
	case MediaImage:
		a.renderImageBubble(c, snap, msg, isLocal, showProgress, task, false)
	case MediaAnimated:
		a.renderImageBubble(c, snap, msg, isLocal, showProgress, task, true)
	case MediaVideo:
		a.renderVideoBubble(c, snap, msg, isLocal, showProgress, task)
	case MediaAudio:
		a.renderAudioBubble(c, snap, msg, isLocal, showProgress, task)
	default:
		a.renderStandardFileBubble(c, snap, msg, isLocal, showProgress, task)
	}
}

func (a *App) renderStandardFileBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message, isLocal, showProgress bool, task model.TransferTask) {
	t := tr(snap.Locale)
	p := zenPal(c)
	fb := msg.File
	_, hasTask := snap.Transfers[fb.BlobHash]
	ext, tileBg, tileFg := zenFileTileColors(fb.FileName, p.Dark)

	var meta string
	switch {
	case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
		pct := int(task.Progress() * 100)
		meta = fmt.Sprintf("%s / %s · %d%%", FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal), pct)
	case hasTask && task.State == model.TransferPaused:
		meta = fmt.Sprintf(t.TransferPausedF, FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal))
	case hasTask && task.State == model.TransferFailed:
		meta = fmt.Sprintf(t.TransferFailedF, FormatBytes(fb.FileSize))
		if task.LastError != "" {
			meta += " · " + task.LastError
		}
	default:
		meta = formatSeedersMeta(snap.Locale, FormatBytes(fb.FileSize), len(fb.Seeders))
	}

	footText := t.ClickToDownload
	switch {
	case isLocal:
		footText = t.Blake3Verified
	case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued || task.State == model.TransferPaused):
		footText = t.ResumableTransfer
	case hasTask && task.State == model.TransferFailed:
		footText = t.TransferRetryHint
	}

	mygoui.Column(c).
		Width(332).
		MaxWidthPercent(90).
		Padding(12, 14).
		Gap(10).
		Radius(16).
		Background(p.Surface).
		Border(1, p.Border).
		Shadow(0, 3, 12, 0, p.ShadowSoft).
		Clip().
		Children(func() {
			// File header: extension tile + filename & metadata.
			renderHeaderContent := func() {
				mygoui.Row(c).
					Size(44, 44).
					Radius(12).
					Background(tileBg).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Shrink(0).
					Children(func() {
						mygoui.Text(c, ext).
							FontSize(11).
							Bold().
							Font("SF Mono, Menlo, monospace").
							TextColor(tileFg)
					})

				mygoui.Column(c).Grow(1).MinWidth(0).Clip().Gap(2).Children(func() {
					mygoui.Text(c, fb.FileName).
						FontSize(13.5).
						FontWeight(600).
						TextColor(p.Ink).
						FillWidth().
						SingleLine()
					mygoui.Text(c, meta).
						FontSize(11.5).
						TextColor(p.InkSecondary).
						FillWidth().
						SingleLine()
				})
			}

			if isLocal {
				headerBtn := mygoui.ButtonBase(c.Key("open-direct-" + msg.MsgID))
				hdrBg := mygoui.RGBA(0, 0, 0, 0)
				if headerBtn.Pressed() {
					hdrBg = p.InputFill
				} else if headerBtn.Hovered() {
					hdrBg = p.SurfaceSubtle
				}
				headerBtn.Gap(11).
					Padding(4).
					Radius(12).
					Background(hdrBg).
					Justify(mygoui.Start).
					AlignItems(mygoui.Center).
					FillWidth().
					MinWidth(0).
					Clip().
					Cursor(mygoui.CursorPointer).
					Label(t.OpenFileDirectly).
					Tooltip(fb.FileName + "\n" + t.OpenFileDirectly).
					Children(renderHeaderContent)
				if headerBtn.Clicked() {
					a.openFileDirectly(fb)
				}
			} else {
				mygoui.Row(c).Gap(11).AlignItems(mygoui.Center).FillWidth().Clip().Tooltip(fb.FileName).Children(renderHeaderContent)
			}

			// Sleek 6px rounded progress track when actively transferring/paused/failed.
			if showProgress {
				pct := task.Progress()
				if pct < 0 {
					pct = 0
				}
				if pct > 1 {
					pct = 1
				}
				barColor := p.Accent
				if task.State == model.TransferPaused {
					barColor = p.InkMuted
				} else if task.State == model.TransferFailed {
					barColor = p.Danger
				}
				mygoui.Row(c).
					Height(6).
					FillWidth().
					Radius(99).
					Background(p.ProgressTrack).
					Clip().
					Label(t.TransferProgress).
					Children(func() {
						mygoui.Box(c).
							Height(6).
							WidthPercent(float32(pct * 100)).
							Radius(99).
							Background(barColor)
					})
			}

			// Footer status + pill CTA buttons.
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Text(c, footText).
					FontSize(11.5).
					TextColor(p.InkMuted).
					Grow(1).
					MinWidth(0).
					SingleLine()

				mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Shrink(0).Children(func() {
					blobHash := fb.BlobHash
					fileSize := fb.FileSize
					switch {
					case isLocal:
						if zenIconPillBtn(c, "reveal-"+msg.MsgID, iconFolderOpen, t.ShowInFolder, false, false).Clicked() {
							a.revealFileInManager(fb)
						}
					case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
						if zenPillBtn(c, "pause-"+msg.MsgID, t.Pause, false, false).Clicked() {
							a.pauseTransfer(blobHash)
						}
						if zenPillBtn(c, "cancel-"+msg.MsgID, t.Cancel, false, false).Clicked() {
							a.cancelTransfer(blobHash)
						}
					case hasTask && task.State == model.TransferPaused:
						if zenPillBtn(c, "resume-"+msg.MsgID, t.Resume, true, false).Clicked() {
							a.startDownload(blobHash, fileSize)
						}
						if zenPillBtn(c, "cancel-"+msg.MsgID, t.Cancel, false, false).Clicked() {
							a.cancelTransfer(blobHash)
						}
					case hasTask && task.State == model.TransferFailed:
						if zenPillBtn(c, "retry-"+msg.MsgID, t.RetryDownload, true, false).Clicked() {
							a.startDownload(blobHash, fileSize)
						}
					default:
						if zenPillBtn(c, "dl-"+msg.MsgID, t.DownloadNow, true, false).Clicked() {
							a.startDownload(blobHash, fileSize)
						}
					}
				})
			})
		})
}

func (a *App) renderComposer(c *mygoui.Context, loc Locale, dragOver bool) {
	t := tr(loc)
	p := zenPal(c)
	mygoui.Column(c).
		Padding(4, 14, 14, 14).
		Gap(8).
		FillWidth().
		Children(func() {
			// Show drag-and-drop prompt pill only while actively dragging files over the window.
			if dragOver {
				mygoui.Row(c).
					Padding(6, 12).
					Radius(99).
					Background(p.AccentSoft).
					Border(1, p.Accent).
					BorderStyle(mygoui.BorderDashed).
					Justify(mygoui.Center).
					Clip().
					Children(func() {
						mygoui.Text(c, t.DropStripActive).FontSize(11.5).FontWeight(600).TextColor(p.Accent).SingleLine()
					})
			}

			// Floating Capsule (Option A): single sculpted pill containing quick actions, borderless input, lock toggle & send CTA.
			capBorder := p.Border
			capShadow := p.ShadowCard
			if dragOver {
				capBorder = p.Accent
				capShadow = p.Accent.Alpha(0.24)
			}

			mygoui.Row(c).
				Height(52).
				Padding(6, 7, 6, 8).
				Gap(4).
				Radius(26).
				Background(p.Surface).
				Border(1, capBorder).
				Shadow(0, 8, 24, 0, capShadow).
				AlignItems(mygoui.Center).
				FillWidth().
				Children(func() {
					if zenCapsuleIconBtn(c, "btn-attach", iconPlus, t.AttachFiles, 34, 17, false, false).Clicked() {
						a.triggerAttachFile()
					}
					if zenCapsuleIconBtn(c, "btn-paste", iconClipboard, t.PasteClipboard, 34, 15, false, false).Clicked() {
						a.triggerPasteClipboard()
					}

					// Hairline vertical divider separating quick actions from text input.
					mygoui.Row(c).Padding(0, 3).AlignItems(mygoui.Center).Shrink(0).Children(func() {
						mygoui.Box(c).Size(1, 18).Background(p.Border)
					})

					var in mygoui.Element
					mygoui.Row(c).
						Padding(0, 6).
						Grow(1).
						MinWidth(0).
						AlignItems(mygoui.Center).
						Children(func() {
							in = mygoui.TextInputBase(c.Key("composer"), &a.composer).
								Placeholder(t.ComposerPlace).
								Label(t.ComposerLabel).
								Bind(&a.composerRef).
								FillWidth().
								FontSize(13.5).
								TextColor(p.Ink)
						})

					if zenCapsuleIconBtn(c, "btn-concealed", iconLock, t.PrivateCopy, 30, 14, true, a.concealedCopy).Clicked() {
						a.concealedCopy = !a.concealedCopy
						if a.concealedCopy {
							a.state.SetToast(t.PrivateCopyOn, false)
						} else {
							a.state.SetToast(t.PrivateCopyOff, false)
						}
					}

					canSend := strings.TrimSpace(a.composer) != ""
					sendBtn := mygoui.ButtonBase(c.Key("btn-send"))
					sendBg := p.ProgressTrack
					sendFg := p.InkMuted
					if canSend {
						sendBg = p.AccentSolid
						sendFg = p.SelfBubbleInk
						if sendBtn.Pressed() {
							sendBg = p.AccentPressed
						} else if sendBtn.Hovered() {
							sendBg = p.AccentHover
						}
					}
					sendBtn.Size(36, 36).
						Radius(99).
						Background(sendBg).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Shrink(0).
						Disabled(!canSend).
						Label(t.Send).
						Tooltip(t.SendTooltip)
					if canSend {
						sendBtn.Cursor(mygoui.CursorPointer).
							Shadow(0, 3, 10, 0, p.AccentSolid.Alpha(0.32))
					}
					sendBtn.Children(func() {
						mygoui.Icon(c, iconArrowUp).Size(16, 16).TextColor(sendFg)
					})

					if (sendBtn.Clicked() || in.Submitted()) && canSend {
						a.submitComposer()
					}
				})
		})
}

func (a *App) dispatchAsync(fn func()) {
	if a.runAsync != nil {
		a.runAsync(fn)
		return
	}
	go fn()
}

func (a *App) startDownload(blobHash string, fileSize int64) {
	if blobHash == "" {
		return
	}
	a.state.MarkTransferStarting(blobHash, fileSize)
	a.dispatchAsync(func() {
		_ = a.state.DownloadFile(context.Background(), blobHash)
	})
}

func (a *App) pauseTransfer(blobHash string) {
	if blobHash == "" {
		return
	}
	a.dispatchAsync(func() {
		_ = a.state.PauseTransfer(blobHash)
	})
}

func (a *App) cancelTransfer(blobHash string) {
	if blobHash == "" {
		return
	}
	a.dispatchAsync(func() {
		_ = a.state.CancelTransfer(blobHash)
	})
}

func (a *App) setSearchQuery(query string) {
	trimmed, seq, needRun := a.state.BeginSearchQuery(query)
	if !needRun {
		return
	}
	a.dispatchAsync(func() {
		a.state.RunSearchQuery(trimmed, seq)
	})
}

func (a *App) applySettingsAsync(cfg model.AppConfig, hotSwitchDERP bool) {
	if a.settingsSaving {
		return
	}
	a.settingsSaving = true
	a.settingsErr = ""
	a.dispatchAsync(func() {
		err := a.state.ApplySettings(context.Background(), cfg, hotSwitchDERP)
		a.asyncMu.Lock()
		a.pendingSettingsDone = true
		if err != nil {
			a.pendingSettingsErr = err.Error()
			a.pendingSettingsClose = false
		} else {
			a.pendingSettingsErr = ""
			a.pendingSettingsClose = true
		}
		a.asyncMu.Unlock()
		a.invalidate()
	})
	a.drainPendingAsync()
}

func (a *App) savePeerDirectAddrAsync(deviceID, rawAddr string) {
	if a.deviceDirectSaving {
		return
	}
	// Fast synchronous format validation so invalid input shows an inline error immediately.
	if _, err := model.NormalizeDirectAddr(rawAddr, model.DefaultDirectPort); err != nil {
		a.deviceDirectErr = tr(a.state.Locale()).ErrInvalidPeerDirect
		return
	}
	a.deviceDirectSaving = true
	a.deviceDirectErr = ""
	devID := deviceID
	val := rawAddr
	a.dispatchAsync(func() {
		_, err := a.state.SetPeerDirectAddr(context.Background(), devID, val)
		a.asyncMu.Lock()
		a.pendingPeerDirectDone = true
		a.pendingPeerDirectDev = devID
		if err != nil {
			a.pendingPeerDirectErr = err.Error()
		} else {
			a.pendingPeerDirectErr = ""
		}
		a.asyncMu.Unlock()
		a.invalidate()
	})
	a.drainPendingAsync()
}

func (a *App) triggerOpenExternalURL(rawURL string) {
	if a.openExternalURL == nil || strings.TrimSpace(rawURL) == "" {
		return
	}
	a.dispatchAsync(func() {
		if err := a.openExternalURL(rawURL); err != nil {
			a.state.SetToast(tr(a.state.Locale()).OpenURLFailPref+err.Error(), true)
		}
	})
}

func (a *App) submitComposer() {
	text := strings.TrimSpace(a.composer)
	if text == "" {
		return
	}
	a.composer = ""
	a.dispatchAsync(func() {
		_ = a.state.SendText(context.Background(), text)
	})
}

func (a *App) copyText(text string, concealed bool) {
	t := tr(a.state.Locale())
	if err := a.writeClipboardText(text, concealed); err != nil {
		a.state.SetToast(t.CopyFailedPref+err.Error(), true)
		return
	}
	if concealed {
		a.state.SetToast(t.CopiedPrivateToast, false)
	} else {
		a.state.SetToast(t.CopiedToast, false)
	}
}

func (a *App) triggerAttachFile() {
	if a.openFileDialog == nil {
		return
	}
	a.dispatchAsync(func() {
		paths, err := a.openFileDialog()
		if err != nil {
			a.state.SetToast(tr(a.state.Locale()).OpenDialogFailPref+err.Error(), true)
			return
		}
		if len(paths) > 0 {
			a.sendFilesSync(paths)
		}
	})
}

func (a *App) triggerPasteClipboard() {
	if a.readClipboardImage != nil {
		if img := a.readClipboardImage(); len(img) > 0 {
			name := fmt.Sprintf("clipboard-%s.png", time.Now().Format("20060102-150405"))
			imgCopy := append([]byte(nil), img...)
			a.state.SetToast(tr(a.state.Locale()).ToastSendingFilePref+name, false)
			a.dispatchAsync(func() {
				_ = a.state.SendBytes(context.Background(), name, "image/png", imgCopy)
			})
			return
		}
	}
	if a.readClipboardFiles != nil {
		if files, err := a.readClipboardFiles(); err == nil && len(files) > 0 {
			a.sendFiles(files)
			return
		}
	}
	if a.readClipboardText != nil {
		if txt := a.readClipboardText(); txt != "" {
			if a.composer == "" {
				a.composer = txt
			} else {
				a.composer += "\n" + txt
			}
			a.composerRef.Focus()
			return
		}
	}
	a.state.SetToast(tr(a.state.Locale()).ClipboardEmpty, false)
}

func (a *App) sendFiles(paths []string) {
	cleaned := make([]string, 0, len(paths))
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean == "" || clean == "." {
			continue
		}
		cleaned = append(cleaned, clean)
	}
	if len(cleaned) == 0 {
		return
	}
	a.dispatchAsync(func() {
		a.sendFilesSync(cleaned)
	})
}

func (a *App) sendFilesSync(paths []string) {
	t := tr(a.state.Locale())
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean == "" || clean == "." {
			continue
		}
		a.state.SetToast(t.ToastSendingFilePref+filepath.Base(clean), false)
		_ = a.state.SendFile(context.Background(), clean)
	}
}

func (a *App) handleDrop(drop transfer.Drop) {
	if paths, err := drop.Data.Files(); err == nil && len(paths) > 0 {
		a.sendFiles(paths)
		return
	}
	if b, err := drop.Data.Read(transfer.Text); err == nil && len(b) > 0 {
		if a.composer == "" {
			a.composer = string(b)
		} else {
			a.composer += "\n" + string(b)
		}
	}
}

func (a *App) resolveFilePath(fb *model.FileBlob) (string, error) {
	if fb == nil {
		return "", fmt.Errorf("missing file metadata")
	}
	if fb.BlobHash != "" {
		path, err := a.state.ResolveLocalFile(context.Background(), fb.BlobHash)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(path) != "" {
			return path, nil
		}
	}
	if strings.TrimSpace(fb.LocalPath) != "" {
		return fb.LocalPath, nil
	}
	return "", fmt.Errorf("local file unavailable")
}

func (a *App) openFileDirectly(fb *model.FileBlob) {
	if fb == nil {
		return
	}
	fbCopy := *fb
	a.dispatchAsync(func() {
		t := tr(a.state.Locale())
		path, err := a.resolveFilePath(&fbCopy)
		if err != nil {
			a.state.SetToast(t.OpenFileFailedPref+err.Error(), true)
			return
		}
		if a.openLocalPath == nil {
			return
		}
		if err := a.openLocalPath(path); err != nil {
			a.state.SetToast(t.OpenFileFailedPref+err.Error(), true)
		}
	})
}

func (a *App) revealFileInManager(fb *model.FileBlob) {
	if fb == nil {
		return
	}
	fbCopy := *fb
	a.dispatchAsync(func() {
		t := tr(a.state.Locale())
		path, err := a.resolveFilePath(&fbCopy)
		if err != nil {
			a.state.SetToast(t.ShowInFolderFailPref+err.Error(), true)
			return
		}
		if a.showInFolder == nil {
			return
		}
		if err := a.showInFolder(path); err != nil {
			a.state.SetToast(t.ShowInFolderFailPref+err.Error(), true)
		}
	})
}

func (a *App) appIcon() mygoui.ImageSource {
	a.appIconOnce.Do(func() {
		if len(dockIconPNG) == 0 {
			return
		}
		if bmp, err := mygoui.DecodeBitmapScaled(dockIconPNG, 136); err == nil && bmp != nil {
			a.appIconSrc = bmp
			return
		}
		if bmp, err := mygoui.DecodeBitmap(dockIconPNG); err == nil && bmp != nil {
			a.appIconSrc = bmp
		}
	})
	return a.appIconSrc
}

func deviceIconForAuthor(deviceID string, devices []model.Device) *mygoui.SVG {
	for _, d := range devices {
		if d.DeviceID == deviceID {
			p := strings.ToLower(d.Platform)
			switch {
			case strings.Contains(p, "ios") || strings.Contains(p, "android") || strings.Contains(p, "iphone"):
				return iconSmartphone
			case strings.Contains(p, "linux") || strings.Contains(p, "server") || strings.Contains(p, "nas"):
				return iconServer
			default:
				return iconLaptop
			}
		}
	}
	return iconSmartphone
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10] + "..."
}
