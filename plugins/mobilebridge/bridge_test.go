package mobilebridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tailchat/internal/model"
)

type fakeNative struct {
	mu        sync.Mutex
	available bool
	calls     []string
	handlers  map[string]func([]byte)
	outbox    []SharePayload
}

func newFakeNative(available bool) *fakeNative {
	return &fakeNative{
		available: available,
		handlers:  make(map[string]func([]byte)),
	}
}

func (f *fakeNative) Available() bool { return f.available }

func (f *fakeNative) Call(_ context.Context, method string, args, reply any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	if method == "drainOutbox" && reply != nil {
		if dst, ok := reply.(*[]SharePayload); ok {
			*dst = append([]SharePayload(nil), f.outbox...)
			f.outbox = nil
		}
	}
	return nil
}

func (f *fakeNative) On(name string, fn func([]byte)) func() {
	f.mu.Lock()
	f.handlers[name] = fn
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.handlers, name)
		f.mu.Unlock()
	}
}

func (f *fakeNative) emit(name string, v any) {
	raw, _ := json.Marshal(v)
	f.mu.Lock()
	h := f.handlers[name]
	f.mu.Unlock()
	if h != nil {
		h(raw)
	}
}

func (f *fakeNative) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

type stubAppAPI struct {
	mu        sync.Mutex
	active    bool
	transfers []model.TransferTask
	texts     []string
	files     []string
	subs      []chan model.AppEvent
}

func (s *stubAppAPI) Start(context.Context) error                              { return nil }
func (s *stubAppAPI) Close(context.Context) error                              { return nil }
func (s *stubAppAPI) Identity() model.LocalIdentity                            { return model.LocalIdentity{} }
func (s *stubAppAPI) Config() model.AppConfig                                  { return model.AppConfig{} }
func (s *stubAppAPI) UpdateConfig(context.Context, model.AppConfig) error      { return nil }
func (s *stubAppAPI) UpdateDERP(context.Context, model.DERPConfig) error       { return nil }
func (s *stubAppAPI) DeleteMessage(context.Context, string) error              { return nil }
func (s *stubAppAPI) ListMessages(int, string) ([]model.Message, error)        { return nil, nil }
func (s *stubAppAPI) SearchMessages(string, int) ([]model.Message, error)      { return nil, nil }
func (s *stubAppAPI) DownloadFile(context.Context, string) error               { return nil }
func (s *stubAppAPI) PauseTransfer(string) error                               { return nil }
func (s *stubAppAPI) CancelTransfer(string) error                              { return nil }
func (s *stubAppAPI) GetTransfer(string) (*model.TransferTask, error)          { return nil, nil }
func (s *stubAppAPI) ListDevices(bool) ([]model.Device, error)                 { return nil, nil }
func (s *stubAppAPI) PeerStatuses() []model.PeerConnStatus                     { return nil }
func (s *stubAppAPI) CancelPairing() error                                     { return nil }
func (s *stubAppAPI) JoinWithPairingCode(context.Context, string) error        { return nil }
func (s *stubAppAPI) RevokeDevice(context.Context, string) error               { return nil }
func (s *stubAppAPI) CreatePairingCode(context.Context, int) (string, model.PairingToken, error) {
	return "", model.PairingToken{}, nil
}

func (s *stubAppAPI) SendText(_ context.Context, text string) (*model.Message, error) {
	s.mu.Lock()
	s.texts = append(s.texts, text)
	s.mu.Unlock()
	return &model.Message{Kind: model.MessageKindText, TextBody: text}, nil
}

func (s *stubAppAPI) SendFile(_ context.Context, path string) (*model.Message, error) {
	s.mu.Lock()
	s.files = append(s.files, path)
	s.mu.Unlock()
	return &model.Message{Kind: model.MessageKindFile}, nil
}

func (s *stubAppAPI) SendBytes(_ context.Context, _, _ string, _ []byte) (*model.Message, error) {
	return &model.Message{Kind: model.MessageKindFile}, nil
}

func (s *stubAppAPI) ListTransfers() ([]model.TransferTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.TransferTask, len(s.transfers))
	copy(out, s.transfers)
	return out, nil
}

func (s *stubAppAPI) HasActiveTransfers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *stubAppAPI) Subscribe() (<-chan model.AppEvent, func()) {
	ch := make(chan model.AppEvent, 16)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	return ch, func() {}
}

func (s *stubAppAPI) emitTransfer(active bool, done, total int64) {
	s.mu.Lock()
	s.active = active
	state := model.TransferDone
	if active {
		state = model.TransferRunning
	}
	task := model.TransferTask{
		BlobHash:   "blob-1",
		State:      state,
		BytesDone:  done,
		BytesTotal: total,
	}
	s.transfers = []model.TransferTask{task}
	subs := append([]chan model.AppEvent(nil), s.subs...)
	s.mu.Unlock()
	for _, ch := range subs {
		ch <- model.AppEvent{Type: model.EventTransferUpdated, Transfer: &task}
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestForegroundLifecycleAndShareEvents(t *testing.T) {
	api := &stubAppAPI{}
	nat := newFakeNative(true)
	nat.outbox = []SharePayload{{Text: "queued from iOS Share Extension"}}

	ctrl := attachWith(context.Background(), api, nat, false)
	defer ctrl.Close()

	// Outbox should drain on attach.
	waitForCondition(t, time.Second, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.texts) == 1 && api.texts[0] == "queued from iOS Share Extension"
	}, "expected outbox text to be drained on attach")

	// Transfer transitions: false -> true -> update -> false.
	api.emitTransfer(true, 1024, 4096)
	waitForCondition(t, time.Second, func() bool {
		for _, c := range nat.recordedCalls() {
			if c == "startForeground" {
				return true
			}
		}
		return false
	}, "expected startForeground call")

	api.emitTransfer(true, 2048, 4096)
	waitForCondition(t, time.Second, func() bool {
		for _, c := range nat.recordedCalls() {
			if c == "updateForeground" {
				return true
			}
		}
		return false
	}, "expected updateForeground call")

	api.emitTransfer(false, 4096, 4096)
	waitForCondition(t, time.Second, func() bool {
		for _, c := range nat.recordedCalls() {
			if c == "stopForeground" {
				return true
			}
		}
		return false
	}, "expected stopForeground call")

	// Live share event from Android ShareReceiverActivity.
	nat.emit("share", SharePayload{
		Text:  "Shared link",
		Files: []string{"/cache/shared-inbox/photo.jpg"},
	})
	waitForCondition(t, time.Second, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.texts) == 2 && len(api.files) == 1 && api.files[0] == "/cache/shared-inbox/photo.jpg"
	}, "expected live share event to call SendText and SendFile")
}

func TestDesktopNoOpWhenUnavailable(t *testing.T) {
	api := &stubAppAPI{}
	nat := newFakeNative(false)

	ctrl := attachWith(context.Background(), api, nat, false)
	api.emitTransfer(true, 100, 200)
	time.Sleep(30 * time.Millisecond)
	ctrl.Close()

	if calls := nat.recordedCalls(); len(calls) != 0 {
		t.Fatalf("expected zero native calls when unavailable on desktop, got %v", calls)
	}
}

func TestDrainOutboxDir(t *testing.T) {
	dir := t.TempDir()
	itemDir := filepath.Join(dir, "item-001")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(itemDir, "clip.png")
	if err := os.WriteFile(imgPath, []byte("fakepng"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(OutboxManifest{
		ID:        "item-001",
		Text:      "Check out this screenshot",
		Files:     []string{"clip.png"},
		CreatedAt: time.Now().UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(itemDir, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}

	api := &stubAppAPI{}
	n, err := DrainOutboxDir(context.Background(), dir, api)
	if err != nil {
		t.Fatalf("DrainOutboxDir: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 item processed, got %d", n)
	}
	if len(api.texts) != 1 || api.texts[0] != "Check out this screenshot" {
		t.Fatalf("unexpected texts: %v", api.texts)
	}
	if len(api.files) != 1 || api.files[0] != imgPath {
		t.Fatalf("unexpected files: %v", api.files)
	}
	if _, err := os.Stat(itemDir); !os.IsNotExist(err) {
		t.Fatalf("expected consumed item directory to be removed")
	}
}
