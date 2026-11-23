package ui

import (
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

	joinedCodes    []string
	revokedIDs     []string
	updatedDERPs   []model.DERPConfig
	downloadedHash []string
	sentFiles      []string
	lastSearch     string
	cancelPairCnt  int
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
	app.writeClipboardText = func(string, bool) error { return nil }
	app.readClipboardText = func() string { return "" }
	app.openLocalPath = func(string) error { return nil }
	app.openFileDialog = func() ([]string, error) { return nil, nil }

	tt := mygoui.NewTester(app.View, 540, 740)
	return mock, state, app, tt
}

func TestAllIconsSupported(t *testing.T) {
	if len(zenIcons) < 22 {
		t.Fatalf("expected at least 22 embedded SVG icons, got %d", len(zenIcons))
	}
	for _, required := range []string{"qrCode", "refreshCw", "link", "check"} {
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

	// 2. Settings & DERP modal: switch to 自建 derper & hot-switch.
	if err := tt.Click("设置与 DERP"); err != nil {
		t.Fatalf("click 设置与 DERP: %v", err)
	}
	tt.Frame()
	if !tt.HasText("DERP 中继服务器") {
		t.Fatalf("expected Settings modal; texts: %v", tt.Texts())
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
}
