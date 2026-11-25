package ui

import (
	"context"
	"fmt"
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
		app.Close()
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
		if runtime.GOOS == "darwin" && len(dockIconPNG) > 0 {
			if err := mygo.App.Dock.SetIcon(dockIconPNG); err != nil {
				log.Printf("tailchat: dock icon warning: %v", err)
			}
		}
		state.Start(rootCtx)

		w := mygo.NewWindow(mygo.WindowOptions{
			Title:                "TailChat",
			Width:                540,
			Height:               740,
			MinWidth:             420,
			MinHeight:            540,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			HideWindowButtons:    runtime.GOOS == "linux",
			TrafficLightPosition: &mygo.Point{X: 16, Y: 22},
			BackgroundColor:      "#F3F5F9",
			StateKey:             "tailchat-zen-v1",
			Content:              mygoui.View(app.View),
		})
		mu.Lock()
		win = w
		mu.Unlock()

		app.SetWindow(w)
		state.SetInvalidate(w.Invalidate)

		if runtime.GOOS == "linux" {
			wb := mygo.Theme.WindowButtons()
			log.Printf("tailchat: linux window buttons layout: start=%v end=%v", wb.Start, wb.End)
			app.SetCustomWindowButtons(wb)
			mygo.Theme.OnUpdated(func() {
				app.SetCustomWindowButtons(mygo.Theme.WindowButtons())
			})
			w.OnMaximize(w.Invalidate)
			w.OnUnmaximize(w.Invalidate)
		}

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
			onQuickPair := func() {
				showAndFocus()
				app.OpenPairingModal()
			}
			if t := setupSystemTray(state.Locale(), toggleWindow, onQuickPair); t != nil {
				mu.Lock()
				tray = t
				mu.Unlock()

				state.SetOnConfigChanged(func(cfg model.AppConfig) {
					loc := ResolveLocale(cfg.Language)
					mygo.RunOnMain(func() {
						mu.Lock()
						curTray := tray
						isClosed := closed
						mu.Unlock()
						if curTray == nil || isClosed {
							return
						}
						curTray.SetMenu(buildTrayMenu(loc, toggleWindow, onQuickPair))
						curTray.SetToolTip(tr(loc).TrayTooltip)
					})
				})
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
			t := tr(state.Locale())
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
					body = fmt.Sprintf(t.NotifySharedFileF, msg.File.FileName, FormatBytes(msg.File.FileSize))
				}
				if body == "" {
					body = t.NotifyNewMessage
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

		if isDesktopPlatform() {
			go func() {
				select {
				case <-time.After(3 * time.Second):
				case <-rootCtx.Done():
					return
				}
				state.CheckForUpdates(rootCtx, false)

				ticker := time.NewTicker(12 * time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						state.CheckForUpdates(rootCtx, false)
					case <-rootCtx.Done():
						return
					}
				}
			}()
		}

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

func buildTrayMenu(loc Locale, onToggle func(), onQuickPair func()) *mygo.Menu {
	t := tr(loc)
	return mygo.NewMenu([]*mygo.MenuItem{
		{
			ID:    "toggle",
			Label: t.TrayShowHide,
			Click: func(*mygo.MenuItem, *mygo.Window) { onToggle() },
		},
		{
			ID:    "pair",
			Label: t.TrayQuickPair,
			Click: func(*mygo.MenuItem, *mygo.Window) { onQuickPair() },
		},
		mygo.Separator(),
		{
			ID:    "quit",
			Label: t.TrayQuit,
			Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() },
		},
	})
}

func setupSystemTray(loc Locale, onToggle func(), onQuickPair func()) *mygo.Tray {
	opts := mygo.TrayOptions{
		ID:             "tailchat-tray",
		Icon:           trayIconPNG(),
		IconIsTemplate: runtime.GOOS == "darwin",
		ToolTip:        tr(loc).TrayTooltip,
		Menu:           buildTrayMenu(loc, onToggle, onQuickPair),
	}
	t, err := mygo.NewTray(opts)
	if err != nil {
		log.Printf("tailchat: system tray unavailable: %v", err)
		return nil
	}
	return t
}

// trayIconPNG returns the embedded 32x32 Retina (@2x for 16pt) TailChat
// Option A chat-bubble tray icon. macOS uses the monochrome template asset
// so AppKit adapts automatically to light/dark menu bars; Windows and Linux
// use the full-color cobalt-blue variant.
func trayIconPNG() []byte {
	if runtime.GOOS == "darwin" {
		return trayIconTemplatePNG
	}
	return trayIconColorPNG
}
