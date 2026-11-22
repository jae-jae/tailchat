package mobilebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"tailchat/internal/model"
)

const (
	// PluginID matches mobile.id in mygo-plugin.json.
	PluginID = "mobilebridge"
	// AppGroupID is the shared iOS App Group identifier between TailChat and ShareExtension.
	AppGroupID = "group.dev.tailchat.shared"
	// FlushOutboxURL is the deep link opened by the iOS Share Extension ("Send Now").
	FlushOutboxURL = "tailchat://flush-outbox"
)

// SharePayload represents text and/or local file paths forwarded from Android ACTION_SEND
// or drained from the iOS App Group outbox.
type SharePayload struct {
	ID     string   `json:"id,omitempty"`
	Text   string   `json:"text,omitempty"`
	Files  []string `json:"files,omitempty"`
	Source string   `json:"source,omitempty"`
}

// ForegroundStatus describes the aggregate active transfer status passed to Android's
// TransferForegroundService notification.
type ForegroundStatus struct {
	Title       string `json:"title"`
	Detail      string `json:"detail"`
	ActiveCount int    `json:"active_count"`
	ProgressPct int    `json:"progress_pct"`
	BytesDone   int64  `json:"bytes_done"`
	BytesTotal  int64  `json:"bytes_total"`
}

// nativeTransport abstracts mygo.NativePlugin so bridge logic can be unit-tested on desktop.
type nativeTransport interface {
	Available() bool
	Call(ctx context.Context, method string, args, reply any) error
	On(name string, fn func(data []byte)) (off func())
}

type mygoNativeAdapter struct {
	p *mygo.NativePlugin
}

func (a mygoNativeAdapter) Available() bool { return a.p.Available() }
func (a mygoNativeAdapter) Call(ctx context.Context, method string, args, reply any) error {
	return a.p.Call(ctx, method, args, reply)
}
func (a mygoNativeAdapter) On(name string, fn func(data []byte)) func() {
	return a.p.On(name, fn)
}

// Controller bridges model.AppAPI lifecycle and transfer events with native iOS/Android
// plugins while remaining a safe no-op on desktop platforms.
type Controller struct {
	api    model.AppAPI
	native nativeTransport

	mu          sync.Mutex
	fgRunning   bool
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
	unsubs      []func()
}

// Attach connects TailChat's AppAPI to the native mobilebridge plugin and app URL/resume hooks.
// On desktop builds where the native mobile plugin is unavailable, native calls are safely no-ops.
func Attach(ctx context.Context, api model.AppAPI) *Controller {
	return attachWith(ctx, api, mygoNativeAdapter{p: mygo.Native(PluginID)}, true)
}

func attachWith(ctx context.Context, api model.AppAPI, native nativeTransport, registerAppHooks bool) *Controller {
	loopCtx, cancel := context.WithCancel(ctx)
	c := &Controller{
		api:    api,
		native: native,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// Listen for push share events from Android ShareReceiverActivity or iOS plugin.
	if off := native.On("share", func(data []byte) {
		var payload SharePayload
		if err := json.Unmarshal(data, &payload); err != nil {
			log.Printf("mobilebridge: invalid share event: %v", err)
			return
		}
		go c.ingestPayload(loopCtx, payload)
	}); off != nil {
		c.unsubs = append(c.unsubs, off)
	}

	if registerAppHooks {
		if off := mygo.App.OnOpenURL(func(rawURL string) {
			if strings.HasPrefix(strings.ToLower(rawURL), FlushOutboxURL) {
				go c.FlushOutbox(loopCtx)
			}
		}); off != nil {
			c.unsubs = append(c.unsubs, off)
		}
		if off := mygo.App.OnDidBecomeActive(func() {
			go c.FlushOutbox(loopCtx)
		}); off != nil {
			c.unsubs = append(c.unsubs, off)
		}
	}

	ch, unsubEvents := api.Subscribe()
	c.unsubs = append(c.unsubs, unsubEvents)

	// Drain any shares queued before Go finished booting.
	if native.Available() {
		go c.FlushOutbox(loopCtx)
	}

	// Sync initial foreground transfer state.
	c.syncForegroundState(loopCtx)

	go func() {
		defer close(c.done)
		for {
			select {
			case <-loopCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Type == model.EventTransferUpdated {
					c.syncForegroundState(loopCtx)
				}
			}
		}
	}()

	return c
}

// Close detaches listeners and stops any active foreground service notification.
func (c *Controller) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	wasFG := c.fgRunning
	c.fgRunning = false
	cancel := c.cancel
	unsubs := append([]func(){}, c.unsubs...)
	c.unsubs = nil
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, off := range unsubs {
		if off != nil {
			off()
		}
	}
	<-c.done

	if wasFG && c.native.Available() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = c.native.Call(stopCtx, "stopForeground", nil, nil)
		_ = c.native.Call(stopCtx, "setActiveTransfers", map[string]any{"active": false}, nil)
	}
}

// FlushOutbox asks the native plugin to drain pending items queued by the iOS Share Extension
// (App Group outbox/) or Android ShareReceiverActivity before Go was ready.
func (c *Controller) FlushOutbox(ctx context.Context) error {
	if !c.native.Available() {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var items []SharePayload
	if err := c.native.Call(callCtx, "drainOutbox", nil, &items); err != nil {
		return err
	}
	for _, item := range items {
		c.ingestPayload(ctx, item)
	}
	return nil
}

func (c *Controller) ingestPayload(ctx context.Context, payload SharePayload) {
	if text := strings.TrimSpace(payload.Text); text != "" {
		if _, err := c.api.SendText(ctx, text); err != nil {
			log.Printf("mobilebridge: SendText failed: %v", err)
		}
	}
	for _, f := range payload.Files {
		clean := strings.TrimSpace(f)
		if clean == "" {
			continue
		}
		if _, err := c.api.SendFile(ctx, clean); err != nil {
			log.Printf("mobilebridge: SendFile(%q) failed: %v", clean, err)
		}
	}
}

func (c *Controller) syncForegroundState(ctx context.Context) {
	active := c.api.HasActiveTransfers()
	status := c.computeStatus()

	c.mu.Lock()
	wasRunning := c.fgRunning
	c.fgRunning = active
	c.mu.Unlock()

	if !c.native.Available() {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if active != wasRunning {
		_ = c.native.Call(callCtx, "setActiveTransfers", map[string]any{"active": active}, nil)
	}

	switch {
	case active && !wasRunning:
		if err := c.native.Call(callCtx, "startForeground", status, nil); err != nil {
			log.Printf("mobilebridge: startForeground failed: %v", err)
		}
	case active && wasRunning:
		if err := c.native.Call(callCtx, "updateForeground", status, nil); err != nil {
			log.Printf("mobilebridge: updateForeground failed: %v", err)
		}
	case !active && wasRunning:
		if err := c.native.Call(callCtx, "stopForeground", nil, nil); err != nil {
			log.Printf("mobilebridge: stopForeground failed: %v", err)
		}
	}
}

func (c *Controller) computeStatus() ForegroundStatus {
	tasks, err := c.api.ListTransfers()
	if err != nil {
		return ForegroundStatus{Title: "TailChat", Detail: "Syncing files..."}
	}
	var activeCount int
	var bytesDone, bytesTotal int64
	for _, t := range tasks {
		if t.State == model.TransferRunning || t.State == model.TransferQueued {
			activeCount++
			bytesDone += t.BytesDone
			bytesTotal += t.BytesTotal
		}
	}
	pct := 0
	if bytesTotal > 0 {
		pct = int((bytesDone * 100) / bytesTotal)
		if pct > 100 {
			pct = 100
		}
	}
	detail := fmt.Sprintf("Transferring %d file(s) · %d%%", activeCount, pct)
	if activeCount == 0 {
		detail = "Idle"
	}
	return ForegroundStatus{
		Title:       "TailChat Transfer",
		Detail:      detail,
		ActiveCount: activeCount,
		ProgressPct: pct,
		BytesDone:   bytesDone,
		BytesTotal:  bytesTotal,
	}
}

// OutboxManifest is the JSON schema written by iOS ShareExtension into outbox/<uuid>/manifest.json.
type OutboxManifest struct {
	ID        string   `json:"id"`
	Text      string   `json:"text,omitempty"`
	Files     []string `json:"files,omitempty"` // Relative filenames inside outbox/<uuid>/
	CreatedAt int64    `json:"created_at"`
}

// DrainOutboxDir reads all queued subdirectories under outboxDir (each containing manifest.json),
// submits their text and files via api, and removes processed folders.
func DrainOutboxDir(ctx context.Context, outboxDir string, api model.AppAPI) (int, error) {
	entries, err := os.ReadDir(outboxDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		itemDir := filepath.Join(outboxDir, entry.Name())
		manifestPath := filepath.Join(itemDir, "manifest.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var m OutboxManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		var sendErr error
		if txt := strings.TrimSpace(m.Text); txt != "" {
			if _, err := api.SendText(ctx, txt); err != nil {
				sendErr = err
			}
		}
		for _, rel := range m.Files {
			cleanRel := filepath.Base(rel)
			if cleanRel == "" || cleanRel == "." || cleanRel == ".." {
				continue
			}
			fullPath := filepath.Join(itemDir, cleanRel)
			if _, err := api.SendFile(ctx, fullPath); err != nil {
				sendErr = err
			}
		}
		if sendErr == nil {
			_ = os.RemoveAll(itemDir)
			processed++
		}
	}
	return processed, nil
}
