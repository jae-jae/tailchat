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

// App renders the unified TailChat native GPU interface using mygo/ui.
type App struct {
	state *State
	win   *mygo.Window

	// Main view interactive fields (accessed on UI thread).
	composer      string
	composerRef   mygoui.Handle
	searchInput   string
	searchRef     mygoui.Handle
	concealedCopy bool

	// Modal state.
	pairOpen      bool
	pairCode      string
	pairToken     *model.PairingToken
	joinCodeInput string
	pairErr       string

	devicesOpen bool
	devicesErr  string

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
			Title:    "Select files to send",
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

// OpenPairingModal opens the pairing dialog (also usable from tray menu).
func (a *App) OpenPairingModal() {
	a.pairErr = ""
	a.pairOpen = true
	if a.win != nil {
		a.win.Invalidate()
	}
}

// View builds the full TailChat native UI tree for each frame.
func (a *App) View(c *mygoui.Context) {
	if a.win == nil {
		svc := c.Services()
		a.state.SetInvalidate(svc.Invalidate)
	}

	// Global window shortcuts.
	c.OnShortcut(mygoui.Cmd, mygoui.KeyF, func() {
		a.searchRef.Focus()
	})
	if c.Shortcut(mygoui.Cmd, mygoui.KeyEnter) && strings.TrimSpace(a.composer) != "" && !a.anyModalOpen() {
		a.submitComposer()
	}

	snap := a.state.Snapshot()
	t := c.Theme()

	root := mygoui.Column(c).Fill().Background(t.Background)

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
		root.Border(2, t.Accent)
	}

	root.Children(func() {
		a.renderTopBar(c, snap)

		if dragOver {
			a.renderDropBanner(c)
		}

		if snap.ToastText != "" {
			a.renderToastBanner(c, snap)
		}

		a.renderTimeline(c, snap)
		a.renderComposer(c)
	})

	a.renderPairingModal(c, snap)
	a.renderDevicesModal(c, snap)
	a.renderSettingsModal(c, snap)
}

func (a *App) anyModalOpen() bool {
	return a.pairOpen || a.devicesOpen || a.settingsOpen
}

func (a *App) renderTopBar(c *mygoui.Context, snap ViewSnapshot) {
	t := c.Theme()

	mygoui.Row(c).
		Gap(10).
		AlignItems(mygoui.Center).
		Padding(10, 16).
		Background(t.Surface).
		BorderWidth(0, 0, 1, 0).
		BorderColor(t.Border).
		Children(func() {
			mygoui.Text(c, "TailChat").FontSize(17).Bold()

			// Live connectivity badge: green for Direct, amber for DERP relay, muted for Offline.
			badgeBg := t.Border
			badgeFg := t.TextMuted
			if snap.OnlineCount > 0 {
				if snap.HasDirect {
					badgeBg = t.Success.Alpha(0.18)
					badgeFg = t.Success
				} else {
					badgeBg = t.Warning.Alpha(0.20)
					badgeFg = t.Warning
				}
			}
			mygoui.Row(c).
				Padding(4, 10).
				Radius(12).
				Background(badgeBg).
				AlignItems(mygoui.Center).
				Children(func() {
					mygoui.Text(c, snap.ConnectivityTxt).FontSize(12).Bold().TextColor(badgeFg)
				})

			sf := mygoui.SearchField(c.Key("search"), &a.searchInput).
				Placeholder("Search messages...").
				Label("Search messages").
				Bind(&a.searchRef).
				Grow(1).
				MinWidth(140)
			if sf.Changed() || sf.Submitted() {
				a.state.SetSearchQuery(a.searchInput)
			}

			if mygoui.Button(c.Key("btn-pair"), "Pair").Clicked() {
				a.pairErr = ""
				a.pairOpen = true
			}
			if mygoui.Button(c.Key("btn-devices"), "Devices").Clicked() {
				a.devicesErr = ""
				a.devicesOpen = true
			}
			if mygoui.Button(c.Key("btn-settings"), "Settings").Clicked() {
				a.settingsDraft = NewSettingsDraft(snap.Config)
				a.settingsErr = ""
				a.settingsOpen = true
			}
		})
}

func (a *App) renderDropBanner(c *mygoui.Context) {
	t := c.Theme()
	mygoui.Row(c).
		Padding(8, 16).
		Background(t.Accent.Alpha(0.15)).
		Justify(mygoui.Center).
		Children(func() {
			mygoui.Text(c, "Drop files to send to all paired devices").Bold().TextColor(t.Accent)
		})
}

func (a *App) renderToastBanner(c *mygoui.Context, snap ViewSnapshot) {
	t := c.Theme()
	bg := t.Accent.Alpha(0.14)
	fg := t.Text
	if snap.ToastIsErr {
		bg = t.Danger.Alpha(0.16)
		fg = t.Danger
	}
	mygoui.Row(c).
		Gap(8).
		AlignItems(mygoui.Center).
		Padding(8, 16).
		Background(bg).
		BorderWidth(0, 0, 1, 0).
		BorderColor(t.Border).
		Children(func() {
			mygoui.Text(c, snap.ToastText).FontSize(12).TextColor(fg).Grow(1).Wrap()
			if mygoui.Button(c.Key("toast-dismiss"), "Dismiss").Clicked() {
				a.state.ClearToast()
			}
		})
}

func (a *App) renderTimeline(c *mygoui.Context, snap ViewSnapshot) {
	t := c.Theme()

	msgs := snap.Messages
	if snap.SearchQuery != "" {
		msgs = snap.SearchResults
	}

	mygoui.Scroll(c.Key("timeline-scroll")).Grow(1).Children(func() {
		mygoui.Column(c).Padding(16, 20).Gap(12).FillWidth().Children(func() {
			if len(msgs) == 0 {
				emptyText := "No messages yet. Type a message or drop a file to sync across your devices."
				if snap.SearchQuery != "" {
					emptyText = fmt.Sprintf("No messages matching %q.", snap.SearchQuery)
				}
				mygoui.Row(c).Padding(32, 16).Justify(mygoui.Center).Children(func() {
					mygoui.Text(c, emptyText).TextColor(t.TextMuted)
				})
				return
			}

			for _, msg := range msgs {
				if msg.Kind == model.MessageKindTombstone {
					continue
				}
				a.renderMessageCard(c, snap, msg)
			}
		})
	})
}

func (a *App) renderMessageCard(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	t := c.Theme()
	isSelf := snap.Identity.DeviceID != "" && msg.AuthorDeviceID == snap.Identity.DeviceID

	cardBg := t.Surface
	if isSelf {
		cardBg = t.Accent.Alpha(0.08)
	}

	authorLabel := msg.AuthorName
	if authorLabel == "" {
		authorLabel = shortID(msg.AuthorDeviceID)
	}
	if isSelf {
		authorLabel = authorLabel + " (You)"
	}

	mygoui.Column(c.Key("msg-"+msg.MsgID)).
		Padding(12, 14).
		Gap(8).
		Radius(10).
		Background(cardBg).
		Border(1, t.Border).
		FillWidth().
		Children(func() {
			// Card header: Author badge, timestamp, actions.
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				mygoui.Badge(c, authorLabel)
				if ts := FormatTimestamp(msg.CreatedAt); ts != "" {
					mygoui.Text(c, ts).FontSize(11).TextColor(t.TextMuted)
				}
				mygoui.Spacer(c)

				if msg.Kind == model.MessageKindText {
					copyLabel := "Copy"
					if a.concealedCopy {
						copyLabel = "Copy Private"
					}
					if mygoui.Button(c.Key("copy-"+msg.MsgID), copyLabel).Clicked() {
						a.copyText(msg.TextBody, a.concealedCopy)
					}
				}
				if mygoui.Button(c.Key("del-"+msg.MsgID), "Delete").Clicked() {
					msgID := msg.MsgID
					_ = a.state.DeleteMessage(context.Background(), msgID)
				}
			})

			// Body content.
			switch msg.Kind {
			case model.MessageKindText:
				mygoui.Text(c, msg.TextBody).Wrap().Selectable().FontSize(14)

			case model.MessageKindFile:
				a.renderFileBody(c, snap, msg)
			}
		})
}

func (a *App) renderFileBody(c *mygoui.Context, snap ViewSnapshot, msg model.Message) {
	t := c.Theme()
	fb := msg.File
	if fb == nil {
		mygoui.Text(c, "File "+shortID(msg.BlobHash)).TextColor(t.TextMuted)
		return
	}

	task, hasTask := snap.Transfers[fb.BlobHash]
	isLocal := fb.LocalPath != "" || (hasTask && task.State == model.TransferDone)

	seederCount := len(fb.Seeders)
	seederLabel := fmt.Sprintf("%d seeders", seederCount)
	if seederCount == 1 {
		seederLabel = "1 seeder"
	}

	mygoui.Column(c).Gap(8).FillWidth().Children(func() {
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			mygoui.Column(c).Grow(1).Gap(2).Children(func() {
				mygoui.Text(c, fb.FileName).Bold().FontSize(14)
				meta := fmt.Sprintf("%s · %s", FormatBytes(fb.FileSize), seederLabel)
				if fb.MimeType != "" {
					meta += " · " + fb.MimeType
				}
				mygoui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
			})

			if isLocal {
				mygoui.Row(c).Padding(3, 8).Radius(10).Background(t.Success.Alpha(0.18)).Children(func() {
					mygoui.Text(c, "Local").FontSize(11).Bold().TextColor(t.Success)
				})
			} else {
				mygoui.Row(c).Padding(3, 8).Radius(10).Background(t.Border).Children(func() {
					mygoui.Text(c, "Remote").FontSize(11).TextColor(t.TextMuted)
				})
			}
		})

		if hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued || task.State == model.TransferPaused || task.State == model.TransferFailed) {
			pct := int(task.Progress() * 100)
			mygoui.Column(c).Gap(4).FillWidth().Children(func() {
				mygoui.Progress(c, task.Progress()).FillWidth().Label("Transfer progress")
				statusLine := fmt.Sprintf("%s · %d%% (%s / %s)", string(task.State), pct, FormatBytes(task.BytesDone), FormatBytes(task.BytesTotal))
				if task.LastError != "" {
					statusLine += " — " + task.LastError
				}
				mygoui.Text(c, statusLine).FontSize(11).TextColor(t.TextMuted)
			})
		}

		// File action buttons.
		mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
			blobHash := fb.BlobHash
			switch {
			case isLocal && fb.LocalPath != "":
				if mygoui.PrimaryButton(c.Key("open-"+msg.MsgID), "Open").Clicked() {
					if err := a.openLocalPath(fb.LocalPath); err != nil {
						a.state.SetToast("Cannot open file: "+err.Error(), true)
					}
				}
			case hasTask && (task.State == model.TransferRunning || task.State == model.TransferQueued):
				if mygoui.Button(c.Key("pause-"+msg.MsgID), "Pause").Clicked() {
					_ = a.state.PauseTransfer(blobHash)
				}
				if mygoui.Button(c.Key("cancel-"+msg.MsgID), "Cancel").Clicked() {
					_ = a.state.CancelTransfer(blobHash)
				}
			case hasTask && task.State == model.TransferPaused:
				if mygoui.PrimaryButton(c.Key("resume-"+msg.MsgID), "Resume").Clicked() {
					_ = a.state.DownloadFile(context.Background(), blobHash)
				}
				if mygoui.Button(c.Key("cancel-"+msg.MsgID), "Cancel").Clicked() {
					_ = a.state.CancelTransfer(blobHash)
				}
			default:
				if mygoui.PrimaryButton(c.Key("dl-"+msg.MsgID), "Download").Clicked() {
					_ = a.state.DownloadFile(context.Background(), blobHash)
				}
			}
		})
	})
}

func (a *App) renderComposer(c *mygoui.Context) {
	t := c.Theme()

	mygoui.Column(c).
		Padding(10, 16).
		Gap(8).
		Background(t.Surface).
		BorderWidth(1, 0, 0, 0).
		BorderColor(t.Border).
		Children(func() {
			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				if mygoui.Button(c.Key("btn-attach"), "Attach File").Clicked() {
					a.triggerAttachFile()
				}
				if mygoui.Button(c.Key("btn-paste"), "Paste").Clicked() {
					a.triggerPasteClipboard()
				}
				mygoui.Checkbox(c.Key("chk-concealed"), &a.concealedCopy, "Concealed Copy")
				mygoui.Spacer(c)
				mygoui.Text(c, "Drop files anywhere to send").FontSize(11).TextColor(t.TextMuted)
			})

			mygoui.Row(c).Gap(8).AlignItems(mygoui.Center).Children(func() {
				in := mygoui.TextInput(c.Key("composer"), &a.composer).
					Placeholder("Message your devices...").
					Label("Message input").
					Bind(&a.composerRef).
					Grow(1)

				sendClicked := mygoui.PrimaryButton(c.Key("btn-send"), "Send").
					Disabled(strings.TrimSpace(a.composer) == "").
					Clicked()

				if (sendClicked || in.Submitted()) && strings.TrimSpace(a.composer) != "" {
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
		a.state.SetToast("Copy failed: "+err.Error(), true)
		return
	}
	if concealed {
		a.state.SetToast("Copied (concealed)", false)
	} else {
		a.state.SetToast("Copied to clipboard", false)
	}
}

func (a *App) triggerAttachFile() {
	if a.openFileDialog == nil {
		return
	}
	go func() {
		paths, err := a.openFileDialog()
		if err != nil {
			a.state.SetToast("File picker error: "+err.Error(), true)
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
	a.state.SetToast("Clipboard has no image, file, or text", false)
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

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10] + "..."
}
