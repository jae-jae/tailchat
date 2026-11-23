package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"tailchat/internal/model"
)

// ViewSnapshot is an immutable point-in-time copy of UI state for lock-free rendering.
type ViewSnapshot struct {
	Identity        model.LocalIdentity
	Config          model.AppConfig
	Locale          Locale
	Messages        []model.Message
	SearchQuery     string
	SearchResults   []model.Message
	Devices         []model.Device
	Peers           map[string]model.PeerConnStatus
	Transfers       map[string]model.TransferTask
	ToastText       string
	ToastIsErr      bool
	ToastExpiresAt  time.Time
	OnlineCount     int
	HasDirect       bool
	HasDERP         bool
	ConnectivityTxt string
}

// State is the thread-safe UI view-model backed by model.AppAPI.
type State struct {
	api model.AppAPI

	mu              sync.RWMutex
	identity        model.LocalIdentity
	config          model.AppConfig
	messages        []model.Message
	searchQuery     string
	searchResults   []model.Message
	devices         []model.Device
	peers           map[string]model.PeerConnStatus
	transfers       map[string]model.TransferTask
	toastText       string
	toastIsErr      bool
	toastExpiresAt  time.Time
	onInvalidate    func()
	onRemoteMessage func(model.Message)
	onConfigChanged func(model.AppConfig)

	subMu       sync.Mutex
	unsub       func()
	cancelLoop  context.CancelFunc
	loopDone    chan struct{}
	started     bool
}

// NewState creates a new UI view-model backed by api.
func NewState(api model.AppAPI) *State {
	return &State{
		api:       api,
		config:    model.AppConfig{Language: model.LanguageAuto, MinimizeToTray: true},
		peers:     make(map[string]model.PeerConnStatus),
		transfers: make(map[string]model.TransferTask),
	}
}

// SetInvalidate registers the callback invoked whenever background state changes
// require the window to repaint (e.g. win.Invalidate or services.Invalidate).
func (s *State) SetInvalidate(fn func()) {
	s.mu.Lock()
	s.onInvalidate = fn
	s.mu.Unlock()
}

// SetOnRemoteMessage registers a callback fired when a message authored by another
// device arrives via EventMessageAdded.
func (s *State) SetOnRemoteMessage(fn func(model.Message)) {
	s.mu.Lock()
	s.onRemoteMessage = fn
	s.mu.Unlock()
}

// SetOnConfigChanged registers a callback fired whenever AppConfig updates.
func (s *State) SetOnConfigChanged(fn func(model.AppConfig)) {
	s.mu.Lock()
	s.onConfigChanged = fn
	s.mu.Unlock()
}

func (s *State) notifyConfigChanged(cfg model.AppConfig) {
	s.mu.RLock()
	fn := s.onConfigChanged
	s.mu.RUnlock()
	if fn != nil {
		fn(cfg)
	}
}

func (s *State) notifyInvalidate() {
	s.mu.RLock()
	fn := s.onInvalidate
	s.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// Start performs the initial snapshot load and begins listening to AppAPI events.
func (s *State) Start(ctx context.Context) {
	s.subMu.Lock()
	if s.started {
		s.subMu.Unlock()
		return
	}
	s.started = true
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancelLoop = cancel
	ch, unsub := s.api.Subscribe()
	s.unsub = unsub
	done := make(chan struct{})
	s.loopDone = done
	s.subMu.Unlock()

	s.RefreshAll()

	go func() {
		defer close(done)
		for {
			select {
			case <-loopCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				s.handleEvent(ev)
			}
		}
	}()
}

// Stop unsubscribes from AppAPI events and waits for the event goroutine to exit.
func (s *State) Stop() {
	s.subMu.Lock()
	if !s.started {
		s.subMu.Unlock()
		return
	}
	s.started = false
	cancel := s.cancelLoop
	unsub := s.unsub
	done := s.loopDone
	s.cancelLoop = nil
	s.unsub = nil
	s.loopDone = nil
	s.subMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if unsub != nil {
		unsub()
	}
	if done != nil {
		<-done
	}
}

// RefreshAll reloads identity, config, messages, devices, peers, and transfers from AppAPI.
func (s *State) RefreshAll() {
	id := s.api.Identity()
	cfg := s.api.Config()
	msgs, _ := s.api.ListMessages(200, "")
	devs, _ := s.api.ListDevices(false)
	peers := s.api.PeerStatuses()
	transfers, _ := s.api.ListTransfers()

	sortMessagesAsc(msgs)

	s.mu.Lock()
	s.identity = id
	s.config = cfg
	s.messages = msgs
	s.devices = devs
	s.peers = make(map[string]model.PeerConnStatus, len(peers))
	for _, p := range peers {
		s.peers[p.DeviceID] = p
	}
	s.transfers = make(map[string]model.TransferTask, len(transfers))
	for _, t := range transfers {
		s.transfers[t.BlobHash] = t
	}
	q := s.searchQuery
	s.mu.Unlock()

	if q != "" {
		s.refreshSearchLocked(q)
	}
	s.notifyInvalidate()
}

func (s *State) handleEvent(ev model.AppEvent) {
	switch ev.Type {
	case model.EventMessageAdded:
		if ev.Message != nil {
			msg := *ev.Message
			var remoteCb func(model.Message)
			var isRemote bool
			var activeSearch string

			s.mu.Lock()
			s.upsertMessageLocked(msg)
			isRemote = s.identity.DeviceID != "" && msg.AuthorDeviceID != s.identity.DeviceID && msg.Kind != model.MessageKindTombstone
			remoteCb = s.onRemoteMessage
			activeSearch = s.searchQuery
			s.mu.Unlock()

			if activeSearch != "" {
				s.refreshSearchLocked(activeSearch)
			}
			if isRemote && remoteCb != nil {
				remoteCb(msg)
			}
		}
	case model.EventMessageDeleted:
		if ev.Message != nil {
			target := ev.Message.TargetMsgID
			if target == "" {
				target = ev.Message.MsgID
			}
			s.mu.Lock()
			s.deleteMessageLocked(target)
			activeSearch := s.searchQuery
			s.mu.Unlock()
			if activeSearch != "" {
				s.refreshSearchLocked(activeSearch)
			}
		}
	case model.EventTransferUpdated:
		if ev.Transfer != nil {
			t := *ev.Transfer
			s.mu.Lock()
			s.transfers[t.BlobHash] = t
			s.mu.Unlock()
			if t.State == model.TransferDone {
				if msgs, err := s.api.ListMessages(200, ""); err == nil {
					sortMessagesAsc(msgs)
					s.mu.Lock()
					s.messages = msgs
					s.mu.Unlock()
				}
			}
		}
	case model.EventDevicesUpdated:
		devs, _ := s.api.ListDevices(false)
		peers := s.api.PeerStatuses()
		s.mu.Lock()
		s.devices = devs
		for _, p := range peers {
			s.peers[p.DeviceID] = p
		}
		s.mu.Unlock()
	case model.EventPeerStatus:
		if ev.Peer != nil {
			p := *ev.Peer
			s.mu.Lock()
			s.peers[p.DeviceID] = p
			s.mu.Unlock()
		}
	case model.EventConfigUpdated:
		cfg := s.api.Config()
		s.mu.Lock()
		s.config = cfg
		s.mu.Unlock()
		s.notifyConfigChanged(cfg)
	case model.EventError:
		if ev.ErrText != "" {
			s.SetToast(ev.ErrText, true)
			return
		}
	}
	s.notifyInvalidate()
}

func (s *State) upsertMessageLocked(msg model.Message) {
	if msg.Kind == model.MessageKindTombstone {
		if msg.TargetMsgID != "" {
			s.deleteMessageLocked(msg.TargetMsgID)
		}
		return
	}
	for i := range s.messages {
		if s.messages[i].MsgID == msg.MsgID {
			s.messages[i] = msg
			return
		}
	}
	s.messages = append(s.messages, msg)
	sortMessagesAsc(s.messages)
}

func (s *State) deleteMessageLocked(msgID string) {
	if msgID == "" {
		return
	}
	out := s.messages[:0]
	for _, m := range s.messages {
		if m.MsgID != msgID {
			out = append(out, m)
		}
	}
	s.messages = out
}

// Config returns the latest cached AppConfig.
func (s *State) Config() model.AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// Locale returns the resolved UI Locale for the active AppConfig.Language preference.
func (s *State) Locale() Locale {
	return ResolveLocale(s.Config().Language)
}

// SetToast displays a transient status or error banner in the UI.
func (s *State) SetToast(text string, isErr bool) {
	s.mu.Lock()
	s.toastText = text
	s.toastIsErr = isErr
	if text == "" {
		s.toastExpiresAt = time.Time{}
	} else {
		s.toastExpiresAt = time.Now().Add(5 * time.Second)
	}
	s.mu.Unlock()
	s.notifyInvalidate()
}

// ClearToast clears the active toast banner.
func (s *State) ClearToast() {
	s.SetToast("", false)
}

// Snapshot returns a thread-safe copy of the view state for rendering.
func (s *State) Snapshot() ViewSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	msgs := make([]model.Message, len(s.messages))
	copy(msgs, s.messages)

	var searchRes []model.Message
	if s.searchQuery != "" {
		searchRes = make([]model.Message, len(s.searchResults))
		copy(searchRes, s.searchResults)
	}

	devs := make([]model.Device, len(s.devices))
	copy(devs, s.devices)

	peers := make(map[string]model.PeerConnStatus, len(s.peers))
	onlineCount := 0
	hasDirect := false
	hasDERP := false
	for id, p := range s.peers {
		peers[id] = p
		if id == s.identity.DeviceID {
			continue
		}
		if p.Connected {
			onlineCount++
			switch p.PathKind {
			case model.ConnPathDirect:
				hasDirect = true
			case model.ConnPathDERP:
				hasDERP = true
			}
		}
	}

	transfers := make(map[string]model.TransferTask, len(s.transfers))
	for k, v := range s.transfers {
		transfers[k] = v
	}

	loc := ResolveLocale(s.config.Language)
	connTxt := formatConnectivityLabel(loc, onlineCount, hasDirect, hasDERP)

	toastTxt := s.toastText
	toastErr := s.toastIsErr
	if toastTxt != "" && !s.toastExpiresAt.IsZero() && time.Now().After(s.toastExpiresAt) {
		toastTxt = ""
		toastErr = false
	}

	return ViewSnapshot{
		Identity:        s.identity,
		Config:          s.config,
		Locale:          loc,
		Messages:        msgs,
		SearchQuery:     s.searchQuery,
		SearchResults:   searchRes,
		Devices:         devs,
		Peers:           peers,
		Transfers:       transfers,
		ToastText:       toastTxt,
		ToastIsErr:      toastErr,
		ToastExpiresAt:  s.toastExpiresAt,
		OnlineCount:     onlineCount,
		HasDirect:       hasDirect,
		HasDERP:         hasDERP,
		ConnectivityTxt: connTxt,
	}
}

func formatConnectivityLabel(loc Locale, onlineCount int, hasDirect, hasDERP bool) string {
	t := tr(loc)
	if onlineCount == 0 {
		return t.ConnOffline
	}
	if hasDirect && hasDERP {
		return fmt.Sprintf(t.ConnDirectDERPF, onlineCount)
	}
	if hasDirect {
		return fmt.Sprintf(t.ConnDirectF, onlineCount)
	}
	return fmt.Sprintf(t.ConnDERPF, onlineCount)
}

// SetSearchQuery updates the live FTS5 search query and reloads matching messages.
func (s *State) SetSearchQuery(query string) {
	trimmed := strings.TrimSpace(query)
	s.mu.Lock()
	s.searchQuery = trimmed
	if trimmed == "" {
		s.searchResults = nil
		s.mu.Unlock()
		s.notifyInvalidate()
		return
	}
	s.mu.Unlock()
	s.refreshSearchLocked(trimmed)
	s.notifyInvalidate()
}

func (s *State) refreshSearchLocked(query string) {
	res, err := s.api.SearchMessages(query, 200)
	if err != nil {
		return
	}
	sortMessagesAsc(res)
	s.mu.Lock()
	if s.searchQuery == query {
		s.searchResults = res
	}
	s.mu.Unlock()
}

// SendText sends a text message via AppAPI and updates local state immediately.
func (s *State) SendText(ctx context.Context, text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	msg, err := s.api.SendText(ctx, trimmed)
	if err != nil {
		s.SetToast(tr(s.Locale()).ToastSendFailPref+err.Error(), true)
		return err
	}
	if msg != nil {
		s.mu.Lock()
		s.upsertMessageLocked(*msg)
		s.mu.Unlock()
		s.notifyInvalidate()
	}
	return nil
}

// SendFile ingests and broadcasts a local file via AppAPI.
func (s *State) SendFile(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	t := tr(s.Locale())
	msg, err := s.api.SendFile(ctx, path)
	if err != nil {
		s.SetToast(t.ToastFileFailPref+err.Error(), true)
		return err
	}
	if msg != nil {
		s.mu.Lock()
		s.upsertMessageLocked(*msg)
		s.mu.Unlock()
		s.SetToast(t.ToastFileSentPref+fileDisplayName(msg, path), false)
	}
	return nil
}

// SendBytes ingests and broadcasts in-memory bytes (e.g., pasted clipboard image).
func (s *State) SendBytes(ctx context.Context, fileName, mimeType string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	t := tr(s.Locale())
	msg, err := s.api.SendBytes(ctx, fileName, mimeType, data)
	if err != nil {
		s.SetToast(t.ToastPasteFailPref+err.Error(), true)
		return err
	}
	if msg != nil {
		s.mu.Lock()
		s.upsertMessageLocked(*msg)
		s.mu.Unlock()
		s.SetToast(t.ToastSentPref+fileName, false)
	}
	return nil
}

// DeleteMessage deletes a message via AppAPI.
func (s *State) DeleteMessage(ctx context.Context, msgID string) error {
	if err := s.api.DeleteMessage(ctx, msgID); err != nil {
		s.SetToast(tr(s.Locale()).ToastDelFailPref+err.Error(), true)
		return err
	}
	s.mu.Lock()
	s.deleteMessageLocked(msgID)
	s.mu.Unlock()
	s.notifyInvalidate()
	return nil
}

// DownloadFile starts or resumes downloading a content-addressed file blob.
func (s *State) DownloadFile(ctx context.Context, blobHash string) error {
	if err := s.api.DownloadFile(ctx, blobHash); err != nil {
		s.SetToast(tr(s.Locale()).ToastDLFailPref+err.Error(), true)
		return err
	}
	if t, err := s.api.GetTransfer(blobHash); err == nil && t != nil {
		s.mu.Lock()
		s.transfers[blobHash] = *t
		s.mu.Unlock()
		s.notifyInvalidate()
	}
	return nil
}

// PauseTransfer pauses an in-flight download.
func (s *State) PauseTransfer(blobHash string) error {
	if err := s.api.PauseTransfer(blobHash); err != nil {
		s.SetToast(tr(s.Locale()).ToastPauseFailPref+err.Error(), true)
		return err
	}
	if t, err := s.api.GetTransfer(blobHash); err == nil && t != nil {
		s.mu.Lock()
		s.transfers[blobHash] = *t
		s.mu.Unlock()
		s.notifyInvalidate()
	}
	return nil
}

// CancelTransfer cancels an in-flight or paused download.
func (s *State) CancelTransfer(blobHash string) error {
	if err := s.api.CancelTransfer(blobHash); err != nil {
		s.SetToast(tr(s.Locale()).ToastCancelFailPre+err.Error(), true)
		return err
	}
	if t, err := s.api.GetTransfer(blobHash); err == nil && t != nil {
		s.mu.Lock()
		s.transfers[blobHash] = *t
		s.mu.Unlock()
		s.notifyInvalidate()
	}
	return nil
}

// CreatePairingCode opens a short-lived pairing session and returns the encoded token.
func (s *State) CreatePairingCode(ctx context.Context, ttlSeconds int) (string, model.PairingToken, error) {
	code, tok, err := s.api.CreatePairingCode(ctx, ttlSeconds)
	if err != nil {
		s.SetToast(tr(s.Locale()).ToastPairFailPref+err.Error(), true)
		return "", model.PairingToken{}, err
	}
	return code, tok, nil
}

// CancelPairing stops any active pairing listener.
func (s *State) CancelPairing() error {
	return s.api.CancelPairing()
}

// JoinWithPairingCode joins an existing group using a pairing code.
func (s *State) JoinWithPairingCode(ctx context.Context, code string) error {
	t := tr(s.Locale())
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return fmt.Errorf("%s", t.ErrEmptyPairCode)
	}
	if err := s.api.JoinWithPairingCode(ctx, trimmed); err != nil {
		s.SetToast(t.ToastJoinFailPref+err.Error(), true)
		return err
	}
	s.RefreshAll()
	s.SetToast(t.ToastJoinedGroup, false)
	return nil
}

// RevokeDevice revokes a paired device and disconnects its tunnels.
func (s *State) RevokeDevice(ctx context.Context, deviceID string) error {
	t := tr(s.Locale())
	if err := s.api.RevokeDevice(ctx, deviceID); err != nil {
		s.SetToast(t.ToastRevokeFailPre+err.Error(), true)
		return err
	}
	s.RefreshAll()
	s.SetToast(t.ToastRevokedDevice, false)
	return nil
}

// ApplySettings persists config updates and optionally hot-switches DERP settings.
func (s *State) ApplySettings(ctx context.Context, cfg model.AppConfig, hotSwitchDERP bool) error {
	if err := s.api.UpdateConfig(ctx, cfg); err != nil {
		s.SetToast(tr(s.Locale()).ToastSaveFailPref+err.Error(), true)
		return err
	}
	if hotSwitchDERP {
		if err := s.api.UpdateDERP(ctx, cfg.DERP); err != nil {
			s.SetToast(tr(s.Locale()).ToastDERPFailPref+err.Error(), true)
			return err
		}
	}
	newCfg := s.api.Config()
	s.mu.Lock()
	s.config = newCfg
	s.mu.Unlock()
	s.notifyConfigChanged(newCfg)
	t := tr(ResolveLocale(newCfg.Language))
	if hotSwitchDERP {
		s.SetToast(t.ToastDERPSwitched, false)
	} else {
		s.SetToast(t.ToastSettingsSaved, false)
	}
	return nil
}

func sortMessagesAsc(msgs []model.Message) {
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].CreatedAt != msgs[j].CreatedAt {
			return msgs[i].CreatedAt < msgs[j].CreatedAt
		}
		return msgs[i].MsgID < msgs[j].MsgID
	})
}

func fileDisplayName(msg *model.Message, fallback string) string {
	if msg != nil && msg.File != nil && msg.File.FileName != "" {
		return msg.File.FileName
	}
	return fallback
}

// FormatBytes formats a byte size into a compact human-readable string.
func FormatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n < 0:
		return "0 B"
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	case n < gb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	}
}

// FormatTimestamp formats a UnixMilli timestamp into a compact clock string.
func FormatTimestamp(unixMilli int64) string {
	if unixMilli <= 0 {
		return ""
	}
	return time.UnixMilli(unixMilli).Local().Format("15:04")
}

// FormatTimeChip formats a UnixMilli timestamp into a Zen Popover timeline pill label
// using the auto-detected locale (falling back to English).
func FormatTimeChip(unixMilli int64) string {
	return FormatTimeChipLocale(ResolveLocale(model.LanguageAuto), unixMilli)
}
