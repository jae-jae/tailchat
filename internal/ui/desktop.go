package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
)

const defaultGlobalHotkey = "CmdOrCtrl+Shift+T"

// Run boots the native MyGo desktop/mobile GUI backed by api.
// onReady, if non-nil, is invoked once the application and main window are initialized.
func Run(api model.AppAPI, onReady func()) error {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	state := NewState(api)
	app := NewApp(state)

	var (
		mu       sync.Mutex
		win      *mygo.Window
		tray     *mygo.Tray
		quitting bool
		closed   bool
	)

	showAndFocus := func() {
		mu.Lock()
		w := win
		mu.Unlock()
		if w == nil {
			return
		}
		w.Show()
		w.Focus()
		mygo.App.Focus()
	}

	toggleWindow := func() {
		mu.Lock()
		w := win
		mu.Unlock()
		if w == nil {
			return
		}
		if w.IsVisible() && w.IsFocused() {
			w.Hide()
			return
		}
		w.Show()
		w.Focus()
		mygo.App.Focus()
	}

	shutdownOnce := func() {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		closed = true
		quitting = true
		t := tray
		mu.Unlock()

		mygo.GlobalShortcut.UnregisterAll()
		if t != nil {
			t.Destroy()
		}
		state.Stop()
		cancelRoot()

		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := api.Close(closeCtx); err != nil {
			log.Printf("tailchat: graceful close warning: %v", err)
		}
	}

	mygo.App.SetName("TailChat")
	// Keep the app alive in the system tray when windows are hidden or closed.
	mygo.App.OnWindowAllClosed(func() {})

	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		shutdownOnce()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		shutdownOnce()
	})

	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			showAndFocus()
		}
	})

	mygo.App.WhenReady(func() {
		state.Start(rootCtx)

		w := mygo.NewWindow(mygo.WindowOptions{
			Title:     "TailChat",
			Width:     960,
			Height:    680,
			MinWidth:  640,
			MinHeight: 480,
			StateKey:  "tailchat-main",
			Content:   mygoui.View(app.View),
		})
		mu.Lock()
		win = w
		mu.Unlock()

		app.SetWindow(w)
		state.SetInvalidate(w.Invalidate)

		w.OnClose(func(e *mygo.CloseEvent) {
			mu.Lock()
			isQuitting := quitting
			mu.Unlock()
			if !isQuitting && state.Config().MinimizeToTray && isDesktopPlatform() {
				e.PreventDefault()
				w.Hide()
			}
		})

		// Desktop-only integrations: system tray & global hotkey.
		if isDesktopPlatform() {
			if t := setupSystemTray(toggleWindow, func() {
				showAndFocus()
				app.OpenPairingModal()
			}); t != nil {
				mu.Lock()
				tray = t
				mu.Unlock()
			}

			hotkey := state.Config().GlobalHotkey
			if hotkey == "" {
				hotkey = defaultGlobalHotkey
			}
			if err := mygo.GlobalShortcut.Register(hotkey, showAndFocus); err != nil {
				log.Printf("tailchat: global shortcut %q unavailable: %v", hotkey, err)
			}
		}

		// Local desktop notification when a remote message arrives while unfocused.
		state.SetOnRemoteMessage(func(msg model.Message) {
			if !mygo.NotificationsSupported() {
				return
			}
			mygo.RunOnMain(func() {
				if w.IsVisible() && w.IsFocused() {
					return
				}
				title := "TailChat"
				if msg.AuthorName != "" {
					title = fmt.Sprintf("TailChat · %s", msg.AuthorName)
				}
				body := msg.TextBody
				if msg.Kind == model.MessageKindFile && msg.File != nil {
					body = fmt.Sprintf("Shared file: %s (%s)", msg.File.FileName, FormatBytes(msg.File.FileSize))
				}
				if body == "" {
					body = "New message received"
				}
				n := mygo.NewNotification(mygo.NotificationOptions{
					Title: title,
					Body:  body,
					Group: "tailchat-messages",
				})
				n.OnClick(showAndFocus)
				if err := n.Show(); err != nil {
					log.Printf("tailchat: notification failed: %v", err)
				}
			})
		})

		if onReady != nil {
			onReady()
		}
	})

	err := mygo.App.Run()
	shutdownOnce()
	return err
}

func isDesktopPlatform() bool {
	return runtime.GOOS != "ios" && runtime.GOOS != "android"
}

func setupSystemTray(onToggle func(), onQuickPair func()) *mygo.Tray {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{
			ID:    "toggle",
			Label: "Show / Hide TailChat",
			Click: func(*mygo.MenuItem, *mygo.Window) { onToggle() },
		},
		{
			ID:    "pair",
			Label: "Quick Pair...",
			Click: func(*mygo.MenuItem, *mygo.Window) { onQuickPair() },
		},
		mygo.Separator(),
		{
			ID:    "quit",
			Label: "Quit TailChat",
			Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() },
		},
	})

	opts := mygo.TrayOptions{
		ID:             "tailchat-tray",
		Icon:           renderTrayIconPNG(),
		IconIsTemplate: runtime.GOOS == "darwin",
		ToolTip:        "TailChat — Private P2P Device Mesh",
		Menu:           menu,
	}
	t, err := mygo.NewTray(opts)
	if err != nil {
		log.Printf("tailchat: system tray unavailable: %v", err)
		return nil
	}
	return t
}

// renderTrayIconPNG generates a crisp 32x32 PNG chat-bubble template icon in memory.
func renderTrayIconPNG() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	ink := color.NRGBA{R: 56, G: 130, B: 246, A: 255}
	if runtime.GOOS == "darwin" {
		ink = color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	}
	uniform := image.NewUniform(ink)
	// Outer chat bubble border + tail.
	rects := []image.Rectangle{
		image.Rect(4, 5, 28, 8),
		image.Rect(4, 21, 28, 24),
		image.Rect(4, 8, 7, 21),
		image.Rect(25, 8, 28, 21),
		image.Rect(8, 24, 13, 28),
		image.Rect(10, 12, 22, 14),
		image.Rect(10, 16, 18, 18),
	}
	for _, r := range rects {
		draw.Draw(img, r, uniform, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
