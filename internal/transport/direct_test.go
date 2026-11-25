package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"tailchat/internal/model"
)

func TestNormalizeDirectAddr(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		defPort int
		want    string
		wantErr bool
	}{
		{name: "empty", raw: "   ", defPort: 19840, want: ""},
		{name: "ipv4_default_port", raw: "192.168.1.50", defPort: 19840, want: "192.168.1.50:19840"},
		{name: "ipv4_explicit_port", raw: " 192.168.1.50:20001 ", defPort: 19840, want: "192.168.1.50:20001"},
		{name: "ipv6_bare", raw: "fd00::1234", defPort: 19840, want: "[fd00::1234]:19840"},
		{name: "ipv6_bracketed_no_port", raw: "[fd00::1234]", defPort: 19840, want: "[fd00::1234]:19840"},
		{name: "ipv6_bracketed_with_port", raw: "[fd00::1234]:21000", defPort: 19840, want: "[fd00::1234]:21000"},
		{name: "hostname_default_port", raw: "mac-mini.local", defPort: 19840, want: "mac-mini.local:19840"},
		{name: "hostname_with_port", raw: "nas.lan:19999", defPort: 19840, want: "nas.lan:19999"},
		{name: "reject_scheme", raw: "http://192.168.1.50:19840", defPort: 19840, wantErr: true},
		{name: "reject_path", raw: "192.168.1.50/24", defPort: 19840, wantErr: true},
		{name: "reject_port_zero", raw: "192.168.1.50:0", defPort: 19840, wantErr: true},
		{name: "reject_port_overflow", raw: "192.168.1.50:70000", defPort: 19840, wantErr: true},
		{name: "reject_port_alpha", raw: "192.168.1.50:abc", defPort: 19840, wantErr: true},
		{name: "reject_whitespace_inside", raw: "192.168.1.50 19840", defPort: 19840, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.NormalizeDirectAddr(tc.raw, tc.defPort)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeDirectAddr(%q) succeeded with %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeDirectAddr(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeDirectAddr(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFilterDirectIPs(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast
	ifaces := []ifaceAddrInfo{
		{
			name:  "lo0",
			flags: net.FlagUp | net.FlagLoopback,
			addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")},
		},
		{
			name:  "utun4",
			flags: net.FlagUp | net.FlagPointToPoint,
			addrs: []netip.Addr{netip.MustParseAddr("198.18.0.1"), netip.MustParseAddr("100.64.12.34")},
		},
		{
			name:  "docker0",
			flags: up,
			addrs: []netip.Addr{netip.MustParseAddr("172.17.0.1")},
		},
		{
			name:  "awdl0",
			flags: up,
			addrs: []netip.Addr{netip.MustParseAddr("fe80::1")},
		},
		{
			name:  "en1",
			flags: 0, // Down interface must be ignored
			addrs: []netip.Addr{netip.MustParseAddr("192.168.99.99")},
		},
		{
			name:  "en0",
			flags: up,
			addrs: []netip.Addr{
				netip.MustParseAddr("fe80::abcd"),      // link-local -> excluded
				netip.MustParseAddr("198.18.1.2"),      // proxy fake-IP -> excluded
				netip.MustParseAddr("100.100.100.100"), // CGNAT -> excluded
				netip.MustParseAddr("fd12:3456::1"),    // IPv6 ULA -> rank 5
				netip.MustParseAddr("172.20.10.2"),     // RFC1918 172/12 -> rank 3
				netip.MustParseAddr("10.0.0.15"),       // RFC1918 10/8 -> rank 2
				netip.MustParseAddr("192.168.1.88"),    // RFC1918 192.168/16 -> rank 1
				netip.MustParseAddr("192.168.1.88"),    // duplicate -> deduped
			},
		},
	}

	got := filterDirectIPs(ifaces, 19840)
	want := []string{
		"192.168.1.88:19840",
		"10.0.0.15:19840",
		"172.20.10.2:19840",
		"[fd12:3456::1]:19840",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterDirectIPs = %v, want %v", got, want)
	}

	if empty := filterDirectIPs(ifaces, 0); len(empty) != 0 {
		t.Fatalf("expected empty list for port 0, got %v", empty)
	}
}

// startDirectOnlyMesh initializes a TailcatMesh strictly for direct TCP+mTLS tests
// without connecting to public DERP relays, binding a loopback ephemeral listener when listen=true.
func startDirectOnlyMesh(t *testing.T, id model.LocalIdentity, listen bool) (*TailcatMesh, string) {
	t.Helper()
	m := NewTailcatMesh(TailcatOptions{})
	cert, err := buildIdentityTLSCert(id)
	if err != nil {
		t.Fatalf("buildIdentityTLSCert: %v", err)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.id = id
	m.started = true
	m.rootCtx = rootCtx
	m.rootCancel = cancel
	m.directCert = cert
	m.mu.Unlock()

	var listenAddr string
	if listen {
		ln, actualPort, err := m.openDirectListener("127.0.0.1:0")
		if err != nil {
			cancel()
			t.Fatalf("openDirectListener: %v", err)
		}
		listenAddr = ln.Addr().String()
		m.mu.Lock()
		m.directListener = ln
		m.directListenPort = actualPort
		m.directActualPort = actualPort
		m.mu.Unlock()
		if !m.goSpawn(func() { m.directAcceptLoop(ln) }) {
			_ = ln.Close()
			cancel()
			t.Fatalf("failed to spawn directAcceptLoop")
		}
	}

	t.Cleanup(func() {
		_ = m.Close(context.Background())
	})
	return m, listenAddr
}

func makePeerDevice(id model.LocalIdentity) model.Device {
	return model.Device{
		DeviceID:      id.DeviceID,
		Name:          id.DeviceName,
		Platform:      id.Platform,
		SignPublicKey: append([]byte(nil), id.SignPublicKey...),
		RouteEpoch:    1,
		Route: model.RouteRecord{
			DeviceID:      id.DeviceID,
			SignPublicKey: append([]byte(nil), id.SignPublicKey...),
			RouteEpoch:    1,
		},
	}
}

func ensureTestPeer(t *testing.T, m *TailcatMesh, dev model.Device) *peerState {
	t.Helper()
	pub, err := ParseNodePublic(dev.DeviceID)
	if err != nil {
		t.Fatalf("ParseNodePublic(%q): %v", dev.DeviceID, err)
	}
	ps := m.getOrCreatePeer(dev.DeviceID, pub)
	ps.mu.Lock()
	ps.device = dev
	ps.mu.Unlock()
	return ps
}

func waitMeshCondition(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, desc)
}

func TestDirectMTLSConnectionAndBidirectionalStream(t *testing.T) {
	idA, err := GenerateLocalIdentity("node-a", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity A: %v", err)
	}
	idB, err := GenerateLocalIdentity("node-b", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity B: %v", err)
	}
	// Share the same group ID as paired devices do.
	idB.GroupID = idA.GroupID

	// Ensure we explicitly test asymmetric custom IP dialing where the dialer has the LARGER DeviceID.
	dialerID, listenerID := idA, idB
	if dialerID.DeviceID < listenerID.DeviceID {
		dialerID, listenerID = idB, idA
	}

	listenerMesh, listenAddr := startDirectOnlyMesh(t, listenerID, true)
	dialerMesh, _ := startDirectOnlyMesh(t, dialerID, false)

	payload := make([]byte, 256*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	// Fixed-size XOR echo handler on both sides so we can verify bidirectional streams over the single direct mTLS+yamux link.
	installFixedEcho := func(m *TailcatMesh, size int, tag byte) {
		m.SetStreamHandler(func(_ context.Context, _ string, _ model.StreamKind, stream io.ReadWriteCloser) {
			defer stream.Close()
			buf := make([]byte, size)
			if _, err := io.ReadFull(stream, buf); err != nil {
				return
			}
			for i := range buf {
				buf[i] ^= tag
			}
			_, _ = stream.Write(buf)
		})
	}
	installFixedEcho(listenerMesh, len(payload), 0x5A)
	installFixedEcho(dialerMesh, len(payload), 0xA5)

	devDialer := makePeerDevice(dialerID)
	devListener := makePeerDevice(listenerID)

	listenerMesh.SetAllowedPeers([]model.Device{devDialer})
	listenerMesh.SyncPeers([]model.Device{devDialer})

	dialerMesh.SetAllowedPeers([]model.Device{devListener})
	dialerMesh.SyncPeers([]model.Device{devListener})

	// Configure custom override on dialer (even though dialerID > listenerID).
	if err := dialerMesh.ConfigureDirect(0, map[string]string{listenerID.DeviceID: listenAddr}, nil); err != nil {
		t.Fatalf("dialer ConfigureDirect: %v", err)
	}

	// Verify dialer reports ConnPathCustomDirect (configured via override) and listener reports ConnPathLAN (inbound local loopback).
	waitMeshCondition(t, 5*time.Second, "dialer custom direct status", func() bool {
		for _, st := range dialerMesh.PeerStatuses() {
			if st.DeviceID == listenerID.DeviceID && st.Connected && st.PathKind == model.ConnPathCustomDirect && st.Endpoint == listenAddr {
				return true
			}
		}
		return false
	})
	waitMeshCondition(t, 5*time.Second, "listener LAN direct inbound status", func() bool {
		for _, st := range listenerMesh.PeerStatuses() {
			if st.DeviceID == dialerID.DeviceID && st.Connected && st.PathKind == model.ConnPathLAN && st.Endpoint != "" {
				return true
			}
		}
		return false
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Dialer -> Listener: send 256 KiB random payload over direct mTLS yamux stream.
	s1, err := dialerMesh.OpenStream(ctx, listenerID.DeviceID, model.StreamKindControl)
	if err != nil {
		t.Fatalf("OpenStream A->B: %v", err)
	}
	if _, err := s1.Write(payload); err != nil {
		t.Fatalf("write A->B: %v", err)
	}
	resp1 := make([]byte, len(payload))
	if _, err := io.ReadFull(s1, resp1); err != nil {
		t.Fatalf("read A->B: %v", err)
	}
	_ = s1.Close()

	want1 := make([]byte, len(payload))
	for i := range payload {
		want1[i] = payload[i] ^ 0x5A
	}
	if !bytes.Equal(resp1, want1) {
		t.Fatalf("A->B payload mismatch over direct mTLS")
	}

	// 2. Listener -> Dialer (reusing listener's inbound direct yamux session!).
	s2, err := listenerMesh.OpenStream(ctx, dialerID.DeviceID, model.StreamKindChunkData)
	if err != nil {
		t.Fatalf("OpenStream B->A via inbound direct session: %v", err)
	}
	if _, err := s2.Write(payload); err != nil {
		t.Fatalf("write B->A: %v", err)
	}
	resp2 := make([]byte, len(payload))
	if _, err := io.ReadFull(s2, resp2); err != nil {
		t.Fatalf("read B->A: %v", err)
	}
	_ = s2.Close()

	want2 := make([]byte, len(payload))
	for i := range payload {
		want2[i] = payload[i] ^ 0xA5
	}
	if !bytes.Equal(resp2, want2) {
		t.Fatalf("B->A payload mismatch over inbound direct mTLS")
	}
}

func TestDirectMTLSRejectsUnauthorizedWrongKeyWrongGroupAndRevoked(t *testing.T) {
	idServer, err := GenerateLocalIdentity("server", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity server: %v", err)
	}
	idClient, err := GenerateLocalIdentity("client", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity client: %v", err)
	}
	idOther, err := GenerateLocalIdentity("other", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity other: %v", err)
	}

	serverMesh, serverAddr := startDirectOnlyMesh(t, idServer, true)

	// Case 1: Client NOT in server's allowlist (same group).
	idUnlisted := idClient
	idUnlisted.GroupID = idServer.GroupID
	unlistedMesh, _ := startDirectOnlyMesh(t, idUnlisted, false)
	unlistedMesh.SetAllowedPeers([]model.Device{makePeerDevice(idServer)})
	psUnlisted := ensureTestPeer(t, unlistedMesh, makePeerDevice(idServer))
	unlistedMesh.mu.Lock()
	unlistedMesh.peerDirectOverrides = map[string]string{idServer.DeviceID: serverAddr}
	unlistedMesh.mu.Unlock()
	if sess, _, _, _, err := unlistedMesh.dialDirectPeer(context.Background(), psUnlisted); err == nil {
		_ = sess.Close()
		t.Fatalf("expected unlisted peer handshake to fail, but succeeded")
	}

	// Case 2: Peer is in server allowlist, but presents wrong GroupID.
	idWrongGroup := idClient
	idWrongGroup.GroupID = "wrong-group-id"
	serverMesh.SetAllowedPeers([]model.Device{makePeerDevice(idClient)})
	wrongGroupMesh, _ := startDirectOnlyMesh(t, idWrongGroup, false)
	psWrongGroup := ensureTestPeer(t, wrongGroupMesh, makePeerDevice(idServer))
	wrongGroupMesh.mu.Lock()
	wrongGroupMesh.peerDirectOverrides = map[string]string{idServer.DeviceID: serverAddr}
	wrongGroupMesh.mu.Unlock()
	if sess, _, _, _, err := wrongGroupMesh.dialDirectPeer(context.Background(), psWrongGroup); err == nil {
		_ = sess.Close()
		t.Fatalf("expected wrong GroupID peer handshake to fail, but succeeded")
	}

	// Case 3: Client expects wrong Ed25519 signing key for server -> client rejects server cert!
	idValidClient := idClient
	idValidClient.GroupID = idServer.GroupID
	validClientMesh, _ := startDirectOnlyMesh(t, idValidClient, false)
	badServerDev := makePeerDevice(idServer)
	badServerDev.SignPublicKey = append([]byte(nil), idOther.SignPublicKey...)
	badServerDev.Route.SignPublicKey = append([]byte(nil), idOther.SignPublicKey...)
	psBadKey := ensureTestPeer(t, validClientMesh, badServerDev)
	validClientMesh.mu.Lock()
	validClientMesh.peerDirectOverrides = map[string]string{idServer.DeviceID: serverAddr}
	validClientMesh.mu.Unlock()
	if sess, _, _, _, err := validClientMesh.dialDirectPeer(context.Background(), psBadKey); err == nil {
		_ = sess.Close()
		t.Fatalf("expected mismatched Ed25519 key handshake to fail, but succeeded")
	}

	// Case 4: Valid key succeeds (reporting ConnPathCustomDirect), then after RevokePeer server rejects subsequent direct dials.
	goodClientMesh, _ := startDirectOnlyMesh(t, idValidClient, false)
	psGood := ensureTestPeer(t, goodClientMesh, makePeerDevice(idServer))
	goodClientMesh.mu.Lock()
	goodClientMesh.peerDirectOverrides = map[string]string{idServer.DeviceID: serverAddr}
	goodClientMesh.mu.Unlock()
	sess, _, kind, _, err := goodClientMesh.dialDirectPeer(context.Background(), psGood)
	if err != nil {
		t.Fatalf("expected valid direct mTLS handshake to succeed, got: %v", err)
	}
	if kind != model.ConnPathCustomDirect {
		_ = sess.Close()
		t.Fatalf("dialDirectPeer custom kind = %q, want %q", kind, model.ConnPathCustomDirect)
	}
	_ = sess.Close()

	serverMesh.RevokePeer(idValidClient.DeviceID)
	if sess2, _, _, _, err := goodClientMesh.dialDirectPeer(context.Background(), psGood); err == nil {
		_ = sess2.Close()
		t.Fatalf("expected revoked peer handshake to fail, but succeeded")
	}
}

func TestDirectFallbackToSecondCandidateAndUpgrade(t *testing.T) {
	idA, err := GenerateLocalIdentity("node-a", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity A: %v", err)
	}
	idB, err := GenerateLocalIdentity("node-b", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity B: %v", err)
	}
	idB.GroupID = idA.GroupID

	// Reserve a closed local TCP port to simulate an unreachable custom IP that fails fast (ECONNREFUSED).
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	deadAddr := closedLn.Addr().String()
	_ = closedLn.Close()

	serverMesh, liveAddr := startDirectOnlyMesh(t, idB, true)
	clientMesh, _ := startDirectOnlyMesh(t, idA, false)

	serverMesh.SetAllowedPeers([]model.Device{makePeerDevice(idA)})
	clientMesh.SetAllowedPeers([]model.Device{makePeerDevice(idB)})
	ps := ensureTestPeer(t, clientMesh, makePeerDevice(idB))

	// Custom override points to deadAddr; learned LAN list contains liveAddr.
	// dialDirectPeer must fail fast on deadAddr and succeed on liveAddr as ConnPathLAN.
	clientMesh.mu.Lock()
	clientMesh.peerDirectOverrides = map[string]string{idB.DeviceID: deadAddr}
	clientMesh.peerLearnedDirect = map[string][]string{idB.DeviceID: {liveAddr}}
	clientMesh.mu.Unlock()

	start := time.Now()
	sess, chosenAddr, chosenKind, _, err := clientMesh.dialDirectPeer(context.Background(), ps)
	if err != nil {
		t.Fatalf("dialDirectPeer fallback failed: %v", err)
	}
	_ = sess.Close()
	if chosenAddr != liveAddr {
		t.Fatalf("chosenAddr = %q, want fallback %q", chosenAddr, liveAddr)
	}
	if chosenKind != model.ConnPathLAN {
		t.Fatalf("chosenKind = %q, want %q for learned LAN fallback", chosenKind, model.ConnPathLAN)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fallback took too long (%v), expected fast failure on closed port", elapsed)
	}

	// Verify tryUpgradeToDirect promotes peer to ConnPathLAN.
	clientMesh.tryUpgradeToDirect(context.Background(), ps, true)
	waitMeshCondition(t, 3*time.Second, "tryUpgradeToDirect promotes peer to ConnPathLAN", func() bool {
		for _, st := range clientMesh.PeerStatuses() {
			if st.DeviceID == idB.DeviceID && st.Connected && st.PathKind == model.ConnPathLAN && st.Endpoint == liveAddr {
				return true
			}
		}
		return false
	})
}

func TestSetGroupIdentityRebuildsDirectCertWithoutRestart(t *testing.T) {
	idMac, err := GenerateLocalIdentity("macos-host", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity mac: %v", err)
	}
	// Fresh Windows install starts with its own distinct initial GroupID before pairing.
	idWin, err := GenerateLocalIdentity("win-joiner", "windows")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity win: %v", err)
	}
	if idWin.GroupID == idMac.GroupID {
		t.Fatalf("expected distinct initial GroupIDs before pairing")
	}

	macMesh, macAddr := startDirectOnlyMesh(t, idMac, true)
	winMesh, winAddr := startDirectOnlyMesh(t, idWin, true)

	macMesh.SetAllowedPeers([]model.Device{makePeerDevice(idWin)})
	winMesh.SetAllowedPeers([]model.Device{makePeerDevice(idMac)})
	psMacOnWin := ensureTestPeer(t, winMesh, makePeerDevice(idMac))
	psWinOnMac := ensureTestPeer(t, macMesh, makePeerDevice(idWin))

	winMesh.mu.Lock()
	winMesh.peerDirectOverrides = map[string]string{idMac.DeviceID: macAddr}
	winMesh.mu.Unlock()
	macMesh.mu.Lock()
	macMesh.peerLearnedDirect = map[string][]string{idWin.DeviceID: {winAddr}}
	macMesh.mu.Unlock()

	// Before adopting macOS's GroupID, direct mTLS must reject with group mismatch.
	if sess, _, _, _, err := winMesh.dialDirectPeer(context.Background(), psMacOnWin); err == nil {
		_ = sess.Close()
		t.Fatalf("expected direct mTLS to fail before SetGroupIdentity")
	}

	// Simulate JoinWithPairingCode adopting macOS's GroupID + root secret WITHOUT restart.
	winMesh.SetGroupIdentity(idMac.GroupID, idMac.GroupPrivateKey)

	// Outbound Windows -> macOS must now succeed immediately as ConnPathCustomDirect.
	sessOut, _, kindOut, _, err := winMesh.dialDirectPeer(context.Background(), psMacOnWin)
	if err != nil {
		t.Fatalf("expected outbound direct mTLS to succeed after SetGroupIdentity, got: %v", err)
	}
	_ = sessOut.Close()
	if kindOut != model.ConnPathCustomDirect {
		t.Fatalf("kindOut = %q, want %q", kindOut, model.ConnPathCustomDirect)
	}

	// Inbound macOS -> Windows must also succeed immediately because Windows rebuilt its server directCert.
	sessIn, _, kindIn, _, err := macMesh.dialDirectPeer(context.Background(), psWinOnMac)
	if err != nil {
		t.Fatalf("expected inbound direct mTLS to Windows to succeed after SetGroupIdentity, got: %v", err)
	}
	_ = sessIn.Close()
	if kindIn != model.ConnPathLAN {
		t.Fatalf("kindIn = %q, want %q", kindIn, model.ConnPathLAN)
	}
}

func TestConfigureDirectUpgradesExistingInboundSessionOnCustomIP(t *testing.T) {
	// Generate two identities and assign idHigh > idLow so the joining peer starts with dialActive=false.
	id1, err := GenerateLocalIdentity("node-1", "macos")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity 1: %v", err)
	}
	id2, err := GenerateLocalIdentity("node-2", "windows")
	if err != nil {
		t.Fatalf("GenerateLocalIdentity 2: %v", err)
	}
	idLow, idHigh := id1, id2
	if idLow.DeviceID > idHigh.DeviceID {
		idLow, idHigh = id2, id1
	}
	idHigh.GroupID = idLow.GroupID

	macMesh, macAddr := startDirectOnlyMesh(t, idLow, true)
	winMesh, _ := startDirectOnlyMesh(t, idHigh, false)

	macMesh.SetAllowedPeers([]model.Device{makePeerDevice(idHigh)})
	winMesh.SetAllowedPeers([]model.Device{makePeerDevice(idLow)})
	ps := ensureTestPeer(t, winMesh, makePeerDevice(idLow))

	// Simulate an already-active non-direct Tailcat inbound session (e.g. P2P UDP) with dialActive=false.
	c1, c2 := net.Pipe()
	t.Cleanup(func() { _ = c1.Close(); _ = c2.Close() })
	fakeInSess, err := yamux.Server(c1, newYamuxConfig())
	if err != nil {
		t.Fatalf("yamux.Server pipe: %v", err)
	}
	t.Cleanup(func() { _ = fakeInSess.Close() })
	fakeRemoteSess, err := yamux.Client(c2, newYamuxConfig())
	if err != nil {
		t.Fatalf("yamux.Client pipe: %v", err)
	}
	t.Cleanup(func() { _ = fakeRemoteSess.Close() })

	ps.mu.Lock()
	ps.inSession = fakeInSess
	ps.inIsDirect = false
	ps.dialActive = false
	ps.status = model.PeerConnStatus{
		DeviceID:  idLow.DeviceID,
		Connected: true,
		PathKind:  model.ConnPathDirect,
		Endpoint:  "192.168.1.10:41641",
	}
	ps.mu.Unlock()

	// User manually configures macOS's LAN IP on Windows -> must immediately dial and upgrade to ConnPathCustomDirect
	// without waiting for the existing Tailcat inbound session to close or restarting the app.
	if err := winMesh.ConfigureDirect(0, map[string]string{idLow.DeviceID: macAddr}, nil); err != nil {
		t.Fatalf("ConfigureDirect: %v", err)
	}

	waitMeshCondition(t, 4*time.Second, "ConfigureDirect upgrades existing inbound Tailcat session to ConnPathCustomDirect", func() bool {
		for _, st := range winMesh.PeerStatuses() {
			if st.DeviceID == idLow.DeviceID && st.Connected && st.PathKind == model.ConnPathCustomDirect && st.Endpoint == macAddr {
				return true
			}
		}
		return false
	})
}

