package transport

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"tailchat/internal/model"
)

// MemNetwork is an in-memory virtual mesh fabric connecting MemTransport nodes
// via net.Pipe() and hashicorp/yamux for deterministic multi-node tests.
type MemNetwork struct {
	mu               sync.RWMutex
	meshEndpoints    map[string]*MemTransport
	pairingEndpoints map[string]*memPairingEndpoint
}

type memPairingEndpoint struct {
	addr    string
	port    uint16
	ctx     context.Context
	handler model.PairingHandler
	owner   *MemTransport
}

// NewMemNetwork creates an isolated in-memory transport fabric.
func NewMemNetwork() *MemNetwork {
	return &MemNetwork{
		meshEndpoints:    make(map[string]*MemTransport),
		pairingEndpoints: make(map[string]*memPairingEndpoint),
	}
}

// NewTransport constructs a MemTransport attached to this MemNetwork.
func (n *MemNetwork) NewTransport() *MemTransport {
	return NewMemTransport(n)
}

func (n *MemNetwork) registerMesh(addr string, t *MemTransport) {
	n.mu.Lock()
	n.meshEndpoints[addr] = t
	n.mu.Unlock()
}

func (n *MemNetwork) unregisterMesh(addr string, t *MemTransport) {
	n.mu.Lock()
	if cur, ok := n.meshEndpoints[addr]; ok && cur == t {
		delete(n.meshEndpoints, addr)
	}
	n.mu.Unlock()
}

func (n *MemNetwork) lookupMesh(addr string) (*MemTransport, bool) {
	n.mu.RLock()
	t, ok := n.meshEndpoints[addr]
	n.mu.RUnlock()
	return t, ok
}

func (n *MemNetwork) registerPairing(ep *memPairingEndpoint) {
	n.mu.Lock()
	n.pairingEndpoints[ep.addr] = ep
	n.mu.Unlock()
}

func (n *MemNetwork) unregisterPairing(addr string) {
	n.mu.Lock()
	delete(n.pairingEndpoints, addr)
	n.mu.Unlock()
}

func (n *MemNetwork) lookupPairing(addr string) (*memPairingEndpoint, bool) {
	n.mu.RLock()
	ep, ok := n.pairingEndpoints[addr]
	n.mu.RUnlock()
	return ep, ok
}

// MemTransport implements model.PeerTransportProvider over MemNetwork using
// net.Pipe() and hashicorp/yamux.
type MemTransport struct {
	net *MemNetwork

	mu      sync.Mutex
	started bool
	closed  bool
	id      model.LocalIdentity
	derp    model.DERPConfig
	route   model.RouteRecord

	registeredAddrs []string

	rootCtx    context.Context
	rootCancel context.CancelFunc

	allowed       map[string]struct{}
	peers         map[string]*memPeerState
	streamHandler model.PeerStreamHandler
	statusHandler func(model.PeerConnStatus)

	pairingAddr   string
	pairingCancel context.CancelFunc

	wg sync.WaitGroup
}

var _ model.PeerTransportProvider = (*MemTransport)(nil)

type memPeerState struct {
	deviceID string

	dialMu sync.Mutex

	mu         sync.Mutex
	device     model.Device
	revoked    bool
	dialCancel context.CancelFunc
	dialActive bool

	outConn    net.Conn
	outSession *yamux.Session
	outAddr    string

	inConn    net.Conn
	inSession *yamux.Session

	status model.PeerConnStatus
}

// NewMemTransport constructs a MemTransport bound to net.
func NewMemTransport(net *MemNetwork) *MemTransport {
	if net == nil {
		net = NewMemNetwork()
	}
	return &MemTransport{
		net:     net,
		allowed: make(map[string]struct{}),
		peers:   make(map[string]*memPeerState),
	}
}

func newMemYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 5 * time.Second
	cfg.ConnectionWriteTimeout = 2 * time.Second
	cfg.LogOutput = io.Discard
	return cfg
}

// Start registers this node in MemNetwork and returns its initial signed RouteRecord.
func (t *MemTransport) Start(ctx context.Context, id model.LocalIdentity, derp model.DERPConfig) (model.RouteRecord, error) {
	if id.DeviceID == "" && id.NodePrivateKey != "" {
		priv, err := ParseNodePrivate(id.NodePrivateKey)
		if err != nil {
			return model.RouteRecord{}, err
		}
		id.DeviceID = FormatNodePublic(priv.Public())
	}
	if id.DeviceID == "" {
		return model.RouteRecord{}, errors.New("empty device_id")
	}
	if len(id.SignPrivateKey) != ed25519.PrivateKeySize {
		return model.RouteRecord{}, errors.New("invalid ed25519 sign_private_key")
	}
	if id.RouteEpoch == 0 {
		id.RouteEpoch = 1
	}

	derp = normalizeDERPConfig(derp)
	if _, _, _, err := BuildServerDERPConfig(derp); err != nil {
		return model.RouteRecord{}, err
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return model.RouteRecord{}, errors.New("mem transport already closed")
	}
	if t.started {
		t.mu.Unlock()
		return model.RouteRecord{}, errors.New("mem transport already started")
	}

	addr := deterministicMemAddr(id.DeviceID, id.PresharedKey, derp, id.RouteEpoch)
	rec := model.RouteRecord{
		DeviceID:      id.DeviceID,
		SignPublicKey: append([]byte(nil), id.SignPublicKey...),
		TailcatAddr:   addr,
		DERP:          derp,
		RouteEpoch:    id.RouteEpoch,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := model.SignRouteRecord(&rec, ed25519.PrivateKey(id.SignPrivateKey)); err != nil {
		t.mu.Unlock()
		return model.RouteRecord{}, fmt.Errorf("sign route record: %w", err)
	}

	t.id = id
	t.derp = derp
	t.route = rec
	t.registeredAddrs = append(t.registeredAddrs, addr)
	t.rootCtx, t.rootCancel = context.WithCancel(context.Background())
	t.started = true
	t.mu.Unlock()

	t.net.registerMesh(addr, t)
	return rec, nil
}

func deterministicMemAddr(deviceID, psk string, derp model.DERPConfig, epoch uint64) string {
	seed := fmt.Sprintf("%s|%s|%s|%d|%s|%s|%d", deviceID, psk, derp.Mode, derp.RegionID, derp.CustomHost, derp.DERPMapURL, epoch)
	return fmt.Sprintf("tcmem-%s-e%d", model.Blake3Hex([]byte(seed))[:24], epoch)
}

// UpdateDERP increments RouteEpoch, registers a fresh endpoint address in MemNetwork,
// signs the updated RouteRecord, and returns it.
func (t *MemTransport) UpdateDERP(ctx context.Context, derp model.DERPConfig) (model.RouteRecord, error) {
	derp = normalizeDERPConfig(derp)
	if _, _, _, err := BuildServerDERPConfig(derp); err != nil {
		return model.RouteRecord{}, err
	}

	t.mu.Lock()
	if !t.started || t.closed {
		t.mu.Unlock()
		return model.RouteRecord{}, errors.New("mem transport not running")
	}
	nextEpoch := t.route.RouteEpoch + 1
	newAddr := deterministicMemAddr(t.id.DeviceID, t.id.PresharedKey, derp, nextEpoch)
	rec := model.RouteRecord{
		DeviceID:      t.id.DeviceID,
		SignPublicKey: append([]byte(nil), t.id.SignPublicKey...),
		TailcatAddr:   newAddr,
		DERP:          derp,
		RouteEpoch:    nextEpoch,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := model.SignRouteRecord(&rec, ed25519.PrivateKey(t.id.SignPrivateKey)); err != nil {
		t.mu.Unlock()
		return model.RouteRecord{}, fmt.Errorf("sign updated route record: %w", err)
	}

	t.derp = derp
	t.route = rec
	t.id.RouteEpoch = nextEpoch
	t.registeredAddrs = append(t.registeredAddrs, newAddr)
	peersSnapshot := t.snapshotPeersLocked()
	t.mu.Unlock()

	t.net.registerMesh(newAddr, t)

	// Reset outbound sessions so peers reconnect cleanly under the updated config.
	for _, ps := range peersSnapshot {
		t.resetOutboundPeer(ps)
	}

	return rec, nil
}

// CurrentRoute returns a copy of the node's current signed RouteRecord.
func (t *MemTransport) CurrentRoute() model.RouteRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.route
	if len(r.SignPublicKey) > 0 {
		r.SignPublicKey = append([]byte(nil), r.SignPublicKey...)
	}
	if len(r.Signature) > 0 {
		r.Signature = append([]byte(nil), r.Signature...)
	}
	return r
}

// SetAllowedPeers replaces the allowlist and disconnects any peer removed from it.
func (t *MemTransport) SetAllowedPeers(peers []model.Device) {
	t.mu.Lock()
	selfID := t.id.DeviceID
	next := make(map[string]struct{}, len(peers))
	for _, dev := range peers {
		if dev.IsSelf || dev.Revoked || dev.DeviceID == "" || dev.DeviceID == selfID {
			continue
		}
		next[dev.DeviceID] = struct{}{}
	}

	var removed []*memPeerState
	for id := range t.allowed {
		if _, ok := next[id]; !ok {
			if ps, exists := t.peers[id]; exists {
				removed = append(removed, ps)
			}
		}
	}
	t.allowed = next
	t.mu.Unlock()

	for _, ps := range removed {
		t.teardownPeer(ps, true)
	}
}

// RevokePeer removes deviceID from the allowlist and immediately disconnects active sessions.
func (t *MemTransport) RevokePeer(deviceID string) {
	if deviceID == "" {
		return
	}
	t.mu.Lock()
	delete(t.allowed, deviceID)
	ps := t.peers[deviceID]
	t.mu.Unlock()

	if ps != nil {
		t.teardownPeer(ps, true)
	} else {
		t.emitStatus(model.PeerConnStatus{
			DeviceID:  deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			UpdatedAt: time.Now().UnixMilli(),
		})
	}
}

// SyncPeers updates known peers and proactively dials peers with smaller local DeviceID.
func (t *MemTransport) SyncPeers(peers []model.Device) {
	t.mu.Lock()
	if !t.started || t.closed {
		t.mu.Unlock()
		return
	}
	selfID := t.id.DeviceID
	rootCtx := t.rootCtx
	t.mu.Unlock()

	seen := make(map[string]struct{}, len(peers))
	var toRevoke []string
	var toStartDial []*memPeerState
	var toResetOutbound []*memPeerState

	for _, dev := range peers {
		if dev.IsSelf || dev.DeviceID == "" || dev.DeviceID == selfID {
			continue
		}
		if dev.Revoked {
			toRevoke = append(toRevoke, dev.DeviceID)
			continue
		}
		seen[dev.DeviceID] = struct{}{}

		ps := t.getOrCreatePeer(dev.DeviceID)
		newAddr := strings.TrimSpace(dev.Route.TailcatAddr)

		ps.mu.Lock()
		ps.device = dev
		ps.revoked = false
		routeChanged := ps.outSession != nil && ps.outAddr != newAddr
		shouldDial := selfID < dev.DeviceID
		if shouldDial && !ps.dialActive {
			ps.dialActive = true
			toStartDial = append(toStartDial, ps)
		} else if !shouldDial && ps.dialCancel != nil {
			cancel := ps.dialCancel
			ps.dialCancel = nil
			ps.dialActive = false
			cancel()
		}
		ps.mu.Unlock()

		if routeChanged {
			toResetOutbound = append(toResetOutbound, ps)
		}
	}

	t.mu.Lock()
	var removed []*memPeerState
	for id, ps := range t.peers {
		if _, ok := seen[id]; !ok {
			removed = append(removed, ps)
		}
	}
	t.mu.Unlock()

	for _, ps := range removed {
		ps.mu.Lock()
		if ps.dialCancel != nil {
			ps.dialCancel()
			ps.dialCancel = nil
			ps.dialActive = false
		}
		ps.mu.Unlock()
	}

	for _, id := range toRevoke {
		t.RevokePeer(id)
	}
	for _, ps := range toResetOutbound {
		t.resetOutboundPeer(ps)
	}
	for _, ps := range toStartDial {
		dialCtx, cancel := context.WithCancel(rootCtx)
		ps.mu.Lock()
		if ps.revoked || !ps.dialActive {
			ps.mu.Unlock()
			cancel()
			continue
		}
		ps.dialCancel = cancel
		ps.mu.Unlock()

		p := ps
		dCtx := dialCtx
		if !t.goSpawn(func() { t.peerDialLoop(dCtx, p) }) {
			cancel()
		}
	}
}

// OpenStream opens a multiplexed yamux stream to peerDeviceID with the 1-byte StreamKind header.
func (t *MemTransport) OpenStream(ctx context.Context, peerDeviceID string, kind model.StreamKind) (io.ReadWriteCloser, error) {
	t.mu.Lock()
	if !t.started || t.closed {
		t.mu.Unlock()
		return nil, errors.New("mem transport not running")
	}
	ps := t.peers[peerDeviceID]
	t.mu.Unlock()

	if ps == nil {
		return nil, fmt.Errorf("unknown peer %q", peerDeviceID)
	}

	if sess := t.pickActiveSession(ps); sess != nil {
		stream, err := openTypedStream(ctx, sess, kind)
		if err == nil {
			return stream, nil
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.clearClosedSession(ps, sess)
			}
			return nil, err
		}
		t.clearClosedSession(ps, sess)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ps.dialMu.Lock()
	defer ps.dialMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if sess := t.pickActiveSession(ps); sess != nil {
		stream, err := openTypedStream(ctx, sess, kind)
		if err == nil {
			return stream, nil
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.clearClosedSession(ps, sess)
			}
			return nil, err
		}
		t.clearClosedSession(ps, sess)
	}

	sess, err := t.ensureOutboundLocked(ctx, ps)
	if err != nil {
		return nil, err
	}
	return openTypedStream(ctx, sess, kind)
}

func (t *MemTransport) pickActiveSession(ps *memPeerState) *yamux.Session {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.revoked {
		return nil
	}
	if ps.outSession != nil && !ps.outSession.IsClosed() {
		return ps.outSession
	}
	if ps.inSession != nil && !ps.inSession.IsClosed() {
		return ps.inSession
	}
	return nil
}

func (t *MemTransport) clearClosedSession(ps *memPeerState, sess *yamux.Session) {
	var oldConn net.Conn
	ps.mu.Lock()
	if ps.outSession == sess {
		ps.outSession = nil
		oldConn = ps.outConn
		ps.outConn = nil
		ps.outAddr = ""
	}
	if ps.inSession == sess {
		ps.inSession = nil
		if oldConn == nil {
			oldConn = ps.inConn
		}
		ps.inConn = nil
	}
	ps.mu.Unlock()

	if oldConn != nil {
		_ = oldConn.Close()
	}
	_ = sess.Close()
}

func (t *MemTransport) ensureOutboundLocked(ctx context.Context, ps *memPeerState) (*yamux.Session, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	t.mu.Lock()
	if !t.started || t.closed {
		t.mu.Unlock()
		return nil, errors.New("mem transport not running")
	}
	selfID := t.id.DeviceID
	rootCtx := t.rootCtx
	t.mu.Unlock()

	ps.mu.Lock()
	if ps.revoked {
		ps.mu.Unlock()
		return nil, fmt.Errorf("peer %q is revoked", ps.deviceID)
	}
	if ps.outSession != nil && !ps.outSession.IsClosed() {
		s := ps.outSession
		ps.mu.Unlock()
		return s, nil
	}
	if ps.inSession != nil && !ps.inSession.IsClosed() {
		s := ps.inSession
		ps.mu.Unlock()
		return s, nil
	}
	addr := strings.TrimSpace(ps.device.Route.TailcatAddr)
	ps.mu.Unlock()

	if addr == "" {
		return nil, fmt.Errorf("peer %q has no tailcat_addr", ps.deviceID)
	}

	remote, ok := t.net.lookupMesh(addr)
	if !ok {
		err := fmt.Errorf("peer %q endpoint %q not found", ps.deviceID, addr)
		t.updatePeerStatus(ps, model.PeerConnStatus{
			DeviceID:  ps.deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			LastError: err.Error(),
			UpdatedAt: time.Now().UnixMilli(),
		})
		return nil, err
	}

	clientConn, serverConn := net.Pipe()
	if err := remote.acceptMemDial(selfID, serverConn); err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		t.updatePeerStatus(ps, model.PeerConnStatus{
			DeviceID:  ps.deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			LastError: err.Error(),
			UpdatedAt: time.Now().UnixMilli(),
		})
		return nil, fmt.Errorf("dial peer %q rejected: %w", ps.deviceID, err)
	}

	sess, err := yamux.Client(clientConn, newMemYamuxConfig())
	if err != nil {
		_ = clientConn.Close()
		return nil, fmt.Errorf("init mem yamux client: %w", err)
	}

	ps.mu.Lock()
	if ps.revoked || rootCtx.Err() != nil {
		ps.mu.Unlock()
		_ = clientConn.Close()
		_ = sess.Close()
		return nil, fmt.Errorf("peer %q revoked or closed during dial", ps.deviceID)
	}
	ps.outConn = clientConn
	ps.outSession = sess
	ps.outAddr = addr
	ps.mu.Unlock()

	t.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: true,
		PathKind:  model.ConnPathDirect,
		Endpoint:  "mem://" + addr,
		LatencyMs: 0.5,
		UpdatedAt: time.Now().UnixMilli(),
	})

	if !t.goSpawn(func() { t.serveYamuxSession(rootCtx, ps, sess, clientConn, true) }) {
		_ = clientConn.Close()
		_ = sess.Close()
		return nil, errors.New("mem transport closed")
	}

	return sess, nil
}

func (t *MemTransport) acceptMemDial(callerDeviceID string, serverConn net.Conn) error {
	t.mu.Lock()
	if !t.started || t.closed {
		t.mu.Unlock()
		return errors.New("remote transport closed")
	}
	_, allowed := t.allowed[callerDeviceID]
	rootCtx := t.rootCtx
	t.mu.Unlock()

	if !allowed {
		return fmt.Errorf("peer %q not in allowlist", callerDeviceID)
	}

	sess, err := yamux.Server(serverConn, newMemYamuxConfig())
	if err != nil {
		return err
	}

	ps := t.getOrCreatePeer(callerDeviceID)
	var prevConn net.Conn
	var prevSess *yamux.Session

	ps.mu.Lock()
	if ps.revoked {
		ps.mu.Unlock()
		_ = serverConn.Close()
		_ = sess.Close()
		return fmt.Errorf("peer %q is revoked", callerDeviceID)
	}
	prevConn = ps.inConn
	prevSess = ps.inSession
	ps.inConn = serverConn
	ps.inSession = sess
	ps.mu.Unlock()

	if prevConn != nil {
		_ = prevConn.Close()
	}
	if prevSess != nil {
		_ = prevSess.Close()
	}

	t.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:  callerDeviceID,
		Connected: true,
		PathKind:  model.ConnPathDirect,
		Endpoint:  "mem://inbound",
		LatencyMs: 0.5,
		UpdatedAt: time.Now().UnixMilli(),
	})

	if !t.goSpawn(func() { t.serveYamuxSession(rootCtx, ps, sess, serverConn, false) }) {
		_ = serverConn.Close()
		_ = sess.Close()
		return errors.New("remote transport closed")
	}

	return nil
}

func (t *MemTransport) peerDialLoop(ctx context.Context, ps *memPeerState) {
	backoff := 25 * time.Millisecond
	const maxBackoff = 500 * time.Millisecond

	for {
		if ctx.Err() != nil {
			return
		}
		if sess := t.pickActiveSession(ps); sess != nil {
			backoff = 25 * time.Millisecond
			select {
			case <-ctx.Done():
				return
			case <-sess.CloseChan():
			}
			continue
		}

		ps.mu.Lock()
		revoked := ps.revoked
		addr := strings.TrimSpace(ps.device.Route.TailcatAddr)
		ps.mu.Unlock()
		if revoked {
			return
		}
		if addr == "" {
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}

		ps.dialMu.Lock()
		sess, err := t.ensureOutboundLocked(ctx, ps)
		ps.dialMu.Unlock()

		if err == nil && sess != nil {
			backoff = 25 * time.Millisecond
			select {
			case <-ctx.Done():
				return
			case <-sess.CloseChan():
			}
			continue
		}

		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (t *MemTransport) serveYamuxSession(ctx context.Context, ps *memPeerState, sess *yamux.Session, conn net.Conn, isOutbound bool) {
	defer func() {
		stillConnected := false
		ps.mu.Lock()
		if isOutbound && ps.outSession == sess {
			ps.outSession = nil
			ps.outConn = nil
			ps.outAddr = ""
		}
		if !isOutbound && ps.inSession == sess {
			ps.inSession = nil
			ps.inConn = nil
		}
		stillConnected = (ps.outSession != nil && !ps.outSession.IsClosed()) ||
			(ps.inSession != nil && !ps.inSession.IsClosed())
		ps.mu.Unlock()

		_ = conn.Close()
		_ = sess.Close()

		if !stillConnected {
			t.updatePeerStatus(ps, model.PeerConnStatus{
				DeviceID:  ps.deviceID,
				Connected: false,
				PathKind:  model.ConnPathDisconnected,
				UpdatedAt: time.Now().UnixMilli(),
			})
		}
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return
		}
		st := stream
		if !t.goSpawn(func() { t.dispatchInboundStream(ctx, ps.deviceID, st) }) {
			_ = st.Close()
			return
		}
	}
}

func (t *MemTransport) goSpawn(fn func()) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		fn()
	}()
	return true
}

func (t *MemTransport) dispatchInboundStream(ctx context.Context, peerDeviceID string, st *yamux.Stream) {
	_ = st.SetReadDeadline(time.Now().Add(streamHeaderReadTimeout))
	var hdr [1]byte
	if _, err := io.ReadFull(st, hdr[:]); err != nil {
		_ = st.Close()
		return
	}
	_ = st.SetReadDeadline(time.Time{})

	t.mu.Lock()
	handler := t.streamHandler
	t.mu.Unlock()

	if handler == nil {
		_ = st.Close()
		return
	}
	handler(ctx, peerDeviceID, model.StreamKind(hdr[0]), st)
}

// SetStreamHandler registers the callback for incoming multiplexed streams.
func (t *MemTransport) SetStreamHandler(h model.PeerStreamHandler) {
	t.mu.Lock()
	t.streamHandler = h
	t.mu.Unlock()
}

// SetPeerStatusHandler registers the callback for peer connectivity updates.
func (t *MemTransport) SetPeerStatusHandler(h func(model.PeerConnStatus)) {
	t.mu.Lock()
	t.statusHandler = h
	t.mu.Unlock()
}

// PeerStatuses returns a sorted snapshot of all known peers' statuses.
func (t *MemTransport) PeerStatuses() []model.PeerConnStatus {
	t.mu.Lock()
	peers := t.snapshotPeersLocked()
	t.mu.Unlock()

	out := make([]model.PeerConnStatus, 0, len(peers))
	for _, ps := range peers {
		ps.mu.Lock()
		st := ps.status
		if st.DeviceID == "" {
			st.DeviceID = ps.deviceID
			st.PathKind = model.ConnPathDisconnected
		}
		ps.mu.Unlock()
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

// StartPairing registers an unauthenticated pairing endpoint in MemNetwork.
func (t *MemTransport) StartPairing(ctx context.Context, h model.PairingHandler) (string, uint16, error) {
	if h == nil {
		return "", 0, errors.New("pairing handler is required")
	}
	_ = t.StopPairing()

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return "", 0, errors.New("mem transport is closed")
	}
	addr := "tcpair-" + model.NewULID()
	port := model.DefaultPairingPort
	pCtx, pCancel := context.WithCancel(context.Background())
	t.pairingAddr = addr
	t.pairingCancel = pCancel
	t.mu.Unlock()

	t.net.registerPairing(&memPairingEndpoint{
		addr:    addr,
		port:    port,
		ctx:     pCtx,
		handler: h,
		owner:   t,
	})
	return addr, port, nil
}

// StopPairing unregisters the temporary pairing endpoint.
func (t *MemTransport) StopPairing() error {
	t.mu.Lock()
	addr := t.pairingAddr
	cancel := t.pairingCancel
	t.pairingAddr = ""
	t.pairingCancel = nil
	t.mu.Unlock()

	if addr != "" {
		t.net.unregisterPairing(addr)
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

// DialPairing connects directly to a pairing endpoint registered in MemNetwork.
func (t *MemTransport) DialPairing(ctx context.Context, tailcatAddr string, port uint16, derp model.DERPConfig) (net.Conn, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ep, ok := t.net.lookupPairing(strings.TrimSpace(tailcatAddr))
	if !ok {
		return nil, fmt.Errorf("pairing endpoint %q not found", tailcatAddr)
	}
	if port != 0 && ep.port != port {
		return nil, fmt.Errorf("pairing port mismatch: got %d, want %d", port, ep.port)
	}

	ep.owner.mu.Lock()
	if ep.owner.closed || ep.owner.pairingAddr != ep.addr {
		ep.owner.mu.Unlock()
		return nil, fmt.Errorf("pairing endpoint %q closed", tailcatAddr)
	}
	ep.owner.wg.Add(1)
	ep.owner.mu.Unlock()

	clientConn, serverConn := net.Pipe()
	go func() {
		defer ep.owner.wg.Done()
		ep.handler(ep.ctx, serverConn)
	}()
	return clientConn, nil
}

// Close shuts down the MemTransport, unregistering endpoints and closing all sessions.
func (t *MemTransport) Close(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cancel := t.rootCancel
	addrs := append([]string(nil), t.registeredAddrs...)
	t.registeredAddrs = nil
	peers := t.snapshotPeersLocked()
	t.mu.Unlock()

	for _, addr := range addrs {
		t.net.unregisterMesh(addr, t)
	}
	_ = t.StopPairing()
	if cancel != nil {
		cancel()
	}

	for _, ps := range peers {
		t.teardownPeer(ps, false)
	}

	t.wg.Wait()
	return nil
}

func (t *MemTransport) getOrCreatePeer(deviceID string) *memPeerState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ps, ok := t.peers[deviceID]; ok {
		return ps
	}
	ps := &memPeerState{
		deviceID: deviceID,
		status: model.PeerConnStatus{
			DeviceID:  deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			UpdatedAt: time.Now().UnixMilli(),
		},
	}
	t.peers[deviceID] = ps
	return ps
}

func (t *MemTransport) snapshotPeersLocked() []*memPeerState {
	out := make([]*memPeerState, 0, len(t.peers))
	for _, ps := range t.peers {
		out = append(out, ps)
	}
	return out
}

func (t *MemTransport) resetOutboundPeer(ps *memPeerState) {
	ps.mu.Lock()
	outConn := ps.outConn
	outSess := ps.outSession
	ps.outConn = nil
	ps.outSession = nil
	ps.outAddr = ""
	ps.mu.Unlock()

	if outConn != nil {
		_ = outConn.Close()
	}
	if outSess != nil {
		_ = outSess.Close()
	}
}

func (t *MemTransport) teardownPeer(ps *memPeerState, markRevoked bool) {
	ps.mu.Lock()
	if markRevoked {
		ps.revoked = true
	}
	if ps.dialCancel != nil {
		ps.dialCancel()
		ps.dialCancel = nil
		ps.dialActive = false
	}
	outConn := ps.outConn
	outSess := ps.outSession
	inConn := ps.inConn
	inSess := ps.inSession
	ps.outConn = nil
	ps.outSession = nil
	ps.outAddr = ""
	ps.inConn = nil
	ps.inSession = nil
	ps.mu.Unlock()

	// Close underlying net.Pipe() conns first so yamux.Close() never blocks on GoAway.
	if outConn != nil {
		_ = outConn.Close()
	}
	if outSess != nil {
		_ = outSess.Close()
	}
	if inConn != nil {
		_ = inConn.Close()
	}
	if inSess != nil {
		_ = inSess.Close()
	}

	t.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: false,
		PathKind:  model.ConnPathDisconnected,
		UpdatedAt: time.Now().UnixMilli(),
	})
}

func (t *MemTransport) updatePeerStatus(ps *memPeerState, st model.PeerConnStatus) {
	ps.mu.Lock()
	ps.status = st
	ps.mu.Unlock()
	t.emitStatus(st)
}

func (t *MemTransport) emitStatus(st model.PeerConnStatus) {
	t.mu.Lock()
	h := t.statusHandler
	t.mu.Unlock()
	if h != nil {
		h(st)
	}
}
