package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
)

type mockAppAPI struct {
	mu        sync.Mutex
	identity  model.LocalIdentity
	config    model.AppConfig
	messages  []model.Message
	devices   []model.Device
	peers     []model.PeerConnStatus
	transfers map[string]model.TransferTask
	subs      map[int]chan model.AppEvent
	nextSubID int
	nextMsgNo int

	joinedCodes    []string
	revokedIDs     []string
	updatedDERPs   []model.DERPConfig
	downloadedHash []string
	sentFiles      []string
	lastSearch     string
}

func newMockAppAPI() *mockAppAPI {
	now := time.Now().UnixMilli()
	m := &mockAppAPI{
		identity: model.LocalIdentity{
			DeviceID:   "dev-self-001",
			DeviceName: "MacBook Pro",
			Platform:   "macos",
			GroupID:    "grp-alpha",
			RouteEpoch: 3,
		},
		config: model.AppConfig{
			DeviceName:           "MacBook Pro",
			DERP:                 model.DefaultDERPConfig(),
			AutoDownloadMaxBytes: 20 * 1024 * 1024,
			MaxDownloadBytesPerS: 0,
			MinimizeToTray:       true,
			GlobalHotkey:         "CmdOrCtrl+Shift+T",
		},
		messages: []model.Message{
			{
				MsgID:          "msg-001",
				AuthorDeviceID: "dev-peer-002",
				AuthorName:     "iPhone 16",
				AuthorSeq:      1,
				Kind:           model.MessageKindText,
				TextBody:       "Hello from iPhone over Tailcat!",
				CreatedAt:      now - 60_000,
			},
			{
				MsgID:          "msg-002",
				AuthorDeviceID: "dev-peer-002",
				AuthorName:     "iPhone 16",
				AuthorSeq:      2,
				Kind:           model.MessageKindFile,
				BlobHash:       "blake3-abc123",
				CreatedAt:      now - 30_000,
				File: &model.FileBlob{
					BlobHash:   "blake3-abc123",
					FileName:   "design-spec.pdf",
					FileSize:   4 * 1024 * 1024,
					MimeType:   "application/pdf",
					ChunkSize:  model.DefaultChunkSize,
					ChunkCount: 4,
					Seeders:    []string{"dev-peer-002"},
				},
			},
		},
		devices: []model.Device{
			{
				DeviceID:   "dev-self-001",
				Name:       "MacBook Pro",
				Platform:   "macos",
				RouteEpoch: 3,
				IsSelf:     true,
			},
			{
				DeviceID:   "dev-peer-002",
				Name:       "iPhone 16",
				Platform:   "ios",
				RouteEpoch: 2,
			},
		},
		peers: []model.PeerConnStatus{
			{
				DeviceID:  "dev-peer-002",
				Connected: true,
				PathKind:  model.ConnPathDirect,
				Endpoint:  "192.168.1.42:41641",
				LatencyMs: 3.4,
				UpdatedAt: now,
			},
		},
		transfers: make(map[string]model.TransferTask),
		subs:      make(map[int]chan model.AppEvent),
		nextMsgNo: 10,
	}
	return m
}

func (m *mockAppAPI) emit(ev model.AppEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (m *mockAppAPI) Start(context.Context) error { return nil }
func (m *mockAppAPI) Close(context.Context) error { return nil }

func (m *mockAppAPI) Identity() model.LocalIdentity {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.identity
}

func (m *mockAppAPI) Config() model.AppConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

func (m *mockAppAPI) UpdateConfig(_ context.Context, cfg model.AppConfig) error {
	m.mu.Lock()
	m.config = cfg
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventConfigUpdated})
	return nil
}

func (m *mockAppAPI) UpdateDERP(_ context.Context, derp model.DERPConfig) error {
	m.mu.Lock()
	m.config.DERP = derp
	m.updatedDERPs = append(m.updatedDERPs, derp)
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventConfigUpdated})
	return nil
}

func (m *mockAppAPI) SendText(_ context.Context, text string) (*model.Message, error) {
	m.mu.Lock()
	m.nextMsgNo++
	msg := model.Message{
		MsgID:          fmt.Sprintf("msg-%03d", m.nextMsgNo),
		AuthorDeviceID: m.identity.DeviceID,
		AuthorName:     m.identity.DeviceName,
		AuthorSeq:      uint64(m.nextMsgNo),
		Kind:           model.MessageKindText,
		TextBody:       text,
		CreatedAt:      time.Now().UnixMilli(),
	}
	m.messages = append(m.messages, msg)
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventMessageAdded, Message: &msg})
	return &msg, nil
}

func (m *mockAppAPI) SendFile(_ context.Context, filePath string) (*model.Message, error) {
	m.mu.Lock()
	m.nextMsgNo++
	m.sentFiles = append(m.sentFiles, filePath)
	hash := fmt.Sprintf("blake3-file-%d", m.nextMsgNo)
	msg := model.Message{
		MsgID:          fmt.Sprintf("msg-%03d", m.nextMsgNo),
		AuthorDeviceID: m.identity.DeviceID,
		AuthorName:     m.identity.DeviceName,
		AuthorSeq:      uint64(m.nextMsgNo),
		Kind:           model.MessageKindFile,
		BlobHash:       hash,
		CreatedAt:      time.Now().UnixMilli(),
		File: &model.FileBlob{
			BlobHash:  hash,
			FileName:  filepath.Base(filePath),
			FileSize:  2048,
			LocalPath: filePath,
			Seeders:   []string{m.identity.DeviceID},
		},
	}
	m.messages = append(m.messages, msg)
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventMessageAdded, Message: &msg})
	return &msg, nil
}

func (m *mockAppAPI) SendBytes(_ context.Context, fileName, mimeType string, data []byte) (*model.Message, error) {
	m.mu.Lock()
	m.nextMsgNo++
	hash := fmt.Sprintf("blake3-bytes-%d", m.nextMsgNo)
	msg := model.Message{
		MsgID:          fmt.Sprintf("msg-%03d", m.nextMsgNo),
		AuthorDeviceID: m.identity.DeviceID,
		AuthorName:     m.identity.DeviceName,
		AuthorSeq:      uint64(m.nextMsgNo),
		Kind:           model.MessageKindFile,
		BlobHash:       hash,
		CreatedAt:      time.Now().UnixMilli(),
		File: &model.FileBlob{
			BlobHash:  hash,
			FileName:  fileName,
			FileSize:  int64(len(data)),
			MimeType:  mimeType,
			LocalPath: "/tmp/" + fileName,
			Seeders:   []string{m.identity.DeviceID},
		},
	}
	m.messages = append(m.messages, msg)
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventMessageAdded, Message: &msg})
	return &msg, nil
}

func (m *mockAppAPI) DeleteMessage(_ context.Context, msgID string) error {
	m.mu.Lock()
	out := m.messages[:0]
	for _, msg := range m.messages {
		if msg.MsgID != msgID {
			out = append(out, msg)
		}
	}
	m.messages = out
	m.mu.Unlock()
	m.emit(model.AppEvent{
		Type:    model.EventMessageDeleted,
		Message: &model.Message{TargetMsgID: msgID},
	})
	return nil
}

func (m *mockAppAPI) ListMessages(limit int, _ string) ([]model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Message, len(m.messages))
	copy(out, m.messages)
	return out, nil
}

func (m *mockAppAPI) SearchMessages(query string, _ int) ([]model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSearch = query
	q := strings.ToLower(query)
	var out []model.Message
	for _, msg := range m.messages {
		if strings.Contains(strings.ToLower(msg.TextBody), q) ||
			(msg.File != nil && strings.Contains(strings.ToLower(msg.File.FileName), q)) {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *mockAppAPI) DownloadFile(_ context.Context, blobHash string) error {
	m.mu.Lock()
	m.downloadedHash = append(m.downloadedHash, blobHash)
	task := model.TransferTask{
		BlobHash:   blobHash,
		Direction:  model.TransferDownload,
		State:      model.TransferRunning,
		BytesDone:  2 * 1024 * 1024,
		BytesTotal: 4 * 1024 * 1024,
	}
	m.transfers[blobHash] = task
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &task})
	return nil
}

func (m *mockAppAPI) PauseTransfer(blobHash string) error {
	m.mu.Lock()
	task := m.transfers[blobHash]
	task.State = model.TransferPaused
	m.transfers[blobHash] = task
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &task})
	return nil
}

func (m *mockAppAPI) CancelTransfer(blobHash string) error {
	m.mu.Lock()
	task := m.transfers[blobHash]
	task.State = model.TransferCancelled
	m.transfers[blobHash] = task
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &task})
	return nil
}

func (m *mockAppAPI) GetTransfer(blobHash string) (*model.TransferTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.transfers[blobHash]; ok {
		return &t, nil
	}
	return nil, nil
}

func (m *mockAppAPI) ListTransfers() ([]model.TransferTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.TransferTask, 0, len(m.transfers))
	for _, t := range m.transfers {
		out = append(out, t)
	}
	return out, nil
}

func (m *mockAppAPI) HasActiveTransfers() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.transfers {
		if t.State == model.TransferRunning || t.State == model.TransferQueued {
			return true
		}
	}
	return false
}

func (m *mockAppAPI) ListDevices(includeRevoked bool) ([]model.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Device
	for _, d := range m.devices {
		if !includeRevoked && d.Revoked {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (m *mockAppAPI) PeerStatuses() []model.PeerConnStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.PeerConnStatus, len(m.peers))
	copy(out, m.peers)
	return out
}

func (m *mockAppAPI) CreatePairingCode(_ context.Context, ttlSeconds int) (string, model.PairingToken, error) {
	tok := model.PairingToken{
		Version:     1,
		GroupID:     "grp-alpha",
		HostDevID:   "dev-self-001",
		HostName:    "MacBook Pro",
		TailcatAddr: "tc-test-addr-001",
		PairingPort: model.DefaultPairingPort,
		Secret:      "one-time-secret",
		DERP:        m.Config().DERP,
		ExpiresAt:   time.Now().Add(time.Duration(ttlSeconds) * time.Second).UnixMilli(),
	}
	code, err := tok.Encode()
	return code, tok, err
}

func (m *mockAppAPI) CancelPairing() error { return nil }

func (m *mockAppAPI) JoinWithPairingCode(_ context.Context, code string) error {
	m.mu.Lock()
	m.joinedCodes = append(m.joinedCodes, code)
	m.mu.Unlock()
	return nil
}

func (m *mockAppAPI) RevokeDevice(_ context.Context, deviceID string) error {
	m.mu.Lock()
	m.revokedIDs = append(m.revokedIDs, deviceID)
	for i := range m.devices {
		if m.devices[i].DeviceID == deviceID {
			m.devices[i].Revoked = true
		}
	}
	m.mu.Unlock()
	m.emit(model.AppEvent{Type: model.EventDevicesUpdated})
	return nil
}

func (m *mockAppAPI) Subscribe() (<-chan model.AppEvent, func()) {
	m.mu.Lock()
	id := m.nextSubID
	m.nextSubID++
	ch := make(chan model.AppEvent, 32)
	m.subs[id] = ch
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		if c, ok := m.subs[id]; ok {
			delete(m.subs, id)
			close(c)
		}
		m.mu.Unlock()
	}
}

func newTestApp(t *testing.T) (*mockAppAPI, *State, *App, *mygoui.Tester) {
	t.Helper()
	mock := newMockAppAPI()
	state := NewState(mock)
	state.Start(context.Background())
	t.Cleanup(state.Stop)

	app := NewApp(state)
	var copied string
	app.writeClipboardText = func(text string, _ bool) error {
		copied = text
		_ = copied
		return nil
	}
	app.openLocalPath = func(string) error { return nil }
	app.openFileDialog = func() ([]string, error) { return nil, nil }

	tt := mygoui.NewTester(app.View, 1024, 720)
	return mock, state, app, tt
}

func TestInitialRenderWithTextAndFileMessages(t *testing.T) {
	_, _, _, tt := newTestApp(t)

	for _, want := range []string{
		"TailChat",
		"1 online · Direct",
		"Hello from iPhone over Tailcat!",
		"design-spec.pdf",
		"4.0 MB · 1 seeder · application/pdf",
		"Download",
	} {
		if !tt.HasText(want) {
			t.Fatalf("expected UI to render %q; rendered texts: %v", want, tt.Texts())
		}
	}
}

func TestSendTextMessageUpdatesTimeline(t *testing.T) {
	mock, _, _, tt := newTestApp(t)

	if err := tt.Click("Message input"); err != nil {
		t.Fatalf("failed to focus message input: %v", err)
	}
	tt.Type("Syncing notes to iPhone")
	if err := tt.Click("Send"); err != nil {
		t.Fatalf("failed to click Send: %v", err)
	}
	tt.Frame()

	if !tt.HasText("Syncing notes to iPhone") {
		t.Fatalf("expected newly sent text in timeline; texts: %v", tt.Texts())
	}

	msgs, _ := mock.ListMessages(50, "")
	if len(msgs) != 3 || msgs[2].TextBody != "Syncing notes to iPhone" {
		t.Fatalf("unexpected mock messages: %+v", msgs)
	}
}

func TestPairingDevicesAndSettingsDERPSwitch(t *testing.T) {
	mock, _, app, tt := newTestApp(t)

	// 1. Pairing dialog: generate code & copy.
	if err := tt.Click("Pair"); err != nil {
		t.Fatalf("click Pair: %v", err)
	}
	if !tt.HasText("Pair Devices") {
		t.Fatalf("expected Pairing modal to open; texts: %v", tt.Texts())
	}
	if err := tt.Click("Generate Pairing Code"); err != nil {
		t.Fatalf("click Generate Pairing Code: %v", err)
	}
	if !strings.HasPrefix(app.pairCode, "tailchat-pair://") {
		t.Fatalf("expected generated tailchat-pair:// code, got %q", app.pairCode)
	}
	if !tt.HasText("Copy Code") {
		t.Fatalf("expected Copy Code button after generating pairing code")
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatalf("close Pairing modal: %v", err)
	}

	// 2. Devices dialog: list paired devices & revoke peer.
	if err := tt.Click("Devices"); err != nil {
		t.Fatalf("click Devices: %v", err)
	}
	if !tt.HasText("Paired Devices") || !tt.HasText("iPhone 16") {
		t.Fatalf("expected Devices modal with iPhone 16; texts: %v", tt.Texts())
	}
	if err := tt.Click("Revoke"); err != nil {
		t.Fatalf("click Revoke: %v", err)
	}
	tt.Frame()
	mock.mu.Lock()
	revoked := append([]string(nil), mock.revokedIDs...)
	mock.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "dev-peer-002" {
		t.Fatalf("expected dev-peer-002 revoked, got %v", revoked)
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatalf("close Devices modal: %v", err)
	}

	// 3. Settings dialog: switch to Custom Self-Hosted derper & hot-switch.
	if err := tt.Click("Settings"); err != nil {
		t.Fatalf("click Settings: %v", err)
	}
	if !tt.HasText("DERP Relay Server") {
		t.Fatalf("expected Settings modal; texts: %v", tt.Texts())
	}
	if err := tt.Click("Custom Self-Hosted derper"); err != nil {
		t.Fatalf("select Custom Self-Hosted derper: %v", err)
	}
	if err := tt.Click("Custom DERP Host"); err != nil {
		t.Fatalf("focus Custom DERP Host: %v", err)
	}
	tt.Type("derp.myhome.net")
	if err := tt.Click("Apply & Hot-Switch DERP"); err != nil {
		t.Fatalf("click Apply & Hot-Switch DERP: %v", err)
	}
	tt.Frame()

	mock.mu.Lock()
	derps := append([]model.DERPConfig(nil), mock.updatedDERPs...)
	mock.mu.Unlock()
	if len(derps) != 1 {
		t.Fatalf("expected 1 UpdateDERP call, got %d", len(derps))
	}
	if derps[0].Mode != model.DERPModeCustomRegion || derps[0].CustomHost != "derp.myhome.net" {
		t.Fatalf("unexpected DERP config: %+v", derps[0])
	}
	if !tt.HasText("Switched DERP server") {
		t.Fatalf("expected toast banner 'Switched DERP server'; texts: %v", tt.Texts())
	}
}

func TestDownloadFlowAndDropFiles(t *testing.T) {
	mock, _, _, tt := newTestApp(t)

	// Trigger download for remote file.
	if err := tt.Click("Download"); err != nil {
		t.Fatalf("click Download: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Pause") {
		t.Fatalf("expected Pause button while downloading; texts: %v", tt.Texts())
	}
	if err := tt.Click("Pause"); err != nil {
		t.Fatalf("click Pause: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Resume") {
		t.Fatalf("expected Resume button when paused; texts: %v", tt.Texts())
	}

	// Drop a file onto the chat window.
	if !tt.DropFiles(500, 360, "/tmp/photo.png") {
		t.Fatalf("expected chat window to accept dropped file")
	}
	tt.Frame()
	mock.mu.Lock()
	sent := append([]string(nil), mock.sentFiles...)
	mock.mu.Unlock()
	if len(sent) != 1 || sent[0] != "/tmp/photo.png" {
		t.Fatalf("expected /tmp/photo.png sent via SendFile, got %v", sent)
	}
}
