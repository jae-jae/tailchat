package syncproto_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailchat/internal/model"
	"tailchat/internal/store"
	"tailchat/internal/syncproto"
)

// --- In-memory model.Store mock ---

type memStore struct {
	mu           sync.RWMutex
	closed       bool
	identity     *model.LocalIdentity
	cfg          *model.AppConfig
	devices      map[string]model.Device
	memberEvents map[string]model.MemberEvent
	messages     map[string]model.Message
	authorSeq    map[string]uint64
	blobs        map[string]model.FileBlob
	transfers    map[string]model.TransferTask
}

func newMemStore() *memStore {
	return &memStore{
		devices:      make(map[string]model.Device),
		memberEvents: make(map[string]model.MemberEvent),
		messages:     make(map[string]model.Message),
		authorSeq:    make(map[string]uint64),
		blobs:        make(map[string]model.FileBlob),
		transfers:    make(map[string]model.TransferTask),
	}
}

func (m *memStore) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}

func (m *memStore) LoadIdentity() (*model.LocalIdentity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.identity == nil {
		return nil, nil
	}
	cp := *m.identity
	cp.SignPublicKey = append([]byte(nil), cp.SignPublicKey...)
	cp.SignPrivateKey = append([]byte(nil), cp.SignPrivateKey...)
	cp.GroupPublicKey = append([]byte(nil), cp.GroupPublicKey...)
	cp.GroupPrivateKey = append([]byte(nil), cp.GroupPrivateKey...)
	return &cp, nil
}

func (m *memStore) SaveIdentity(id model.LocalIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := id
	cp.SignPublicKey = append([]byte(nil), cp.SignPublicKey...)
	cp.SignPrivateKey = append([]byte(nil), cp.SignPrivateKey...)
	cp.GroupPublicKey = append([]byte(nil), cp.GroupPublicKey...)
	cp.GroupPrivateKey = append([]byte(nil), cp.GroupPrivateKey...)
	m.identity = &cp
	return nil
}

func (m *memStore) LoadConfig() (*model.AppConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return nil, nil
	}
	cp := *m.cfg
	return &cp, nil
}

func (m *memStore) SaveConfig(cfg model.AppConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := cfg
	m.cfg = &cp
	return nil
}

func cloneDev(d model.Device) model.Device {
	cp := d
	cp.SignPublicKey = append([]byte(nil), d.SignPublicKey...)
	cp.Route.SignPublicKey = append([]byte(nil), d.Route.SignPublicKey...)
	cp.Route.Signature = append([]byte(nil), d.Route.Signature...)
	return cp
}

func (m *memStore) UpsertDevice(dev model.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[dev.DeviceID] = cloneDev(dev)
	return nil
}

func (m *memStore) GetDevice(deviceID string) (*model.Device, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.devices[deviceID]
	if !ok {
		return nil, nil
	}
	cp := cloneDev(d)
	return &cp, nil
}

func (m *memStore) ListDevices(includeRevoked bool) ([]model.Device, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Device, 0, len(m.devices))
	for _, d := range m.devices {
		if !includeRevoked && d.Revoked {
			continue
		}
		out = append(out, cloneDev(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out, nil
}

func cloneEvent(ev model.MemberEvent) model.MemberEvent {
	cp := ev
	cp.SignPublicKey = append([]byte(nil), ev.SignPublicKey...)
	cp.Signature = append([]byte(nil), ev.Signature...)
	if ev.Route != nil {
		r := *ev.Route
		r.SignPublicKey = append([]byte(nil), r.SignPublicKey...)
		r.Signature = append([]byte(nil), r.Signature...)
		cp.Route = &r
	}
	return cp
}

func (m *memStore) SaveMemberEvent(ev model.MemberEvent) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.memberEvents[ev.EventID]; exists {
		return false, nil
	}
	m.memberEvents[ev.EventID] = cloneEvent(ev)
	return true, nil
}

func (m *memStore) ListMemberEvents() ([]model.MemberEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.MemberEvent, 0, len(m.memberEvents))
	for _, ev := range m.memberEvents {
		out = append(out, cloneEvent(ev))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].EventID < out[j].EventID
	})
	return out, nil
}

func (m *memStore) NextAuthorSeq(authorDeviceID string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authorSeq[authorDeviceID]++
	return m.authorSeq[authorDeviceID], nil
}

func cloneBlob(b model.FileBlob) model.FileBlob {
	cp := b
	cp.ChunkHashes = append([]string(nil), b.ChunkHashes...)
	cp.ThumbnailWebP = append([]byte(nil), b.ThumbnailWebP...)
	cp.Seeders = append([]string(nil), b.Seeders...)
	return cp
}

func (m *memStore) hydrateMsgLocked(msg model.Message) model.Message {
	cp := msg
	if cp.Kind == model.MessageKindFile && cp.BlobHash != "" {
		if b, ok := m.blobs[cp.BlobHash]; ok {
			bc := cloneBlob(b)
			cp.File = &bc
		}
	} else if cp.File != nil {
		bc := cloneBlob(*cp.File)
		cp.File = &bc
	}
	return cp
}

func (m *memStore) SaveMessage(msg model.Message) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.messages[msg.MsgID]; exists {
		return false, nil
	}
	cp := msg
	if msg.File != nil {
		bc := cloneBlob(*msg.File)
		cp.File = &bc
	}
	m.messages[msg.MsgID] = cp
	if msg.AuthorSeq > m.authorSeq[msg.AuthorDeviceID] {
		m.authorSeq[msg.AuthorDeviceID] = msg.AuthorSeq
	}
	return true, nil
}

func (m *memStore) GetMessage(msgID string) (*model.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	msg, ok := m.messages[msgID]
	if !ok {
		return nil, nil
	}
	cp := m.hydrateMsgLocked(msg)
	return &cp, nil
}

func (m *memStore) ListMessages(limit int, beforeMsgID string) ([]model.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := make([]model.Message, 0, len(m.messages))
	for _, msg := range m.messages {
		all = append(all, m.hydrateMsgLocked(msg))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt < all[j].CreatedAt
		}
		return all[i].MsgID < all[j].MsgID
	})
	if beforeMsgID != "" {
		filtered := all[:0]
		for _, msg := range all {
			if msg.MsgID < beforeMsgID {
				filtered = append(filtered, msg)
			}
		}
		all = filtered
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func (m *memStore) SearchMessages(query string, limit int) ([]model.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	q := strings.ToLower(query)
	var out []model.Message
	for _, msg := range m.messages {
		if strings.Contains(strings.ToLower(msg.TextBody), q) {
			out = append(out, m.hydrateMsgLocked(msg))
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) MessagesAfterClock(vc model.VectorClock, limit int) ([]model.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []model.Message
	for _, msg := range m.messages {
		if msg.AuthorSeq > vc[msg.AuthorDeviceID] {
			out = append(out, m.hydrateMsgLocked(msg))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].MsgID < out[j].MsgID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) VectorClock() (model.VectorClock, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vc := make(model.VectorClock)
	for _, msg := range m.messages {
		if msg.AuthorSeq > vc[msg.AuthorDeviceID] {
			vc[msg.AuthorDeviceID] = msg.AuthorSeq
		}
	}
	return vc, nil
}

func (m *memStore) UpsertFileBlob(blob model.FileBlob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[blob.BlobHash] = cloneBlob(blob)
	return nil
}

func (m *memStore) GetFileBlob(blobHash string) (*model.FileBlob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.blobs[blobHash]
	if !ok {
		return nil, nil
	}
	cp := cloneBlob(b)
	return &cp, nil
}

func (m *memStore) AddBlobSeeder(blobHash, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[blobHash]
	if !ok {
		return nil
	}
	for _, s := range b.Seeders {
		if s == deviceID {
			return nil
		}
	}
	b.Seeders = append(b.Seeders, deviceID)
	m.blobs[blobHash] = b
	return nil
}

func (m *memStore) UpsertTransferTask(task model.TransferTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := task
	cp.ChunkBitmap = append([]byte(nil), task.ChunkBitmap...)
	m.transfers[task.BlobHash] = cp
	return nil
}

func (m *memStore) GetTransferTask(blobHash string) (*model.TransferTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.transfers[blobHash]
	if !ok {
		return nil, nil
	}
	cp := t
	cp.ChunkBitmap = append([]byte(nil), t.ChunkBitmap...)
	return &cp, nil
}

func (m *memStore) ListTransferTasks() ([]model.TransferTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.TransferTask, 0, len(m.transfers))
	for _, t := range m.transfers {
		cp := t
		cp.ChunkBitmap = append([]byte(nil), t.ChunkBitmap...)
		out = append(out, cp)
	}
	return out, nil
}

// --- In-memory model.CASStore mock ---

type memCAS struct {
	mu          sync.RWMutex
	chunks      map[string]map[int][]byte
	assembled   map[string]string
	onReadChunk func(blobHash string, index int)
}

func newMemCAS() *memCAS {
	return &memCAS{
		chunks:    make(map[string]map[int][]byte),
		assembled: make(map[string]string),
	}
}

func (c *memCAS) SetReadHook(fn func(blobHash string, index int)) {
	c.mu.Lock()
	c.onReadChunk = fn
	c.mu.Unlock()
}

func (c *memCAS) IngestFile(srcPath string) (*model.FileBlob, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, err
	}
	blob, err := c.IngestBytes(filepath.Base(srcPath), "application/octet-stream", data)
	if err != nil {
		return nil, err
	}
	blob.LocalPath = srcPath
	c.mu.Lock()
	c.assembled[blob.BlobHash] = srcPath
	c.mu.Unlock()
	return blob, nil
}

func (c *memCAS) IngestBytes(fileName, mimeType string, data []byte) (*model.FileBlob, error) {
	rootHash := model.Blake3Hex(data)
	chunkSize := model.DefaultChunkSize
	chunkCount := (len(data) + chunkSize - 1) / chunkSize
	chunkHashes := make([]string, chunkCount)

	c.mu.Lock()
	if c.chunks[rootHash] == nil {
		c.chunks[rootHash] = make(map[int][]byte, chunkCount)
	}
	for i := 0; i < chunkCount; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := append([]byte(nil), data[start:end]...)
		chunkHashes[i] = model.Blake3Hex(chunk)
		c.chunks[rootHash][i] = chunk
	}
	c.mu.Unlock()

	return &model.FileBlob{
		BlobHash:    rootHash,
		FileName:    fileName,
		FileSize:    int64(len(data)),
		MimeType:    mimeType,
		ChunkSize:   chunkSize,
		ChunkCount:  chunkCount,
		ChunkHashes: chunkHashes,
	}, nil
}

func (c *memCAS) ReadChunk(blobHash string, index int) ([]byte, error) {
	c.mu.RLock()
	hook := c.onReadChunk
	c.mu.RUnlock()
	if hook != nil {
		hook(blobHash, index)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	byIdx, ok := c.chunks[blobHash]
	if !ok {
		return nil, fmt.Errorf("blob %s not found", blobHash)
	}
	chunk, ok := byIdx[index]
	if !ok {
		return nil, fmt.Errorf("chunk %d not found for blob %s", index, blobHash)
	}
	return append([]byte(nil), chunk...), nil
}

func (c *memCAS) WriteChunk(blobHash string, index int, expectedHash string, data []byte) error {
	actual := model.Blake3Hex(data)
	if expectedHash != "" && actual != expectedHash {
		return fmt.Errorf("chunk %d hash mismatch: got %s want %s", index, actual, expectedHash)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chunks[blobHash] == nil {
		c.chunks[blobHash] = make(map[int][]byte)
	}
	c.chunks[blobHash][index] = append([]byte(nil), data...)
	return nil
}

func (c *memCAS) AssembleFile(blob model.FileBlob, outputDir string) (string, error) {
	c.mu.RLock()
	byIdx := c.chunks[blob.BlobHash]
	var buf bytes.Buffer
	for i := 0; i < blob.ChunkCount; i++ {
		chunk, ok := byIdx[i]
		if !ok {
			c.mu.RUnlock()
			return "", fmt.Errorf("missing chunk %d", i)
		}
		buf.Write(chunk)
	}
	c.mu.RUnlock()

	full := buf.Bytes()
	if got := model.Blake3Hex(full); got != blob.BlobHash {
		return "", fmt.Errorf("assembled hash mismatch: got %s want %s", got, blob.BlobHash)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	outPath := filepath.Join(outputDir, blob.FileName)
	if err := os.WriteFile(outPath, full, 0o644); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.assembled[blob.BlobHash] = outPath
	c.mu.Unlock()
	return outPath, nil
}

func (c *memCAS) HasCompleteBlob(blobHash string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.assembled[blobHash]
	return p, ok && p != ""
}

// --- In-memory PeerTransportProvider mock mesh ---

type memNetwork struct {
	mu      sync.RWMutex
	byID    map[string]*memTransport
	byAddr  map[string]*memTransport
	connMap map[[2]string]bool // ordered pair [min, max] -> connected
}

func newMemNetwork() *memNetwork {
	return &memNetwork{
		byID:    make(map[string]*memTransport),
		byAddr:  make(map[string]*memTransport),
		connMap: make(map[[2]string]bool),
	}
}

func orderedPair(a, b string) [2]string {
	if a < b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

func (n *memNetwork) reconcileConnectivity() {
	type notif struct {
		tr     *memTransport
		status model.PeerConnStatus
	}
	var notifications []notif

	n.mu.Lock()
	ids := make([]string, 0, len(n.byID))
	for id := range n.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a := n.byID[ids[i]]
			b := n.byID[ids[j]]
			pair := orderedPair(a.id.DeviceID, b.id.DeviceID)
			wasConn := n.connMap[pair]
			nowConn := a.canConnectToLocked(b.id.DeviceID) && b.canConnectToLocked(a.id.DeviceID)
			if wasConn == nowConn {
				continue
			}
			n.connMap[pair] = nowConn
			now := time.Now().UnixMilli()
			pathKind := model.ConnPathDisconnected
			if nowConn {
				pathKind = model.ConnPathDirect
			}
			notifications = append(notifications,
				notif{
					tr: a,
					status: model.PeerConnStatus{
						DeviceID:  b.id.DeviceID,
						Connected: nowConn,
						PathKind:  pathKind,
						UpdatedAt: now,
					},
				},
				notif{
					tr: b,
					status: model.PeerConnStatus{
						DeviceID:  a.id.DeviceID,
						Connected: nowConn,
						PathKind:  pathKind,
						UpdatedAt: now,
					},
				},
			)
		}
	}
	n.mu.Unlock()

	for _, item := range notifications {
		item.tr.notifyStatus(item.status)
	}
}

type memTransport struct {
	net *memNetwork

	mu                   sync.RWMutex
	id                   model.LocalIdentity
	route                model.RouteRecord
	online               bool
	closed               bool
	allowed              map[string]model.Device
	revoked              map[string]bool
	streamHandler        model.PeerStreamHandler
	statusHandler        func(model.PeerConnStatus)
	pairHandler          model.PairingHandler
	pairPort             uint16
	failChunkOpens       atomic.Int32
	zombieSession        atomic.Bool
	sessionResets        atomic.Int32
	directListenPort     int
	directOverrides      map[string]string
	directLearned        map[string][]string
	localDirectAddrs     []string
	configureDirectCalls int
}

func newMemTransport(net *memNetwork) *memTransport {
	return &memTransport{
		net:     net,
		allowed: make(map[string]model.Device),
		revoked: make(map[string]bool),
	}
}

func (t *memTransport) ConfigureDirect(listenPort int, peerOverrides map[string]string, learnedAddrs map[string][]string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.directListenPort = listenPort
	t.configureDirectCalls++
	t.directOverrides = make(map[string]string, len(peerOverrides))
	for k, v := range peerOverrides {
		t.directOverrides[k] = v
	}
	t.directLearned = make(map[string][]string, len(learnedAddrs))
	for k, v := range learnedAddrs {
		t.directLearned[k] = append([]string(nil), v...)
	}
	return nil
}

func (t *memTransport) LocalDirectAddrs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.directListenPort <= 0 {
		return nil
	}
	return append([]string(nil), t.localDirectAddrs...)
}

func (t *memTransport) canConnectToLocked(peerID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.online || t.closed {
		return false
	}
	if t.revoked[peerID] {
		return false
	}
	_, ok := t.allowed[peerID]
	return ok
}

func (t *memTransport) notifyStatus(st model.PeerConnStatus) {
	t.mu.RLock()
	h := t.statusHandler
	closed := t.closed
	t.mu.RUnlock()
	if h != nil && !closed {
		go h(st)
	}
}

func (t *memTransport) SetOffline(offline bool) {
	t.mu.Lock()
	t.online = !offline
	t.mu.Unlock()
	t.net.reconcileConnectivity()
}

func (t *memTransport) Start(ctx context.Context, id model.LocalIdentity, derp model.DERPConfig) (model.RouteRecord, error) {
	t.mu.Lock()
	t.id = id
	t.online = true
	t.closed = false
	epoch := id.RouteEpoch
	if epoch == 0 {
		epoch = 1
	}
	addr := fmt.Sprintf("tc-%s-e%d", id.DeviceID, epoch)
	route := model.RouteRecord{
		DeviceID:      id.DeviceID,
		SignPublicKey: append([]byte(nil), id.SignPublicKey...),
		TailcatAddr:   addr,
		DERP:          derp,
		RouteEpoch:    epoch,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := model.SignRouteRecord(&route, ed25519.PrivateKey(id.SignPrivateKey)); err != nil {
		t.mu.Unlock()
		return model.RouteRecord{}, err
	}
	t.route = route
	t.mu.Unlock()

	t.net.mu.Lock()
	t.net.byID[id.DeviceID] = t
	t.net.byAddr[addr] = t
	t.net.mu.Unlock()

	t.net.reconcileConnectivity()
	return route, nil
}

func (t *memTransport) UpdateDERP(ctx context.Context, derp model.DERPConfig) (model.RouteRecord, error) {
	t.mu.Lock()
	nextEpoch := t.route.RouteEpoch + 1
	addr := fmt.Sprintf("tc-%s-e%d", t.id.DeviceID, nextEpoch)
	route := model.RouteRecord{
		DeviceID:      t.id.DeviceID,
		SignPublicKey: append([]byte(nil), t.id.SignPublicKey...),
		TailcatAddr:   addr,
		DERP:          derp,
		RouteEpoch:    nextEpoch,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := model.SignRouteRecord(&route, ed25519.PrivateKey(t.id.SignPrivateKey)); err != nil {
		t.mu.Unlock()
		return model.RouteRecord{}, err
	}
	t.route = route
	t.mu.Unlock()

	t.net.mu.Lock()
	t.net.byAddr[addr] = t
	t.net.mu.Unlock()

	return route, nil
}

func (t *memTransport) CurrentRoute() model.RouteRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.route
}

func (t *memTransport) SetAllowedPeers(peers []model.Device) {
	t.mu.Lock()
	next := make(map[string]model.Device, len(peers))
	for _, p := range peers {
		if !p.Revoked && !t.revoked[p.DeviceID] {
			next[p.DeviceID] = p
		}
	}
	t.allowed = next
	t.mu.Unlock()
	t.net.reconcileConnectivity()
}

func (t *memTransport) RevokePeer(deviceID string) {
	t.mu.Lock()
	t.revoked[deviceID] = true
	delete(t.allowed, deviceID)
	t.mu.Unlock()
	t.net.reconcileConnectivity()
}

func (t *memTransport) SyncPeers(peers []model.Device) {
	t.net.reconcileConnectivity()
}

func (t *memTransport) OpenStream(ctx context.Context, peerDeviceID string, kind model.StreamKind) (io.ReadWriteCloser, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if kind == model.StreamKindChunkData && t.zombieSession.Load() {
		return &zombieTimeoutStream{tr: t}, nil
	}

	if kind == model.StreamKindChunkData && t.failChunkOpens.Add(-1) >= 0 {
		c1, c2 := net.Pipe()
		go func() {
			// Drain request header then abruptly drop connection to simulate DERP relay unexpected EOF.
			var req syncproto.ChunkRequest
			_ = syncproto.ReadFrame(c2, &req)
			_, _ = c2.Write([]byte{0x00, 0x00, 0x10}) // truncated frame header -> unexpected EOF
			_ = c2.Close()
		}()
		return c1, nil
	}

	t.mu.RLock()
	myID := t.id.DeviceID
	t.mu.RUnlock()

	if !t.canConnectToLocked(peerDeviceID) {
		return nil, fmt.Errorf("peer %s not allowed or local offline", peerDeviceID)
	}

	t.net.mu.RLock()
	remote := t.net.byID[peerDeviceID]
	t.net.mu.RUnlock()
	if remote == nil || !remote.canConnectToLocked(myID) {
		return nil, fmt.Errorf("peer %s unreachable or rejected", peerDeviceID)
	}

	remote.mu.RLock()
	handler := remote.streamHandler
	remote.mu.RUnlock()
	if handler == nil {
		return nil, errors.New("remote has no stream handler")
	}

	c1, c2 := net.Pipe()
	go handler(context.Background(), myID, kind, c2)
	return c1, nil
}

func (t *memTransport) SetStreamHandler(h model.PeerStreamHandler) {
	t.mu.Lock()
	t.streamHandler = h
	t.mu.Unlock()
}

func (t *memTransport) SetPeerStatusHandler(h func(model.PeerConnStatus)) {
	t.mu.Lock()
	t.statusHandler = h
	t.mu.Unlock()
}

func (t *memTransport) PeerStatuses() []model.PeerConnStatus {
	t.mu.RLock()
	myID := t.id.DeviceID
	allowedIDs := make([]string, 0, len(t.allowed))
	for pid := range t.allowed {
		allowedIDs = append(allowedIDs, pid)
	}
	t.mu.RUnlock()

	out := make([]model.PeerConnStatus, 0, len(allowedIDs))
	t.net.mu.RLock()
	for _, pid := range allowedIDs {
		conn := t.net.connMap[orderedPair(myID, pid)]
		kind := model.ConnPathDisconnected
		if conn {
			kind = model.ConnPathDirect
		}
		out = append(out, model.PeerConnStatus{
			DeviceID:  pid,
			Connected: conn,
			PathKind:  kind,
		})
	}
	t.net.mu.RUnlock()
	return out
}

func (t *memTransport) StartPairing(ctx context.Context, h model.PairingHandler) (string, uint16, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pairHandler = h
	t.pairPort = model.DefaultPairingPort
	return t.route.TailcatAddr, t.pairPort, nil
}

func (t *memTransport) StopPairing() error {
	t.mu.Lock()
	t.pairHandler = nil
	t.mu.Unlock()
	return nil
}

func (t *memTransport) DialPairing(ctx context.Context, tailcatAddr string, port uint16, derp model.DERPConfig) (net.Conn, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	t.mu.RLock()
	localOnline := t.online && !t.closed
	t.mu.RUnlock()
	if !localOnline {
		return nil, errors.New("local transport offline")
	}

	t.net.mu.RLock()
	host := t.net.byAddr[tailcatAddr]
	t.net.mu.RUnlock()
	if host == nil {
		return nil, fmt.Errorf("pairing host %s not found", tailcatAddr)
	}

	host.mu.RLock()
	handler := host.pairHandler
	hostOnline := host.online && !host.closed
	hostPort := host.pairPort
	host.mu.RUnlock()
	if !hostOnline || handler == nil || hostPort != port {
		return nil, errors.New("pairing listener not active on host")
	}

	c1, c2 := net.Pipe()
	go handler(context.Background(), c2)
	return c1, nil
}

func (t *memTransport) Close(ctx context.Context) error {
	t.mu.Lock()
	t.online = false
	t.closed = true
	t.pairHandler = nil
	t.mu.Unlock()
	t.net.reconcileConnectivity()
	return nil
}

func (t *memTransport) IsRevoked(deviceID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.revoked[deviceID]
}

// --- Test Harness ---

type testNode struct {
	name  string
	store *memStore
	cas   *memCAS
	tr    *memTransport
	svc   *syncproto.Service
	dlDir string
}

func newTestNode(t *testing.T, net *memNetwork, name string, autoDownloadBytes int64, workers int) *testNode {
	t.Helper()
	st := newMemStore()
	cas := newMemCAS()
	tr := newMemTransport(net)
	dlDir := t.TempDir()

	idGen := func(devName, platform string) (model.LocalIdentity, error) {
		signPub, signPriv, err := model.GenerateEd25519Keypair()
		if err != nil {
			return model.LocalIdentity{}, err
		}
		grpPub, grpPriv, err := model.GenerateEd25519Keypair()
		if err != nil {
			return model.LocalIdentity{}, err
		}
		return model.LocalIdentity{
			DeviceID:        "dev-" + strings.ToLower(devName) + "-" + model.NewULID(),
			NodePrivateKey:  "priv-" + devName,
			PresharedKey:    "psk-" + devName,
			SignPublicKey:   signPub,
			SignPrivateKey:  signPriv,
			GroupID:         "grp-" + model.NewULID(),
			GroupPublicKey:  grpPub,
			GroupPrivateKey: grpPriv,
			DeviceName:      devName,
			Platform:        platform,
			RouteEpoch:      1,
		}, nil
	}

	svc, err := syncproto.NewService(st, cas, tr, syncproto.Options{
		Platform:            "linux",
		DefaultDeviceName:   name,
		DownloadDir:         dlDir,
		IdentityGenerator:   idGen,
		AntiEntropyInterval: 100 * time.Millisecond,
		DownloadWorkers:     workers,
	})
	if err != nil {
		t.Fatalf("NewService(%s): %v", name, err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start(%s): %v", name, err)
	}
	cfg := svc.Config()
	cfg.AutoDownloadMaxBytes = autoDownloadBytes
	if err := svc.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateConfig(%s): %v", name, err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})

	return &testNode{
		name:  name,
		store: st,
		cas:   cas,
		tr:    tr,
		svc:   svc,
		dlDir: dlDir,
	}
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

func formThreeNodeGroup(t *testing.T, net *memNetwork, autoDownload int64, workers int) (*testNode, *testNode, *testNode) {
	t.Helper()
	ctx := context.Background()
	a := newTestNode(t, net, "NodeA", autoDownload, workers)
	b := newTestNode(t, net, "NodeB", autoDownload, workers)
	c := newTestNode(t, net, "NodeC", autoDownload, workers)

	// B joins A
	codeAB, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, codeAB); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	// C joins B
	codeBC, _, err := b.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("B.CreatePairingCode: %v", err)
	}
	if err := c.svc.JoinWithPairingCode(ctx, codeBC); err != nil {
		t.Fatalf("C.JoinWithPairingCode: %v", err)
	}

	// Wait until A automatically learns about C via gossip and all 3 are mutually connected.
	waitUntil(t, 4*time.Second, "3-node full mesh via gossip", func() bool {
		return hasActiveDevice(a.svc, c.svc.Identity().DeviceID) &&
			hasActiveDevice(c.svc, a.svc.Identity().DeviceID) &&
			isPeerConnected(a.svc, c.svc.Identity().DeviceID) &&
			isPeerConnected(c.svc, a.svc.Identity().DeviceID)
	})
	return a, b, c
}

func hasActiveDevice(svc *syncproto.Service, devID string) bool {
	devs, err := svc.ListDevices(false)
	if err != nil {
		return false
	}
	for _, d := range devs {
		if d.DeviceID == devID && !d.Revoked {
			return true
		}
	}
	return false
}

func isPeerConnected(svc *syncproto.Service, peerID string) bool {
	for _, st := range svc.PeerStatuses() {
		if st.DeviceID == peerID && st.Connected {
			return true
		}
	}
	return false
}

// --- Scenario 1: One-time pairing & gossip propagation ---

func TestPairingAndGossipPropagation(t *testing.T) {
	net := newMemNetwork()
	a, b, c := formThreeNodeGroup(t, net, 0, 2)

	idA := a.svc.Identity()
	idB := b.svc.Identity()
	idC := c.svc.Identity()

	if idA.GroupID != idB.GroupID || idA.GroupID != idC.GroupID {
		t.Fatalf("group IDs diverged: A=%s B=%s C=%s", idA.GroupID, idB.GroupID, idC.GroupID)
	}

	// Verify A sees all 3 active devices even though A never paired with C directly.
	devsA, err := a.svc.ListDevices(false)
	if err != nil {
		t.Fatalf("A.ListDevices: %v", err)
	}
	if len(devsA) != 3 {
		t.Fatalf("expected A to have 3 devices, got %d", len(devsA))
	}

	// Verify C can send a message directly to A.
	msg, err := c.svc.SendText(context.Background(), "hello A via gossip-learned mesh!")
	if err != nil {
		t.Fatalf("C.SendText: %v", err)
	}

	waitUntil(t, 3*time.Second, "A receives message from C", func() bool {
		got, _ := a.store.GetMessage(msg.MsgID)
		return got != nil && got.TextBody == "hello A via gossip-learned mesh!"
	})
}

// --- Scenario 2: Chat sync & offline catch-up via Vector Clock ---

func TestChatSyncAndOfflineCatchup(t *testing.T) {
	net := newMemNetwork()
	a, b, c := formThreeNodeGroup(t, net, 0, 2)
	ctx := context.Background()

	// Take C offline.
	c.tr.SetOffline(true)
	waitUntil(t, 2*time.Second, "C disconnected from A and B", func() bool {
		return !isPeerConnected(a.svc, c.svc.Identity().DeviceID) &&
			!isPeerConnected(b.svc, c.svc.Identity().DeviceID)
	})

	// A sends two messages and deletes the first while C is offline.
	msg1, err := a.svc.SendText(ctx, "secret temporary note")
	if err != nil {
		t.Fatalf("A.SendText(msg1): %v", err)
	}
	msg2, err := a.svc.SendText(ctx, "persistent announcement")
	if err != nil {
		t.Fatalf("A.SendText(msg2): %v", err)
	}
	if err := a.svc.DeleteMessage(ctx, msg1.MsgID); err != nil {
		t.Fatalf("A.DeleteMessage(msg1): %v", err)
	}

	// Wait until B receives all 3 messages (msg1, msg2, tombstone).
	waitUntil(t, 3*time.Second, "B receives msg1, msg2, and tombstone", func() bool {
		msgs, _ := b.svc.ListMessages(100, "")
		return len(msgs) == 3
	})

	// Now take A offline while B stays online!
	a.tr.SetOffline(true)
	waitUntil(t, 2*time.Second, "A disconnected from B", func() bool {
		return !isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	// Bring C back online -> C must catch up from B via Vector Clock even though author A is offline.
	c.tr.SetOffline(false)

	waitUntil(t, 4*time.Second, "C catches up msg1, msg2, and tombstone from B", func() bool {
		msgs, _ := c.svc.ListMessages(100, "")
		if len(msgs) < 3 {
			return false
		}
		var hasMsg1, hasMsg2, hasTombstone bool
		for _, m := range msgs {
			switch {
			case m.MsgID == msg1.MsgID && m.TextBody == "secret temporary note":
				hasMsg1 = true
			case m.MsgID == msg2.MsgID && m.TextBody == "persistent announcement":
				hasMsg2 = true
			case m.Kind == model.MessageKindTombstone && m.TargetMsgID == msg1.MsgID:
				hasTombstone = true
			}
		}
		return hasMsg1 && hasMsg2 && hasTombstone
	})
}

// --- Scenario 3: Multi-chunk file transfer + pause/resume ---

func TestMultiChunkFileTransferPauseResume(t *testing.T) {
	net := newMemNetwork()
	// Use AutoDownloadMaxBytes=0 so B triggers DownloadFile explicitly, and 1 worker for deterministic chunk 0 -> pause -> resume.
	a, b, _ := formThreeNodeGroup(t, net, 0, 1)
	ctx := context.Background()

	// Create 2.5 MiB random payload (3 chunks: 1 MiB + 1 MiB + 0.5 MiB).
	payload := make([]byte, 2*model.DefaultChunkSize+512*1024)
	if _, err := io.ReadFull(rand.Reader, payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	wantHash := model.Blake3Hex(payload)

	fileMsg, err := a.svc.SendBytes(ctx, "archive.bin", "application/octet-stream", payload)
	if err != nil {
		t.Fatalf("A.SendBytes: %v", err)
	}
	blobHash := fileMsg.BlobHash
	if blobHash != wantHash {
		t.Fatalf("unexpected blob hash: got %s want %s", blobHash, wantHash)
	}

	// Wait until B receives the file message metadata.
	waitUntil(t, 3*time.Second, "B receives file message", func() bool {
		m, _ := b.store.GetMessage(fileMsg.MsgID)
		return m != nil && m.File != nil && m.File.ChunkCount == 3
	})

	// Subscribe on B to pause immediately once chunk 0 is persisted, while blocking chunk 1 on A.
	chunk1Gate := make(chan struct{})
	chunk1Reached := make(chan struct{})
	var chunk1Once sync.Once
	var resumeMode atomic.Bool
	var resumedChunksMu sync.Mutex
	var resumedChunks []int

	a.cas.SetReadHook(func(bh string, idx int) {
		if bh != blobHash {
			return
		}
		if resumeMode.Load() {
			resumedChunksMu.Lock()
			resumedChunks = append(resumedChunks, idx)
			resumedChunksMu.Unlock()
			return
		}
		if idx == 1 {
			chunk1Once.Do(func() { close(chunk1Reached) })
			<-chunk1Gate
		}
	})

	evCh, unsub := b.svc.Subscribe()
	defer unsub()

	dlErrCh := make(chan error, 1)
	go func() {
		dlErrCh <- b.svc.DownloadFile(ctx, blobHash)
	}()

	// Wait until chunk 0 is recorded in B's transfer event AND chunk 1 request hits A's gate.
	chunk0Done := false
	timeout := time.After(5 * time.Second)
	for !chunk0Done {
		select {
		case ev := <-evCh:
			if ev.Type == model.EventTransferUpdated && ev.Transfer != nil && ev.Transfer.BlobHash == blobHash {
				if model.HasChunk(ev.Transfer.ChunkBitmap, 0) {
					chunk0Done = true
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for chunk 0 completion on B")
		}
	}
	select {
	case <-chunk1Reached:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for chunk 1 request on A")
	}

	// Pause transfer mid-flight!
	pauseDone := make(chan error, 1)
	go func() {
		pauseDone <- b.svc.PauseTransfer(blobHash)
	}()
	// Release gate so A's ReadChunk handler exits cleanly too.
	close(chunk1Gate)

	if err := <-pauseDone; err != nil {
		t.Fatalf("B.PauseTransfer: %v", err)
	}
	dlErr := <-dlErrCh
	if !errors.Is(dlErr, syncproto.ErrTransferPaused) {
		t.Fatalf("expected ErrTransferPaused from DownloadFile, got %v", dlErr)
	}

	// Verify persisted bitmap has chunk 0 and is in TransferPaused state.
	task, err := b.svc.GetTransfer(blobHash)
	if err != nil || task == nil {
		t.Fatalf("B.GetTransfer after pause: task=%v err=%v", task, err)
	}
	if task.State != model.TransferPaused {
		t.Fatalf("expected state %s, got %s", model.TransferPaused, task.State)
	}
	if !model.HasChunk(task.ChunkBitmap, 0) {
		t.Fatalf("expected chunk 0 to be set in persisted bitmap")
	}
	if model.CountChunks(task.ChunkBitmap, 3) >= 3 {
		t.Fatalf("expected transfer to be incomplete on pause, got %d/3 chunks", model.CountChunks(task.ChunkBitmap, 3))
	}

	// Resume download and track chunks requested during resume.
	resumeMode.Store(true)
	if err := b.svc.DownloadFile(ctx, blobHash); err != nil {
		t.Fatalf("B.DownloadFile(resume): %v", err)
	}

	// Verify chunk 0 was NOT re-downloaded during resume!
	resumedChunksMu.Lock()
	for _, idx := range resumedChunks {
		if idx == 0 {
			t.Fatalf("chunk 0 should not have been re-downloaded on resume; fetched=%v", resumedChunks)
		}
	}
	if len(resumedChunks) == 0 {
		t.Fatalf("expected remaining chunks to be downloaded on resume")
	}
	resumedChunksMu.Unlock()

	// Verify final transfer state and assembled file byte-for-byte integrity.
	finalTask, err := b.svc.GetTransfer(blobHash)
	if err != nil || finalTask == nil || finalTask.State != model.TransferDone {
		t.Fatalf("expected TransferDone, got %+v (err=%v)", finalTask, err)
	}
	blobB, err := b.store.GetFileBlob(blobHash)
	if err != nil || blobB == nil || blobB.LocalPath == "" {
		t.Fatalf("expected assembled LocalPath on B, got %+v (err=%v)", blobB, err)
	}
	diskBytes, err := os.ReadFile(blobB.LocalPath)
	if err != nil {
		t.Fatalf("read assembled file: %v", err)
	}
	if !bytes.Equal(diskBytes, payload) {
		t.Fatalf("assembled file content mismatch (got %d bytes, want %d)", len(diskBytes), len(payload))
	}
	if gotHash := model.Blake3Hex(diskBytes); gotHash != wantHash {
		t.Fatalf("assembled BLAKE3 mismatch: got %s want %s", gotHash, wantHash)
	}

	// Verify FrameBlobHave propagated back to A so A knows B is now a seeder.
	waitUntil(t, 3*time.Second, "A records B as blob seeder via FrameBlobHave", func() bool {
		blobA, _ := a.store.GetFileBlob(blobHash)
		if blobA == nil {
			return false
		}
		for _, sid := range blobA.Seeders {
			if sid == b.svc.Identity().DeviceID {
				return true
			}
		}
		return false
	})
}

// --- Scenario 4: DERP route hot-update ---

func TestDERPRouteHotUpdate(t *testing.T) {
	net := newMemNetwork()
	a, b, c := formThreeNodeGroup(t, net, 0, 2)
	ctx := context.Background()

	idA := a.svc.Identity()
	initialEpoch := idA.RouteEpoch

	newDERP := model.DERPConfig{
		Mode:             model.DERPModeCustomRegion,
		RegionID:         901,
		CustomHost:       "derp.selfhosted.example",
		CustomDERPPort:   443,
		CustomSTUNPort:   3478,
		CustomRegionName: "MyVPS",
	}
	if err := a.svc.UpdateDERP(ctx, newDERP); err != nil {
		t.Fatalf("A.UpdateDERP: %v", err)
	}

	updatedEpoch := a.svc.Identity().RouteEpoch
	if updatedEpoch <= initialEpoch {
		t.Fatalf("expected RouteEpoch to increment: before=%d after=%d", initialEpoch, updatedEpoch)
	}

	// Verify both B and C receive the signed MemberEventRoute and update A's route without losing membership.
	checkNode := func(label string, n *testNode) {
		waitUntil(t, 3*time.Second, label+" receives A's updated DERP route", func() bool {
			dev, _ := n.store.GetDevice(idA.DeviceID)
			return dev != nil &&
				!dev.Revoked &&
				dev.RouteEpoch == updatedEpoch &&
				dev.Route.DERP.CustomHost == "derp.selfhosted.example" &&
				model.VerifyRouteRecord(dev.Route)
		})
	}
	checkNode("B", b)
	checkNode("C", c)

	// Verify messaging still works across all nodes after DERP hot-update.
	msg, err := a.svc.SendText(ctx, "post-DERP-switch ping")
	if err != nil {
		t.Fatalf("A.SendText: %v", err)
	}
	waitUntil(t, 3*time.Second, "B and C receive post-DERP message", func() bool {
		mB, _ := b.store.GetMessage(msg.MsgID)
		mC, _ := c.store.GetMessage(msg.MsgID)
		return mB != nil && mC != nil
	})
}

// --- Scenario 5: Device revocation ---

func TestDeviceRevocation(t *testing.T) {
	net := newMemNetwork()
	a, b, c := formThreeNodeGroup(t, net, 0, 2)
	ctx := context.Background()

	idC := c.svc.Identity().DeviceID

	// A revokes C.
	if err := a.svc.RevokeDevice(ctx, idC); err != nil {
		t.Fatalf("A.RevokeDevice(C): %v", err)
	}

	// Verify B receives signed MemberEventRevoke via gossip and revokes C in store and transport.
	waitUntil(t, 3*time.Second, "B marks C revoked in store and transport", func() bool {
		dev, _ := b.store.GetDevice(idC)
		return dev != nil && dev.Revoked && b.tr.IsRevoked(idC)
	})
	if !a.tr.IsRevoked(idC) {
		t.Fatalf("expected A transport to revoke C")
	}
	if hasActiveDevice(a.svc, idC) || hasActiveDevice(b.svc, idC) {
		t.Fatalf("revoked device C must not appear in ListDevices(false)")
	}

	// Verify C can no longer open streams to A or B, and messages sent by C never arrive at A or B.
	if _, err := c.tr.OpenStream(ctx, a.svc.Identity().DeviceID, model.StreamKindControl); err == nil {
		t.Fatalf("expected C -> A OpenStream to fail after revocation")
	}
	if _, err := c.tr.OpenStream(ctx, b.svc.Identity().DeviceID, model.StreamKindControl); err == nil {
		t.Fatalf("expected C -> B OpenStream to fail after revocation")
	}

	revokedMsg, err := c.svc.SendText(ctx, "unauthorized message from revoked C")
	if err != nil {
		t.Fatalf("C.SendText: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	if gotA, _ := a.store.GetMessage(revokedMsg.MsgID); gotA != nil {
		t.Fatalf("A unexpectedly received message from revoked device C")
	}
	if gotB, _ := b.store.GetMessage(revokedMsg.MsgID); gotB != nil {
		t.Fatalf("B unexpectedly received message from revoked device C")
	}
}

func TestDefaultConfigMinimizeToTray(t *testing.T) {
	net := newMemNetwork()
	n := newTestNode(t, net, "TrayDefault", 0, 1)

	cfg := n.svc.Config()
	if !cfg.MinimizeToTray {
		t.Fatalf("expected MinimizeToTray to default to true, got false")
	}
	if cfg.Language != model.LanguageAuto {
		t.Fatalf("expected Language to default to %q, got %q", model.LanguageAuto, cfg.Language)
	}

	cfg.MinimizeToTray = false
	cfg.Language = model.LanguageEnglish
	if err := n.svc.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	got := n.svc.Config()
	if got.MinimizeToTray {
		t.Fatalf("expected explicit MinimizeToTray=false to be persisted")
	}
	if got.Language != model.LanguageEnglish {
		t.Fatalf("expected explicit Language=%q to be persisted, got %q", model.LanguageEnglish, got.Language)
	}

	cfg.Language = "fr-FR"
	if err := n.svc.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateConfig invalid language: %v", err)
	}
	if got := n.svc.Config().Language; got != model.LanguageAuto {
		t.Fatalf("expected unsupported language to normalize to %q, got %q", model.LanguageAuto, got)
	}
}

type realStoreTestNode struct {
	name  string
	store *store.SQLiteStore
	cas   *store.FileCAS
	tr    *memTransport
	svc   *syncproto.Service
	dlDir string
}

func newRealStoreTestNode(t *testing.T, net *memNetwork, name string, autoDownloadBytes int64) *realStoreTestNode {
	t.Helper()
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "tailchat.db"))
	if err != nil {
		t.Fatalf("store.Open(%s): %v", name, err)
	}
	cas, err := store.NewCAS(filepath.Join(base, "cas"))
	if err != nil {
		_ = st.Close()
		t.Fatalf("store.NewCAS(%s): %v", name, err)
	}
	dlDir := filepath.Join(base, "downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		_ = st.Close()
		t.Fatalf("MkdirAll(%s): %v", dlDir, err)
	}
	tr := newMemTransport(net)

	idGen := func(devName, platform string) (model.LocalIdentity, error) {
		signPub, signPriv, err := model.GenerateEd25519Keypair()
		if err != nil {
			return model.LocalIdentity{}, err
		}
		grpPub, grpPriv, err := model.GenerateEd25519Keypair()
		if err != nil {
			return model.LocalIdentity{}, err
		}
		return model.LocalIdentity{
			DeviceID:        "dev-" + strings.ToLower(devName) + "-" + model.NewULID(),
			NodePrivateKey:  "priv-" + devName,
			PresharedKey:    "psk-" + devName,
			SignPublicKey:   signPub,
			SignPrivateKey:  signPriv,
			GroupID:         "grp-" + model.NewULID(),
			GroupPublicKey:  grpPub,
			GroupPrivateKey: grpPriv,
			DeviceName:      devName,
			Platform:        platform,
			RouteEpoch:      1,
		}, nil
	}

	svc, err := syncproto.NewService(st, cas, tr, syncproto.Options{
		Platform:            "macos",
		DefaultDeviceName:   name,
		DownloadDir:         dlDir,
		IdentityGenerator:   idGen,
		AntiEntropyInterval: 100 * time.Millisecond,
		DownloadWorkers:     2,
	})
	if err != nil {
		_ = st.Close()
		t.Fatalf("NewService(%s): %v", name, err)
	}
	if err := svc.Start(context.Background()); err != nil {
		_ = st.Close()
		t.Fatalf("Start(%s): %v", name, err)
	}
	cfg := svc.Config()
	cfg.AutoDownloadMaxBytes = autoDownloadBytes
	if err := svc.UpdateConfig(context.Background(), cfg); err != nil {
		t.Fatalf("UpdateConfig(%s): %v", name, err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	})

	return &realStoreTestNode{
		name:  name,
		store: st,
		cas:   cas,
		tr:    tr,
		svc:   svc,
		dlDir: dlDir,
	}
}

func TestSendFilePreservesRealPathAndResolveLocalFile(t *testing.T) {
	net := newMemNetwork()
	n := newRealStoreTestNode(t, net, "Sender", 0)
	ctx := context.Background()

	// 1. SendFile with a file:// URL-encoded path containing spaces preserves the real source path.
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "my report 2026.txt")
	srcContent := []byte("hello tailchat real file path test")
	if err := os.WriteFile(srcPath, srcContent, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(srcPath)}).String()
	msg, err := n.svc.SendFile(ctx, "  "+fileURL+"  ")
	if err != nil {
		t.Fatalf("SendFile(file://): %v", err)
	}
	if msg.File == nil {
		t.Fatalf("expected non-nil File metadata")
	}
	if msg.File.LocalPath != srcPath {
		t.Fatalf("expected LocalPath to preserve real source path %q, got %q", srcPath, msg.File.LocalPath)
	}
	if strings.Contains(filepath.ToSlash(msg.File.LocalPath), "/cas/blobs/") {
		t.Fatalf("LocalPath must not point into internal CAS blobs dir: %q", msg.File.LocalPath)
	}

	resolved, err := n.svc.ResolveLocalFile(ctx, msg.BlobHash)
	if err != nil {
		t.Fatalf("ResolveLocalFile(original exists): %v", err)
	}
	if resolved != srcPath {
		t.Fatalf("expected ResolveLocalFile to return %q, got %q", srcPath, resolved)
	}

	// 2. Deleting the original source file still allows ResolveLocalFile to materialize from CAS into DownloadDir.
	if err := os.Remove(srcPath); err != nil {
		t.Fatalf("Remove source file: %v", err)
	}
	materialized, err := n.svc.ResolveLocalFile(ctx, msg.BlobHash)
	if err != nil {
		t.Fatalf("ResolveLocalFile(after delete): %v", err)
	}
	if filepath.Dir(materialized) != filepath.Clean(n.dlDir) {
		t.Fatalf("expected materialized file in DownloadDir %q, got %q", n.dlDir, materialized)
	}
	if filepath.Base(materialized) != "my report 2026.txt" {
		t.Fatalf("expected materialized filename 'my report 2026.txt', got %q", filepath.Base(materialized))
	}
	gotBytes, err := os.ReadFile(materialized)
	if err != nil || !bytes.Equal(gotBytes, srcContent) {
		t.Fatalf("materialized file content mismatch: err=%v len=%d", err, len(gotBytes))
	}
	// Verify updated LocalPath was persisted in store.
	persistedBlob, err := n.store.GetFileBlob(msg.BlobHash)
	if err != nil || persistedBlob == nil || persistedBlob.LocalPath != materialized {
		t.Fatalf("expected persisted LocalPath %q, got %+v (err=%v)", materialized, persistedBlob, err)
	}

	// 3. Simulate legacy internal CAS blob LocalPath and verify ResolveLocalFile transparently repairs it.
	casBlobPath, ok := n.cas.HasCompleteBlob(msg.BlobHash)
	if !ok {
		t.Fatalf("expected CAS to have complete blob %s", msg.BlobHash)
	}
	persistedBlob.LocalPath = casBlobPath
	if err := n.store.UpsertFileBlob(*persistedBlob); err != nil {
		t.Fatalf("UpsertFileBlob(legacy cas path): %v", err)
	}
	repairedPath, err := n.svc.ResolveLocalFile(ctx, msg.BlobHash)
	if err != nil {
		t.Fatalf("ResolveLocalFile(legacy cas path): %v", err)
	}
	if repairedPath == casBlobPath || filepath.Dir(repairedPath) != filepath.Clean(n.dlDir) {
		t.Fatalf("expected legacy CAS path to be materialized into DownloadDir %q, got %q", n.dlDir, repairedPath)
	}

	// 4. SendBytes writes a real named file directly into DownloadDir.
	pasteData := []byte("fake-png-clipboard-payload")
	pasteMsg, err := n.svc.SendBytes(ctx, "clipboard-shot.png", "image/png", pasteData)
	if err != nil {
		t.Fatalf("SendBytes: %v", err)
	}
	if pasteMsg.File == nil || pasteMsg.File.LocalPath == "" {
		t.Fatalf("expected non-empty LocalPath for SendBytes")
	}
	if filepath.Dir(pasteMsg.File.LocalPath) != filepath.Clean(n.dlDir) {
		t.Fatalf("expected SendBytes file in DownloadDir %q, got %q", n.dlDir, pasteMsg.File.LocalPath)
	}
	if filepath.Base(pasteMsg.File.LocalPath) != "clipboard-shot.png" {
		t.Fatalf("expected filename 'clipboard-shot.png', got %q", filepath.Base(pasteMsg.File.LocalPath))
	}
	pasteDisk, err := os.ReadFile(pasteMsg.File.LocalPath)
	if err != nil || !bytes.Equal(pasteDisk, pasteData) {
		t.Fatalf("SendBytes disk content mismatch: err=%v", err)
	}

	// 5. ResolveLocalFile for an undownloaded remote blob returns an informative error.
	remoteHash := model.Blake3Hex([]byte("not-present-locally"))
	if err := n.store.UpsertFileBlob(model.FileBlob{
		BlobHash:   remoteHash,
		FileName:   "remote.bin",
		FileSize:   19,
		ChunkSize:  model.DefaultChunkSize,
		ChunkCount: 1,
	}); err != nil {
		t.Fatalf("UpsertFileBlob(remote): %v", err)
	}
	if _, err := n.svc.ResolveLocalFile(ctx, remoteHash); err == nil || !strings.Contains(err.Error(), "file not available locally") {
		t.Fatalf("expected 'file not available locally' error, got %v", err)
	}

	// 6. UpdateConfig normalizes & creates a new DownloadDir and rejects invalid (file) paths.
	newDLDir := filepath.Join(t.TempDir(), "custom-downloads", "nested")
	cfg := n.svc.Config()
	cfg.DownloadDir = "  " + newDLDir + string(filepath.Separator) + "  "
	if err := n.svc.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateConfig(new DownloadDir): %v", err)
	}
	if got := n.svc.Config().DownloadDir; got != filepath.Clean(newDLDir) {
		t.Fatalf("expected cleaned DownloadDir %q, got %q", filepath.Clean(newDLDir), got)
	}
	if st, err := os.Stat(newDLDir); err != nil || !st.IsDir() {
		t.Fatalf("expected UpdateConfig to create DownloadDir %q (err=%v)", newDLDir, err)
	}

	notADirFile := filepath.Join(t.TempDir(), "regular-file.txt")
	if err := os.WriteFile(notADirFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg.DownloadDir = notADirFile
	if err := n.svc.UpdateConfig(ctx, cfg); err == nil {
		t.Fatalf("expected UpdateConfig to reject regular file as DownloadDir")
	}
}

func TestClearAllMessagesSyncsAcrossPeersAndPreservesFiles(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 10*1024*1024)
	b := newRealStoreTestNode(t, net, "NodeB", 10*1024*1024)

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	// Send text messages from both peers and a file from A (which auto-downloads to B's DownloadDir).
	if _, err := a.svc.SendText(ctx, "hello from A"); err != nil {
		t.Fatalf("A.SendText: %v", err)
	}
	srcFile := filepath.Join(t.TempDir(), "design-doc.pdf")
	fileBytes := []byte("pdf-payload-must-survive-clear-all")
	if err := os.WriteFile(srcFile, fileBytes, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileMsg, err := a.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("A.SendFile: %v", err)
	}
	if _, err := b.svc.SendText(ctx, "reply from B"); err != nil {
		t.Fatalf("B.SendText: %v", err)
	}

	// Wait until both peers have all 3 active messages and B has downloaded the file locally.
	waitUntil(t, 4*time.Second, "both peers synced all 3 messages", func() bool {
		msgsA, _ := a.svc.ListMessages(100, "")
		msgsB, _ := b.svc.ListMessages(100, "")
		return len(msgsA) == 3 && len(msgsB) == 3
	})
	var bDownloadedPath string
	waitUntil(t, 4*time.Second, "B downloaded file into DownloadDir", func() bool {
		blobB, _ := b.store.GetFileBlob(fileMsg.BlobHash)
		if blobB == nil || blobB.LocalPath == "" {
			return false
		}
		if _, err := os.Stat(blobB.LocalPath); err != nil {
			return false
		}
		bDownloadedPath = blobB.LocalPath
		return true
	})

	// Clear all messages from A.
	if err := a.svc.ClearAllMessages(ctx); err != nil {
		t.Fatalf("A.ClearAllMessages: %v", err)
	}

	// A's active message list must be empty immediately.
	msgsA, err := a.svc.ListMessages(100, "")
	if err != nil {
		t.Fatalf("A.ListMessages after clear: %v", err)
	}
	if len(msgsA) != 0 {
		t.Fatalf("expected 0 active messages on A after ClearAllMessages, got %d", len(msgsA))
	}

	// Peer B must also clear all active messages via synced tombstones.
	waitUntil(t, 4*time.Second, "B clears all messages via synced tombstones", func() bool {
		msgsB, err := b.svc.ListMessages(100, "")
		return err == nil && len(msgsB) == 0
	})

	// Both local files (A's original and B's downloaded copy) must remain intact on disk!
	gotA, err := os.ReadFile(srcFile)
	if err != nil || !bytes.Equal(gotA, fileBytes) {
		t.Fatalf("A's local file was unexpectedly modified or removed: err=%v", err)
	}
	gotB, err := os.ReadFile(bDownloadedPath)
	if err != nil || !bytes.Equal(gotB, fileBytes) {
		t.Fatalf("B's downloaded local file %q was unexpectedly modified or removed: err=%v", bDownloadedPath, err)
	}

	// Calling ClearAllMessages on an already-empty timeline is a safe no-op.
	if err := a.svc.ClearAllMessages(ctx); err != nil {
		t.Fatalf("second A.ClearAllMessages: %v", err)
	}
}

func TestDownloadRetriesTransientChunkEOF(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 0)
	b := newRealStoreTestNode(t, net, "NodeB", 0)

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	// Build a multi-chunk payload (~2.2 MiB -> 3 chunks at 1 MiB each).
	payload := make([]byte, 2*model.DefaultChunkSize+200*1024)
	for i := range payload {
		payload[i] = byte((i*31 + 17) & 0xff)
	}
	srcFile := filepath.Join(t.TempDir(), "large-video.bin")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fileMsg, err := a.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("A.SendFile: %v", err)
	}

	// Receiver sees the file message metadata immediately before downloading chunks.
	waitUntil(t, 4*time.Second, "B receives file message metadata before download", func() bool {
		msgsB, err := b.svc.ListMessages(20, "")
		if err != nil || len(msgsB) != 1 {
			return false
		}
		return msgsB[0].BlobHash == fileMsg.BlobHash
	})
	blobBefore, err := b.store.GetFileBlob(fileMsg.BlobHash)
	if err != nil || blobBefore == nil {
		t.Fatalf("GetFileBlob before download: blob=%v err=%v", blobBefore, err)
	}
	if blobBefore.LocalPath != "" {
		t.Fatalf("expected empty LocalPath before manual download, got %q", blobBefore.LocalPath)
	}

	// Inject 2 transient truncated-frame EOF errors on StreamKindChunkData (simulating DERP relay drops).
	b.tr.failChunkOpens.Store(2)

	if err := b.svc.DownloadFile(ctx, fileMsg.BlobHash); err != nil {
		t.Fatalf("B.DownloadFile should succeed via retry after transient EOFs, got: %v", err)
	}
	if got := b.tr.failChunkOpens.Load(); got >= 0 {
		t.Fatalf("expected transient chunk EOF injection to trigger, remaining counter=%d", got)
	}

	blobAfter, err := b.store.GetFileBlob(fileMsg.BlobHash)
	if err != nil || blobAfter == nil || blobAfter.LocalPath == "" {
		t.Fatalf("expected downloaded LocalPath after DownloadFile, blob=%+v err=%v", blobAfter, err)
	}
	gotBytes, err := os.ReadFile(blobAfter.LocalPath)
	if err != nil || !bytes.Equal(gotBytes, payload) {
		t.Fatalf("downloaded file payload mismatch: err=%v len=%d want=%d", err, len(gotBytes), len(payload))
	}

	// Verify temporary chunk staging directory was cleaned up after assembly.
	chunkStageDir := filepath.Join(filepath.Dir(b.dlDir), "cas", "chunks", fileMsg.BlobHash)
	if _, err := os.Stat(chunkStageDir); !os.IsNotExist(err) {
		t.Fatalf("expected temporary chunk dir %q to be removed after AssembleFile, stat err=%v", chunkStageDir, err)
	}
}

func TestResendingFailedLargeFileClearsStaleErrorAndPreservesChunks(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	// NodeB has a 1 MiB auto-download limit; file is ~2.2 MiB (> threshold).
	a := newRealStoreTestNode(t, net, "NodeA", 1024*1024)
	b := newRealStoreTestNode(t, net, "NodeB", 1024*1024)

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	payload := make([]byte, 2*model.DefaultChunkSize+128*1024)
	for i := range payload {
		payload[i] = byte((i*13 + 7) & 0xff)
	}
	srcFile := filepath.Join(t.TempDir(), "Downloads.zip")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	firstMsg, err := a.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("first A.SendFile: %v", err)
	}
	waitUntil(t, 4*time.Second, "B receives first file message", func() bool {
		msgs, _ := b.svc.ListMessages(10, "")
		return len(msgs) == 1
	})

	// Simulate a previous interrupted download: chunk 1 cached on disk, task stuck in TransferFailed.
	chunk1 := payload[model.DefaultChunkSize : 2*model.DefaultChunkSize]
	if err := b.cas.WriteChunk(firstMsg.BlobHash, 1, model.Blake3Hex(chunk1), chunk1); err != nil {
		t.Fatalf("WriteChunk(1): %v", err)
	}
	bm := model.SetChunk(model.NewChunkBitmap(3), 1)
	if err := b.store.UpsertTransferTask(model.TransferTask{
		BlobHash:    firstMsg.BlobHash,
		Direction:   model.TransferDownload,
		State:       model.TransferFailed,
		ChunkCount:  3,
		ChunkBitmap: bm,
		BytesDone:   int64(len(chunk1)),
		BytesTotal:  int64(len(payload)),
		LastError:   "unexpected EOF",
		UpdatedAt:   time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("UpsertTransferTask(failed): %v", err)
	}

	// Sender re-sends the exact same large file.
	if _, err := a.svc.SendFile(ctx, srcFile); err != nil {
		t.Fatalf("second A.SendFile: %v", err)
	}
	waitUntil(t, 4*time.Second, "B receives re-sent file and clears stale failed state", func() bool {
		msgs, _ := b.svc.ListMessages(10, "")
		if len(msgs) != 2 {
			return false
		}
		task, _ := b.svc.GetTransfer(firstMsg.BlobHash)
		return task != nil && task.State == model.TransferPaused && task.LastError == ""
	})

	task, err := b.svc.GetTransfer(firstMsg.BlobHash)
	if err != nil || task == nil {
		t.Fatalf("GetTransfer after resend: task=%+v err=%v", task, err)
	}
	if task.BytesDone != int64(len(chunk1)) {
		t.Fatalf("expected partial BytesDone=%d preserved, got %d", len(chunk1), task.BytesDone)
	}

	// Clicking Resume / DownloadFile completes cleanly using the preserved chunk.
	if err := b.svc.DownloadFile(ctx, firstMsg.BlobHash); err != nil {
		t.Fatalf("B.DownloadFile resume after resend: %v", err)
	}
	blobAfter, err := b.store.GetFileBlob(firstMsg.BlobHash)
	if err != nil || blobAfter == nil || blobAfter.LocalPath == "" {
		t.Fatalf("expected completed LocalPath, blob=%+v err=%v", blobAfter, err)
	}
	got, err := os.ReadFile(blobAfter.LocalPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("resumed payload mismatch: err=%v len=%d want=%d", err, len(got), len(payload))
	}
}

type testTimeoutErr struct{}

func (testTimeoutErr) Error() string   { return "i/o timeout" }
func (testTimeoutErr) Timeout() bool   { return true }
func (testTimeoutErr) Temporary() bool { return true }

// zombieTimeoutStream simulates a half-dead yamux stream where Write succeeds locally
// in memory, Read hits the header timeout, and ResetSession() evicts the zombie session.
type zombieTimeoutStream struct {
	tr *memTransport
}

func (z *zombieTimeoutStream) Write(p []byte) (int, error)          { return len(p), nil }
func (z *zombieTimeoutStream) Read(_ []byte) (int, error)           { return 0, testTimeoutErr{} }
func (z *zombieTimeoutStream) Close() error                         { return nil }
func (z *zombieTimeoutStream) SetReadDeadline(_ time.Time) error    { return nil }
func (z *zombieTimeoutStream) ResetSession() {
	if z.tr.zombieSession.Swap(false) {
		z.tr.sessionResets.Add(1)
	}
}

func TestDownloadEvictsZombieSessionAndRecoversImmediately(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 0)
	b := newRealStoreTestNode(t, net, "NodeB", 0)

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	payload := make([]byte, 2*model.DefaultChunkSize+100*1024)
	for i := range payload {
		payload[i] = byte((i*29 + 5) & 0xff)
	}
	srcFile := filepath.Join(t.TempDir(), "zombie-recover.bin")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileMsg, err := a.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("A.SendFile: %v", err)
	}
	waitUntil(t, 4*time.Second, "B receives metadata", func() bool {
		msgs, _ := b.svc.ListMessages(10, "")
		return len(msgs) == 1
	})

	// Simulate a half-dead yamux session on B; without ResetSession() this would fail every chunk.
	b.tr.zombieSession.Store(true)

	if err := b.svc.DownloadFile(ctx, fileMsg.BlobHash); err != nil {
		t.Fatalf("B.DownloadFile should evict zombie session and recover, got: %v", err)
	}
	if got := b.tr.sessionResets.Load(); got != 1 {
		t.Fatalf("expected exactly 1 zombie session eviction, got %d", got)
	}
	blobAfter, err := b.store.GetFileBlob(fileMsg.BlobHash)
	if err != nil || blobAfter == nil || blobAfter.LocalPath == "" {
		t.Fatalf("expected completed LocalPath, blob=%+v err=%v", blobAfter, err)
	}
	got, err := os.ReadFile(blobAfter.LocalPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload mismatch: err=%v len=%d want=%d", err, len(got), len(payload))
	}
}

func TestDownloadMultiPassRecoversEarlierFailedChunks(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 0)
	b := newRealStoreTestNode(t, net, "NodeB", 0)

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	// 4 chunks (3.2 MiB); with 2 workers and 5 forced failures, the first chunk exhausts
	// all 4 single-chunk retry rounds in pass 1 while later chunks succeed in pass 1.
	// Pass 2 must automatically catch up the earlier failed chunk instead of failing the download.
	payload := make([]byte, 3*model.DefaultChunkSize+200*1024)
	for i := range payload {
		payload[i] = byte((i*19 + 3) & 0xff)
	}
	srcFile := filepath.Join(t.TempDir(), "multipass.bin")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileMsg, err := a.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("A.SendFile: %v", err)
	}
	waitUntil(t, 4*time.Second, "B receives metadata", func() bool {
		msgs, _ := b.svc.ListMessages(10, "")
		return len(msgs) == 1
	})

	b.tr.failChunkOpens.Store(5)
	if err := b.svc.DownloadFile(ctx, fileMsg.BlobHash); err != nil {
		t.Fatalf("B.DownloadFile should recover earlier failed chunks in pass 2, got: %v", err)
	}
	blobAfter, err := b.store.GetFileBlob(fileMsg.BlobHash)
	if err != nil || blobAfter == nil || blobAfter.LocalPath == "" {
		t.Fatalf("expected completed LocalPath, blob=%+v err=%v", blobAfter, err)
	}
	got, err := os.ReadFile(blobAfter.LocalPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload mismatch: err=%v len=%d want=%d", err, len(got), len(payload))
	}
}

func TestDownloadReclaimsVerifiedLocalCASChunks(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 0)
	b := newRealStoreTestNode(t, net, "NodeB", 0)

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 3*time.Second, "A and B connected", func() bool {
		return isPeerConnected(a.svc, b.svc.Identity().DeviceID) &&
			isPeerConnected(b.svc, a.svc.Identity().DeviceID)
	})

	payload := make([]byte, 2*model.DefaultChunkSize+64*1024)
	for i := range payload {
		payload[i] = byte((i*41 + 11) & 0xff)
	}
	srcFile := filepath.Join(t.TempDir(), "reclaim.bin")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileMsg, err := a.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("A.SendFile: %v", err)
	}
	waitUntil(t, 4*time.Second, "B receives metadata", func() bool {
		msgs, _ := b.svc.ListMessages(10, "")
		return len(msgs) == 1
	})

	// Pre-populate all chunks on B's disk while keeping B's transfer_tasks bitmap completely empty.
	for i := 0; i < fileMsg.File.ChunkCount; i++ {
		start := i * model.DefaultChunkSize
		end := start + model.DefaultChunkSize
		if end > len(payload) {
			end = len(payload)
		}
		chunk := payload[start:end]
		if err := b.cas.WriteChunk(fileMsg.BlobHash, i, model.Blake3Hex(chunk), chunk); err != nil {
			t.Fatalf("WriteChunk(%d): %v", i, err)
		}
	}

	// Even with sender offline (zero network fetches possible), B must reclaim all verified
	// local chunks from CAS and assemble the file immediately.
	a.tr.SetOffline(true)
	if err := b.svc.DownloadFile(ctx, fileMsg.BlobHash); err != nil {
		t.Fatalf("B.DownloadFile should reclaim all verified CAS chunks offline, got: %v", err)
	}
	blobAfter, err := b.store.GetFileBlob(fileMsg.BlobHash)
	if err != nil || blobAfter == nil || blobAfter.LocalPath == "" {
		t.Fatalf("expected assembled LocalPath, blob=%+v err=%v", blobAfter, err)
	}
	got, err := os.ReadFile(blobAfter.LocalPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("assembled payload mismatch: err=%v len=%d want=%d", err, len(got), len(payload))
	}
}

func TestDirectAddrsExchangeAndListDevicesHydration(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 0)
	b := newRealStoreTestNode(t, net, "NodeB", 0)

	// Advertise distinct LAN addresses on A and B.
	a.tr.mu.Lock()
	a.tr.localDirectAddrs = []string{"192.168.1.10:19840"}
	a.tr.mu.Unlock()
	b.tr.mu.Lock()
	b.tr.localDirectAddrs = []string{"192.168.1.20:19840"}
	b.tr.mu.Unlock()

	code, _, err := a.svc.CreatePairingCode(ctx, 60)
	if err != nil {
		t.Fatalf("A.CreatePairingCode: %v", err)
	}
	if err := b.svc.JoinWithPairingCode(ctx, code); err != nil {
		t.Fatalf("B.JoinWithPairingCode: %v", err)
	}

	aID := a.svc.Identity().DeviceID
	bID := b.svc.Identity().DeviceID

	// Wait for Hello exchange to learn each peer's DirectAddrs and push them to transport.ConfigureDirect.
	waitUntil(t, 4*time.Second, "A learns B LAN addr", func() bool {
		a.tr.mu.RLock()
		defer a.tr.mu.RUnlock()
		addrs := a.tr.directLearned[bID]
		return len(addrs) == 1 && addrs[0] == "192.168.1.20:19840"
	})
	waitUntil(t, 4*time.Second, "B learns A LAN addr", func() bool {
		b.tr.mu.RLock()
		defer b.tr.mu.RUnlock()
		addrs := b.tr.directLearned[aID]
		return len(addrs) == 1 && addrs[0] == "192.168.1.10:19840"
	})

	// Verify ListDevices hydrates DirectAddrs for both self and peer without affecting signed RouteRecord.
	devsA, err := a.svc.ListDevices(false)
	if err != nil {
		t.Fatalf("A.ListDevices: %v", err)
	}
	var sawSelf, sawPeer bool
	for _, dev := range devsA {
		if !model.VerifyRouteRecord(dev.Route) {
			t.Fatalf("RouteRecord signature failed verification for %s", dev.DeviceID)
		}
		if dev.DeviceID == aID {
			sawSelf = true
			if len(dev.DirectAddrs) != 1 || dev.DirectAddrs[0] != "192.168.1.10:19840" {
				t.Fatalf("A self DirectAddrs = %v, want [192.168.1.10:19840]", dev.DirectAddrs)
			}
		}
		if dev.DeviceID == bID {
			sawPeer = true
			if len(dev.DirectAddrs) != 1 || dev.DirectAddrs[0] != "192.168.1.20:19840" {
				t.Fatalf("A peer B DirectAddrs = %v, want [192.168.1.20:19840]", dev.DirectAddrs)
			}
		}
	}
	if !sawSelf || !sawPeer {
		t.Fatalf("expected both self and peer in ListDevices; sawSelf=%v sawPeer=%v", sawSelf, sawPeer)
	}
}

func TestUpdateConfigDirectSettingsValidationAndPersistence(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeA", 0)

	// Default DirectListenPort must be 19840 on startup.
	initCfg := a.svc.Config()
	if initCfg.EffectiveDirectListenPort() != model.DefaultDirectPort {
		t.Fatalf("default EffectiveDirectListenPort = %d, want %d", initCfg.EffectiveDirectListenPort(), model.DefaultDirectPort)
	}
	a.tr.mu.RLock()
	gotInitPort := a.tr.directListenPort
	a.tr.mu.RUnlock()
	if gotInitPort != model.DefaultDirectPort {
		t.Fatalf("transport initial directListenPort = %d, want %d", gotInitPort, model.DefaultDirectPort)
	}

	// Update direct port + custom peer IP (bare IPv4 should normalize with default port 19840).
	cfg := initCfg.Clone()
	cfg.DirectListenPort = 20001
	cfg.PeerDirectAddrs = map[string]string{
		"peer-lan-1": "192.168.1.99",
		"peer-lan-2": "[fd00::5]:21000",
	}
	if err := a.svc.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateConfig valid direct settings failed: %v", err)
	}

	persisted := a.svc.Config()
	if persisted.DirectListenPort != 20001 {
		t.Fatalf("persisted DirectListenPort = %d, want 20001", persisted.DirectListenPort)
	}
	if persisted.PeerDirectAddrs["peer-lan-1"] != "192.168.1.99:19840" {
		t.Fatalf("peer-lan-1 = %q, want 192.168.1.99:19840", persisted.PeerDirectAddrs["peer-lan-1"])
	}
	if persisted.PeerDirectAddrs["peer-lan-2"] != "[fd00::5]:21000" {
		t.Fatalf("peer-lan-2 = %q, want [fd00::5]:21000", persisted.PeerDirectAddrs["peer-lan-2"])
	}

	a.tr.mu.RLock()
	gotPort := a.tr.directListenPort
	gotOverride1 := a.tr.directOverrides["peer-lan-1"]
	a.tr.mu.RUnlock()
	if gotPort != 20001 || gotOverride1 != "192.168.1.99:19840" {
		t.Fatalf("transport ConfigureDirect not hot-applied: port=%d override1=%q", gotPort, gotOverride1)
	}

	// Disabling direct listener must pass port 0 to transport.
	cfgDisable := persisted.Clone()
	cfgDisable.DirectListenDisabled = true
	if err := a.svc.UpdateConfig(ctx, cfgDisable); err != nil {
		t.Fatalf("UpdateConfig disable direct failed: %v", err)
	}
	a.tr.mu.RLock()
	gotDisabledPort := a.tr.directListenPort
	a.tr.mu.RUnlock()
	if gotDisabledPort != 0 {
		t.Fatalf("expected directListenPort == 0 when disabled, got %d", gotDisabledPort)
	}

	// Reject invalid direct port & invalid custom peer address without corrupting saved config.
	badPortCfg := persisted.Clone()
	badPortCfg.DirectListenPort = 70000
	if err := a.svc.UpdateConfig(ctx, badPortCfg); err == nil {
		t.Fatalf("expected error for DirectListenPort=70000")
	}

	badAddrCfg := persisted.Clone()
	badAddrCfg.PeerDirectAddrs["peer-lan-1"] = "http://192.168.1.99"
	if err := a.svc.UpdateConfig(ctx, badAddrCfg); err == nil {
		t.Fatalf("expected error for invalid custom peer address with scheme")
	}
}

func TestUpdateConfigThemePersistenceAndNormalization(t *testing.T) {
	net := newMemNetwork()
	ctx := context.Background()

	a := newRealStoreTestNode(t, net, "NodeTheme", 0)

	// Default theme must be "auto".
	if got := a.svc.Config().Theme; got != model.ThemeAuto {
		t.Fatalf("default Theme = %q, want %q", got, model.ThemeAuto)
	}

	// Persisting dark theme.
	cfgDark := a.svc.Config().Clone()
	cfgDark.Theme = model.ThemeDark
	if err := a.svc.UpdateConfig(ctx, cfgDark); err != nil {
		t.Fatalf("UpdateConfig(dark): %v", err)
	}
	if got := a.svc.Config().Theme; got != model.ThemeDark {
		t.Fatalf("after dark update, Theme = %q, want %q", got, model.ThemeDark)
	}

	// Updating unrelated field with empty Theme must preserve previous Theme.
	cfgEmptyTheme := a.svc.Config().Clone()
	cfgEmptyTheme.Theme = ""
	cfgEmptyTheme.DeviceName = "Renamed Node"
	if err := a.svc.UpdateConfig(ctx, cfgEmptyTheme); err != nil {
		t.Fatalf("UpdateConfig(preserve theme): %v", err)
	}
	if got := a.svc.Config().Theme; got != model.ThemeDark {
		t.Fatalf("empty Theme update clobbered persisted theme: got %q, want %q", got, model.ThemeDark)
	}

	// Switching to light theme with whitespace/casing normalizes cleanly.
	cfgLight := a.svc.Config().Clone()
	cfgLight.Theme = "  LIGHT "
	if err := a.svc.UpdateConfig(ctx, cfgLight); err != nil {
		t.Fatalf("UpdateConfig(light): %v", err)
	}
	if got := a.svc.Config().Theme; got != model.ThemeLight {
		t.Fatalf("normalized light Theme = %q, want %q", got, model.ThemeLight)
	}

	// Unknown theme value normalizes safely back to "auto".
	cfgUnknown := a.svc.Config().Clone()
	cfgUnknown.Theme = "solarized"
	if err := a.svc.UpdateConfig(ctx, cfgUnknown); err != nil {
		t.Fatalf("UpdateConfig(unknown): %v", err)
	}
	if got := a.svc.Config().Theme; got != model.ThemeAuto {
		t.Fatalf("unknown Theme = %q, want %q", got, model.ThemeAuto)
	}
}
