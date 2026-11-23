package syncproto

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"tailchat/internal/model"
)

var (
	// ErrNotStarted is returned when operations require Start to have been called first.
	ErrNotStarted = errors.New("syncproto: service not started")
	// ErrTransferPaused is returned by DownloadFile when paused via PauseTransfer.
	ErrTransferPaused = errors.New("syncproto: transfer paused")
	// ErrTransferCancelled is returned by DownloadFile when cancelled via CancelTransfer.
	ErrTransferCancelled = errors.New("syncproto: transfer cancelled")
)

// Options configures a Service instance.
type Options struct {
	Platform          string
	DefaultDeviceName string
	DownloadDir       string
	IdentityGenerator func(name, platform string) (model.LocalIdentity, error)

	// AntiEntropyInterval overrides the background sync tick interval (default 15s).
	AntiEntropyInterval time.Duration
	// DownloadWorkers overrides concurrent chunk workers per file (default 4).
	DownloadWorkers int
}

type transferStopAction int

const (
	stopNone transferStopAction = iota
	stopPause
	stopCancel
)

type activeTransfer struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	action transferStopAction
}

func (a *activeTransfer) setAction(act transferStopAction) {
	a.mu.Lock()
	a.action = act
	a.mu.Unlock()
}

func (a *activeTransfer) getAction() transferStopAction {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.action
}

type pairingSession struct {
	secret    string
	expiresAt int64
}

// Service implements model.AppAPI by coordinating storage, CAS, transport, gossip, and transfers.
type Service struct {
	store model.Store
	cas   model.CASStore
	tr    model.PeerTransportProvider
	opts  Options

	mu       sync.RWMutex
	started  bool
	closed   bool
	identity model.LocalIdentity
	cfg      model.AppConfig
	route    model.RouteRecord

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	pairMu   sync.Mutex
	pair     *pairingSession
	memberMu sync.Mutex

	peerMu         sync.RWMutex
	connectedPeers map[string]bool

	transfersMu     sync.Mutex
	activeTransfers map[string]*activeTransfer
	resumeWakeCh    chan struct{}

	limiter *rateLimiter

	subsMu    sync.RWMutex
	nextSubID uint64
	subs      map[uint64]chan model.AppEvent
}

var _ model.AppAPI = (*Service)(nil)

// NewService constructs a Service wired to the provided store, CAS, and peer transport.
func NewService(st model.Store, cas model.CASStore, tr model.PeerTransportProvider, opts Options) (*Service, error) {
	if st == nil {
		return nil, errors.New("syncproto: store is nil")
	}
	if cas == nil {
		return nil, errors.New("syncproto: cas is nil")
	}
	if tr == nil {
		return nil, errors.New("syncproto: transport is nil")
	}
	if opts.AntiEntropyInterval <= 0 {
		opts.AntiEntropyInterval = 15 * time.Second
	}
	if opts.DownloadWorkers <= 0 {
		opts.DownloadWorkers = 4
	}
	return &Service{
		store:           st,
		cas:             cas,
		tr:              tr,
		opts:            opts,
		connectedPeers:  make(map[string]bool),
		activeTransfers: make(map[string]*activeTransfer),
		resumeWakeCh:    make(chan struct{}, 1),
		limiter:         newRateLimiter(0),
		subs:            make(map[uint64]chan model.AppEvent),
	}, nil
}

// Start loads or initializes local identity/config, starts transport, and launches background loops.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.closed {
		s.mu.Unlock()
		return errors.New("syncproto: service already closed")
	}
	s.mu.Unlock()

	id, err := s.loadOrCreateIdentity()
	if err != nil {
		return fmt.Errorf("load identity: %w", err)
	}
	cfg, err := s.loadOrCreateConfig(id)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	s.limiter.SetLimit(cfg.MaxDownloadBytesPerS)

	runCtx, runCancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.identity = id
	s.cfg = cfg
	s.ctx = runCtx
	s.cancel = runCancel
	s.started = true
	s.mu.Unlock()

	// Register handlers before starting transport so no early streams/events are missed.
	s.tr.SetStreamHandler(s.handleStream)
	s.tr.SetPeerStatusHandler(s.handlePeerStatus)

	route, err := s.tr.Start(ctx, id, cfg.DERP)
	if err != nil {
		runCancel()
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return fmt.Errorf("start transport: %w", err)
	}

	route, err = s.ensureSignedRoute(id, cfg.DERP, route)
	if err != nil {
		_ = s.tr.Close(ctx)
		runCancel()
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return fmt.Errorf("sign self route: %w", err)
	}

	if route.RouteEpoch > id.RouteEpoch {
		id.RouteEpoch = route.RouteEpoch
		if err := s.store.SaveIdentity(id); err != nil {
			_ = s.tr.Close(ctx)
			runCancel()
			s.mu.Lock()
			s.started = false
			s.mu.Unlock()
			return fmt.Errorf("save identity epoch: %w", err)
		}
	}

	s.mu.Lock()
	s.identity = id
	s.route = route
	s.mu.Unlock()

	if err := s.ensureSelfDeviceAndEvent(id, cfg, route); err != nil {
		_ = s.tr.Close(ctx)
		runCancel()
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return fmt.Errorf("bootstrap self device: %w", err)
	}

	s.refreshTransportPeers()

	// Seed connectedPeers from initial PeerStatuses snapshot.
	for _, st := range s.tr.PeerStatuses() {
		if st.Connected {
			s.peerMu.Lock()
			s.connectedPeers[st.DeviceID] = true
			s.peerMu.Unlock()
		}
	}

	s.wg.Add(1)
	go s.antiEntropyLoop(runCtx)

	return nil
}

// Close gracefully cancels background workers, pauses active transfers, and shuts down transport and store.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	wasStarted := s.started
	cancel := s.cancel
	s.mu.Unlock()

	if !wasStarted {
		return nil
	}

	// Cancel active pairing if running.
	_ = s.CancelPairing()

	// Pause all running transfers cleanly so their bitmaps are persisted before store.Close().
	s.pauseAllActiveTransfers()

	// Cancel background loops and wait for them to finish.
	if cancel != nil {
		cancel()
	}

	waitDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
	}

	trErr := s.tr.Close(ctx)
	stErr := s.store.Close()

	// Close subscriber channels cleanly after workers have stopped.
	s.subsMu.Lock()
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
	s.subsMu.Unlock()

	if trErr != nil {
		return trErr
	}
	return stErr
}

// Identity returns a copy of the local device identity.
func (s *Service) Identity() model.LocalIdentity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.identity
	id.SignPublicKey = cloneBytes(id.SignPublicKey)
	id.SignPrivateKey = cloneBytes(id.SignPrivateKey)
	id.GroupPublicKey = cloneBytes(id.GroupPublicKey)
	id.GroupPrivateKey = cloneBytes(id.GroupPrivateKey)
	return id
}

// Config returns the current runtime configuration.
func (s *Service) Config() model.AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// UpdateConfig persists updated configuration, applying DERP changes and rate limits if changed.
func (s *Service) UpdateConfig(ctx context.Context, cfg model.AppConfig) error {
	if err := s.requireStarted(); err != nil {
		return err
	}

	oldCfg := s.Config()
	if cfg.DeviceName == "" {
		cfg.DeviceName = oldCfg.DeviceName
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = oldCfg.DownloadDir
	}
	if cfg.DERP.Mode == "" {
		cfg.DERP = oldCfg.DERP
	}

	derpChanged := cfg.DERP != oldCfg.DERP

	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.limiter.SetLimit(cfg.MaxDownloadBytesPerS)

	s.mu.Lock()
	s.cfg = cfg
	id := s.identity
	nameChanged := cfg.DeviceName != id.DeviceName
	if nameChanged {
		id.DeviceName = cfg.DeviceName
		s.identity = id
	}
	s.mu.Unlock()

	if nameChanged {
		_ = s.store.SaveIdentity(id)
		s.memberMu.Lock()
		updated := false
		if dev, err := s.store.GetDevice(id.DeviceID); err == nil && dev != nil {
			dev.Name = cfg.DeviceName
			_ = s.store.UpsertDevice(*dev)
			updated = true
		}
		s.memberMu.Unlock()
		if updated {
			s.emit(model.AppEvent{Type: model.EventDevicesUpdated})
		}
	}

	s.emit(model.AppEvent{Type: model.EventConfigUpdated})

	if derpChanged {
		return s.UpdateDERP(ctx, cfg.DERP)
	}
	return nil
}

// UpdateDERP hot-switches DERP settings, increments RouteEpoch, signs MemberEventRoute, and gossips it.
func (s *Service) UpdateDERP(ctx context.Context, derp model.DERPConfig) error {
	if err := s.requireStarted(); err != nil {
		return err
	}

	route, err := s.tr.UpdateDERP(ctx, derp)
	if err != nil {
		return fmt.Errorf("transport update DERP: %w", err)
	}

	s.mu.Lock()
	id := s.identity
	cfg := s.cfg
	prevEpoch := id.RouteEpoch
	if s.route.RouteEpoch > prevEpoch {
		prevEpoch = s.route.RouteEpoch
	}
	if route.RouteEpoch <= prevEpoch {
		route.RouteEpoch = prevEpoch + 1
	}
	route.DeviceID = id.DeviceID
	route.SignPublicKey = cloneBytes(id.SignPublicKey)
	route.DERP = derp
	route.UpdatedAt = time.Now().UnixMilli()
	if err := model.SignRouteRecord(&route, ed25519.PrivateKey(id.SignPrivateKey)); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("sign updated route: %w", err)
	}

	id.RouteEpoch = route.RouteEpoch
	cfg.DERP = derp
	s.identity = id
	s.cfg = cfg
	s.route = route
	s.mu.Unlock()

	if err := s.store.SaveIdentity(id); err != nil {
		return err
	}
	if err := s.store.SaveConfig(cfg); err != nil {
		return err
	}
	s.emit(model.AppEvent{Type: model.EventConfigUpdated})

	ev := model.MemberEvent{
		EventID:        model.NewULID(),
		GroupID:        id.GroupID,
		Kind:           model.MemberEventRoute,
		TargetDeviceID: id.DeviceID,
		DeviceName:     cfg.DeviceName,
		Platform:       id.Platform,
		SignPublicKey:  cloneBytes(id.SignPublicKey),
		Route:          &route,
		Epoch:          route.RouteEpoch,
		IssuerDeviceID: id.DeviceID,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := model.SignMemberEvent(&ev, ed25519.PrivateKey(id.GroupPrivateKey)); err != nil {
		return fmt.Errorf("sign member route event: %w", err)
	}

	if _, err := s.applyMemberEvent(ev, ""); err != nil {
		return err
	}

	s.gossipMemberEvents(ctx, []model.MemberEvent{ev}, "")
	return nil
}

// SendText creates, persists, and broadcasts a text message.
func (s *Service) SendText(ctx context.Context, text string) (*model.Message, error) {
	if err := s.requireStarted(); err != nil {
		return nil, err
	}
	id := s.Identity()
	cfg := s.Config()

	seq, err := s.store.NextAuthorSeq(id.DeviceID)
	if err != nil {
		return nil, err
	}
	msg := model.Message{
		MsgID:          model.NewULID(),
		AuthorDeviceID: id.DeviceID,
		AuthorName:     cfg.DeviceName,
		AuthorSeq:      seq,
		Kind:           model.MessageKindText,
		TextBody:       text,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if _, err := s.store.SaveMessage(msg); err != nil {
		return nil, err
	}

	msgCopy := cloneMessage(msg)
	s.emit(model.AppEvent{
		Type:    model.EventMessageAdded,
		Message: &msgCopy,
	})
	s.broadcastMessages(ctx, []model.Message{msg})
	return &msg, nil
}

// SendFile ingests a local file into CAS, creates a file message, and broadcasts metadata to peers.
func (s *Service) SendFile(ctx context.Context, filePath string) (*model.Message, error) {
	if err := s.requireStarted(); err != nil {
		return nil, err
	}
	blob, err := s.cas.IngestFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("ingest file: %w", err)
	}
	return s.publishFileBlob(ctx, blob)
}

// SendBytes ingests in-memory bytes into CAS, creates a file message, and broadcasts metadata to peers.
func (s *Service) SendBytes(ctx context.Context, fileName, mimeType string, data []byte) (*model.Message, error) {
	if err := s.requireStarted(); err != nil {
		return nil, err
	}
	blob, err := s.cas.IngestBytes(fileName, mimeType, data)
	if err != nil {
		return nil, fmt.Errorf("ingest bytes: %w", err)
	}
	return s.publishFileBlob(ctx, blob)
}

func (s *Service) publishFileBlob(ctx context.Context, blob *model.FileBlob) (*model.Message, error) {
	id := s.Identity()
	cfg := s.Config()

	if blob.ChunkSize <= 0 {
		blob.ChunkSize = model.DefaultChunkSize
	}
	blob.Seeders = appendUniqueString(blob.Seeders, id.DeviceID)

	if err := s.store.UpsertFileBlob(*blob); err != nil {
		return nil, err
	}
	_ = s.store.AddBlobSeeder(blob.BlobHash, id.DeviceID)

	seq, err := s.store.NextAuthorSeq(id.DeviceID)
	if err != nil {
		return nil, err
	}
	msg := model.Message{
		MsgID:          model.NewULID(),
		AuthorDeviceID: id.DeviceID,
		AuthorName:     cfg.DeviceName,
		AuthorSeq:      seq,
		Kind:           model.MessageKindFile,
		BlobHash:       blob.BlobHash,
		CreatedAt:      time.Now().UnixMilli(),
		File:           cloneFileBlob(blob),
	}
	if _, err := s.store.SaveMessage(msg); err != nil {
		return nil, err
	}

	msgCopy := cloneMessage(msg)
	s.emit(model.AppEvent{
		Type:    model.EventMessageAdded,
		Message: &msgCopy,
	})
	s.broadcastMessages(ctx, []model.Message{msg})
	return &msg, nil
}

// DeleteMessage appends a tombstone message for msgID and broadcasts it to peers.
func (s *Service) DeleteMessage(ctx context.Context, msgID string) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	id := s.Identity()
	cfg := s.Config()

	seq, err := s.store.NextAuthorSeq(id.DeviceID)
	if err != nil {
		return err
	}
	msg := model.Message{
		MsgID:          model.NewULID(),
		AuthorDeviceID: id.DeviceID,
		AuthorName:     cfg.DeviceName,
		AuthorSeq:      seq,
		Kind:           model.MessageKindTombstone,
		TargetMsgID:    msgID,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if _, err := s.store.SaveMessage(msg); err != nil {
		return err
	}

	msgCopy := cloneMessage(msg)
	s.emit(model.AppEvent{
		Type:    model.EventMessageDeleted,
		Message: &msgCopy,
	})
	s.broadcastMessages(ctx, []model.Message{msg})
	return nil
}

// ListMessages returns paginated messages from the local store.
func (s *Service) ListMessages(limit int, beforeMsgID string) ([]model.Message, error) {
	return s.store.ListMessages(limit, beforeMsgID)
}

// SearchMessages searches messages via the local store.
func (s *Service) SearchMessages(query string, limit int) ([]model.Message, error) {
	return s.store.SearchMessages(query, limit)
}

// DownloadFile downloads missing chunks for blobHash concurrently and assembles the verified file.
func (s *Service) DownloadFile(ctx context.Context, blobHash string) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	return s.runDownload(ctx, blobHash)
}

// PauseTransfer pauses an active or queued download while preserving its verified chunk bitmap.
func (s *Service) PauseTransfer(blobHash string) error {
	s.transfersMu.Lock()
	at, ok := s.activeTransfers[blobHash]
	s.transfersMu.Unlock()

	if ok {
		at.setAction(stopPause)
		at.cancel()
		<-at.done
		return nil
	}

	task, err := s.store.GetTransferTask(blobHash)
	if err != nil || task == nil {
		return err
	}
	if task.State == model.TransferDone || task.State == model.TransferPaused {
		return nil
	}
	task.State = model.TransferPaused
	task.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpsertTransferTask(*task); err != nil {
		return err
	}
	tc := cloneTransferTask(*task)
	s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
	return nil
}

// CancelTransfer cancels an active or queued transfer and marks it cancelled.
func (s *Service) CancelTransfer(blobHash string) error {
	s.transfersMu.Lock()
	at, ok := s.activeTransfers[blobHash]
	s.transfersMu.Unlock()

	if ok {
		at.setAction(stopCancel)
		at.cancel()
		<-at.done
		return nil
	}

	task, err := s.store.GetTransferTask(blobHash)
	if err != nil || task == nil {
		return err
	}
	if task.State == model.TransferDone || task.State == model.TransferCancelled {
		return nil
	}
	task.State = model.TransferCancelled
	task.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.UpsertTransferTask(*task); err != nil {
		return err
	}
	tc := cloneTransferTask(*task)
	s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
	return nil
}

// GetTransfer returns the persisted transfer task for blobHash.
func (s *Service) GetTransfer(blobHash string) (*model.TransferTask, error) {
	return s.store.GetTransferTask(blobHash)
}

// ListTransfers returns all persisted transfer tasks.
func (s *Service) ListTransfers() ([]model.TransferTask, error) {
	return s.store.ListTransferTasks()
}

// HasActiveTransfers reports whether any transfer is currently queued or running.
func (s *Service) HasActiveTransfers() bool {
	s.transfersMu.Lock()
	active := len(s.activeTransfers) > 0
	s.transfersMu.Unlock()
	if active {
		return true
	}
	tasks, err := s.store.ListTransferTasks()
	if err != nil {
		return false
	}
	for _, t := range tasks {
		if t.State == model.TransferQueued || t.State == model.TransferRunning {
			return true
		}
	}
	return false
}

// ListDevices lists paired devices from the local store.
func (s *Service) ListDevices(includeRevoked bool) ([]model.Device, error) {
	return s.store.ListDevices(includeRevoked)
}

// PeerStatuses returns the transport's current peer connectivity snapshot.
func (s *Service) PeerStatuses() []model.PeerConnStatus {
	return s.tr.PeerStatuses()
}

// CreatePairingCode opens a temporary pairing listener and returns a one-time pairing code.
func (s *Service) CreatePairingCode(ctx context.Context, ttlSeconds int) (string, model.PairingToken, error) {
	if err := s.requireStarted(); err != nil {
		return "", model.PairingToken{}, err
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 300
	}

	secret, err := model.RandomSecret(24)
	if err != nil {
		return "", model.PairingToken{}, fmt.Errorf("generate pairing secret: %w", err)
	}

	addr, port, err := s.tr.StartPairing(ctx, s.handlePairingConn)
	if err != nil {
		return "", model.PairingToken{}, fmt.Errorf("start pairing listener: %w", err)
	}

	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second).UnixMilli()
	s.pairMu.Lock()
	s.pair = &pairingSession{
		secret:    secret,
		expiresAt: expiresAt,
	}
	s.pairMu.Unlock()

	id := s.Identity()
	cfg := s.Config()
	tok := model.PairingToken{
		Version:     1,
		GroupID:     id.GroupID,
		HostDevID:   id.DeviceID,
		HostName:    cfg.DeviceName,
		TailcatAddr: addr,
		PairingPort: port,
		Secret:      secret,
		DERP:        cfg.DERP,
		ExpiresAt:   expiresAt,
	}
	code, err := tok.Encode()
	if err != nil {
		_ = s.CancelPairing()
		return "", model.PairingToken{}, err
	}
	return code, tok, nil
}

// CancelPairing invalidates any active pairing code and closes the pairing listener.
func (s *Service) CancelPairing() error {
	s.pairMu.Lock()
	s.pair = nil
	s.pairMu.Unlock()
	return s.tr.StopPairing()
}

// JoinWithPairingCode connects to a pairing host, adopts the group keys, replays member events, and syncs.
func (s *Service) JoinWithPairingCode(ctx context.Context, code string) error {
	if err := s.requireStarted(); err != nil {
		return err
	}

	tok, err := model.DecodePairingToken(code)
	if err != nil {
		return err
	}
	if tok.IsExpired(time.Now()) {
		return errors.New("pairing code has expired")
	}

	conn, err := s.tr.DialPairing(ctx, tok.TailcatAddr, tok.PairingPort, tok.DERP)
	if err != nil {
		return fmt.Errorf("dial pairing host: %w", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	}

	id := s.Identity()
	cfg := s.Config()
	route := s.currentRoute()

	req := PairRequest{
		Secret:        tok.Secret,
		DeviceID:      id.DeviceID,
		DeviceName:    cfg.DeviceName,
		Platform:      id.Platform,
		SignPublicKey: cloneBytes(id.SignPublicKey),
		Route:         route,
	}
	if err := WriteFrame(conn, req); err != nil {
		return fmt.Errorf("write pair request: %w", err)
	}

	var resp PairResponse
	if err := ReadFrame(conn, &resp); err != nil {
		return fmt.Errorf("read pair response: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "pairing rejected by host"
		}
		return errors.New(resp.Error)
	}
	if resp.GroupID == "" || !model.VerifyEd25519KeyLength(resp.GroupPublicKey) || len(resp.GroupPrivateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid group credentials in pair response")
	}

	// Adopt the host's group credentials.
	s.mu.Lock()
	s.identity.GroupID = resp.GroupID
	s.identity.GroupPublicKey = cloneBytes(resp.GroupPublicKey)
	s.identity.GroupPrivateKey = cloneBytes(resp.GroupPrivateKey)
	updatedID := s.identity
	s.mu.Unlock()

	if err := s.store.SaveIdentity(updatedID); err != nil {
		return fmt.Errorf("save adopted group identity: %w", err)
	}
	if gs, ok := s.tr.(interface{ SetGroupSecret([]byte) }); ok {
		gs.SetGroupSecret(updatedID.GroupPrivateKey)
	}

	// Sort and replay all verified member events from the host.
	events := append([]model.MemberEvent(nil), resp.MemberEvents...)
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt != events[j].CreatedAt {
			return events[i].CreatedAt < events[j].CreatedAt
		}
		return events[i].EventID < events[j].EventID
	})

	for _, ev := range events {
		if ev.GroupID != resp.GroupID {
			continue
		}
		if _, err := s.applyMemberEvent(ev, tok.HostDevID); err != nil {
			return fmt.Errorf("apply member event %s: %w", ev.EventID, err)
		}
	}

	// Ensure self device row is marked IsSelf and carries our latest route.
	s.memberMu.Lock()
	if dev, err := s.store.GetDevice(updatedID.DeviceID); err == nil && dev != nil {
		dev.IsSelf = true
		if route.RouteEpoch >= dev.RouteEpoch {
			dev.Route = route
			dev.RouteEpoch = route.RouteEpoch
		}
		_ = s.store.UpsertDevice(*dev)
	}
	s.memberMu.Unlock()

	s.refreshTransportPeers()
	s.emit(model.AppEvent{Type: model.EventDevicesUpdated})

	// Trigger anti-entropy sync with all active peers asynchronously.
	s.goBackground(func(bgCtx context.Context) {
		s.syncAllPeers(bgCtx)
	})

	return nil
}

// RevokeDevice signs a revocation event, revokes the peer locally, and gossips to remaining peers.
func (s *Service) RevokeDevice(ctx context.Context, deviceID string) error {
	if err := s.requireStarted(); err != nil {
		return err
	}
	id := s.Identity()
	if deviceID == "" {
		return errors.New("device ID is empty")
	}
	if deviceID == id.DeviceID {
		return errors.New("cannot revoke self device")
	}

	ev := model.MemberEvent{
		EventID:        model.NewULID(),
		GroupID:        id.GroupID,
		Kind:           model.MemberEventRevoke,
		TargetDeviceID: deviceID,
		IssuerDeviceID: id.DeviceID,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := model.SignMemberEvent(&ev, ed25519.PrivateKey(id.GroupPrivateKey)); err != nil {
		return fmt.Errorf("sign revoke event: %w", err)
	}

	if _, err := s.applyMemberEvent(ev, ""); err != nil {
		return err
	}

	s.gossipMemberEvents(ctx, []model.MemberEvent{ev}, "")
	return nil
}

// Subscribe registers a non-blocking buffered event observer channel.
func (s *Service) Subscribe() (<-chan model.AppEvent, func()) {
	ch := make(chan model.AppEvent, 64)
	s.subsMu.Lock()
	s.nextSubID++
	subID := s.nextSubID
	s.subs[subID] = ch
	s.subsMu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			s.subsMu.Lock()
			if c, ok := s.subs[subID]; ok {
				delete(s.subs, subID)
				close(c)
			}
			s.subsMu.Unlock()
		})
	}
	return ch, unsub
}

func (s *Service) emit(ev model.AppEvent) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Service) requireStarted() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.started || s.closed {
		return ErrNotStarted
	}
	return nil
}

func (s *Service) currentRoute() model.RouteRecord {
	s.mu.RLock()
	r := s.route
	s.mu.RUnlock()
	if trRoute := s.tr.CurrentRoute(); trRoute.TailcatAddr != "" && trRoute.RouteEpoch >= r.RouteEpoch {
		if model.VerifyRouteRecord(trRoute) {
			return trRoute
		}
	}
	return r
}

func (s *Service) loadOrCreateIdentity() (model.LocalIdentity, error) {
	existing, err := s.store.LoadIdentity()
	if err != nil {
		return model.LocalIdentity{}, err
	}
	if existing != nil && existing.DeviceID != "" {
		return *existing, nil
	}
	if s.opts.IdentityGenerator == nil {
		return model.LocalIdentity{}, errors.New("no stored identity and IdentityGenerator is nil")
	}

	name := s.opts.DefaultDeviceName
	if name == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			name = h
		} else {
			name = "tailchat-device"
		}
	}
	platform := s.opts.Platform
	if platform == "" {
		platform = runtime.GOOS
	}

	id, err := s.opts.IdentityGenerator(name, platform)
	if err != nil {
		return model.LocalIdentity{}, err
	}
	if id.DeviceName == "" {
		id.DeviceName = name
	}
	if id.Platform == "" {
		id.Platform = platform
	}
	if len(id.SignPublicKey) == 0 || len(id.SignPrivateKey) == 0 {
		pub, priv, err := model.GenerateEd25519Keypair()
		if err != nil {
			return model.LocalIdentity{}, err
		}
		id.SignPublicKey = pub
		id.SignPrivateKey = priv
	}
	if id.GroupID == "" {
		id.GroupID = model.NewULID()
	}
	if len(id.GroupPublicKey) == 0 || len(id.GroupPrivateKey) == 0 {
		pub, priv, err := model.GenerateEd25519Keypair()
		if err != nil {
			return model.LocalIdentity{}, err
		}
		id.GroupPublicKey = pub
		id.GroupPrivateKey = priv
	}
	if id.RouteEpoch == 0 {
		id.RouteEpoch = 1
	}

	if err := s.store.SaveIdentity(id); err != nil {
		return model.LocalIdentity{}, err
	}
	return id, nil
}

func (s *Service) loadOrCreateConfig(id model.LocalIdentity) (model.AppConfig, error) {
	existing, err := s.store.LoadConfig()
	if err != nil {
		return model.AppConfig{}, err
	}
	if existing != nil {
		cfg := *existing
		changed := false
		if cfg.DeviceName == "" {
			cfg.DeviceName = id.DeviceName
			changed = true
		}
		if cfg.DownloadDir == "" && s.opts.DownloadDir != "" {
			cfg.DownloadDir = s.opts.DownloadDir
			changed = true
		}
		if cfg.DERP.Mode == "" {
			cfg.DERP = model.DefaultDERPConfig()
			changed = true
		}
		if changed {
			if err := s.store.SaveConfig(cfg); err != nil {
				return model.AppConfig{}, err
			}
		}
		return cfg, nil
	}

	autoDownload := int64(model.DefaultAutoDownloadDesktopBytes)
	if id.Platform == "ios" || id.Platform == "android" {
		autoDownload = model.DefaultAutoDownloadMobileBytes
	}
	cfg := model.AppConfig{
		DeviceName:           id.DeviceName,
		DERP:                 model.DefaultDERPConfig(),
		AutoDownloadMaxBytes: autoDownload,
		DownloadDir:          s.opts.DownloadDir,
		MinimizeToTray:       true,
	}
	if err := s.store.SaveConfig(cfg); err != nil {
		return model.AppConfig{}, err
	}
	return cfg, nil
}

func (s *Service) ensureSignedRoute(id model.LocalIdentity, derp model.DERPConfig, route model.RouteRecord) (model.RouteRecord, error) {
	if route.DeviceID == "" {
		route.DeviceID = id.DeviceID
	}
	if len(route.SignPublicKey) == 0 {
		route.SignPublicKey = cloneBytes(id.SignPublicKey)
	}
	if route.RouteEpoch == 0 {
		if id.RouteEpoch > 0 {
			route.RouteEpoch = id.RouteEpoch
		} else {
			route.RouteEpoch = 1
		}
	}
	if route.DERP.Mode == "" {
		route.DERP = derp
	}
	if route.UpdatedAt == 0 {
		route.UpdatedAt = time.Now().UnixMilli()
	}
	if !model.VerifyRouteRecord(route) {
		if err := model.SignRouteRecord(&route, ed25519.PrivateKey(id.SignPrivateKey)); err != nil {
			return model.RouteRecord{}, err
		}
	}
	return route, nil
}

func (s *Service) ensureSelfDeviceAndEvent(id model.LocalIdentity, cfg model.AppConfig, route model.RouteRecord) error {
	now := time.Now().UnixMilli()
	existing, err := s.store.GetDevice(id.DeviceID)
	if err != nil {
		return err
	}
	addedAt := now
	if existing != nil && existing.AddedAt > 0 {
		addedAt = existing.AddedAt
	}
	selfDev := model.Device{
		DeviceID:      id.DeviceID,
		Name:          cfg.DeviceName,
		Platform:      id.Platform,
		SignPublicKey: cloneBytes(id.SignPublicKey),
		Route:         route,
		RouteEpoch:    route.RouteEpoch,
		Revoked:       false,
		AddedAt:       addedAt,
		LastSeenAt:    now,
		IsSelf:        true,
	}
	if err := s.store.UpsertDevice(selfDev); err != nil {
		return err
	}

	events, err := s.store.ListMemberEvents()
	if err != nil {
		return err
	}
	hasSelfAdd := false
	for _, ev := range events {
		if ev.GroupID == id.GroupID && ev.Kind == model.MemberEventAdd && ev.TargetDeviceID == id.DeviceID {
			hasSelfAdd = true
			break
		}
	}
	if !hasSelfAdd {
		ev := model.MemberEvent{
			EventID:        model.NewULID(),
			GroupID:        id.GroupID,
			Kind:           model.MemberEventAdd,
			TargetDeviceID: id.DeviceID,
			DeviceName:     cfg.DeviceName,
			Platform:       id.Platform,
			SignPublicKey:  cloneBytes(id.SignPublicKey),
			Route:          &route,
			Epoch:          route.RouteEpoch,
			IssuerDeviceID: id.DeviceID,
			CreatedAt:      now,
		}
		if err := model.SignMemberEvent(&ev, ed25519.PrivateKey(id.GroupPrivateKey)); err != nil {
			return err
		}
		if _, err := s.store.SaveMemberEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func appendUniqueString(slice []string, val string) []string {
	if val == "" {
		return slice
	}
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}
