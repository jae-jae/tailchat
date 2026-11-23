package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

	// Settings & DERP modal state.
	settingsOpen  bool
	settingsDraft SettingsDraft
	settingsErr   string

	// Platform integration hooks (overridable in headless tests).
	openFileDialog     func() ([]string, error)
	readClipboardImage func() []byte
	readClipboardFiles func() ([]string, error)
	readClipboardText  func() string
	writeClipboardText func(text string, concealed bool) error
	openLocalPath      func(path string) error
}

// NewApp constructs a TailChat UI controller bound to state.
func NewApp(state *State) *App {
	a := &App{
		state: state,
	}
	a.openFileDialog = func() ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:   a.win,
			Title:    "选择要发送的文件",
			Multiple: true,
		})
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
	return a
}

// SetWindow attaches the host desktop window to this App.
func (a *App) SetWindow(w *mygo.Window) {
	a.win = w
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
		a.state.SetToast("已刷新配对二维码", false)
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

		if a.searchOpen || a.searchInput != "" {
			a.renderSearchDrawer(c)
		}

		if dragOver {
			a.renderDropOverlayHint(c)
		}

		if snap.ToastText != "" {
			a.renderToastPill(c, snap)
		}

		a.renderTimeline(c, snap)
		a.renderComposer(c, dragOver)
	})

	a.renderDevicesPopover(c, devicesBtn, snap)
	a.renderPairingModal(c, snap)
	a.renderSettingsModal(c, snap)
}

func (a *App) anyOverlayOpen() bool {
	return a.devicesPopoverOpen || a.pairOpen || a.settingsOpen
}

func (a *App) renderHeader(c *mygoui.Context, snap ViewSnapshot) mygoui.Element {
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
						NoWrap().
						Ellipsis("…")
				})
			})

			// Right action bar: Search, Devices, Settings, and dedicated Pair CTA pill.
			mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
				searchActive := a.searchOpen || a.searchInput != ""
				if zenCircleBtn(c, "btn-search", iconSearch, "搜索消息", searchActive).Clicked() {
					a.searchOpen = !a.searchOpen
					if a.searchOpen {
						a.focusSearchPending = true
					} else if a.searchInput != "" {
						a.searchInput = ""
						a.state.SetSearchQuery("")
					}
				}

				devicesBtn = zenCircleBtn(c, "btn-devices", iconSmartphone, "已配对设备", a.devicesPopoverOpen)
				if devicesBtn.Clicked() {
					a.devicesPopoverOpen = !a.devicesPopoverOpen
					if a.devicesPopoverOpen {
						a.pairOpen = false
						a.settingsOpen = false
						a.devicesErr = ""
					}
				}

				if zenCircleBtn(c, "btn-settings", iconSettings, "设置与 DERP", a.settingsOpen).Clicked() {
					a.settingsDraft = NewSettingsDraft(snap.Config)
					a.settingsErr = ""
					a.devicesPopoverOpen = false
					a.pairOpen = false
					a.settingsOpen = true
				}

				if zenPairHeaderBtn(c, a.pairOpen).Clicked() {
					a.openPairingFlow(0)
				}
			})
		})

	return devicesBtn
}

func (a *App) renderSearchDrawer(c *mygoui.Context) {
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
						Placeholder("搜索文字或文件名…").
						Label("搜索消息").
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
							Label("清除搜索").
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

func (a *App) renderDropOverlayHint(c *mygoui.Context) {
	mygoui.Row(c).
		Padding(8, 16).
		Background(zenAccentSoft).
		BorderWidth(0, 0, 1, 0).
		BorderColor(zenAccentBorder).
		Justify(mygoui.Center).
		Children(func() {
			mygoui.Text(c, "松开鼠标，将文件广播给所有已配对设备").FontSize(12).Bold().TextColor(zenAccent)
		})
}

func (a *App) renderToastPill(c *mygoui.Context, snap ViewSnapshot) {
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
						Label("关闭提示").
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
				a.renderEmptyState(c, snap.SearchQuery)
				return
			}

			var prevTS int64
			for _, msg := range msgs {
				if msg.Kind == model.MessageKindTombstone {
					continue
				}
				if msg.CreatedAt > 0 && (prevTS == 0 || msg.CreatedAt-prevTS > timeChipGapMs) {
					if chip := FormatTimeChip(msg.CreatedAt); chip != "" {
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

func (a *App) renderEmptyState(c *mygoui.Context, searchQuery string) {
	title := "随时跨设备同步文字与文件"
	subtitle := "在下方输入文字、直接拖入任意文件到此窗口，或点击右上角「配对」按钮快速配对。"
	icon := iconLaptop
	if searchQuery != "" {
		title = fmt.Sprintf("未找到包含 %q 的消息", searchQuery)
		subtitle = "试试更换关键词或文件名重新搜索。"
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
				label := "本机"
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
				copyLabel := "复制"
				if a.concealedCopy {
					copyLabel = "隐私复制"
				}
				if zenMicroBtn(c, "copy-"+msg.MsgID, iconCopy, copyLabel, false).Clicked() {
					a.copyText(msg.TextBody, a.concealedCopy)
				}
			}
			if zenMicroBtn(c, "del-"+msg.MsgID, iconTrash, "删除", true).Clicked() {
				msgID := msg.MsgID
				_ = a.state.DeleteMessage(context.Background(), msgID)
			}
		})
	})
}

func (a *App) renderFileBubble(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	fb := msg.File
	if fb == nil {
		mygoui.Column(c).
			Padding(10, 14).
			Radius(16).
			Background(zenSurface).
			Border(1, zenBorder).
			Children(func() {
				mygoui.Text(c, "文件 "+shortID(msg.BlobHash)).FontSize(13).TextColor(zenInkMuted)
			})
		return
	}

	task, hasTask := snap.Transfers[fb.BlobHash]
	isLocal := fb.LocalPath != "" || (hasTask && task.State == model.TransferDone)
	showProgress := hasTask && (task.State == model.TransferRunning ||
		task.State == model.TransferQueued ||
		task.State == model.TransferPaused ||
		task.State == model.TransferFailed)

	ext, tileBg, tileFg := zenFileTileColors(fb.FileName)

	var meta string
	switch {
	case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
		pct := int(task.Progress() * 100)
		meta = fmt.Sprintf("%s / %s · %d%%", FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal), pct)
	case hasTask && task.State == model.TransferPaused:
		meta = fmt.Sprintf("%s / %s · 已暂停", FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal))
	case hasTask && task.State == model.TransferFailed:
		meta = fmt.Sprintf("%s · 传输失败", FormatBytes(fb.FileSize))
		if task.LastError != "" {
			meta += " · " + task.LastError
		}
	default:
		seederCount := len(fb.Seeders)
		if seederCount > 0 {
			meta = fmt.Sprintf("%s · %d 台设备有源", FormatBytes(fb.FileSize), seederCount)
		} else {
			meta = FormatBytes(fb.FileSize)
		}
	}

	footText := "点击按需下载"
	switch {
	case isLocal:
		footText = "BLAKE3 校验通过"
	case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued || task.State == model.TransferPaused):
		footText = "支持断点续传"
	case hasTask && task.State == model.TransferFailed:
		footText = "传输中断 · 可重新下载"
	}

	mygoui.Column(c).
		Width(332).
		Padding(12, 14).
		Gap(10).
		Radius(16).
		Background(zenSurface).
		Border(1, zenBorder).
		Shadow(0, 3, 12, 0, mygoui.RGBA(15, 23, 42, 0.06)).
		Children(func() {
			// File header: extension tile + filename & metadata.
			mygoui.Row(c).Gap(11).AlignItems(mygoui.Center).FillWidth().Children(func() {
				mygoui.Row(c).
					Size(44, 44).
					Radius(12).
					Background(tileBg).
					Justify(mygoui.Center).
					AlignItems(mygoui.Center).
					Children(func() {
						mygoui.Text(c, ext).
							FontSize(11).
							Bold().
							Font("SF Mono, Menlo, monospace").
							TextColor(tileFg)
					})

				mygoui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					mygoui.Text(c, fb.FileName).
						FontSize(13.5).
						FontWeight(600).
						TextColor(zenInk).
						NoWrap().
						Ellipsis("…")
					mygoui.Text(c, meta).
						FontSize(11.5).
						TextColor(zenInkSecondary).
						NoWrap().
						Ellipsis("…")
				})
			})

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
					Label("传输进度").
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
					NoWrap().
					Ellipsis("…")

				mygoui.Row(c).Gap(6).AlignItems(mygoui.Center).Children(func() {
					blobHash := fb.BlobHash
					switch {
					case isLocal && fb.LocalPath != "":
						if zenPillBtn(c, "open-"+msg.MsgID, "打开文件", false, false).Clicked() {
							if err := a.openLocalPath(fb.LocalPath); err != nil {
								a.state.SetToast("无法打开文件："+err.Error(), true)
							}
						}
					case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
						if zenPillBtn(c, "pause-"+msg.MsgID, "暂停", false, false).Clicked() {
							_ = a.state.PauseTransfer(blobHash)
						}
						if zenPillBtn(c, "cancel-"+msg.MsgID, "取消", false, false).Clicked() {
							_ = a.state.CancelTransfer(blobHash)
						}
					case hasTask && task.State == model.TransferPaused:
						if zenPillBtn(c, "resume-"+msg.MsgID, "继续", true, false).Clicked() {
							_ = a.state.DownloadFile(context.Background(), blobHash)
						}
						if zenPillBtn(c, "cancel-"+msg.MsgID, "取消", false, false).Clicked() {
							_ = a.state.CancelTransfer(blobHash)
						}
					default:
						if zenPillBtn(c, "dl-"+msg.MsgID, "立即下载", true, false).Clicked() {
							_ = a.state.DownloadFile(context.Background(), blobHash)
						}
					}
				})
			})
		})
}

func (a *App) renderComposer(c *mygoui.Context, dragOver bool) {
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
			hintText := "拖入任意文件到此窗口，或点击粘贴截图广播给所有设备"
			if dragOver {
				hintBg = zenAccentSoft
				hintBorder = zenAccent
				hintFg = zenAccent
				hintText = "松开鼠标立即广播文件给所有设备"
			}
			mygoui.Row(c).
				Padding(5, 10).
				Radius(8).
				Background(hintBg).
				Border(1, hintBorder).
				BorderStyle(mygoui.BorderDashed).
				Justify(mygoui.Center).
				Children(func() {
					mygoui.Text(c, hintText).FontSize(11).TextColor(hintFg).NoWrap().Ellipsis("…")
				})

			// Input row.
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				if zenCircleBtn(c, "btn-attach", iconPlus, "选择文件", false).Clicked() {
					a.triggerAttachFile()
				}
				if zenCircleBtn(c, "btn-paste", iconClipboard, "粘贴剪贴板", false).Clicked() {
					a.triggerPasteClipboard()
				}
				if zenCircleBtn(c, "btn-concealed", iconLock, "隐私复制", a.concealedCopy).Clicked() {
					a.concealedCopy = !a.concealedCopy
					if a.concealedCopy {
						a.state.SetToast("已开启隐私复制（不写入剪贴板历史）", false)
					} else {
						a.state.SetToast("已关闭隐私复制", false)
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
							Placeholder("发一段文字给其他设备…").
							Label("输入消息").
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
					Label("发送").
					Tooltip("发送消息 (Enter)")
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
	if err := a.writeClipboardText(text, concealed); err != nil {
		a.state.SetToast("复制失败："+err.Error(), true)
		return
	}
	if concealed {
		a.state.SetToast("已隐私复制（不留历史）", false)
	} else {
		a.state.SetToast("已复制到剪贴板", false)
	}
}

func (a *App) triggerAttachFile() {
	if a.openFileDialog == nil {
		return
	}
	go func() {
		paths, err := a.openFileDialog()
		if err != nil {
			a.state.SetToast("打开文件选择器失败："+err.Error(), true)
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
	a.state.SetToast("剪贴板中没有可发送的图片、文件或文字", false)
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
