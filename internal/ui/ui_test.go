package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mygoui "github.com/egoist/mygo/ui"

	"tailchat/internal/model"
)

type mockAppAPI struct {
	mu          sync.Mutex
	identity    model.LocalIdentity
	config      model.AppConfig
	messages    []model.Message
	devices     []model.Device
	peers       []model.PeerConnStatus
	transfers   map[string]model.TransferTask
	subs        map[int]chan model.AppEvent
	nextSubID   int
	nextMsgNo   int
	nextPairSeq int

	joinedCodes     []string
	revokedIDs      []string
	updatedDERPs    []model.DERPConfig
	downloadedHash  []string
	sentFiles       []string
	lastSearch      string
	cancelPairCnt   int
	clearedAllCount int
	resolvedHashes  []string
	resolvePaths    map[string]string
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
			Language:             model.LanguageChinese,
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

func (m *mockAppAPI) ClearAllMessages(_ context.Context) error {
	m.mu.Lock()
	m.clearedAllCount++
	removed := make([]string, 0, len(m.messages))
	for _, msg := range m.messages {
		removed = append(removed, msg.MsgID)
	}
	m.messages = nil
	m.mu.Unlock()
	for _, id := range removed {
		m.emit(model.AppEvent{
			Type:    model.EventMessageDeleted,
			Message: &model.Message{TargetMsgID: id},
		})
	}
	return nil
}

func (m *mockAppAPI) ResolveLocalFile(_ context.Context, blobHash string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolvedHashes = append(m.resolvedHashes, blobHash)
	if m.resolvePaths != nil {
		if p, ok := m.resolvePaths[blobHash]; ok && p != "" {
			return p, nil
		}
	}
	for _, msg := range m.messages {
		if msg.File != nil && msg.File.BlobHash == blobHash && msg.File.LocalPath != "" {
			return msg.File.LocalPath, nil
		}
	}
	return "", fmt.Errorf("local file not found for blob %s", blobHash)
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
	m.mu.Lock()
	m.nextPairSeq++
	seq := m.nextPairSeq
	derp := m.config.DERP
	m.mu.Unlock()

	tok := model.PairingToken{
		Version:     1,
		GroupID:     "grp-alpha",
		HostDevID:   "dev-self-001",
		HostName:    "MacBook Pro",
		TailcatAddr: "tc-test-addr-001",
		PairingPort: model.DefaultPairingPort,
		Secret:      fmt.Sprintf("one-time-secret-%03d", seq),
		DERP:        derp,
		ExpiresAt:   time.Now().Add(time.Duration(ttlSeconds) * time.Second).UnixMilli(),
	}
	code, err := tok.Encode()
	return code, tok, err
}

func (m *mockAppAPI) CancelPairing() error {
	m.mu.Lock()
	m.cancelPairCnt++
	m.mu.Unlock()
	return nil
}

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
	app.runAsync = func(fn func()) { fn() }
	app.writeClipboardText = func(string, bool) error { return nil }
	app.readClipboardText = func() string { return "" }
	app.openLocalPath = func(string) error { return nil }
	app.showInFolder = func(string) error { return nil }
	app.openFileDialog = func() ([]string, error) { return nil, nil }
	app.chooseDirDialog = func(string) (string, error) { return "", nil }
	app.openExternalURL = func(string) error { return nil }

	tt := mygoui.NewTester(app.View, 540, 740)
	return mock, state, app, tt
}

func TestAllIconsSupported(t *testing.T) {
	if len(zenIcons) < 27 {
		t.Fatalf("expected at least 27 embedded SVG icons, got %d", len(zenIcons))
	}
	for _, required := range []string{"qrCode", "refreshCw", "link", "check", "folderOpen", "info", "globe", "github", "alertTriangle"} {
		if zenIcons[required] == nil {
			t.Fatalf("missing required icon %q in zenIcons", required)
		}
	}
	for name, svg := range zenIcons {
		if svg == nil {
			t.Fatalf("icon %q is nil", name)
		}
		if unsupported := svg.Unsupported(); len(unsupported) > 0 {
			t.Fatalf("icon %q contains unsupported SVG features: %v", name, unsupported)
		}
		w, h := svg.Size()
		if w <= 0 || h <= 0 {
			t.Fatalf("icon %q has invalid size: %.1fx%.1f", name, w, h)
		}
	}
}

func TestInitialRenderWithTextAndFileMessages(t *testing.T) {
	_, _, _, tt := newTestApp(t)

	for _, want := range []string{
		"TailChat",
		"1 台设备在线 · P2P 直连",
		"配对",
		"Hello from iPhone over Tailcat!",
		"design-spec.pdf",
		"PDF",
		"4.0 MB · 1 台设备有源",
		"立即下载",
	} {
		if !tt.HasText(want) {
			t.Fatalf("expected UI to render %q; rendered texts: %v", want, tt.Texts())
		}
	}
}

func TestSearchDrawerFiltersMessagesLive(t *testing.T) {
	mock, _, _, tt := newTestApp(t)

	if err := tt.Click("搜索消息"); err != nil {
		t.Fatalf("click 搜索消息 toggle: %v", err)
	}
	tt.Frame()

	// Search drawer focuses the search input automatically on open.
	tt.Type("design-spec")
	tt.Frame()

	if !tt.HasText("design-spec.pdf") {
		t.Fatalf("expected design-spec.pdf to match search; texts: %v", tt.Texts())
	}
	if tt.HasText("Hello from iPhone over Tailcat!") {
		t.Fatalf("expected non-matching text message to be filtered out; texts: %v", tt.Texts())
	}
	mock.mu.Lock()
	lastSearch := mock.lastSearch
	mock.mu.Unlock()
	if lastSearch != "design-spec" {
		t.Fatalf("expected SearchMessages called with 'design-spec', got %q", lastSearch)
	}
}

func TestSendTextMessageUpdatesTimeline(t *testing.T) {
	mock, _, _, tt := newTestApp(t)

	if err := tt.Click("输入消息"); err != nil {
		t.Fatalf("failed to focus message input: %v", err)
	}
	tt.Type("Syncing notes to iPhone")
	if err := tt.Click("发送"); err != nil {
		t.Fatalf("failed to click 发送: %v", err)
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

func TestLightweightDevicesPopoverAndSettingsDERPSwitch(t *testing.T) {
	mock, _, app, tt := newTestApp(t)

	// 1. Lightweight Devices-only popover: inspect peer, verify no pairing code/join form, revoke peer, jump to pairing modal.
	if err := tt.Click("已配对设备"); err != nil {
		t.Fatalf("click 已配对设备: %v", err)
	}
	tt.Frame()
	if !tt.HasText("已配对设备") || !tt.HasText("iPhone 16") || !tt.HasText("配对新设备") {
		t.Fatalf("expected lightweight Devices popover with iPhone 16 and 配对新设备; texts: %v", tt.Texts())
	}
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "tailchat-pair://") {
			t.Fatalf("devices popover must never render raw pairing code, found %q", txt)
		}
	}
	if tt.HasText("立即加入") || tt.HasText("一键复制配对码") {
		t.Fatalf("devices popover should not embed pairing flow controls; texts: %v", tt.Texts())
	}

	if err := tt.Click("移除"); err != nil {
		t.Fatalf("click 移除: %v", err)
	}
	tt.Frame()
	mock.mu.Lock()
	revoked := append([]string(nil), mock.revokedIDs...)
	mock.mu.Unlock()
	if len(revoked) != 1 || revoked[0] != "dev-peer-002" {
		t.Fatalf("expected dev-peer-002 revoked, got %v", revoked)
	}

	// Footer CTA jumps cleanly from Devices popover to Dedicated Pairing modal.
	if err := tt.Click("配对新设备"); err != nil {
		t.Fatalf("click 配对新设备: %v", err)
	}
	tt.Frame()
	if app.devicesPopoverOpen {
		t.Fatalf("expected devices popover closed after clicking 配对新设备")
	}
	if !app.pairOpen || app.pairTab != 0 || app.pairQRBitmap == nil {
		t.Fatalf("expected dedicated pairing modal open on QR tab with bitmap; open=%v tab=%d bmp=%v", app.pairOpen, app.pairTab, app.pairQRBitmap)
	}

	// Close pairing modal before opening settings.
	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("close pairing modal: %v", err)
	}
	tt.Frame()

	// 2. Settings & DERP modal: switch to Network tab, then 自建 derper & hot-switch.
	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("click 设置与 DERP: %v", err)
	}
	tt.Frame()
	if err := tt.Click("网络"); err != nil {
		t.Fatalf("switch to 网络 tab: %v", err)
	}
	tt.Frame()
	if !tt.HasText("DERP 中继服务器") {
		t.Fatalf("expected Network tab in Settings modal; texts: %v", tt.Texts())
	}
	if err := tt.Click("自建 derper"); err != nil {
		t.Fatalf("select 自建 derper: %v", err)
	}
	tt.Frame()
	if err := tt.Click("自建 DERP 域名"); err != nil {
		t.Fatalf("focus 自建 DERP 域名: %v", err)
	}
	tt.Type("derp.myhome.net")
	if err := tt.Click("应用并热切 DERP"); err != nil {
		t.Fatalf("click 应用并热切 DERP: %v", err)
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
	if !tt.HasText("已切换 DERP 服务器") {
		t.Fatalf("expected toast pill '已切换 DERP 服务器'; texts: %v", tt.Texts())
	}
}

func TestDedicatedQRPairingFlow(t *testing.T) {
	mock, _, app, tt := newTestApp(t)

	var copiedText string
	var clipboardRead string
	app.writeClipboardText = func(text string, _ bool) error {
		copiedText = text
		return nil
	}
	app.readClipboardText = func() string {
		return clipboardRead
	}

	// 1. Click top-right Pair CTA button.
	if err := tt.Click("配对"); err != nil {
		t.Fatalf("click top-right 配对: %v", err)
	}
	tt.Frame()

	if !app.pairOpen || app.pairTab != 0 {
		t.Fatalf("expected pairing modal open on tab 0, got open=%v tab=%d", app.pairOpen, app.pairTab)
	}
	for _, want := range []string{"配对设备", "扫码配对", "输入配对码加入", "一键复制配对码", "一次配对，全群自动互通"} {
		if !tt.HasText(want) {
			t.Fatalf("expected pairing modal to render %q; texts: %v", want, tt.Texts())
		}
	}
	if !strings.HasPrefix(app.pairCode, "tailchat-pair://") {
		t.Fatalf("expected auto-generated tailchat-pair:// code, got %q", app.pairCode)
	}
	if app.pairQRBitmap == nil {
		t.Fatalf("expected non-nil cached QR bitmap")
	}

	// Raw plaintext pairing code MUST NEVER appear in rendered UI texts.
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "tailchat-pair://") {
			t.Fatalf("raw pairing code must never be displayed in QR tab; found %q", txt)
		}
	}

	// Subsequent frames reuse the cached QR bitmap without regenerating it.
	firstBmp := app.pairQRBitmap
	firstCode := app.pairCode
	tt.Frame()
	if app.pairQRBitmap != firstBmp {
		t.Fatalf("expected cached QR bitmap pointer to remain stable across frames")
	}

	// 2. Click "一键复制配对码" -> copies exact code & shows copied state.
	if err := tt.Click("一键复制配对码"); err != nil {
		t.Fatalf("click 一键复制配对码: %v", err)
	}
	tt.Frame()
	if copiedText != firstCode {
		t.Fatalf("expected clipboard to receive %q, got %q", firstCode, copiedText)
	}
	if !tt.HasText("已复制配对码") {
		t.Fatalf("expected green copied state '已复制配对码'; texts: %v", tt.Texts())
	}

	// 3. Click "刷新二维码" -> cancels old code, generates fresh code & bitmap.
	if err := tt.Click("刷新二维码"); err != nil {
		t.Fatalf("click 刷新二维码: %v", err)
	}
	tt.Frame()
	if app.pairCode == "" || app.pairCode == firstCode {
		t.Fatalf("expected refreshed pairing code to differ from %q, got %q", firstCode, app.pairCode)
	}
	if app.pairQRBitmap == nil || app.pairQRBitmap == firstBmp {
		t.Fatalf("expected refreshed QR bitmap to be rebuilt")
	}
	mock.mu.Lock()
	cancels := mock.cancelPairCnt
	mock.mu.Unlock()
	if cancels < 1 {
		t.Fatalf("expected CancelPairing called on refresh, got %d", cancels)
	}
	for _, txt := range tt.Texts() {
		if strings.Contains(txt, "tailchat-pair://") {
			t.Fatalf("raw pairing code must never be displayed after refresh; found %q", txt)
		}
	}

	// 4. Expired QR state & regenerate CTA.
	app.pairToken.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	tt.Frame()
	if !tt.HasText("配对二维码已过期") || !tt.HasText("已过期") || !tt.HasText("重新生成") {
		t.Fatalf("expected expired QR state UI; texts: %v", tt.Texts())
	}
	expiredCode := app.pairCode
	if err := tt.Click("重新生成"); err != nil {
		t.Fatalf("click 重新生成: %v", err)
	}
	tt.Frame()
	if app.pairCode == expiredCode || app.pairQRBitmap == nil {
		t.Fatalf("expected 重新生成 to create a new valid pairing code and QR bitmap")
	}

	// 5. Switch to "输入配对码加入" tab, paste from clipboard, and join.
	if err := tt.Click("输入配对码加入"); err != nil {
		t.Fatalf("click 输入配对码加入 tab: %v", err)
	}
	tt.Frame()
	if app.pairTab != 1 || !tt.HasText("加入已有设备群") || !tt.HasText("从剪贴板粘贴") {
		t.Fatalf("expected Join tab rendered; tab=%d texts=%v", app.pairTab, tt.Texts())
	}

	clipboardRead = "  tailchat-pair://mock-peer-invite-token  "
	if err := tt.Click("从剪贴板粘贴"); err != nil {
		t.Fatalf("click 从剪贴板粘贴: %v", err)
	}
	tt.Frame()
	if app.joinCodeInput != "tailchat-pair://mock-peer-invite-token" {
		t.Fatalf("expected trimmed clipboard code in joinCodeInput, got %q", app.joinCodeInput)
	}

	if err := tt.Click("立即加入"); err != nil {
		t.Fatalf("click 立即加入: %v", err)
	}
	tt.Frame()
	if app.pairOpen {
		t.Fatalf("expected pairing modal closed after joining")
	}
	mock.mu.Lock()
	joined := append([]string(nil), mock.joinedCodes...)
	mock.mu.Unlock()
	if len(joined) != 1 || joined[0] != "tailchat-pair://mock-peer-invite-token" {
		t.Fatalf("expected JoinWithPairingCode called with pasted code, got %v", joined)
	}
}

func TestDownloadFlowAndDropFiles(t *testing.T) {
	mock, _, _, tt := newTestApp(t)

	// Trigger download for remote file.
	if err := tt.Click("立即下载"); err != nil {
		t.Fatalf("click 立即下载: %v", err)
	}
	tt.Frame()
	if !tt.HasText("暂停") {
		t.Fatalf("expected 暂停 button while downloading; texts: %v", tt.Texts())
	}
	if err := tt.Click("暂停"); err != nil {
		t.Fatalf("click 暂停: %v", err)
	}
	tt.Frame()
	if !tt.HasText("继续") {
		t.Fatalf("expected 继续 button when paused; texts: %v", tt.Texts())
	}

	// Drop a file onto the compact 540x740 chat window.
	if !tt.DropFiles(270, 360, "/tmp/photo.png") {
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

func assertValid2xRender(t *testing.T, img *image.RGBA, outDir, filename string) {
	t.Helper()
	if img == nil {
		t.Fatalf("%s: mygoui.Render returned nil image", filename)
	}
	b := img.Bounds()
	if b.Dx() != 1080 || b.Dy() != 1480 {
		t.Fatalf("%s: expected 1080x1480 rendered image at 2x scale, got %dx%d", filename, b.Dx(), b.Dy())
	}
	first := img.RGBAAt(0, 0)
	diffPixels := 0
	for y := 0; y < b.Dy(); y += 4 {
		for x := 0; x < b.Dx(); x += 4 {
			if img.RGBAAt(x, y) != first {
				diffPixels++
			}
		}
	}
	if diffPixels < 100 {
		t.Fatalf("%s: rendered image appears blank (only %d sampled pixels differ)", filename, diffPixels)
	}
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", outDir, err)
		}
		path := filepath.Join(outDir, filename)
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		if err := png.Encode(f, img); err != nil {
			_ = f.Close()
			t.Fatalf("encode %s: %v", path, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", path, err)
		}
	}
}

func TestRenderZenSnapshot2x(t *testing.T) {
	outDir := os.Getenv("TAILCHAT_SNAPSHOT_DIR")

	// 1. Main window with top-right "配对" button.
	_, _, appMain, _ := newTestApp(t)
	assertValid2xRender(t, mygoui.Render(appMain.View, 540, 740, 2), outDir, "01_main_pair_entry.png")

	// 2. Dedicated "配对设备" modal showing real pure-Go QR code (Tab 0).
	_, _, appQR, _ := newTestApp(t)
	appQR.openPairingFlow(0)
	assertValid2xRender(t, mygoui.Render(appQR.View, 540, 740, 2), outDir, "02_pairing_modal_qr.png")

	// 3. Dedicated "配对设备" modal on "输入配对码加入" (Tab 1).
	_, _, appJoin, _ := newTestApp(t)
	appJoin.openPairingFlow(1)
	assertValid2xRender(t, mygoui.Render(appJoin.View, 540, 740, 2), outDir, "03_pairing_modal_join.png")

	// 4. Lightweight "已配对设备" popover.
	_, _, appDev, _ := newTestApp(t)
	appDev.devicesPopoverOpen = true
	assertValid2xRender(t, mygoui.Render(appDev.View, 540, 740, 2), outDir, "04_devices_popover.png")

	// 9. Redesigned Settings modal: General tab (Tab 0).
	_, stGen, appSetGen, _ := newTestApp(t)
	appSetGen.settingsDraft = NewSettingsDraft(stGen.Config())
	appSetGen.settingsTab = 0
	appSetGen.settingsOpen = true
	assertValid2xRender(t, mygoui.Render(appSetGen.View, 540, 740, 2), outDir, "09_settings_general_tab.png")

	// 9b. Redesigned Settings modal: Transfer tab (Tab 1).
	_, stTrans, appSetTrans, _ := newTestApp(t)
	appSetTrans.settingsDraft = NewSettingsDraft(stTrans.Config())
	appSetTrans.settingsDraft.DownloadDir = "/Users/demo/Downloads/TailChat"
	appSetTrans.settingsTab = 1
	appSetTrans.settingsOpen = true
	assertValid2xRender(t, mygoui.Render(appSetTrans.View, 540, 740, 2), outDir, "09b_settings_transfer_tab.png")

	// 9c. Redesigned Settings modal: Network tab (Tab 2).
	_, stNet, appSetNet, _ := newTestApp(t)
	appSetNet.settingsDraft = NewSettingsDraft(stNet.Config())
	appSetNet.settingsTab = 2
	appSetNet.settingsOpen = true
	assertValid2xRender(t, mygoui.Render(appSetNet.View, 540, 740, 2), outDir, "09c_settings_network_tab.png")

	// 10. Redesigned Settings modal: About tab (Tab 3).
	_, stAbout, appSetAbout, _ := newTestApp(t)
	appSetAbout.settingsDraft = NewSettingsDraft(stAbout.Config())
	appSetAbout.settingsTab = 3
	appSetAbout.settingsOpen = true
	assertValid2xRender(t, mygoui.Render(appSetAbout.View, 540, 740, 2), outDir, "10_settings_about_tab.png")

	// 11. Clear All Chat History confirmation modal.
	_, stClr, appClr, _ := newTestApp(t)
	appClr.settingsDraft = NewSettingsDraft(stClr.Config())
	appClr.settingsTab = 0
	appClr.settingsOpen = true
	appClr.clearConfirmOpen = true
	assertValid2xRender(t, mygoui.Render(appClr.View, 540, 740, 2), outDir, "11_clear_confirm_modal.png")
}

func TestLocaleResolutionAndFallback(t *testing.T) {
	cases := []struct {
		raw  string
		want Locale
	}{
		{"zh", LocaleZH},
		{"zh_CN.UTF-8", LocaleZH},
		{"zh-Hans-CN", LocaleZH},
		{"zh-TW", LocaleZH},
		{"en_US.UTF-8", LocaleEN},
		{"en-GB", LocaleEN},
		{"ja_JP.UTF-8", LocaleEN},
		{"fr_FR.UTF-8", LocaleEN},
		{"de_DE", LocaleEN},
		{"C", LocaleEN},
		{"POSIX", LocaleEN},
		{"", LocaleEN},
	}
	for _, tc := range cases {
		if got := parseSystemLocale(tc.raw); got != tc.want {
			t.Errorf("parseSystemLocale(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	origDetector := systemLocaleDetector
	t.Cleanup(func() { systemLocaleDetector = origDetector })

	// System locale = Japanese -> Auto must fall back to English.
	systemLocaleDetector = func() Locale { return parseSystemLocale("ja_JP.UTF-8") }
	if got := ResolveLocale(model.LanguageAuto); got != LocaleEN {
		t.Fatalf("Auto with ja_JP must fall back to English, got %q", got)
	}
	if got := ResolveLocale(""); got != LocaleEN {
		t.Fatalf("empty preference must fall back to English, got %q", got)
	}
	if got := ResolveLocale(model.LanguageChinese); got != LocaleZH {
		t.Fatalf("explicit zh must override system locale, got %q", got)
	}

	// System locale = Chinese -> Auto resolves to Chinese, explicit en overrides to English.
	systemLocaleDetector = func() Locale { return parseSystemLocale("zh_CN.UTF-8") }
	if got := ResolveLocale(model.LanguageAuto); got != LocaleZH {
		t.Fatalf("Auto with zh_CN must resolve to Chinese, got %q", got)
	}
	if got := ResolveLocale(model.LanguageEnglish); got != LocaleEN {
		t.Fatalf("explicit en must override Chinese system locale, got %q", got)
	}
}

func TestLanguageSwitchingInSettingsUI(t *testing.T) {
	origDetector := systemLocaleDetector
	t.Cleanup(func() { systemLocaleDetector = origDetector })
	systemLocaleDetector = func() Locale { return LocaleEN }

	mock, _, _, tt := newTestApp(t)

	// Starts in Chinese per mockAppAPI.
	if !tt.HasText("配对") || !tt.HasText("立即下载") {
		t.Fatalf("expected initial Chinese UI; texts: %v", tt.Texts())
	}

	// Open Settings and switch Language to English.
	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	tt.Frame()
	if !tt.HasText("语言") || !tt.HasText("自动") || !tt.HasText("English") || !tt.HasText("中文") {
		t.Fatalf("expected Language segmented options in settings; texts: %v", tt.Texts())
	}

	if err := tt.Click("English"); err != nil {
		t.Fatalf("select English: %v", err)
	}
	tt.Frame()
	// Live preview inside Settings modal switches immediately to English.
	if !tt.HasText("Settings") || !tt.HasText("General") || !tt.HasText("About") || !tt.HasText("Save") {
		t.Fatalf("expected live English preview in Settings modal; texts: %v", tt.Texts())
	}

	if err := tt.Click("Save"); err != nil {
		t.Fatalf("click Save: %v", err)
	}
	tt.Frame()

	if got := mock.Config().Language; got != model.LanguageEnglish {
		t.Fatalf("expected persisted config language %q, got %q", model.LanguageEnglish, got)
	}
	for _, want := range []string{"1 online · P2P Direct", "Pair", "Download", "Settings saved"} {
		if !tt.HasText(want) {
			t.Fatalf("expected main UI in English to contain %q; texts: %v", want, tt.Texts())
		}
	}

	// Open Pairing modal and verify English labels.
	if err := tt.Click("Pair"); err != nil {
		t.Fatalf("click Pair: %v", err)
	}
	tt.Frame()
	for _, want := range []string{"Pair Device", "Scan QR", "Enter Code", "Copy Pairing Code"} {
		if !tt.HasText(want) {
			t.Fatalf("expected English pairing modal to contain %q; texts: %v", want, tt.Texts())
		}
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatalf("close English pairing modal: %v", err)
	}
	tt.Frame()

	// Switch to Auto while system locale is non-Chinese -> must stay English fallback.
	if err := tt.Click("Settings & DERP"); err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Auto"); err != nil {
		t.Fatalf("select Auto: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Save"); err != nil {
		t.Fatalf("save Auto: %v", err)
	}
	tt.Frame()
	if got := mock.Config().Language; got != model.LanguageAuto {
		t.Fatalf("expected persisted config language %q, got %q", model.LanguageAuto, got)
	}
	if !tt.HasText("Pair") || !tt.HasText("Download") {
		t.Fatalf("expected Auto with English fallback to render English UI; texts: %v", tt.Texts())
	}
}

func TestEmbeddedIconsValidPNG(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		w, h int
	}{
		{"dock_icon", dockIconPNG, 512, 512},
		{"tray_icon_template", trayIconTemplatePNG, 32, 32},
		{"tray_icon_color", trayIconColorPNG, 32, 32},
		{"tray_icon_active", trayIconPNG(), 32, 32},
	}
	for _, tc := range cases {
		img, err := png.Decode(bytes.NewReader(tc.data))
		if err != nil {
			t.Fatalf("%s: png.Decode failed: %v", tc.name, err)
		}
		b := img.Bounds()
		if b.Dx() != tc.w || b.Dy() != tc.h {
			t.Fatalf("%s: expected %dx%d, got %dx%d", tc.name, tc.w, tc.h, b.Dx(), b.Dy())
		}
	}
}

func TestFileOpenDirectlyVsShowInFolder(t *testing.T) {
	mock, st, app, tt := newTestApp(t)

	const (
		blobHash     = "blake3-local-doc-001"
		initialPath  = "/tmp/stale-report.pdf"
		resolvedPath = "/tmp/resolved-report.pdf"
	)

	now := time.Now().UnixMilli()
	mock.mu.Lock()
	mock.resolvePaths = map[string]string{blobHash: resolvedPath}
	mock.messages = []model.Message{
		{
			MsgID:          "msg-local-pdf",
			AuthorDeviceID: "dev-self-001",
			AuthorName:     "MacBook Pro",
			AuthorSeq:      1,
			Kind:           model.MessageKindFile,
			BlobHash:       blobHash,
			CreatedAt:      now,
			File: &model.FileBlob{
				BlobHash:  blobHash,
				FileName:  "quarterly-report.pdf",
				FileSize:  512 * 1024,
				MimeType:  "application/pdf",
				LocalPath: initialPath,
				Seeders:   []string{"dev-self-001"},
			},
		},
	}
	mock.mu.Unlock()
	st.RefreshAll()
	tt.Frame()

	var openedPaths []string
	var revealedPaths []string
	app.openLocalPath = func(p string) error {
		openedPaths = append(openedPaths, p)
		return nil
	}
	app.showInFolder = func(p string) error {
		revealedPaths = append(revealedPaths, p)
		return nil
	}

	if !tt.HasText("quarterly-report.pdf") || !tt.HasText("打开所在目录") {
		t.Fatalf("expected local file card with 打开所在目录; texts: %v", tt.Texts())
	}

	// 1. Clicking the file header opens it directly using the resolved local path.
	if err := tt.Click("quarterly-report.pdf"); err != nil {
		t.Fatalf("click file header to open directly: %v", err)
	}
	tt.Frame()

	if len(openedPaths) != 1 || openedPaths[0] != resolvedPath {
		t.Fatalf("expected openLocalPath(%q), got %v", resolvedPath, openedPaths)
	}
	if len(revealedPaths) != 0 {
		t.Fatalf("expected showInFolder not called on direct open, got %v", revealedPaths)
	}
	// Verify State cached message updated its LocalPath to the resolved path.
	snap := st.Snapshot()
	if len(snap.Messages) != 1 || snap.Messages[0].File == nil || snap.Messages[0].File.LocalPath != resolvedPath {
		t.Fatalf("expected State to update cached LocalPath to %q, got %+v", resolvedPath, snap.Messages)
	}

	// 2. Clicking "打开所在目录" invokes showInFolder and NOT openLocalPath.
	if err := tt.Click("打开所在目录"); err != nil {
		t.Fatalf("click 打开所在目录: %v", err)
	}
	tt.Frame()

	if len(revealedPaths) != 1 || revealedPaths[0] != resolvedPath {
		t.Fatalf("expected showInFolder(%q), got %v", resolvedPath, revealedPaths)
	}
	if len(openedPaths) != 1 {
		t.Fatalf("expected openLocalPath NOT called when clicking 打开所在目录, got %v", openedPaths)
	}

	mock.mu.Lock()
	hashes := append([]string(nil), mock.resolvedHashes...)
	mock.mu.Unlock()
	if len(hashes) < 2 || hashes[0] != blobHash || hashes[1] != blobHash {
		t.Fatalf("expected ResolveLocalFile called for %q, got %v", blobHash, hashes)
	}
}

func TestSettingsDownloadDirAndBrowsePicker(t *testing.T) {
	mock, _, app, tt := newTestApp(t)

	chosenCh := make(chan struct{})
	app.chooseDirDialog = func(defaultDir string) (string, error) {
		defer close(chosenCh)
		return "/Users/tester/TailChatFiles", nil
	}

	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	tt.Frame()

	if err := tt.Click("传输"); err != nil {
		t.Fatalf("switch to 传输 tab: %v", err)
	}
	tt.Frame()

	if !tt.HasText("文件存储路径") || !tt.HasText("选择目录") {
		t.Fatalf("expected File Storage Path controls in Settings Transfer tab; texts: %v", tt.Texts())
	}

	if err := tt.Click("选择目录"); err != nil {
		t.Fatalf("click 选择目录: %v", err)
	}

	select {
	case <-chosenCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for chooseDirDialog goroutine")
	}

	// Wait briefly until pendingDownloadDir is published, then render a frame to apply it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if app.settingsDraft.DownloadDir == "/Users/tester/TailChatFiles" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if app.settingsDraft.DownloadDir != "/Users/tester/TailChatFiles" {
		t.Fatalf("expected settingsDraft.DownloadDir updated from picker, got %q", app.settingsDraft.DownloadDir)
	}

	if err := tt.Click("保存设置"); err != nil {
		t.Fatalf("click 保存设置: %v", err)
	}
	tt.Frame()

	if got := mock.Config().DownloadDir; got != "/Users/tester/TailChatFiles" {
		t.Fatalf("expected persisted DownloadDir %q, got %q", "/Users/tester/TailChatFiles", got)
	}
}

func TestClearAllHistoryConfirmationFlow(t *testing.T) {
	mock, st, app, tt := newTestApp(t)

	if len(st.Snapshot().Messages) == 0 {
		t.Fatalf("expected initial seeded messages")
	}

	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	tt.Frame()

	if !tt.HasText("清空所有聊天记录") || !tt.HasText("一键清空") {
		t.Fatalf("expected Danger Zone clear history controls; texts: %v", tt.Texts())
	}

	// 1. Click "一键清空" -> opens confirmation modal.
	if err := tt.Click("一键清空"); err != nil {
		t.Fatalf("click 一键清空: %v", err)
	}
	tt.Frame()
	if !app.clearConfirmOpen || !tt.HasText("确认清空所有聊天记录？") {
		t.Fatalf("expected clear confirm modal open; open=%v texts=%v", app.clearConfirmOpen, tt.Texts())
	}

	// 2. Click "取消" -> closes confirmation modal, preserves messages.
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("click 取消: %v", err)
	}
	tt.Frame()
	if app.clearConfirmOpen {
		t.Fatalf("expected clear confirm modal closed after cancel")
	}
	if !app.settingsOpen {
		t.Fatalf("expected settings modal to remain open after cancelling clear confirm")
	}
	mock.mu.Lock()
	cnt := mock.clearedAllCount
	mock.mu.Unlock()
	if cnt != 0 || len(st.Snapshot().Messages) == 0 {
		t.Fatalf("expected messages preserved on cancel; clearedAllCount=%d msgs=%d", cnt, len(st.Snapshot().Messages))
	}

	// 3. Click "一键清空" -> click "确认清空" -> clears all messages & closes both modals.
	if err := tt.Click("一键清空"); err != nil {
		t.Fatalf("click 一键清空 second time: %v", err)
	}
	tt.Frame()
	if err := tt.Click("确认清空"); err != nil {
		t.Fatalf("click 确认清空: %v", err)
	}
	tt.Frame()

	mock.mu.Lock()
	cnt = mock.clearedAllCount
	mock.mu.Unlock()
	if cnt != 1 {
		t.Fatalf("expected ClearAllMessages called once, got %d", cnt)
	}
	if app.clearConfirmOpen || app.settingsOpen {
		t.Fatalf("expected both confirm and settings modals closed; confirm=%v settings=%v", app.clearConfirmOpen, app.settingsOpen)
	}
	if len(st.Snapshot().Messages) != 0 {
		t.Fatalf("expected empty timeline after clear all, got %d messages", len(st.Snapshot().Messages))
	}
	if !tt.HasText("已清空所有聊天记录") || !tt.HasText("随时跨设备同步文字与文件") {
		t.Fatalf("expected cleared toast and empty timeline state; texts: %v", tt.Texts())
	}
}

func TestSettingsAboutTabVersionUpdatesAndGitHub(t *testing.T) {
	_, _, app, tt := newTestApp(t)

	var openedURLs []string
	app.openExternalURL = func(url string) error {
		openedURLs = append(openedURLs, url)
		return nil
	}

	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("open settings: %v", err)
	}
	tt.Frame()

	for _, tabLabel := range []string{"通用", "传输", "网络", "关于"} {
		if !tt.HasText(tabLabel) {
			t.Fatalf("expected %q tab in Settings; texts: %v", tabLabel, tt.Texts())
		}
	}

	// Verify editing draft on General persists across Transfer & Network tab switches.
	app.settingsDraft.DeviceName = "MyDraftDevice"
	if err := tt.Click("传输"); err != nil {
		t.Fatalf("switch to 传输 tab: %v", err)
	}
	tt.Frame()
	if app.settingsTab != 1 || !tt.HasText("文件存储路径") || !tt.HasText("自动下载阈值 (MB)") {
		t.Fatalf("expected Transfer tab (1) controls; tab=%d texts=%v", app.settingsTab, tt.Texts())
	}

	if err := tt.Click("网络"); err != nil {
		t.Fatalf("switch to 网络 tab: %v", err)
	}
	tt.Frame()
	if app.settingsTab != 2 || !tt.HasText("DERP 中继服务器") {
		t.Fatalf("expected Network tab (2) controls; tab=%d texts=%v", app.settingsTab, tt.Texts())
	}
	if app.settingsDraft.DeviceName != "MyDraftDevice" {
		t.Fatalf("expected settingsDraft preserved across tab switches, got %q", app.settingsDraft.DeviceName)
	}

	if err := tt.Click("关于"); err != nil {
		t.Fatalf("switch to 关于 tab: %v", err)
	}
	tt.Frame()

	if app.settingsTab != 3 {
		t.Fatalf("expected settingsTab == 3, got %d", app.settingsTab)
	}
	for _, want := range []string{"TailChat", "v1.0.0", "软件更新", "检查更新", "GitHub", "github.com/jae-jae/tailchat"} {
		if !tt.HasText(want) {
			t.Fatalf("expected About tab to contain %q; texts: %v", want, tt.Texts())
		}
	}

	if err := tt.Click("github.com/jae-jae/tailchat"); err != nil {
		t.Fatalf("click GitHub repo link: %v", err)
	}
	tt.Frame()

	if len(openedURLs) != 1 || openedURLs[0] != "https://github.com/jae-jae/tailchat" {
		t.Fatalf("expected openExternalURL called with GitHub repo URL, got %v", openedURLs)
	}
}

type blockingSendAPI struct {
	*mockAppAPI
	started chan string
	release chan struct{}
}

func (b *blockingSendAPI) SendFile(ctx context.Context, filePath string) (*model.Message, error) {
	select {
	case b.started <- filePath:
	default:
	}
	<-b.release
	return b.mockAppAPI.SendFile(ctx, filePath)
}

func TestDropFilesDoesNotBlockUIThread(t *testing.T) {
	base := newMockAppAPI()
	api := &blockingSendAPI{
		mockAppAPI: base,
		started:    make(chan string, 1),
		release:    make(chan struct{}),
	}
	state := NewState(api)
	state.Start(context.Background())
	t.Cleanup(state.Stop)

	// Keep the real production async dispatch (go fn()).
	app := NewApp(state)
	tt := mygoui.NewTester(app.View, 540, 740)
	tt.Frame()

	start := time.Now()
	if !tt.DropFiles(270, 360, "/tmp/large-video.mp4") {
		t.Fatalf("expected chat window to accept dropped video file")
	}
	tt.Frame()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("DropFiles blocked UI thread for %v (expected immediate return)", elapsed)
	}

	select {
	case got := <-api.started:
		if got != "/tmp/large-video.mp4" {
			t.Fatalf("expected /tmp/large-video.mp4, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for background SendFile to start")
	}

	// While SendFile is still blocked, the UI thread must still render freely and show sending feedback.
	tt.Frame()
	if !tt.HasText("正在发送文件：large-video.mp4") {
		t.Fatalf("expected sending-file toast while SendFile is in flight; texts: %v", tt.Texts())
	}

	// Unblock SendFile and wait for the completion toast & message.
	close(api.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if tt.HasText("已发送文件：large-video.mp4") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected sent-file toast after SendFile unblocked; texts: %v", tt.Texts())
}



