package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"

	"tailchat/internal/model"
)

func TestGenerateLocalIdentityAndKeyParsing(t *testing.T) {
	id, err := GenerateLocalIdentity("macbook-pro", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity failed: %v", err)
	}

	if id.DeviceName != "macbook-pro" {
		t.Fatalf("DeviceName = %q, want %q", id.DeviceName, "macbook-pro")
	}
	if id.Platform != "macos" {
		t.Fatalf("Platform = %q, want %q", id.Platform, "macos")
	}
	if id.GroupID == "" {
		t.Fatal("expected non-empty GroupID")
	}
	if id.RouteEpoch != 1 {
		t.Fatalf("RouteEpoch = %d, want 1", id.RouteEpoch)
	}
	if len(id.SignPublicKey) != ed25519.PublicKeySize || len(id.SignPrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("unexpected device Ed25519 key sizes: pub=%d priv=%d", len(id.SignPublicKey), len(id.SignPrivateKey))
	}
	if len(id.GroupPublicKey) != ed25519.PublicKeySize || len(id.GroupPrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("unexpected group Ed25519 key sizes: pub=%d priv=%d", len(id.GroupPublicKey), len(id.GroupPrivateKey))
	}

	priv, err := ParseNodePrivate(id.NodePrivateKey)
	if err != nil {
		t.Fatalf("ParseNodePrivate failed: %v", err)
	}
	pub, err := ParseNodePublic(id.DeviceID)
	if err != nil {
		t.Fatalf("ParseNodePublic failed: %v", err)
	}
	if priv.Public() != pub {
		t.Fatalf("derived public key %v != parsed DeviceID %v", priv.Public(), pub)
	}
	if FormatNodePublic(pub) != id.DeviceID {
		t.Fatalf("FormatNodePublic mismatch: %q vs %q", FormatNodePublic(pub), id.DeviceID)
	}

	psk, err := ParsePresharedKey(id.PresharedKey)
	if err != nil {
		t.Fatalf("ParsePresharedKey failed: %v", err)
	}
	if psk.IsZero() {
		t.Fatal("expected non-zero PresharedKey")
	}

	// Verify Ed25519 signing keys work with model signing helpers.
	rec := model.RouteRecord{
		DeviceID:      id.DeviceID,
		SignPublicKey: id.SignPublicKey,
		TailcatAddr:   "tc-test",
		DERP:          model.DefaultDERPConfig(),
		RouteEpoch:    1,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := model.SignRouteRecord(&rec, ed25519.PrivateKey(id.SignPrivateKey)); err != nil {
		t.Fatalf("SignRouteRecord failed: %v", err)
	}
	if !model.VerifyRouteRecord(rec) {
		t.Fatal("VerifyRouteRecord returned false")
	}

	// Error cases for key parsers.
	if _, err := ParseNodePrivate(""); err == nil {
		t.Fatal("expected error for empty NodePrivate")
	}
	if _, err := ParseNodePrivate("not-a-key"); err == nil {
		t.Fatal("expected error for invalid NodePrivate")
	}
	if _, err := ParseNodePublic(""); err == nil {
		t.Fatal("expected error for empty NodePublic")
	}
	if _, err := ParseNodePublic("not-a-key"); err == nil {
		t.Fatal("expected error for invalid NodePublic")
	}
	if _, err := ParsePresharedKey(""); err == nil {
		t.Fatal("expected error for empty PresharedKey")
	}
	if _, err := ParsePresharedKey("not-a-psk"); err == nil {
		t.Fatal("expected error for invalid PresharedKey")
	}
}

func TestBuildServerDERPConfig(t *testing.T) {
	t.Run("default_mode_fallback_region", func(t *testing.T) {
		rid, region, mapURL, err := BuildServerDERPConfig(model.DERPConfig{Mode: model.DERPModeDefault})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rid != 304 {
			t.Fatalf("regionID = %d, want 304", rid)
		}
		if region != nil {
			t.Fatalf("expected nil inline region, got %+v", region)
		}
		if mapURL != "" {
			t.Fatalf("mapURL = %q, want empty", mapURL)
		}
	})

	t.Run("default_mode_custom_region_id", func(t *testing.T) {
		rid, region, mapURL, err := BuildServerDERPConfig(model.DERPConfig{
			Mode:     model.DERPModeDefault,
			RegionID: 301,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rid != 301 || region != nil || mapURL != "" {
			t.Fatalf("got (%d, %v, %q), want (301, nil, \"\")", rid, region, mapURL)
		}
	})

	t.Run("custom_inline_region", func(t *testing.T) {
		rid, region, mapURL, err := BuildServerDERPConfig(model.DERPConfig{
			Mode:             model.DERPModeCustomRegion,
			RegionID:         905,
			CustomHost:       "derp.example.com",
			CustomDERPPort:   8443,
			CustomSTUNPort:   3479,
			CustomRegionName: "Home VPS",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rid != 905 {
			t.Fatalf("regionID = %d, want 905", rid)
		}
		if mapURL != "" {
			t.Fatalf("mapURL = %q, want empty", mapURL)
		}
		if region == nil || len(region.Nodes) != 1 {
			t.Fatalf("expected inline region with 1 node, got %+v", region)
		}
		if region.RegionID != 905 || region.RegionCode != "custom" || region.RegionName != "Home VPS" {
			t.Fatalf("unexpected region metadata: %+v", region)
		}
		node := region.Nodes[0]
		if node.Name != "custom-1" || node.RegionID != 905 || node.HostName != "derp.example.com" || node.DERPPort != 8443 || node.STUNPort != 3479 {
			t.Fatalf("unexpected DERPNode: %+v", node)
		}
	})

	t.Run("custom_inline_region_defaults_and_validation", func(t *testing.T) {
		rid, region, _, err := BuildServerDERPConfig(model.DERPConfig{
			Mode:       model.DERPModeCustomRegion,
			CustomHost: "derp.internal",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rid != 900 || region.Nodes[0].DERPPort != 443 || region.Nodes[0].STUNPort != 3478 {
			t.Fatalf("unexpected defaults: rid=%d node=%+v", rid, region.Nodes[0])
		}

		// Negative STUNPort (-1 disables STUN) should be preserved.
		_, regionNoSTUN, _, err := BuildServerDERPConfig(model.DERPConfig{
			Mode:           model.DERPModeCustomRegion,
			CustomHost:     "derp.internal",
			CustomSTUNPort: -1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if regionNoSTUN.Nodes[0].STUNPort != -1 {
			t.Fatalf("STUNPort = %d, want -1", regionNoSTUN.Nodes[0].STUNPort)
		}

		// Empty CustomHost must error.
		if _, _, _, err := BuildServerDERPConfig(model.DERPConfig{Mode: model.DERPModeCustomRegion}); err == nil {
			t.Fatal("expected error for empty CustomHost")
		}
	})

	t.Run("custom_map_url", func(t *testing.T) {
		rid, region, mapURL, err := BuildServerDERPConfig(model.DERPConfig{
			Mode:       model.DERPModeCustomMapURL,
			RegionID:   910,
			DERPMapURL: "https://example.com/derpmap.json",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rid != 910 || region != nil || mapURL != "https://example.com/derpmap.json" {
			t.Fatalf("got (%d, %v, %q)", rid, region, mapURL)
		}

		if _, _, _, err := BuildServerDERPConfig(model.DERPConfig{Mode: model.DERPModeCustomMapURL}); err == nil {
			t.Fatal("expected error for empty DERPMapURL")
		}
		if _, _, _, err := BuildServerDERPConfig(model.DERPConfig{
			Mode:       model.DERPModeCustomMapURL,
			DERPMapURL: "ftp://invalid",
		}); err == nil {
			t.Fatal("expected error for non-http(s) DERPMapURL")
		}
	})

	t.Run("unsupported_mode", func(t *testing.T) {
		if _, _, _, err := BuildServerDERPConfig(model.DERPConfig{Mode: "bogus"}); err == nil {
			t.Fatal("expected error for unsupported mode")
		}
	})

	t.Run("format_server_addr_guards", func(t *testing.T) {
		if _, err := FormatServerAddr(nil, model.DefaultDERPConfig()); err == nil {
			t.Fatal("expected error for nil server")
		}
		// Verify ConnInfo compact encoding round-trip (RegionID-only vs embedded Region).
		priv := tailcat.NewPresharedKey()
		ci := tailcat.ConnInfo{
			PresharedKey: priv,
			RegionID:     tailcfg.DERPRegionID(304),
		}
		compact := ci.Addr()
		parsed, err := tailcat.ParseAddr(compact)
		if err != nil {
			t.Fatalf("ParseAddr failed: %v", err)
		}
		if parsed.RegionID != 304 || len(parsed.Region) != 0 {
			t.Fatalf("unexpected parsed compact ConnInfo: RegionID=%d len(Region)=%d", parsed.RegionID, len(parsed.Region))
		}
	})
}

type memNodeFixture struct {
	id    model.LocalIdentity
	tr    *MemTransport
	route model.RouteRecord
}

func newMemNode(t *testing.T, net *MemNetwork, name string) *memNodeFixture {
	t.Helper()
	id, err := GenerateLocalIdentity(name, "linux")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity(%s): %v", name, err)
	}
	tr := net.NewTransport()
	route, err := tr.Start(context.Background(), id, model.DefaultDERPConfig())
	if err != nil {
		t.Fatalf("Start(%s): %v", name, err)
	}
	if !model.VerifyRouteRecord(route) {
		t.Fatalf("initial RouteRecord for %s failed signature verification", name)
	}
	t.Cleanup(func() {
		_ = tr.Close(context.Background())
	})
	return &memNodeFixture{id: id, tr: tr, route: route}
}

func (f *memNodeFixture) asDevice() model.Device {
	return model.Device{
		DeviceID:      f.id.DeviceID,
		Name:          f.id.DeviceName,
		Platform:      f.id.Platform,
		SignPublicKey: f.id.SignPublicKey,
		Route:         f.tr.CurrentRoute(),
		RouteEpoch:    f.tr.CurrentRoute().RouteEpoch,
	}
}

func waitForConnected(t *testing.T, tr model.PeerTransportProvider, peerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, st := range tr.PeerStatuses() {
			if st.DeviceID == peerID && st.Connected {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for peer %s to become connected (statuses: %+v)", peerID, tr.PeerStatuses())
}

func waitForDisconnected(t *testing.T, tr model.PeerTransportProvider, peerID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, st := range tr.PeerStatuses() {
			if st.DeviceID == peerID && !st.Connected && st.PathKind == model.ConnPathDisconnected {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for peer %s to become disconnected (statuses: %+v)", peerID, tr.PeerStatuses())
}

func TestMemNetworkBidirectionalMultiplexing(t *testing.T) {
	net := NewMemNetwork()
	a := newMemNode(t, net, "alpha")
	b := newMemNode(t, net, "beta")
	c := newMemNode(t, net, "gamma")

	nodes := []*memNodeFixture{a, b, c}

	// Echo handler that prepends "kind=<k>:" to whatever was received.
	for _, n := range nodes {
		node := n
		node.tr.SetStreamHandler(func(ctx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
			defer stream.Close()
			data, err := io.ReadAll(stream)
			if err != nil {
				return
			}
			reply := fmt.Sprintf("to=%s from=%s kind=%d payload=%s", node.id.DeviceName, peerDeviceID, kind, string(data))
			_, _ = stream.Write([]byte(reply))
		})
	}

	allDevices := []model.Device{a.asDevice(), b.asDevice(), c.asDevice()}
	for _, n := range nodes {
		n.tr.SetAllowedPeers(allDevices)
		n.tr.SyncPeers(allDevices)
	}

	// Wait for full mesh connectivity.
	for _, src := range nodes {
		for _, dst := range nodes {
			if src == dst {
				continue
			}
			waitForConnected(t, src.tr, dst.id.DeviceID, 3*time.Second)
		}
	}

	// Exercise concurrent bidirectional Control and ChunkData streams across every ordered pair.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(nodes)*len(nodes)*2)

	for _, src := range nodes {
		for _, dst := range nodes {
			if src == dst {
				continue
			}
			for _, kind := range []model.StreamKind{model.StreamKindControl, model.StreamKindChunkData} {
				sNode, dNode, k := src, dst, kind
				wg.Add(1)
				go func() {
					defer wg.Done()
					st, err := sNode.tr.OpenStream(ctx, dNode.id.DeviceID, k)
					if err != nil {
						errCh <- fmt.Errorf("%s -> %s (kind %d) OpenStream: %w", sNode.id.DeviceName, dNode.id.DeviceName, k, err)
						return
					}
					payload := fmt.Sprintf("hello-%s-to-%s-%d", sNode.id.DeviceName, dNode.id.DeviceName, k)
					if _, err := st.Write([]byte(payload)); err != nil {
						_ = st.Close()
						errCh <- fmt.Errorf("write: %w", err)
						return
					}
					// Half-close or signal EOF? yamux.Stream.Close closes both directions,
					// so use a dedicated ping-pong protocol in a separate test or read exact length.
					// Wait: our echo handler used io.ReadAll which waits for EOF! Let's check below.
					_ = st.Close()
				}()
			}
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Now test full request/response payload verification with framed length prefix on both StreamKindControl and StreamKindChunkData.
	framedHandler := func(nodeName string) model.PeerStreamHandler {
		return func(ctx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
			defer stream.Close()
			var lenBuf [4]byte
			if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
				return
			}
			n := int(lenBuf[0])<<24 | int(lenBuf[1])<<16 | int(lenBuf[2])<<8 | int(lenBuf[3])
			body := make([]byte, n)
			if _, err := io.ReadFull(stream, body); err != nil {
				return
			}
			resp := []byte(fmt.Sprintf("%s|from=%s|kind=%d|len=%d|%s", nodeName, peerDeviceID, kind, n, string(body)))
			outLen := [4]byte{byte(len(resp) >> 24), byte(len(resp) >> 16), byte(len(resp) >> 8), byte(len(resp))}
			_, _ = stream.Write(outLen[:])
			_, _ = stream.Write(resp)
		}
	}
	for _, n := range nodes {
		n.tr.SetStreamHandler(framedHandler(n.id.DeviceName))
	}

	roundTrip := func(src, dst *memNodeFixture, kind model.StreamKind, msg []byte) {
		t.Helper()
		st, err := src.tr.OpenStream(ctx, dst.id.DeviceID, kind)
		if err != nil {
			t.Fatalf("%s -> %s OpenStream(kind=%d): %v", src.id.DeviceName, dst.id.DeviceName, kind, err)
		}
		defer st.Close()

		inLen := [4]byte{byte(len(msg) >> 24), byte(len(msg) >> 16), byte(len(msg) >> 8), byte(len(msg))}
		if _, err := st.Write(inLen[:]); err != nil {
			t.Fatalf("write len: %v", err)
		}
		if _, err := st.Write(msg); err != nil {
			t.Fatalf("write payload: %v", err)
		}

		var outLen [4]byte
		if _, err := io.ReadFull(st, outLen[:]); err != nil {
			t.Fatalf("read resp len: %v", err)
		}
		rLen := int(outLen[0])<<24 | int(outLen[1])<<16 | int(outLen[2])<<8 | int(outLen[3])
		got := make([]byte, rLen)
		if _, err := io.ReadFull(st, got); err != nil {
			t.Fatalf("read resp body: %v", err)
		}
		want := fmt.Sprintf("%s|from=%s|kind=%d|len=%d|%s", dst.id.DeviceName, src.id.DeviceID, kind, len(msg), string(msg))
		if string(got) != want {
			t.Fatalf("got %q, want %q", string(got), want)
		}
	}

	// Verify both directions (including larger DeviceID -> smaller DeviceID over shared yamux session).
	chunkPayload := bytes.Repeat([]byte("CHUNKDATA"), 1024)
	for _, src := range nodes {
		for _, dst := range nodes {
			if src == dst {
				continue
			}
			roundTrip(src, dst, model.StreamKindControl, []byte("ping-control"))
			roundTrip(src, dst, model.StreamKindChunkData, chunkPayload)
		}
	}
}

func TestMemNetworkFallbackDialForLargerDeviceID(t *testing.T) {
	net := NewMemNetwork()
	n1 := newMemNode(t, net, "node-1")
	n2 := newMemNode(t, net, "node-2")

	// Ensure `higher` has lexicographically larger DeviceID than `lower`.
	lower, higher := n1, n2
	if lower.id.DeviceID > higher.id.DeviceID {
		lower, higher = higher, lower
	}

	gotCh := make(chan string, 1)
	lower.tr.SetStreamHandler(func(ctx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
		defer stream.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(stream, buf); err == nil {
			gotCh <- fmt.Sprintf("%s:%d:%s", peerDeviceID, kind, string(buf))
		}
	})

	// Allow higher on lower, and give higher lower's route, WITHOUT calling SyncPeers on lower.
	// Because higher.DeviceID > lower.DeviceID, higher does not eagerly background-dial,
	// so OpenStream must trigger the deduplicated fallback dial.
	lower.tr.SetAllowedPeers([]model.Device{higher.asDevice()})
	higher.tr.SetAllowedPeers([]model.Device{lower.asDevice()})
	higher.tr.SyncPeers([]model.Device{lower.asDevice()})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	st, err := higher.tr.OpenStream(ctx, lower.id.DeviceID, model.StreamKindControl)
	if err != nil {
		t.Fatalf("fallback OpenStream failed: %v", err)
	}
	if _, err := st.Write([]byte("wake")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	_ = st.Close()

	select {
	case got := <-gotCh:
		want := fmt.Sprintf("%s:%d:wake", higher.id.DeviceID, model.StreamKindControl)
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for fallback stream on lower node")
	}
}

func TestMemNetworkAllowlistAndRevokePeer(t *testing.T) {
	net := NewMemNetwork()
	host := newMemNode(t, net, "host")
	peer := newMemNode(t, net, "peer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Peer not in host's allowlist must be rejected.
	peer.tr.SetAllowedPeers([]model.Device{host.asDevice()})
	peer.tr.SyncPeers([]model.Device{host.asDevice()})
	if _, err := peer.tr.OpenStream(ctx, host.id.DeviceID, model.StreamKindControl); err == nil {
		t.Fatal("expected un-allowlisted peer dial to fail")
	}

	// 2. Allow peer and establish a live stream.
	streamOpened := make(chan io.ReadWriteCloser, 1)
	host.tr.SetStreamHandler(func(ctx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
		streamOpened <- stream
	})

	var statusMu sync.Mutex
	var statusHistory []model.PeerConnStatus
	host.tr.SetPeerStatusHandler(func(st model.PeerConnStatus) {
		statusMu.Lock()
		statusHistory = append(statusHistory, st)
		statusMu.Unlock()
	})

	host.tr.SetAllowedPeers([]model.Device{peer.asDevice()})
	host.tr.SyncPeers([]model.Device{peer.asDevice()})
	waitForConnected(t, host.tr, peer.id.DeviceID, 3*time.Second)

	clientStream, err := peer.tr.OpenStream(ctx, host.id.DeviceID, model.StreamKindControl)
	if err != nil {
		t.Fatalf("OpenStream after allowlisting failed: %v", err)
	}
	defer clientStream.Close()

	var serverStream io.ReadWriteCloser
	select {
	case serverStream = <-streamOpened:
		defer serverStream.Close()
	case <-ctx.Done():
		t.Fatal("timed out waiting for server stream")
	}

	// 3. Revoke peer on host: active stream must drop immediately and status must become disconnected.
	host.tr.RevokePeer(peer.id.DeviceID)
	waitForDisconnected(t, host.tr, peer.id.DeviceID, 2*time.Second)

	readErrCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := clientStream.Read(buf)
		readErrCh <- err
	}()
	select {
	case err := <-readErrCh:
		if err == nil {
			t.Fatal("expected read error on revoked peer stream")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked peer stream did not close promptly")
	}

	// 4. Subsequent OpenStream attempts from either side must fail.
	if _, err := host.tr.OpenStream(ctx, peer.id.DeviceID, model.StreamKindControl); err == nil {
		t.Fatal("expected OpenStream from host to revoked peer to fail")
	}
	if _, err := peer.tr.OpenStream(ctx, host.id.DeviceID, model.StreamKindControl); err == nil {
		t.Fatal("expected OpenStream from revoked peer to host to be rejected")
	}

	statusMu.Lock()
	sawDisconnected := false
	for _, st := range statusHistory {
		if st.DeviceID == peer.id.DeviceID && st.PathKind == model.ConnPathDisconnected && !st.Connected {
			sawDisconnected = true
			break
		}
	}
	statusMu.Unlock()
	if !sawDisconnected {
		t.Fatal("expected PeerStatusHandler to receive ConnPathDisconnected on revoke")
	}
}

func TestMemNetworkUpdateDERP(t *testing.T) {
	net := NewMemNetwork()
	a := newMemNode(t, net, "alpha")
	b := newMemNode(t, net, "beta")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r1 := a.tr.CurrentRoute()
	if r1.RouteEpoch != 1 || !model.VerifyRouteRecord(r1) {
		t.Fatalf("unexpected initial route: epoch=%d valid=%v", r1.RouteEpoch, model.VerifyRouteRecord(r1))
	}

	// Switch to custom inline DERP region.
	customDERP := model.DERPConfig{
		Mode:             model.DERPModeCustomRegion,
		RegionID:         901,
		CustomHost:       "derp.selfhosted.local",
		CustomDERPPort:   443,
		CustomSTUNPort:   3478,
		CustomRegionName: "Self-Hosted",
	}
	r2, err := a.tr.UpdateDERP(ctx, customDERP)
	if err != nil {
		t.Fatalf("UpdateDERP(custom_region) failed: %v", err)
	}
	if r2.RouteEpoch != 2 {
		t.Fatalf("RouteEpoch = %d, want 2", r2.RouteEpoch)
	}
	if r2.TailcatAddr == r1.TailcatAddr {
		t.Fatal("expected TailcatAddr to change on UpdateDERP")
	}
	if r2.DERP.Mode != model.DERPModeCustomRegion || r2.DERP.CustomHost != "derp.selfhosted.local" {
		t.Fatalf("unexpected DERP config on r2: %+v", r2.DERP)
	}
	if !model.VerifyRouteRecord(r2) {
		t.Fatal("r2 signature verification failed")
	}
	if cur := a.tr.CurrentRoute(); cur.RouteEpoch != 2 || cur.TailcatAddr != r2.TailcatAddr {
		t.Fatalf("CurrentRoute mismatch: %+v", cur)
	}

	// Switch to custom DERP map URL.
	r3, err := a.tr.UpdateDERP(ctx, model.DERPConfig{
		Mode:       model.DERPModeCustomMapURL,
		RegionID:   910,
		DERPMapURL: "https://derp.example.com/map.json",
	})
	if err != nil {
		t.Fatalf("UpdateDERP(custom_map_url) failed: %v", err)
	}
	if r3.RouteEpoch != 3 || !model.VerifyRouteRecord(r3) {
		t.Fatalf("unexpected r3: epoch=%d valid=%v", r3.RouteEpoch, model.VerifyRouteRecord(r3))
	}

	// Verify peer B can connect using A's updated route.
	received := make(chan string, 1)
	a.tr.SetStreamHandler(func(ctx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
		defer stream.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(stream, buf); err == nil {
			received <- string(buf)
		}
	})
	a.tr.SetAllowedPeers([]model.Device{b.asDevice()})
	b.tr.SetAllowedPeers([]model.Device{a.asDevice()})
	a.tr.SyncPeers([]model.Device{b.asDevice()})
	b.tr.SyncPeers([]model.Device{a.asDevice()})

	st, err := b.tr.OpenStream(ctx, a.id.DeviceID, model.StreamKindControl)
	if err != nil {
		t.Fatalf("OpenStream to updated DERP route failed: %v", err)
	}
	_, _ = st.Write([]byte("hello"))
	_ = st.Close()

	select {
	case msg := <-received:
		if msg != "hello" {
			t.Fatalf("got %q, want %q", msg, "hello")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for stream after UpdateDERP")
	}
}

func TestMemNetworkPairingFlow(t *testing.T) {
	netFabric := NewMemNetwork()
	host := newMemNode(t, netFabric, "host")
	joiner := newMemNode(t, netFabric, "joiner")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Note: joiner is NOT in host's mesh allowlist.
	addr, port, err := host.tr.StartPairing(ctx, func(pCtx context.Context, conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 12)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		if string(buf) == "PAIR-SECRET!" {
			_, _ = conn.Write([]byte("WELCOME"))
		}
	})
	if err != nil {
		t.Fatalf("StartPairing failed: %v", err)
	}
	if addr == "" || port == 0 {
		t.Fatalf("unexpected pairing endpoint (%q, %d)", addr, port)
	}

	conn, err := joiner.tr.DialPairing(ctx, addr, port, model.DefaultDERPConfig())
	if err != nil {
		t.Fatalf("DialPairing failed: %v", err)
	}
	if _, err := conn.Write([]byte("PAIR-SECRET!")); err != nil {
		_ = conn.Close()
		t.Fatalf("write pairing secret: %v", err)
	}
	resp := make([]byte, 7)
	if _, err := io.ReadFull(conn, resp); err != nil {
		_ = conn.Close()
		t.Fatalf("read pairing response: %v", err)
	}
	_ = conn.Close()

	if string(resp) != "WELCOME" {
		t.Fatalf("pairing response = %q, want %q", string(resp), "WELCOME")
	}

	// StopPairing must reject further dials.
	if err := host.tr.StopPairing(); err != nil {
		t.Fatalf("StopPairing failed: %v", err)
	}
	if _, err := joiner.tr.DialPairing(ctx, addr, port, model.DefaultDERPConfig()); err == nil {
		t.Fatal("expected DialPairing after StopPairing to fail")
	}
}

func TestLiveTailcatMesh(t *testing.T) {
	if os.Getenv("TAILCHAT_LIVE_TAILCAT_TEST") != "1" {
		t.Skip("set TAILCHAT_LIVE_TAILCAT_TEST=1 to run live Tailcat DERP test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	idA, err := GenerateLocalIdentity("live-a", "linux")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity A: %v", err)
	}
	idB, err := GenerateLocalIdentity("live-b", "linux")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity B: %v", err)
	}
	// Paired devices in the same group share group root credentials.
	idB.GroupID = idA.GroupID
	idB.GroupPublicKey = append([]byte(nil), idA.GroupPublicKey...)
	idB.GroupPrivateKey = append([]byte(nil), idA.GroupPrivateKey...)

	meshA := NewTailcatMesh(TailcatOptions{
		Logf:              t.Logf,
		DialRetryInterval: time.Second,
		PingInterval:      5 * time.Second,
	})
	meshB := NewTailcatMesh(TailcatOptions{
		Logf:              t.Logf,
		DialRetryInterval: time.Second,
		PingInterval:      5 * time.Second,
	})
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = meshA.Close(closeCtx)
		_ = meshB.Close(closeCtx)
	}()

	routeA, err := meshA.Start(ctx, idA, model.DefaultDERPConfig())
	if err != nil {
		t.Fatalf("meshA.Start: %v", err)
	}
	routeB, err := meshB.Start(ctx, idB, model.DefaultDERPConfig())
	if err != nil {
		t.Fatalf("meshB.Start: %v", err)
	}
	if !model.VerifyRouteRecord(routeA) || !model.VerifyRouteRecord(routeB) {
		t.Fatal("live route records failed signature verification")
	}

	received := make(chan string, 1)
	meshB.SetStreamHandler(func(sCtx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
		defer stream.Close()
		buf := make([]byte, 14)
		if _, err := io.ReadFull(stream, buf); err == nil {
			received <- fmt.Sprintf("%s:%d:%s", peerDeviceID, kind, string(buf))
			_, _ = stream.Write([]byte("pong-live-mesh"))
		}
	})

	devA := model.Device{
		DeviceID:      idA.DeviceID,
		Name:          idA.DeviceName,
		Platform:      idA.Platform,
		SignPublicKey: idA.SignPublicKey,
		Route:         routeA,
		RouteEpoch:    routeA.RouteEpoch,
	}
	devB := model.Device{
		DeviceID:      idB.DeviceID,
		Name:          idB.DeviceName,
		Platform:      idB.Platform,
		SignPublicKey: idB.SignPublicKey,
		Route:         routeB,
		RouteEpoch:    routeB.RouteEpoch,
	}

	meshA.SetAllowedPeers([]model.Device{devB})
	meshB.SetAllowedPeers([]model.Device{devA})
	meshA.SyncPeers([]model.Device{devB})
	meshB.SyncPeers([]model.Device{devA})

	stream, err := meshA.OpenStream(ctx, idB.DeviceID, model.StreamKindControl)
	if err != nil {
		t.Fatalf("meshA.OpenStream: %v", err)
	}
	if _, err := stream.Write([]byte("ping-live-mesh")); err != nil {
		_ = stream.Close()
		t.Fatalf("write payload: %v", err)
	}
	reply := make([]byte, 14)
	if _, err := io.ReadFull(stream, reply); err != nil {
		_ = stream.Close()
		t.Fatalf("read reply: %v", err)
	}
	_ = stream.Close()

	if string(reply) != "pong-live-mesh" {
		t.Fatalf("reply = %q, want %q", string(reply), "pong-live-mesh")
	}

	select {
	case got := <-received:
		want := fmt.Sprintf("%s:%d:ping-live-mesh", idA.DeviceID, model.StreamKindControl)
		if got != want {
			t.Fatalf("server received %q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for server receipt")
	}
}
