package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"tailchat/internal/model"
)

const (
	defaultDialRetryInterval = 3 * time.Second
	maxDialRetryInterval     = 60 * time.Second
	defaultPingInterval      = 15 * time.Second
	derpSwitchGraceWindow    = 15 * time.Second
	defaultDrainTimeout      = 3 * time.Second
	streamHeaderReadTimeout  = 10 * time.Second
)

// TailcatOptions configures a TailcatMesh transport instance.
type TailcatOptions struct {
	Logf              func(format string, args ...any)
	MeshPort          uint16
	PairingPort       uint16
	DialRetryInterval time.Duration
	PingInterval      time.Duration
}

// TailcatMesh implements model.PeerTransportProvider over Tailcat WireGuard+DERP
// tunnels multiplexed with hashicorp/yamux, plus optional direct TCP+mTLS LAN tunnels.
type TailcatMesh struct {
	opts TailcatOptions

	mu       sync.Mutex
	started  bool
	closed   bool
	id       model.LocalIdentity
	nodePriv key.NodePrivate
	nodePub  key.NodePublic
	psk      tailcat.PresharedKey
	derp     model.DERPConfig
	route    model.RouteRecord

	rootCtx    context.Context
	rootCancel context.CancelFunc

	allowSet          tailcat.KeySet
	allowedKeys       map[string]key.NodePublic
	allowedSignKeys   map[string]ed25519.PublicKey
	allowedClientPubs map[key.NodePublic]string
	deviceClientPubs  map[string]key.NodePublic
	groupSecret       []byte
	server            *tailcat.Server
	listener          net.Listener
	drainingServers   map[*tailcat.Server]net.Listener

	// Direct LAN / custom-IP mTLS transport state.
	directCert          tls.Certificate
	directListenPort    int
	directActualPort    int
	directListener      net.Listener
	peerDirectOverrides map[string]string
	peerLearnedDirect   map[string][]string

	peers         map[string]*peerState
	streamHandler model.PeerStreamHandler
	statusHandler func(model.PeerConnStatus)

	pairingServer   *tailcat.Server
	pairingListener net.Listener
	pairingCancel   context.CancelFunc

	wg sync.WaitGroup
}

var (
	_ model.PeerTransportProvider     = (*TailcatMesh)(nil)
	_ model.DirectTransportConfigurer = (*TailcatMesh)(nil)
)

type peerState struct {
	deviceID string
	pubKey   key.NodePublic

	// dialMu serializes outbound dial attempts so at most one outbound tunnel is
	// created/dialed per peer at a time. Lock ordering: never acquire m.mu while
	// holding dialMu or mu; always acquire dialMu before mu if both are needed.
	dialMu sync.Mutex

	mu         sync.Mutex
	device     model.Device
	revoked    bool
	dialCancel context.CancelFunc
	dialActive bool

	outClient         *tailcat.Client
	outSession        *yamux.Session
	outAddr           string
	outDERPURL        string
	outIsDirect       bool
	outDirectKind     model.ConnPathKind
	outDirectAddr     string
	lastDirectAttempt time.Time

	inSession     *yamux.Session
	inIsDirect    bool
	inDirectKind  model.ConnPathKind
	inDirectAddr  string
	inboundPubKey key.NodePublic

	status model.PeerConnStatus
}

// NewTailcatMesh constructs a new TailcatMesh with sensible defaults.
func NewTailcatMesh(opts TailcatOptions) *TailcatMesh {
	if opts.MeshPort == 0 {
		opts.MeshPort = model.DefaultMeshPort
	}
	if opts.PairingPort == 0 {
		opts.PairingPort = model.DefaultPairingPort
	}
	if opts.DialRetryInterval <= 0 {
		opts.DialRetryInterval = defaultDialRetryInterval
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = defaultPingInterval
	}
	return &TailcatMesh{
		opts:                opts,
		allowedKeys:         make(map[string]key.NodePublic),
		allowedSignKeys:     make(map[string]ed25519.PublicKey),
		allowedClientPubs:   make(map[key.NodePublic]string),
		deviceClientPubs:    make(map[string]key.NodePublic),
		drainingServers:     make(map[*tailcat.Server]net.Listener),
		peerDirectOverrides: make(map[string]string),
		peerLearnedDirect:   make(map[string][]string),
		peers:               make(map[string]*peerState),
	}
}

func (m *TailcatMesh) logf(format string, args ...any) {
	if m.opts.Logf != nil {
		m.opts.Logf(format, args...)
	}
}

func newYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 25 * time.Second
	cfg.ConnectionWriteTimeout = 60 * time.Second
	cfg.MaxStreamWindowSize = 1024 * 1024
	cfg.LogOutput = io.Discard
	return cfg
}

// Start initializes the local persistent tailcat.Server and begins accepting
// authenticated peer connections on MeshPort.
func (m *TailcatMesh) Start(ctx context.Context, id model.LocalIdentity, derp model.DERPConfig) (model.RouteRecord, error) {
	nodePriv, err := ParseNodePrivate(id.NodePrivateKey)
	if err != nil {
		return model.RouteRecord{}, fmt.Errorf("invalid local identity node private key: %w", err)
	}
	nodePub := nodePriv.Public()
	canonicalDeviceID := FormatNodePublic(nodePub)
	if id.DeviceID != "" && id.DeviceID != canonicalDeviceID {
		return model.RouteRecord{}, fmt.Errorf("device_id %q does not match node_private_key public key %q", id.DeviceID, canonicalDeviceID)
	}
	id.DeviceID = canonicalDeviceID

	psk, err := ParsePresharedKey(id.PresharedKey)
	if err != nil {
		return model.RouteRecord{}, fmt.Errorf("invalid local identity preshared key: %w", err)
	}
	if len(id.SignPrivateKey) != ed25519.PrivateKeySize {
		return model.RouteRecord{}, errors.New("invalid local identity ed25519 sign_private_key")
	}
	if id.RouteEpoch == 0 {
		id.RouteEpoch = 1
	}

	derp = normalizeDERPConfig(derp)
	directCert, err := buildIdentityTLSCert(id)
	if err != nil {
		return model.RouteRecord{}, fmt.Errorf("build direct mTLS cert: %w", err)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return model.RouteRecord{}, errors.New("tailcat mesh already closed")
	}
	if m.started {
		m.mu.Unlock()
		return model.RouteRecord{}, errors.New("tailcat mesh already started")
	}
	m.id = id
	m.nodePriv = nodePriv
	m.nodePub = nodePub
	m.psk = psk
	m.derp = derp
	m.directCert = directCert
	m.groupSecret = append([]byte(nil), id.GroupPrivateKey...)
	m.rootCtx, m.rootCancel = context.WithCancel(context.Background())
	m.mu.Unlock()

	srv, ln, rec, err := m.startServer(ctx, id, nodePriv, psk, derp, id.RouteEpoch)
	if err != nil {
		m.mu.Lock()
		if m.rootCancel != nil {
			m.rootCancel()
		}
		m.mu.Unlock()
		return model.RouteRecord{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = ln.Close()
		_ = srv.Close()
		return model.RouteRecord{}, errors.New("tailcat mesh closed during startup")
	}
	m.server = srv
	m.listener = ln
	m.route = rec
	m.started = true
	rootCtx := m.rootCtx
	m.mu.Unlock()

	m.goSpawn(func() { m.acceptLoop(srv, ln) })
	m.goSpawn(func() { m.statusMonitorLoop(rootCtx) })

	return rec, nil
}

func (m *TailcatMesh) startServer(
	ctx context.Context,
	id model.LocalIdentity,
	nodePriv key.NodePrivate,
	psk tailcat.PresharedKey,
	derp model.DERPConfig,
	epoch uint64,
) (*tailcat.Server, net.Listener, model.RouteRecord, error) {
	regionID, region, mapURL, err := BuildServerDERPConfig(derp)
	if err != nil {
		return nil, nil, model.RouteRecord{}, err
	}

	srv := &tailcat.Server{
		Key:          nodePriv,
		PresharedKey: psk,
		RegionID:     regionID,
		Region:       region,
		DERPMapURL:   mapURL,
		AllowClient:  m.allowSet.Contains,
		Logf:         m.logf,
	}
	if err := srv.StartContext(ctx); err != nil {
		_ = srv.Close()
		return nil, nil, model.RouteRecord{}, fmt.Errorf("start tailcat server: %w", err)
	}

	ln, err := srv.Listen(ctx, "tcp", fmt.Sprintf(":%d", m.opts.MeshPort))
	if err != nil {
		_ = srv.Close()
		return nil, nil, model.RouteRecord{}, fmt.Errorf("listen on mesh port %d: %w", m.opts.MeshPort, err)
	}

	addr, err := FormatServerAddr(srv, derp)
	if err != nil {
		_ = ln.Close()
		_ = srv.Close()
		return nil, nil, model.RouteRecord{}, fmt.Errorf("format server tailcat addr: %w", err)
	}

	rec := model.RouteRecord{
		DeviceID:      id.DeviceID,
		SignPublicKey: append([]byte(nil), id.SignPublicKey...),
		TailcatAddr:   string(addr),
		DERP:          derp,
		RouteEpoch:    epoch,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := model.SignRouteRecord(&rec, ed25519.PrivateKey(id.SignPrivateKey)); err != nil {
		_ = ln.Close()
		_ = srv.Close()
		return nil, nil, model.RouteRecord{}, fmt.Errorf("sign route record: %w", err)
	}

	return srv, ln, rec, nil
}

// UpdateDERP hot-switches the local tailcat.Server to the new DERP configuration,
// increments RouteEpoch, signs a new RouteRecord, drains the previous server after
// a brief grace window, and restarts outbound clients with the updated DERPMapURL.
func (m *TailcatMesh) UpdateDERP(ctx context.Context, derp model.DERPConfig) (model.RouteRecord, error) {
	derp = normalizeDERPConfig(derp)

	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return model.RouteRecord{}, errors.New("tailcat mesh not running")
	}
	id := m.id
	nodePriv := m.nodePriv
	psk := m.psk
	nextEpoch := m.route.RouteEpoch + 1
	m.mu.Unlock()

	newSrv, newLn, newRec, err := m.startServer(ctx, id, nodePriv, psk, derp, nextEpoch)
	if err != nil {
		return model.RouteRecord{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = newLn.Close()
		_ = newSrv.Close()
		return model.RouteRecord{}, errors.New("tailcat mesh closed during DERP update")
	}
	oldSrv := m.server
	oldLn := m.listener
	m.server = newSrv
	m.listener = newLn
	m.derp = derp
	m.route = newRec
	m.id.RouteEpoch = nextEpoch
	if oldSrv != nil {
		m.drainingServers[oldSrv] = oldLn
	}
	peersSnapshot := m.snapshotPeersLocked()
	rootCtx := m.rootCtx
	m.mu.Unlock()

	m.goSpawn(func() { m.acceptLoop(newSrv, newLn) })

	if oldSrv != nil {
		m.goSpawn(func() { m.retireServerAfterGrace(rootCtx, oldSrv, oldLn, derpSwitchGraceWindow) })
	}

	// Restart outbound clients so they adopt any updated DERPMapURL.
	for _, ps := range peersSnapshot {
		m.resetOutboundPeer(ps)
	}

	return newRec, nil
}

func (m *TailcatMesh) retireServerAfterGrace(ctx context.Context, srv *tailcat.Server, ln net.Listener, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}

	m.mu.Lock()
	_, stillTracked := m.drainingServers[srv]
	if stillTracked {
		delete(m.drainingServers, srv)
	}
	m.mu.Unlock()

	if !stillTracked {
		return
	}
	if ln != nil {
		_ = ln.Close()
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), defaultDrainTimeout)
	defer cancel()
	_ = srv.DrainTCP(drainCtx)
	_ = srv.Close()
}

// CurrentRoute returns a copy of the local node's current signed RouteRecord.
func (m *TailcatMesh) CurrentRoute() model.RouteRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.route
	if len(r.SignPublicKey) > 0 {
		r.SignPublicKey = append([]byte(nil), r.SignPublicKey...)
	}
	if len(r.Signature) > 0 {
		r.Signature = append([]byte(nil), r.Signature...)
	}
	return r
}

// SetGroupSecret updates the group secret used to derive per-pair outbound client keys
// (called when a newly paired device adopts an existing group's root key).
func (m *TailcatMesh) SetGroupSecret(secret []byte) {
	m.mu.Lock()
	m.groupSecret = append([]byte(nil), secret...)
	selfID := m.id.DeviceID
	for devID := range m.allowedKeys {
		if oldPub, ok := m.deviceClientPubs[devID]; ok {
			m.allowSet.Remove(oldPub)
			delete(m.allowedClientPubs, oldPub)
		}
		if len(m.groupSecret) > 0 && selfID != "" {
			derivedPub := DerivePairClientKey(m.groupSecret, devID, selfID).Public()
			m.allowSet.Add(derivedPub)
			m.deviceClientPubs[devID] = derivedPub
			m.allowedClientPubs[derivedPub] = devID
		}
	}
	m.mu.Unlock()
}

// SetAllowedPeers updates the server allowlist and disconnects any peer removed from it.
func (m *TailcatMesh) SetAllowedPeers(peers []model.Device) {
	nextAllowed := make(map[string]key.NodePublic, len(peers))
	nextSignKeys := make(map[string]ed25519.PublicKey, len(peers))

	m.mu.Lock()
	selfID := m.id.DeviceID
	for _, dev := range peers {
		if dev.IsSelf || dev.Revoked || dev.DeviceID == "" || dev.DeviceID == selfID {
			continue
		}
		pub, err := ParseNodePublic(dev.DeviceID)
		if err != nil {
			m.logf("tailcat mesh: ignoring invalid peer key %q: %v", dev.DeviceID, err)
			continue
		}
		canonical := FormatNodePublic(pub)
		nextAllowed[canonical] = pub
		signKey := dev.SignPublicKey
		if len(signKey) != ed25519.PublicKeySize {
			signKey = dev.Route.SignPublicKey
		}
		if len(signKey) == ed25519.PublicKeySize {
			nextSignKeys[canonical] = append(ed25519.PublicKey(nil), signKey...)
		}
		m.allowSet.Add(pub)
		if len(m.groupSecret) > 0 && selfID != "" {
			derivedPub := DerivePairClientKey(m.groupSecret, canonical, selfID).Public()
			if prevPub, ok := m.deviceClientPubs[canonical]; ok && prevPub != derivedPub {
				m.allowSet.Remove(prevPub)
				delete(m.allowedClientPubs, prevPub)
			}
			m.allowSet.Add(derivedPub)
			m.deviceClientPubs[canonical] = derivedPub
			m.allowedClientPubs[derivedPub] = canonical
		}
	}

	var removedIDs []string
	var removedPubs []key.NodePublic
	for devID, pub := range m.allowedKeys {
		if _, ok := nextAllowed[devID]; !ok {
			m.allowSet.Remove(pub)
			removedIDs = append(removedIDs, devID)
			removedPubs = append(removedPubs, pub)
			if derivedPub, ok := m.deviceClientPubs[devID]; ok {
				m.allowSet.Remove(derivedPub)
				delete(m.allowedClientPubs, derivedPub)
				delete(m.deviceClientPubs, devID)
				removedPubs = append(removedPubs, derivedPub)
			}
		}
	}
	m.allowedKeys = nextAllowed
	m.allowedSignKeys = nextSignKeys

	servers := m.activeServersLocked()
	var removedPeers []*peerState
	for _, devID := range removedIDs {
		if ps, ok := m.peers[devID]; ok {
			removedPeers = append(removedPeers, ps)
		}
	}
	m.mu.Unlock()

	for _, pub := range removedPubs {
		for _, srv := range servers {
			srv.DisconnectClient(pub)
		}
	}
	for _, ps := range removedPeers {
		m.teardownPeer(ps, true)
	}
}

// RevokePeer removes deviceID from the allowlist and immediately drops any
// active inbound or outbound tunnels/clients to it.
func (m *TailcatMesh) RevokePeer(deviceID string) {
	if deviceID == "" {
		return
	}

	var pub key.NodePublic
	var hasPub bool
	if parsed, err := ParseNodePublic(deviceID); err == nil {
		pub = parsed
		hasPub = true
		deviceID = FormatNodePublic(parsed)
	}

	var disconnectPubs []key.NodePublic
	m.mu.Lock()
	if existing, ok := m.allowedKeys[deviceID]; ok {
		pub = existing
		hasPub = true
		delete(m.allowedKeys, deviceID)
	}
	delete(m.allowedSignKeys, deviceID)
	if hasPub {
		m.allowSet.Remove(pub)
		disconnectPubs = append(disconnectPubs, pub)
	}
	if derivedPub, ok := m.deviceClientPubs[deviceID]; ok {
		m.allowSet.Remove(derivedPub)
		delete(m.allowedClientPubs, derivedPub)
		delete(m.deviceClientPubs, deviceID)
		disconnectPubs = append(disconnectPubs, derivedPub)
	}
	servers := m.activeServersLocked()
	ps := m.peers[deviceID]
	m.mu.Unlock()

	for _, dp := range disconnectPubs {
		for _, srv := range servers {
			srv.DisconnectClient(dp)
		}
	}
	if ps != nil {
		m.teardownPeer(ps, true)
	} else {
		m.emitStatus(model.PeerConnStatus{
			DeviceID:  deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			UpdatedAt: time.Now().UnixMilli(),
		})
	}
}

// SyncPeers updates known peers and ensures deterministic pairwise connectivity
// (smaller DeviceID, or any peer with an explicit custom direct IP override,
// proactively maintains a single outbound yamux tunnel).
func (m *TailcatMesh) SyncPeers(peers []model.Device) {
	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return
	}
	selfID := m.id.DeviceID
	localDERP := m.derp
	rootCtx := m.rootCtx
	overrideIDs := make(map[string]bool, len(m.peerDirectOverrides))
	for id, addr := range m.peerDirectOverrides {
		if strings.TrimSpace(addr) != "" {
			overrideIDs[id] = true
		}
	}
	m.mu.Unlock()

	seen := make(map[string]struct{}, len(peers))
	var toRevoke []string
	var toStartDial []*peerState
	var toResetOutbound []*peerState

	for _, dev := range peers {
		if dev.IsSelf || dev.DeviceID == "" || dev.DeviceID == selfID {
			continue
		}
		if dev.Revoked {
			toRevoke = append(toRevoke, dev.DeviceID)
			continue
		}
		pub, err := ParseNodePublic(dev.DeviceID)
		if err != nil {
			m.logf("tailcat mesh: SyncPeers skipping invalid peer %q: %v", dev.DeviceID, err)
			continue
		}
		dev.DeviceID = FormatNodePublic(pub)
		seen[dev.DeviceID] = struct{}{}

		ps := m.getOrCreatePeer(dev.DeviceID, pub)
		newAddr := strings.TrimSpace(dev.Route.TailcatAddr)
		newDERPURL := derpMapURLForPeer(dev.Route.DERP, localDERP)

		ps.mu.Lock()
		ps.device = dev
		ps.revoked = false
		routeChanged := ps.outClient != nil && (ps.outAddr != newAddr || ps.outDERPURL != newDERPURL)
		shouldDial := selfID < dev.DeviceID || overrideIDs[dev.DeviceID]
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

	// Cancel background dialers for peers no longer in the sync list.
	m.mu.Lock()
	var removed []*peerState
	for id, ps := range m.peers {
		if _, ok := seen[id]; !ok {
			removed = append(removed, ps)
		}
	}
	m.mu.Unlock()

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
		m.RevokePeer(id)
	}
	for _, ps := range toResetOutbound {
		m.resetOutboundPeer(ps)
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
		if !m.goSpawn(func() { m.peerDialLoop(dCtx, p) }) {
			cancel()
		}
	}
}

// OpenStream opens a multiplexed yamux stream of the given kind to peerDeviceID.
// It reuses any active inbound or outbound yamux session, or performs a single
// deduplicated fallback outbound dial if none exists yet.
func (m *TailcatMesh) OpenStream(ctx context.Context, peerDeviceID string, kind model.StreamKind) (io.ReadWriteCloser, error) {
	if parsed, err := ParseNodePublic(peerDeviceID); err == nil {
		peerDeviceID = FormatNodePublic(parsed)
	}

	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return nil, errors.New("tailcat mesh not running")
	}
	ps := m.peers[peerDeviceID]
	m.mu.Unlock()

	if ps == nil {
		return nil, fmt.Errorf("unknown peer %q", peerDeviceID)
	}

	// Fast path: reuse an existing open inbound or outbound yamux session.
	if sess := m.pickActiveSession(ps); sess != nil {
		stream, err := openTypedStream(ctx, sess, kind)
		if err == nil {
			return &trackedStream{Stream: stream, mesh: m, ps: ps, sess: sess}, nil
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				// Opening a multiplexed stream and writing 1 header byte exceeded deadline -> session is stalled.
				m.clearClosedSession(ps, sess)
			}
			return nil, err
		}
		// Session died concurrently; clear stale reference and fall through to dial.
		m.clearClosedSession(ps, sess)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Slow path: deduplicated fallback dial protected by per-peer dialMu.
	ps.dialMu.Lock()
	defer ps.dialMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if sess := m.pickActiveSession(ps); sess != nil {
		stream, err := openTypedStream(ctx, sess, kind)
		if err == nil {
			return &trackedStream{Stream: stream, mesh: m, ps: ps, sess: sess}, nil
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				m.clearClosedSession(ps, sess)
			}
			return nil, err
		}
		m.clearClosedSession(ps, sess)
	}

	sess, err := m.ensureOutboundLocked(ctx, ps)
	if err != nil {
		return nil, err
	}
	stream, err := openTypedStream(ctx, sess, kind)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			m.clearClosedSession(ps, sess)
		}
		return nil, err
	}
	return &trackedStream{Stream: stream, mesh: m, ps: ps, sess: sess}, nil
}

// trackedStream wraps a yamux stream with a reference to the exact yamux.Session
// that created it so higher layers can evict a stalled/zombie session without
// accidentally closing a newer healthy replacement session.
type trackedStream struct {
	*yamux.Stream
	mesh *TailcatMesh
	ps   *peerState
	sess *yamux.Session
}

// ResetSession evicts the exact yamux session backing this stream if it is still
// registered on the peer, forcing subsequent OpenStream calls to fail over to a
// healthy alternate session or redial immediately.
func (ts *trackedStream) ResetSession() {
	if ts == nil || ts.mesh == nil || ts.ps == nil || ts.sess == nil {
		return
	}
	if ts.mesh.clearClosedSession(ts.ps, ts.sess) {
		log.Printf("tailcat: evicted stalled yamux session for peer %s", shortIDForLog(ts.ps.deviceID))
	}
}

func openTypedStream(ctx context.Context, sess *yamux.Session, kind model.StreamKind) (*yamux.Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sess == nil || sess.IsClosed() {
		return nil, errors.New("yamux session closed")
	}

	type openResult struct {
		stream *yamux.Stream
		err    error
	}
	resCh := make(chan openResult, 1)
	go func() {
		stream, err := sess.OpenStream()
		if err != nil {
			resCh <- openResult{err: fmt.Errorf("open yamux stream: %w", err)}
			return
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = stream.SetWriteDeadline(deadline)
		}
		stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
		_, writeErr := stream.Write([]byte{byte(kind)})
		stop()
		if writeErr != nil {
			_ = stream.Close()
			resCh <- openResult{err: fmt.Errorf("write stream kind header: %w", writeErr)}
			return
		}
		_ = stream.SetWriteDeadline(time.Time{})
		resCh <- openResult{stream: stream}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		if ctx.Err() != nil {
			_ = res.stream.Close()
			return nil, ctx.Err()
		}
		return res.stream, nil
	case <-ctx.Done():
		// Drain asynchronously so any stream that finishes opening late is immediately closed.
		go func() {
			if res := <-resCh; res.stream != nil {
				_ = res.stream.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func (m *TailcatMesh) pickActiveSession(ps *peerState) *yamux.Session {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.revoked {
		return nil
	}
	outOpen := ps.outSession != nil && !ps.outSession.IsClosed()
	inOpen := ps.inSession != nil && !ps.inSession.IsClosed()

	// Always prefer a direct TCP+mTLS session over a DERP-relayed session.
	if outOpen && ps.outIsDirect {
		return ps.outSession
	}
	if inOpen && ps.inIsDirect {
		return ps.inSession
	}
	if outOpen {
		return ps.outSession
	}
	if inOpen {
		return ps.inSession
	}
	return nil
}

func (m *TailcatMesh) clearClosedSession(ps *peerState, sess *yamux.Session) bool {
	var oldClient *tailcat.Client
	evicted := false
	ps.mu.Lock()
	if ps.outSession == sess {
		ps.outSession = nil
		oldClient = ps.outClient
		ps.outClient = nil
		ps.outAddr = ""
		ps.outDERPURL = ""
		ps.outIsDirect = false
		ps.outDirectKind = ""
		ps.outDirectAddr = ""
		evicted = true
	}
	if ps.inSession == sess {
		ps.inSession = nil
		ps.inIsDirect = false
		ps.inDirectKind = ""
		ps.inDirectAddr = ""
		evicted = true
	}
	ps.mu.Unlock()

	if evicted {
		_ = sess.Close()
		if oldClient != nil {
			drainAndCloseClient(oldClient, defaultDrainTimeout)
		}
	}
	return evicted
}

func (m *TailcatMesh) ensureOutboundLocked(ctx context.Context, ps *peerState) (*yamux.Session, error) {
	m.mu.Lock()
	if !m.started || m.closed {
		m.mu.Unlock()
		return nil, errors.New("tailcat mesh not running")
	}
	nodePriv := m.nodePriv
	selfID := m.id.DeviceID
	groupSecret := append([]byte(nil), m.groupSecret...)
	localDERP := m.derp
	rootCtx := m.rootCtx
	m.mu.Unlock()

	clientPriv := nodePriv
	if len(groupSecret) > 0 && selfID != "" && ps.deviceID != "" {
		clientPriv = DerivePairClientKey(groupSecret, selfID, ps.deviceID)
	}

	ps.mu.Lock()
	if ps.revoked {
		ps.mu.Unlock()
		return nil, fmt.Errorf("peer %q is revoked", ps.deviceID)
	}
	if ps.outSession != nil && !ps.outSession.IsClosed() && ps.outIsDirect {
		s := ps.outSession
		ps.mu.Unlock()
		return s, nil
	}
	if ps.inSession != nil && !ps.inSession.IsClosed() && ps.inIsDirect {
		s := ps.inSession
		ps.mu.Unlock()
		return s, nil
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
	addrStr := strings.TrimSpace(ps.device.Route.TailcatAddr)
	derpURL := derpMapURLForPeer(ps.device.Route.DERP, localDERP)
	oldClient := ps.outClient
	ps.outClient = nil
	ps.outSession = nil
	ps.outAddr = ""
	ps.outDERPURL = ""
	ps.outIsDirect = false
	ps.outDirectKind = ""
	ps.outDirectAddr = ""
	ps.mu.Unlock()

	if oldClient != nil {
		drainAndCloseClient(oldClient, defaultDrainTimeout)
	}

	directCandidates := m.directCandidatesForPeer(ps.deviceID)
	if addrStr == "" && len(directCandidates) == 0 {
		return nil, fmt.Errorf("peer %q has no tailcat_addr or direct address", ps.deviceID)
	}

	m.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: false,
		PathKind:  model.ConnPathConnecting,
		UpdatedAt: time.Now().UnixMilli(),
	})

	// Direct-first path: try custom IP override and auto-discovered LAN addresses before DERP.
	var directErr error
	if len(directCandidates) > 0 {
		ps.mu.Lock()
		ps.lastDirectAttempt = time.Now()
		ps.mu.Unlock()

		sess, directAddr, directKind, rttMs, err := m.dialDirectPeer(ctx, ps)
		if err == nil && sess != nil {
			ps.mu.Lock()
			if ps.revoked || rootCtx.Err() != nil {
				ps.mu.Unlock()
				_ = sess.Close()
				return nil, fmt.Errorf("peer %q revoked or mesh closed during direct dial", ps.deviceID)
			}
			ps.outClient = nil
			ps.outSession = sess
			ps.outAddr = ""
			ps.outDERPURL = ""
			ps.outIsDirect = true
			ps.outDirectKind = directKind
			ps.outDirectAddr = directAddr
			ps.mu.Unlock()

			log.Printf("tailcat: connected direct mTLS (%s) to peer %s (%s, %.1fms)", directKind, shortIDForLog(ps.deviceID), directAddr, rttMs)
			m.updatePeerStatus(ps, model.PeerConnStatus{
				DeviceID:  ps.deviceID,
				Connected: true,
				PathKind:  directKind,
				Endpoint:  directAddr,
				LatencyMs: rttMs,
				UpdatedAt: time.Now().UnixMilli(),
			})

			if !m.goSpawn(func() { m.serveYamuxSession(rootCtx, ps, sess, true) }) {
				_ = sess.Close()
				return nil, errors.New("tailcat mesh closed")
			}
			return sess, nil
		}
		directErr = err
		if ctx.Err() != nil {
			m.updatePeerStatus(ps, model.PeerConnStatus{
				DeviceID:  ps.deviceID,
				Connected: false,
				PathKind:  model.ConnPathDisconnected,
				LastError: err.Error(),
				UpdatedAt: time.Now().UnixMilli(),
			})
			return nil, err
		}
		log.Printf("tailcat: direct dial peer %s failed (%v); falling back to Tailcat", shortIDForLog(ps.deviceID), err)
	}

	if addrStr == "" {
		err := directErr
		if err == nil {
			err = fmt.Errorf("peer %q has no tailcat_addr", ps.deviceID)
		}
		m.updatePeerStatus(ps, model.PeerConnStatus{
			DeviceID:  ps.deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			LastError: err.Error(),
			UpdatedAt: time.Now().UnixMilli(),
		})
		return nil, err
	}

	client := &tailcat.Client{
		Server:     tailcat.Addr(addrStr),
		Key:        clientPriv,
		DERPMapURL: derpURL,
		Logf:       m.logf,
	}

	conn, err := client.DialTCPPort(ctx, m.opts.MeshPort)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("tailcat: dial peer %s failed: %v", shortIDForLog(ps.deviceID), err)
		}
		_ = client.Close()
		m.updatePeerStatus(ps, model.PeerConnStatus{
			DeviceID:  ps.deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			LastError: err.Error(),
			UpdatedAt: time.Now().UnixMilli(),
		})
		return nil, fmt.Errorf("dial peer %q: %w", ps.deviceID, err)
	}

	sess, err := yamux.Client(conn, newYamuxConfig())
	if err != nil {
		_ = conn.Close()
		_ = client.Close()
		return nil, fmt.Errorf("init yamux client for %q: %w", ps.deviceID, err)
	}

	ps.mu.Lock()
	if ps.revoked || rootCtx.Err() != nil {
		ps.mu.Unlock()
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("peer %q revoked or mesh closed during dial", ps.deviceID)
	}
	ps.outClient = client
	ps.outSession = sess
	ps.outAddr = addrStr
	ps.outDERPURL = derpURL
	ps.outIsDirect = false
	ps.outDirectKind = ""
	ps.outDirectAddr = ""
	ps.mu.Unlock()

	regionStr := ""
	if r := client.DERPRegion(); r != nil {
		regionStr = formatDERPRegion(r.RegionID, r.RegionCode)
	}
	m.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:   ps.deviceID,
		Connected:  true,
		PathKind:   model.ConnPathDERP,
		DERPRegion: regionStr,
		UpdatedAt:  time.Now().UnixMilli(),
	})

	if !m.goSpawn(func() { m.serveYamuxSession(rootCtx, ps, sess, true) }) {
		_ = sess.Close()
		drainAndCloseClient(client, defaultDrainTimeout)
		return nil, errors.New("tailcat mesh closed")
	}

	// Kick off an immediate non-blocking DiscoPing to upgrade path and measure RTT.
	m.goSpawn(func() { m.probeOutboundClient(rootCtx, ps, client) })

	return sess, nil
}

func (m *TailcatMesh) peerDialLoop(ctx context.Context, ps *peerState) {
	backoff := m.opts.DialRetryInterval
	if backoff <= 0 {
		backoff = defaultDialRetryInterval
	}

	for {
		if ctx.Err() != nil {
			return
		}

		// If any active session already exists, wait for it to close rather than spinning.
		if sess := m.pickActiveSession(ps); sess != nil {
			backoff = m.opts.DialRetryInterval
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
		if addr == "" && len(m.directCandidatesForPeer(ps.deviceID)) == 0 {
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}

		dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		ps.dialMu.Lock()
		sess, err := m.ensureOutboundLocked(dialCtx, ps)
		ps.dialMu.Unlock()
		cancel()

		if err == nil && sess != nil {
			backoff = m.opts.DialRetryInterval
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
		if backoff > maxDialRetryInterval {
			backoff = maxDialRetryInterval
		}
	}
}

func (m *TailcatMesh) acceptLoop(srv *tailcat.Server, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}

		peerKey, ok := srv.PeerKey(conn.RemoteAddr())
		if !ok || !m.allowSet.Contains(peerKey) {
			_ = conn.Close()
			continue
		}
		peerDeviceID := FormatNodePublic(peerKey)
		devicePub := peerKey
		m.mu.Lock()
		if mappedID, ok := m.allowedClientPubs[peerKey]; ok {
			peerDeviceID = mappedID
			if canonicalPub, ok := m.allowedKeys[mappedID]; ok {
				devicePub = canonicalPub
			}
		}
		m.mu.Unlock()

		sess, err := yamux.Server(conn, newYamuxConfig())
		if err != nil {
			_ = conn.Close()
			continue
		}

		ps := m.getOrCreatePeer(peerDeviceID, devicePub)
		var prevIn *yamux.Session
		var keepDirect bool
		ps.mu.Lock()
		if ps.revoked {
			ps.mu.Unlock()
			_ = sess.Close()
			continue
		}
		if ps.inSession != nil && !ps.inSession.IsClosed() && ps.inIsDirect {
			// Keep the active direct inbound session as primary, while still serving streams on this DERP session.
			keepDirect = true
		} else {
			prevIn = ps.inSession
			ps.inSession = sess
			ps.inIsDirect = false
			ps.inDirectKind = ""
			ps.inDirectAddr = ""
			ps.inboundPubKey = peerKey
		}
		ps.mu.Unlock()

		if prevIn != nil {
			_ = prevIn.Close()
		}

		if !keepDirect {
			m.inspectInboundStatus(srv, ps, sess)
		}

		m.mu.Lock()
		rootCtx := m.rootCtx
		m.mu.Unlock()

		p := ps
		s := sess
		if !m.goSpawn(func() { m.serveYamuxSession(rootCtx, p, s, false) }) {
			_ = s.Close()
		}
	}
}

func (m *TailcatMesh) serveYamuxSession(ctx context.Context, ps *peerState, sess *yamux.Session, isOutbound bool) {
	defer func() {
		var oldClient *tailcat.Client
		var remOutSess, remInSess *yamux.Session
		var remOutDirect, remInDirect bool
		var remOutDirectKind, remInDirectKind model.ConnPathKind
		var remOutDirectAddr, remInDirectAddr string
		var remOutClient *tailcat.Client
		stillConnected := false

		ps.mu.Lock()
		if isOutbound && ps.outSession == sess {
			ps.outSession = nil
			oldClient = ps.outClient
			ps.outClient = nil
			ps.outAddr = ""
			ps.outDERPURL = ""
			ps.outIsDirect = false
			ps.outDirectKind = ""
			ps.outDirectAddr = ""
		}
		if !isOutbound && ps.inSession == sess {
			ps.inSession = nil
			ps.inIsDirect = false
			ps.inDirectKind = ""
			ps.inDirectAddr = ""
		}
		if ps.outSession != nil && !ps.outSession.IsClosed() {
			remOutSess = ps.outSession
			remOutDirect = ps.outIsDirect
			remOutDirectKind = ps.outDirectKind
			remOutDirectAddr = ps.outDirectAddr
			remOutClient = ps.outClient
			stillConnected = true
		}
		if ps.inSession != nil && !ps.inSession.IsClosed() {
			remInSess = ps.inSession
			remInDirect = ps.inIsDirect
			remInDirectKind = ps.inDirectKind
			remInDirectAddr = ps.inDirectAddr
			stillConnected = true
		}
		ps.mu.Unlock()

		_ = sess.Close()
		if oldClient != nil {
			drainAndCloseClient(oldClient, defaultDrainTimeout)
		}

		if !stillConnected {
			m.updatePeerStatus(ps, model.PeerConnStatus{
				DeviceID:  ps.deviceID,
				Connected: false,
				PathKind:  model.ConnPathDisconnected,
				UpdatedAt: time.Now().UnixMilli(),
			})
			return
		}

		// Refresh status to reflect whichever remaining session is still active.
		if remOutSess != nil && remOutDirect {
			m.probeDirectSession(ps, remOutSess, remOutDirectKind, remOutDirectAddr)
		} else if remInSess != nil && remInDirect {
			m.probeDirectSession(ps, remInSess, remInDirectKind, remInDirectAddr)
		} else if remOutClient != nil && remOutSess != nil {
			m.goSpawn(func() { m.probeOutboundClient(ctx, ps, remOutClient) })
		} else if remInSess != nil {
			m.mu.Lock()
			srv := m.server
			m.mu.Unlock()
			m.inspectInboundStatus(srv, ps, remInSess)
		}
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, yamux.ErrSessionShutdown) {
				dir := "inbound"
				if isOutbound {
					dir = "outbound"
				}
				log.Printf("tailcat: %s yamux session closed for peer %s: %v", dir, shortIDForLog(ps.deviceID), err)
			}
			return
		}
		st := stream
		if !m.goSpawn(func() { m.dispatchInboundStream(ctx, ps.deviceID, st) }) {
			_ = st.Close()
			return
		}
	}
}

func shortIDForLog(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18] + "…"
}

func (m *TailcatMesh) goSpawn(fn func()) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		fn()
	}()
	return true
}

func (m *TailcatMesh) dispatchInboundStream(ctx context.Context, peerDeviceID string, st *yamux.Stream) {
	_ = st.SetReadDeadline(time.Now().Add(streamHeaderReadTimeout))
	var hdr [1]byte
	if _, err := io.ReadFull(st, hdr[:]); err != nil {
		_ = st.Close()
		return
	}
	_ = st.SetReadDeadline(time.Time{})

	m.mu.Lock()
	handler := m.streamHandler
	m.mu.Unlock()

	if handler == nil {
		_ = st.Close()
		return
	}
	handler(ctx, peerDeviceID, model.StreamKind(hdr[0]), st)
}

func (m *TailcatMesh) statusMonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.opts.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollPeerStatuses(ctx)
		}
	}
}

func (m *TailcatMesh) pollPeerStatuses(ctx context.Context) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	srv := m.server
	selfID := m.id.DeviceID
	peers := m.snapshotPeersLocked()
	overrideIDs := make(map[string]bool, len(m.peerDirectOverrides))
	for id, addr := range m.peerDirectOverrides {
		if strings.TrimSpace(addr) != "" {
			overrideIDs[id] = true
		}
	}
	m.mu.Unlock()

	for _, ps := range peers {
		if ctx.Err() != nil {
			return
		}
		ps.mu.Lock()
		outClient := ps.outClient
		outSess := ps.outSession
		outDirect := ps.outIsDirect
		outDirectKind := ps.outDirectKind
		outDirectAddr := ps.outDirectAddr
		inSess := ps.inSession
		inDirect := ps.inIsDirect
		inDirectKind := ps.inDirectKind
		inDirectAddr := ps.inDirectAddr
		revoked := ps.revoked
		ps.mu.Unlock()

		if revoked {
			continue
		}
		outOpen := outSess != nil && !outSess.IsClosed()
		inOpen := inSess != nil && !inSess.IsClosed()

		if outOpen && outDirect {
			m.probeDirectSession(ps, outSess, outDirectKind, outDirectAddr)
			continue
		}
		if inOpen && inDirect {
			m.probeDirectSession(ps, inSess, inDirectKind, inDirectAddr)
			continue
		}

		if outClient != nil && outOpen {
			m.probeOutboundClient(ctx, ps, outClient)
		} else if srv != nil && inOpen {
			m.inspectInboundStatus(srv, ps, inSess)
		}

		// Background DERP -> Direct upgrade when direct candidates exist.
		if (outOpen || inOpen) && (selfID < ps.deviceID || overrideIDs[ps.deviceID]) && len(m.directCandidatesForPeer(ps.deviceID)) > 0 {
			p := ps
			m.goSpawn(func() { m.tryUpgradeToDirect(ctx, p, false) })
		}
	}
}

func (m *TailcatMesh) probeOutboundClient(ctx context.Context, ps *peerState, client *tailcat.Client) {
	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	res, err := client.DiscoPing(pingCtx)
	if err != nil || res == nil {
		return
	}
	if res.Err != "" {
		return
	}

	st := model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: true,
		LatencyMs: res.LatencySeconds * 1000.0,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if res.Endpoint != "" {
		st.PathKind = model.ConnPathDirect
		st.Endpoint = res.Endpoint
	} else {
		st.PathKind = model.ConnPathDERP
		st.DERPRegion = formatDERPRegion(res.DERPRegionID, res.DERPRegionCode)
	}
	m.updatePeerStatus(ps, st)
}

func (m *TailcatMesh) inspectInboundStatus(srv *tailcat.Server, ps *peerState, sess *yamux.Session) {
	st := model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: true,
		PathKind:  model.ConnPathDERP,
		UpdatedAt: time.Now().UnixMilli(),
	}

	if srv != nil {
		ps.mu.Lock()
		inKey := ps.inboundPubKey
		ps.mu.Unlock()
		if srvStatus := srv.Status(); srvStatus != nil && srvStatus.Peer != nil {
			peerSt := srvStatus.Peer[inKey]
			if peerSt == nil {
				peerSt = srvStatus.Peer[ps.pubKey]
			}
			if peerSt != nil {
				if peerSt.CurAddr != "" {
					st.PathKind = model.ConnPathDirect
					st.Endpoint = peerSt.CurAddr
				} else if peerSt.Relay != "" {
					st.PathKind = model.ConnPathDERP
					st.DERPRegion = peerSt.Relay
				}
			}
		}
	}

	if sess != nil && !sess.IsClosed() {
		if rtt, err := sess.Ping(); err == nil {
			st.LatencyMs = float64(rtt) / float64(time.Millisecond)
		}
	}

	m.updatePeerStatus(ps, st)
}

func formatDERPRegion(id tailcfg.DERPRegionID, code string) string {
	code = strings.TrimSpace(code)
	if code != "" && id > 0 {
		return fmt.Sprintf("%s (%d)", code, id)
	}
	if code != "" {
		return code
	}
	if id > 0 {
		return fmt.Sprintf("derp-%d", id)
	}
	return ""
}

// SetStreamHandler registers the callback for incoming multiplexed streams.
func (m *TailcatMesh) SetStreamHandler(h model.PeerStreamHandler) {
	m.mu.Lock()
	m.streamHandler = h
	m.mu.Unlock()
}

// SetPeerStatusHandler registers the callback invoked on peer status transitions.
func (m *TailcatMesh) SetPeerStatusHandler(h func(model.PeerConnStatus)) {
	m.mu.Lock()
	m.statusHandler = h
	m.mu.Unlock()
}

// PeerStatuses returns a sorted snapshot of all known peers' connection statuses.
func (m *TailcatMesh) PeerStatuses() []model.PeerConnStatus {
	m.mu.Lock()
	peers := m.snapshotPeersLocked()
	m.mu.Unlock()

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

// StartPairing launches a dedicated ephemeral tailcat.Server (AllowClient: nil)
// on PairingPort for onboarding a new device.
func (m *TailcatMesh) StartPairing(ctx context.Context, h model.PairingHandler) (string, uint16, error) {
	if h == nil {
		return "", 0, errors.New("pairing handler is required")
	}

	_ = m.StopPairing()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", 0, errors.New("tailcat mesh is closed")
	}
	derp := m.derp
	if derp.Mode == "" {
		derp = model.DefaultDERPConfig()
	}
	m.mu.Unlock()

	regionID, region, mapURL, err := BuildServerDERPConfig(derp)
	if err != nil {
		return "", 0, err
	}

	// Use an ephemeral NodePrivate + PresharedKey so the pairing server never
	// conflicts with the persistent mesh server's key registration on DERP.
	srv := &tailcat.Server{
		RegionID:    regionID,
		Region:      region,
		DERPMapURL:  mapURL,
		AllowClient: nil,
		Logf:        m.logf,
	}
	if err := srv.StartContext(ctx); err != nil {
		_ = srv.Close()
		return "", 0, fmt.Errorf("start pairing server: %w", err)
	}

	ln, err := srv.Listen(ctx, "tcp", fmt.Sprintf(":%d", m.opts.PairingPort))
	if err != nil {
		_ = srv.Close()
		return "", 0, fmt.Errorf("listen pairing port %d: %w", m.opts.PairingPort, err)
	}

	addr, err := FormatServerAddr(srv, derp)
	if err != nil {
		_ = ln.Close()
		_ = srv.Close()
		return "", 0, fmt.Errorf("format pairing tailcat addr: %w", err)
	}

	pairingCtx, pairingCancel := context.WithCancel(context.Background())

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		pairingCancel()
		_ = ln.Close()
		_ = srv.Close()
		return "", 0, errors.New("tailcat mesh closed during StartPairing")
	}
	m.pairingServer = srv
	m.pairingListener = ln
	m.pairingCancel = pairingCancel
	m.mu.Unlock()

	m.goSpawn(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c := conn
			if !m.goSpawn(func() { h(pairingCtx, c) }) {
				_ = c.Close()
				return
			}
		}
	})

	return string(addr), m.opts.PairingPort, nil
}

// StopPairing drains and closes the temporary pairing server.
func (m *TailcatMesh) StopPairing() error {
	m.mu.Lock()
	srv := m.pairingServer
	ln := m.pairingListener
	cancel := m.pairingCancel
	m.pairingServer = nil
	m.pairingListener = nil
	m.pairingCancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if srv != nil {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer drainCancel()
		_ = srv.DrainTCP(drainCtx)
		return srv.Close()
	}
	return nil
}

// DialPairing creates a one-shot tailcat.Client to dial a remote pairing endpoint
// and returns a net.Conn that drains and closes the temporary client on Close.
func (m *TailcatMesh) DialPairing(ctx context.Context, tailcatAddr string, port uint16, derp model.DERPConfig) (net.Conn, error) {
	if strings.TrimSpace(tailcatAddr) == "" {
		return nil, errors.New("empty pairing tailcat address")
	}
	if port == 0 {
		port = m.opts.PairingPort
	}

	m.mu.Lock()
	localDERP := m.derp
	m.mu.Unlock()

	client := &tailcat.Client{
		Server:     tailcat.Addr(strings.TrimSpace(tailcatAddr)),
		DERPMapURL: derpMapURLForPeer(derp, localDERP),
		Logf:       m.logf,
	}

	conn, err := client.DialTCPPort(ctx, port)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("dial pairing endpoint: %w", err)
	}

	return &pairingClientConn{
		Conn:   conn,
		client: client,
	}, nil
}

type pairingClientConn struct {
	net.Conn
	client    *tailcat.Client
	closeOnce sync.Once
}

func (c *pairingClientConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.Conn.Close()
		drainAndCloseClient(c.client, 2*time.Second)
	})
	return err
}

// Close gracefully drains TCP and shuts down all servers, listeners, and clients.
func (m *TailcatMesh) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.rootCancel
	srv := m.server
	ln := m.listener
	directLn := m.directListener
	m.server = nil
	m.listener = nil
	m.directListener = nil
	m.directActualPort = 0

	draining := make(map[*tailcat.Server]net.Listener, len(m.drainingServers))
	for s, l := range m.drainingServers {
		draining[s] = l
	}
	m.drainingServers = make(map[*tailcat.Server]net.Listener)

	peers := m.snapshotPeersLocked()
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	_ = m.StopPairing()

	if directLn != nil {
		_ = directLn.Close()
	}

	// Close all peer sessions and collect active outbound clients for draining.
	var clients []*tailcat.Client
	for _, ps := range peers {
		ps.mu.Lock()
		if ps.dialCancel != nil {
			ps.dialCancel()
			ps.dialCancel = nil
			ps.dialActive = false
		}
		outSess := ps.outSession
		inSess := ps.inSession
		outClient := ps.outClient
		ps.outSession = nil
		ps.inSession = nil
		ps.outClient = nil
		ps.outIsDirect = false
		ps.outDirectKind = ""
		ps.outDirectAddr = ""
		ps.inIsDirect = false
		ps.inDirectKind = ""
		ps.inDirectAddr = ""
		ps.mu.Unlock()

		if outSess != nil {
			_ = outSess.Close()
		}
		if inSess != nil {
			_ = inSess.Close()
		}
		if outClient != nil {
			clients = append(clients, outClient)
		}
	}

	if ln != nil {
		_ = ln.Close()
	}
	for _, l := range draining {
		if l != nil {
			_ = l.Close()
		}
	}

	drainCtx, drainCancel := boundedDrainContext(ctx, defaultDrainTimeout)
	defer drainCancel()

	var closeWG sync.WaitGroup
	for _, c := range clients {
		closeWG.Add(1)
		go func(cl *tailcat.Client) {
			defer closeWG.Done()
			_ = cl.DrainTCP(drainCtx)
			_ = cl.Close()
		}(c)
	}
	if srv != nil {
		closeWG.Add(1)
		go func(s *tailcat.Server) {
			defer closeWG.Done()
			_ = s.DrainTCP(drainCtx)
			_ = s.Close()
		}(srv)
	}
	for s := range draining {
		closeWG.Add(1)
		go func(ds *tailcat.Server) {
			defer closeWG.Done()
			_ = ds.DrainTCP(drainCtx)
			_ = ds.Close()
		}(s)
	}
	closeWG.Wait()

	m.wg.Wait()
	return nil
}

func (m *TailcatMesh) getOrCreatePeer(deviceID string, pub key.NodePublic) *peerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ps, ok := m.peers[deviceID]; ok {
		return ps
	}
	ps := &peerState{
		deviceID: deviceID,
		pubKey:   pub,
		status: model.PeerConnStatus{
			DeviceID:  deviceID,
			Connected: false,
			PathKind:  model.ConnPathDisconnected,
			UpdatedAt: time.Now().UnixMilli(),
		},
	}
	m.peers[deviceID] = ps
	return ps
}

func (m *TailcatMesh) snapshotPeersLocked() []*peerState {
	out := make([]*peerState, 0, len(m.peers))
	for _, ps := range m.peers {
		out = append(out, ps)
	}
	return out
}

func (m *TailcatMesh) activeServersLocked() []*tailcat.Server {
	var servers []*tailcat.Server
	if m.server != nil {
		servers = append(servers, m.server)
	}
	for s := range m.drainingServers {
		servers = append(servers, s)
	}
	return servers
}

func (m *TailcatMesh) resetOutboundPeer(ps *peerState) {
	ps.mu.Lock()
	outSess := ps.outSession
	outClient := ps.outClient
	ps.outSession = nil
	ps.outClient = nil
	ps.outAddr = ""
	ps.outDERPURL = ""
	ps.outIsDirect = false
	ps.outDirectKind = ""
	ps.outDirectAddr = ""
	ps.mu.Unlock()

	if outSess != nil {
		_ = outSess.Close()
	}
	if outClient != nil {
		drainAndCloseClient(outClient, defaultDrainTimeout)
	}
}

func (m *TailcatMesh) teardownPeer(ps *peerState, markRevoked bool) {
	ps.mu.Lock()
	if markRevoked {
		ps.revoked = true
	}
	if ps.dialCancel != nil {
		ps.dialCancel()
		ps.dialCancel = nil
		ps.dialActive = false
	}
	outSess := ps.outSession
	inSess := ps.inSession
	outClient := ps.outClient
	ps.outSession = nil
	ps.inSession = nil
	ps.outClient = nil
	ps.outAddr = ""
	ps.outDERPURL = ""
	ps.outIsDirect = false
	ps.outDirectKind = ""
	ps.outDirectAddr = ""
	ps.inIsDirect = false
	ps.inDirectKind = ""
	ps.inDirectAddr = ""
	ps.mu.Unlock()

	if outSess != nil {
		_ = outSess.Close()
	}
	if inSess != nil {
		_ = inSess.Close()
	}
	if outClient != nil {
		drainAndCloseClient(outClient, defaultDrainTimeout)
	}

	m.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: false,
		PathKind:  model.ConnPathDisconnected,
		UpdatedAt: time.Now().UnixMilli(),
	})
}

func (m *TailcatMesh) updatePeerStatus(ps *peerState, st model.PeerConnStatus) {
	ps.mu.Lock()
	ps.status = st
	ps.mu.Unlock()
	m.emitStatus(st)
}

func (m *TailcatMesh) emitStatus(st model.PeerConnStatus) {
	m.mu.Lock()
	h := m.statusHandler
	m.mu.Unlock()
	if h != nil {
		h(st)
	}
}

func drainAndCloseClient(c *tailcat.Client, timeout time.Duration) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = c.DrainTCP(ctx)
	_ = c.Close()
}

func boundedDrainContext(ctx context.Context, fallback time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), fallback)
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, fallback)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
