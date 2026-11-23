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

	// Lightweight Devices popover state.
	devicesPopoverOpen bool
	devicesErr         string

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

	// Settings & About modal + Clear History confirmation state.
	settingsOpen       bool
	settingsTab        int // 0 = General, 1 = About
	settingsDraft      SettingsDraft
	settingsErr        string
	clearConfirmOpen   bool
	pendingDirMu       sync.Mutex
	pendingDownloadDir *string

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

	// Platform integration hooks (overridable in headless tests).
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
		state: state,
		media: NewMediaCache(nil),
		audio: NewAudioController(nil),
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
	}
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
	if a.win != nil {
		a.win.Invalidate()
	}
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
	if a.pairCode == "" || a.isPairingCodeExpired() {
		a.generatePairingCode(false)
		return
	}
	_ = a.pairingQRBitmap()
}

func (a *App) generatePairingCode(showToast bool) {
	code, tok, err := a.state.CreatePairingCode(context.Background(), 300)
	if err != nil {
		a.pairErr = err.Error()
		a.pairQRCode = ""
		a.pairQRBitmap = nil
		return
	}
	a.pairErr = ""
	a.pairCode = code
	a.pairToken = &tok
	a.pairQRCode = ""
	a.pairQRBitmap = nil
	a.pairCopiedUntil = time.Time{}
	_ = a.pairingQRBitmap()
	if showToast {
		a.state.SetToast(tr(a.state.Locale()).RefreshedQRToast, false)
	}
}

func (a *App) refreshPairingCode() {
	if a.pairCode != "" {
		_ = a.state.CancelPairing()
	}
	a.clearPairingCode()
	a.generatePairingCode(true)
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
	c.SetTheme(zenTheme())

	if a.win == nil {
		svc := c.Services()
		a.state.SetInvalidate(svc.Invalidate)
		a.media.SetInvalidate(svc.Invalidate)
		a.audio.SetInvalidate(svc.Invalidate)
	}

	// Global window shortcuts.
	c.OnShortcut(mygoui.Cmd, mygoui.KeyF, func() {
		a.searchOpen = true
		a.focusSearchPending = true
		a.searchRef.Focus()
	})
	if c.Shortcut(mygoui.Cmd, mygoui.KeyEnter) && strings.TrimSpace(a.composer) != "" && !a.anyOverlayOpen() {
		a.submitComposer()
	}

	snap := a.state.Snapshot()

	root := mygoui.Column(c).Fill().Background(zenBg)

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
		root.Border(2, zenAccent)
	}

	var devicesBtn mygoui.Element

	root.Children(func() {
		devicesBtn = a.renderHeader(c, snap)

		if snap.Update.BannerVisible() {
			a.renderUpdateBanner(c, snap)
		}

		if a.searchOpen || a.searchInput != "" {
			a.renderSearchDrawer(c, snap.Locale)
		}

		if dragOver {
			a.renderDropOverlayHint(c, snap.Locale)
		}

		if snap.ToastText != "" {
			a.renderToastPill(c, snap)
		}

		a.renderTimeline(c, snap)
		a.renderComposer(c, snap.Locale, dragOver)
	})

	a.renderDevicesPopover(c, devicesBtn, snap)
	a.renderPairingModal(c, snap)
	a.renderSettingsModal(c, snap)
	a.renderClearConfirmModal(c, snap)
	a.renderLightboxModal(c, snap)
	a.renderUpdateModal(c, snap)
}

func (a *App) anyOverlayOpen() bool {
	return a.devicesPopoverOpen || a.pairOpen || a.settingsOpen || a.clearConfirmOpen || a.lightboxOpen || a.updateModalOpen
}

func (a *App) renderHeader(c *mygoui.Context, snap ViewSnapshot) mygoui.Element {
	t := tr(snap.Locale)
	bar := c.TitleBar()
	leftPad := max(bar.Left+10, 18)
	rightPad := max(bar.Right+10, 14)
	headerHeight := max(bar.Height, 60)

	dotColor := zenOffline
	subColor := zenInkMuted
	if snap.OnlineCount > 0 {
		if snap.HasDirect {
			dotColor = zenDirect
			subColor = zenDirectText
		} else {
			dotColor = zenDERP
			subColor = zenDERPText
		}
	}

	var devicesBtn mygoui.Element

	mygoui.Row(c).
		Height(headerHeight).
		Padding(0, rightPad, 0, leftPad).
		Gap(10).
		AlignItems(mygoui.Center).
		Background(zenSurface).
		BorderWidth(0, 0, 1, 0).
		BorderColor(zenBorder).
		DragWindow().
		Children(func() {
			// Title & live connectivity status stack.
			mygoui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
				mygoui.Text(c, "TailChat").FontSize(15).Bold().TextColor(zenInk).NoWrap()
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
			mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
				searchActive := a.searchOpen || a.searchInput != ""
				if zenCircleBtn(c, "btn-search", iconSearch, t.SearchMessages, searchActive).Clicked() {
					a.searchOpen = !a.searchOpen
					if a.searchOpen {
						a.focusSearchPending = true
					} else if a.searchInput != "" {
						a.searchInput = ""
						a.state.SetSearchQuery("")
					}
				}

				devicesBtn = zenCircleBtn(c, "btn-devices", iconSmartphone, t.PairedDevices, a.devicesPopoverOpen)
				if devicesBtn.Clicked() {
					a.devicesPopoverOpen = !a.devicesPopoverOpen
					if a.devicesPopoverOpen {
						a.pairOpen = false
						a.settingsOpen = false
						a.clearConfirmOpen = false
						a.devicesErr = ""
					}
				}

				if zenCircleBtn(c, "btn-settings", iconSettings, t.SettingsAndDERP, a.settingsOpen).Clicked() {
					a.settingsDraft = NewSettingsDraft(snap.Config)
					a.settingsErr = ""
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
		})

	return devicesBtn
}

func (a *App) renderSearchDrawer(c *mygoui.Context, loc Locale) {
	t := tr(loc)
	mygoui.Row(c).
		Padding(8, 16).
		Background(zenSurface).
		BorderWidth(0, 0, 1, 0).
		BorderColor(zenBorder).
		AlignItems(mygoui.Center).
		Children(func() {
			mygoui.Row(c).
				Grow(1).
				Radius(10).
				Background(zenInputFill).
				Border(1, zenBorder).
				Padding(6, 10).
				Gap(7).
				AlignItems(mygoui.Center).
				Children(func() {
					mygoui.Icon(c, iconSearch).Size(14, 14).TextColor(zenInkMuted)
					in := mygoui.TextInputBase(c.Key("search"), &a.searchInput).
						Placeholder(t.SearchPlaceholder).
						Label(t.SearchMessages).
						Bind(&a.searchRef).
						Grow(1).
						FontSize(12.5).
						TextColor(zenInk)
					if a.focusSearchPending {
						a.focusSearchPending = false
						a.searchRef.Focus()
					}
					if in.Changed() || in.Submitted() {
						a.state.SetSearchQuery(a.searchInput)
					}
					if a.searchInput != "" {
						clearBtn := mygoui.ButtonBase(c.Key("search-clear")).
							Size(18, 18).
							Radius(99).
							Background(zenTimeChipBg).
							Justify(mygoui.Center).
							AlignItems(mygoui.Center).
							Cursor(mygoui.CursorPointer).
							Label(t.ClearSearch).
							Children(func() {
								mygoui.Icon(c, iconX).Size(11, 11).TextColor(zenInkSecondary)
							})
						if clearBtn.Clicked() {
							a.searchInput = ""
							a.state.SetSearchQuery("")
							a.searchRef.Focus()
						}
					}
				})
		})
}

func (a *App) renderDropOverlayHint(c *mygoui.Context, loc Locale) {
	mygoui.Row(c).
		Padding(8, 16).
		Background(zenAccentSoft).
		BorderWidth(0, 0, 1, 0).
		BorderColor(zenAccentBorder).
		Justify(mygoui.Center).
		Children(func() {
			mygoui.Text(c, tr(loc).DropOverlayHint).FontSize(12).Bold().TextColor(zenAccent)
		})
}

func (a *App) renderToastPill(c *mygoui.Context, snap ViewSnapshot) {
	t := tr(snap.Locale)
	bg := zenInk
	if snap.ToastIsErr {
		bg = zenDangerDark
	}
	mygoui.Row(c).
		Padding(8, 16, 0, 16).
		Justify(mygoui.Center).
		Children(func() {
			mygoui.Row(c).
				Padding(6, 12).
				Gap(8).
				Radius(99).
				Background(bg).
				AlignItems(mygoui.Center).
				MaxWidthPercent(92).
				Shadow(0, 6, 16, 0, mygoui.RGBA(15, 23, 42, 0.16)).
				Children(func() {
					mygoui.Text(c, snap.ToastText).FontSize(12).TextColor(zenSelfBubbleInk).Wrap()
					dismiss := mygoui.ButtonBase(c.Key("toast-dismiss")).
						Size(18, 18).
						Radius(99).
						Background(mygoui.RGBA(255, 255, 255, 0.14)).
						Justify(mygoui.Center).
						AlignItems(mygoui.Center).
						Cursor(mygoui.CursorPointer).
						Label(t.DismissToast).
						Children(func() {
							mygoui.Icon(c, iconX).Size(11, 11).TextColor(zenSelfBubbleInk)
						})
					if dismiss.Clicked() {
						a.state.ClearToast()
					}
				})
		})
}

func (a *App) renderTimeline(c *mygoui.Context, snap ViewSnapshot) {
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
							Background(zenTimeChipBg).
							Children(func() {
								mygoui.Text(c, chip).FontSize(11).TextColor(zenInkMuted)
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
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.05)).
		AlignItems(mygoui.Center).
		Children(func() {
			mygoui.Row(c).
				Size(42, 42).
				Radius(12).
				Background(zenAccentSoft).
				Justify(mygoui.Center).
				AlignItems(mygoui.Center).
				Children(func() {
					mygoui.Icon(c, icon).Size(20, 20).TextColor(zenAccent)
				})
			mygoui.Text(c, title).FontSize(14).Bold().TextColor(zenInk)
			mygoui.Text(c, subtitle).FontSize(12).TextColor(zenInkSecondary).Wrap()
		})
}

func (a *App) renderMessageBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	t := tr(snap.Locale)
	isSelf := snap.Identity.DeviceID != "" && msg.AuthorDeviceID == snap.Identity.DeviceID
	ts := FormatTimestamp(msg.CreatedAt)

	group := mygoui.Column(c.Key("msg-"+msg.MsgID)).
		Gap(4).
		MaxWidthPercent(84)
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
				mygoui.Text(c, label).FontSize(11).TextColor(zenInkMuted)
			} else {
				mygoui.Icon(c, deviceIconForAuthor(msg.AuthorDeviceID, snap.Devices)).
					Size(12, 12).
					TextColor(zenInkMuted)
				author := msg.AuthorName
				if author == "" {
					author = shortID(msg.AuthorDeviceID)
				}
				if ts != "" {
					author += " · " + ts
				}
				mygoui.Text(c, author).FontSize(11).TextColor(zenInkMuted)
			}
		})

		// Bubble body.
		switch msg.Kind {
		case model.MessageKindText:
			if isSelf {
				mygoui.Column(c).
					Padding(10, 14).
					Radius(16, 5, 16, 16).
					Background(zenSelfBubbleBg).
					Shadow(0, 2, 8, 0, mygoui.RGBA(15, 23, 42, 0.10)).
					Children(func() {
						mygoui.Text(c, msg.TextBody).
							FontSize(13.5).
							TextColor(zenSelfBubbleInk).
							Wrap().
							Selectable()
					})
			} else {
				mygoui.Column(c).
					Padding(10, 14).
					Radius(5, 16, 16, 16).
					Background(zenSurface).
					Border(1, zenBorder).
					Shadow(0, 2, 8, 0, mygoui.RGBA(15, 23, 42, 0.05)).
					Children(func() {
						mygoui.Text(c, msg.TextBody).
							FontSize(13.5).
							TextColor(zenInk).
							Wrap().
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
				_ = a.state.DeleteMessage(context.Background(), msgID)
			}
		})
	})
}

func (a *App) renderFileBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	t := tr(snap.Locale)
	fb := msg.File
	if fb == nil {
		mygoui.Column(c).
			Padding(10, 14).
			Radius(16).
			Background(zenSurface).
			Border(1, zenBorder).
			Children(func() {
				mygoui.Text(c, t.FilePrefix+shortID(msg.BlobHash)).FontSize(13).TextColor(zenInkMuted)
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
	fb := msg.File
	_, hasTask := snap.Transfers[fb.BlobHash]
	ext, tileBg, tileFg := zenFileTileColors(fb.FileName)

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
		Padding(12, 14).
		Gap(10).
		Radius(16).
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.06)).
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
						TextColor(zenInk).
						FillWidth().
						SingleLine()
					mygoui.Text(c, meta).
						FontSize(11.5).
						TextColor(zenInkSecondary).
						FillWidth().
						SingleLine()
				})
			}

			if isLocal {
				headerBtn := mygoui.ButtonBase(c.Key("open-direct-" + msg.MsgID))
				hdrBg := mygoui.RGBA(0, 0, 0, 0)
				if headerBtn.Pressed() {
					hdrBg = zenInputFill
				} else if headerBtn.Hovered() {
					hdrBg = zenSurfaceSubtle
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
				barColor := zenAccent
				if task.State == model.TransferPaused {
					barColor = zenInkMuted
				} else if task.State == model.TransferFailed {
					barColor = zenDanger
				}
				mygoui.Row(c).
					Height(6).
					FillWidth().
					Radius(99).
					Background(zenProgressTrack).
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
					TextColor(zenInkMuted).
					Grow(1).
					MinWidth(0).
					SingleLine()

				mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Shrink(0).Children(func() {
					blobHash := fb.BlobHash
					switch {
					case isLocal:
						if zenIconPillBtn(c, "reveal-"+msg.MsgID, iconFolderOpen, t.ShowInFolder, false, false).Clicked() {
							a.revealFileInManager(fb)
						}
					case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
						if zenPillBtn(c, "pause-"+msg.MsgID, t.Pause, false, false).Clicked() {
							_ = a.state.PauseTransfer(blobHash)
						}
						if zenPillBtn(c, "cancel-"+msg.MsgID, t.Cancel, false, false).Clicked() {
							_ = a.state.CancelTransfer(blobHash)
						}
					case hasTask && task.State == model.TransferPaused:
						if zenPillBtn(c, "resume-"+msg.MsgID, t.Resume, true, false).Clicked() {
							_ = a.state.DownloadFile(context.Background(), blobHash)
						}
						if zenPillBtn(c, "cancel-"+msg.MsgID, t.Cancel, false, false).Clicked() {
							_ = a.state.CancelTransfer(blobHash)
						}
					default:
						if zenPillBtn(c, "dl-"+msg.MsgID, t.DownloadNow, true, false).Clicked() {
							_ = a.state.DownloadFile(context.Background(), blobHash)
						}
					}
				})
			})
		})
}

func (a *App) renderComposer(c *mygoui.Context, loc Locale, dragOver bool) {
	t := tr(loc)
	mygoui.Column(c).
		Padding(10, 14, 14, 14).
		Gap(8).
		Background(zenSurface).
		BorderWidth(1, 0, 0, 0).
		BorderColor(zenBorder).
		Children(func() {
			// Dashed drop-zone hint strip.
			hintBg := zenDropHintBg
			hintBorder := zenBorderStrong
			hintFg := zenInkMuted
			hintText := t.DropStripIdle
			if dragOver {
				hintBg = zenAccentSoft
				hintBorder = zenAccent
				hintFg = zenAccent
				hintText = t.DropStripActive
			}
			mygoui.Row(c).
				Padding(5, 10).
				Radius(8).
				Background(hintBg).
				Border(1, hintBorder).
				BorderStyle(mygoui.BorderDashed).
				Justify(mygoui.Center).
				Clip().
				Children(func() {
					mygoui.Text(c, hintText).FontSize(11).TextColor(hintFg).SingleLine()
				})

			// Input row.
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				if zenCircleBtn(c, "btn-attach", iconPlus, t.AttachFiles, false).Clicked() {
					a.triggerAttachFile()
				}
				if zenCircleBtn(c, "btn-paste", iconClipboard, t.PasteClipboard, false).Clicked() {
					a.triggerPasteClipboard()
				}
				if zenCircleBtn(c, "btn-concealed", iconLock, t.PrivateCopy, a.concealedCopy).Clicked() {
					a.concealedCopy = !a.concealedCopy
					if a.concealedCopy {
						a.state.SetToast(t.PrivateCopyOn, false)
					} else {
						a.state.SetToast(t.PrivateCopyOff, false)
					}
				}

				var in mygoui.Element
				mygoui.Row(c).
					Radius(20).
					Background(zenSurface).
					Border(1, zenBorderStrong).
					Padding(8, 14).
					Grow(1).
					AlignItems(mygoui.Center).
					Children(func() {
						in = mygoui.TextInputBase(c.Key("composer"), &a.composer).
							Placeholder(t.ComposerPlace).
							Label(t.ComposerLabel).
							Bind(&a.composerRef).
							FillWidth().
							FontSize(13).
							TextColor(zenInk)
					})

				canSend := strings.TrimSpace(a.composer) != ""
				sendBtn := mygoui.ButtonBase(c.Key("btn-send"))
				sendBg := zenProgressTrack
				sendFg := zenInkMuted
				if canSend {
					sendBg = zenAccent
					sendFg = zenSelfBubbleInk
					if sendBtn.Pressed() {
						sendBg = zenAccentPressed
					} else if sendBtn.Hovered() {
						sendBg = zenAccentHover
					}
				}
				sendBtn.Size(36, 36).
					Radius(99).
					Background(sendBg).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Disabled(!canSend).
					Label(t.Send).
					Tooltip(t.SendTooltip)
				if canSend {
					sendBtn.Cursor(mygoui.CursorPointer)
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

func (a *App) submitComposer() {
	text := strings.TrimSpace(a.composer)
	if text == "" {
		return
	}
	a.composer = ""
	_ = a.state.SendText(context.Background(), text)
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
	go func() {
		paths, err := a.openFileDialog()
		if err != nil {
			a.state.SetToast(tr(a.state.Locale()).OpenDialogFailPref+err.Error(), true)
			return
		}
		if len(paths) > 0 {
			a.sendFiles(paths)
		}
	}()
}

func (a *App) triggerPasteClipboard() {
	if a.readClipboardImage != nil {
		if img := a.readClipboardImage(); len(img) > 0 {
			name := fmt.Sprintf("clipboard-%s.png", time.Now().Format("20060102-150405"))
			_ = a.state.SendBytes(context.Background(), name, "image/png", img)
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
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean == "" || clean == "." {
			continue
		}
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
	t := tr(a.state.Locale())
	path, err := a.resolveFilePath(fb)
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
}

func (a *App) revealFileInManager(fb *model.FileBlob) {
	t := tr(a.state.Locale())
	path, err := a.resolveFilePath(fb)
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
