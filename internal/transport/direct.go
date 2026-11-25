package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/tailscale/tailcat"

	"tailchat/internal/model"
)

const (
	directALPN                 = "tailchat-direct/1"
	directCustomDialTimeout    = 2500 * time.Millisecond
	directLearnedDialTimeout   = 1500 * time.Millisecond
	directHandshakeTimeout     = 5 * time.Second
	directUpgradeCooldown      = 30 * time.Second
	maxLearnedDirectCandidates = 4
	maxAdvertisedDirectAddrs   = 6
)

var (
	prefixBenchmarkNet = netip.MustParsePrefix("198.18.0.0/15") // RFC 2544 / common macOS proxy fake-IP TUN
	prefixCGNAT        = netip.MustParsePrefix("100.64.0.0/10") // Tailscale / carrier-grade NAT
	prefixRFC1918_10   = netip.MustParsePrefix("10.0.0.0/8")
	prefixRFC1918_172  = netip.MustParsePrefix("172.16.0.0/12")
	prefixRFC1918_192  = netip.MustParsePrefix("192.168.0.0/16")
	prefixIPv6ULA      = netip.MustParsePrefix("fc00::/7")
)

var virtualIfacePrefixes = []string{
	"lo", "utun", "tun", "tap", "awdl", "llw", "bridge", "docker", "br-",
	"veth", "virbr", "vmnet", "vboxnet", "anpi", "ap", "p2p", "wg",
	"tailscale", "zt", "clash", "mihomo", "singbox",
}

type directCandidate struct {
	addr     string
	timeout  time.Duration
	isCustom bool
}

type ifaceAddrInfo struct {
	name  string
	flags net.Flags
	addrs []netip.Addr
}

// buildIdentityTLSCert generates an in-memory self-signed Ed25519 X.509 certificate
// bound to the device's permanent DeviceID (CN) and GroupID (O).
func buildIdentityTLSCert(id model.LocalIdentity) (tls.Certificate, error) {
	if len(id.SignPrivateKey) != ed25519.PrivateKeySize {
		return tls.Certificate{}, errors.New("direct tls: invalid ed25519 private key length")
	}
	priv := ed25519.PrivateKey(id.SignPrivateKey)
	pub := priv.Public().(ed25519.PublicKey)

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("direct tls: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   id.DeviceID,
			Organization: []string{id.GroupID},
		},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("direct tls: create certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("direct tls: parse certificate: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}, nil
}

func isVirtualInterfaceName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return true
	}
	for _, prefix := range virtualIfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func directIPRank(addr netip.Addr) (int, bool) {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return 0, false
	}
	if addr.Is4() {
		if prefixBenchmarkNet.Contains(addr) || prefixCGNAT.Contains(addr) {
			return 0, false
		}
		if prefixRFC1918_192.Contains(addr) {
			return 1, true
		}
		if prefixRFC1918_10.Contains(addr) {
			return 2, true
		}
		if prefixRFC1918_172.Contains(addr) {
			return 3, true
		}
		return 4, true
	}
	if addr.Is6() && prefixIPv6ULA.Contains(addr) {
		return 5, true
	}
	return 0, false
}

func filterDirectIPs(ifaces []ifaceAddrInfo, port int) []string {
	if port <= 0 || port > 65535 {
		return nil
	}
	type rankedAddr struct {
		rank int
		addr string
	}
	seen := make(map[string]struct{})
	var ranked []rankedAddr

	for _, iface := range ifaces {
		if iface.flags&net.FlagUp == 0 {
			continue
		}
		if iface.flags&net.FlagLoopback != 0 || iface.flags&net.FlagPointToPoint != 0 {
			continue
		}
		if isVirtualInterfaceName(iface.name) {
			continue
		}
		for _, ip := range iface.addrs {
			rank, ok := directIPRank(ip)
			if !ok {
				continue
			}
			hostPort := net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(port))
			if _, exists := seen[hostPort]; exists {
				continue
			}
			seen[hostPort] = struct{}{}
			ranked = append(ranked, rankedAddr{rank: rank, addr: hostPort})
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].rank != ranked[j].rank {
			return ranked[i].rank < ranked[j].rank
		}
		return ranked[i].addr < ranked[j].addr
	})

	if len(ranked) > maxAdvertisedDirectAddrs {
		ranked = ranked[:maxAdvertisedDirectAddrs]
	}
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.addr)
	}
	return out
}

func discoverLocalDirectAddrs(port int) []string {
	if port <= 0 || port > 65535 {
		return nil
	}
	netIfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	infos := make([]ifaceAddrInfo, 0, len(netIfaces))
	for _, ni := range netIfaces {
		addrs, err := ni.Addrs()
		if err != nil {
			continue
		}
		info := ifaceAddrInfo{
			name:  ni.Name,
			flags: ni.Flags,
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if len(ip) == 0 {
				continue
			}
			if parsed, ok := netip.AddrFromSlice(ip); ok {
				info.addrs = append(info.addrs, parsed.Unmap())
			}
		}
		infos = append(infos, info)
	}
	return filterDirectIPs(infos, port)
}

func canonicalPeerID(deviceID string) string {
	trimmed := strings.TrimSpace(deviceID)
	if trimmed == "" {
		return ""
	}
	if pub, err := ParseNodePublic(trimmed); err == nil {
		return FormatNodePublic(pub)
	}
	return trimmed
}

// ConfigureDirect updates the direct TCP+mTLS listener port (<=0 disables listening),
// user-configured custom peer IP overrides, and peer-announced LAN addresses.
func (m *TailcatMesh) ConfigureDirect(listenPort int, peerOverrides map[string]string, learnedAddrs map[string][]string) error {
	if listenPort < 0 || listenPort > 65535 {
		return fmt.Errorf("invalid direct listen port %d", listenPort)
	}

	normOverrides := make(map[string]string, len(peerOverrides))
	for devID, raw := range peerOverrides {
		cid := canonicalPeerID(devID)
		if cid == "" {
			continue
		}
		norm, err := model.NormalizeDirectAddr(raw, model.DefaultDirectPort)
		if err != nil {
			return fmt.Errorf("peer %s custom direct address: %w", shortIDForLog(cid), err)
		}
		if norm != "" {
			normOverrides[cid] = norm
		}
	}

	normLearned := make(map[string][]string, len(learnedAddrs))
	for devID, rawList := range learnedAddrs {
		cid := canonicalPeerID(devID)
		if cid == "" {
			continue
		}
		seen := make(map[string]struct{}, len(rawList))
		var cleaned []string
		for _, raw := range rawList {
			norm, err := model.NormalizeDirectAddr(raw, model.DefaultDirectPort)
			if err != nil || norm == "" {
				continue
			}
			if _, ok := seen[norm]; ok {
				continue
			}
			seen[norm] = struct{}{}
			cleaned = append(cleaned, norm)
			if len(cleaned) >= maxAdvertisedDirectAddrs {
				break
			}
		}
		if len(cleaned) > 0 {
			normLearned[cid] = cleaned
		}
	}

	m.mu.Lock()
	if !m.started || m.closed {
		m.directListenPort = listenPort
		m.peerDirectOverrides = normOverrides
		m.peerLearnedDirect = normLearned
		m.mu.Unlock()
		return nil
	}

	prevOverrides := m.peerDirectOverrides
	needRestartListener := (listenPort != m.directListenPort) || (listenPort > 0 && m.directListener == nil)
	oldListener := m.directListener
	id := m.id
	hasCert := len(m.directCert.Certificate) > 0
	selfID := m.id.DeviceID
	rootCtx := m.rootCtx

	m.peerDirectOverrides = normOverrides
	m.peerLearnedDirect = normLearned
	if needRestartListener {
		m.directListenPort = listenPort
		m.directListener = nil
		m.directActualPort = 0
	}
	peersSnapshot := m.snapshotPeersLocked()
	m.mu.Unlock()

	var listenerErr error
	if needRestartListener {
		if oldListener != nil {
			_ = oldListener.Close()
		}
		if listenPort > 0 {
			if !hasCert {
				cert, err := buildIdentityTLSCert(id)
				if err != nil {
					listenerErr = err
				} else {
					m.mu.Lock()
					m.directCert = cert
					m.mu.Unlock()
					hasCert = true
				}
			}
			if hasCert {
				ln, actualPort, err := m.openDirectListener(fmt.Sprintf(":%d", listenPort))
				if err != nil {
					listenerErr = err
					log.Printf("tailcat: failed to start direct TCP listener on :%d: %v", listenPort, err)
				} else {
					m.mu.Lock()
					if m.closed || m.directListenPort != listenPort {
						m.mu.Unlock()
						_ = ln.Close()
					} else {
						m.directListener = ln
						m.directActualPort = actualPort
						m.mu.Unlock()
						log.Printf("tailcat: direct mTLS listener active on :%d", actualPort)
						if !m.goSpawn(func() { m.directAcceptLoop(ln) }) {
							_ = ln.Close()
						}
					}
				}
			}
		}
	}

	// React to updated custom overrides or newly learned LAN addresses.
	for _, ps := range peersSnapshot {
		newOverride := normOverrides[ps.deviceID]
		oldOverride := prevOverrides[ps.deviceID]
		hasOverride := newOverride != ""
		hasLearned := len(normLearned[ps.deviceID]) > 0
		shouldDial := selfID < ps.deviceID || hasOverride

		var startDial bool
		var resetDirect bool
		var tryUpgrade bool
		var upgradeDelay time.Duration
		var reclassifiedStatus *model.PeerConnStatus

		ps.mu.Lock()
		if !ps.revoked {
			if shouldDial && !ps.dialActive {
				ps.dialActive = true
				startDial = true
			} else if !shouldDial && ps.dialCancel != nil {
				cancel := ps.dialCancel
				ps.dialCancel = nil
				ps.dialActive = false
				cancel()
			}
			// If user changed or cleared custom IP while already on a direct outbound session, switch immediately.
			if newOverride != oldOverride && ps.outSession != nil && !ps.outSession.IsClosed() && ps.outIsDirect {
				if (newOverride != "" && ps.outDirectAddr != newOverride) ||
					(newOverride == "" && ps.outDirectKind == model.ConnPathCustomDirect) {
					resetDirect = true
				}
			}
			// If user cleared or updated custom IP while connected via an inbound direct session,
			// immediately reclassify the inbound path kind (e.g. Custom Direct -> LAN Direct)
			// so the UI badge updates without waiting for a reconnect or restart.
			if newOverride != oldOverride && ps.inSession != nil && !ps.inSession.IsClosed() && ps.inIsDirect {
				newKind := classifyInboundDirectKindWithOverride(ps.inDirectAddr, newOverride)
				if newKind != ps.inDirectKind {
					ps.inDirectKind = newKind
					outDirectActive := ps.outSession != nil && !ps.outSession.IsClosed() && ps.outIsDirect && !resetDirect
					if !outDirectActive {
						st := model.PeerConnStatus{
							DeviceID:  ps.deviceID,
							Connected: true,
							PathKind:  newKind,
							Endpoint:  ps.inDirectAddr,
							LatencyMs: ps.status.LatencyMs,
							UpdatedAt: time.Now().UnixMilli(),
						}
						reclassifiedStatus = &st
					}
				}
			}
			// Proactively upgrade connected peers to direct TCP+mTLS:
			// 1. Custom IP override: upgrade immediately even from Tailcat P2P/DERP or auto-LAN.
			// 2. Auto-discovered LAN: designated dialer (selfID < peerID) upgrades immediately;
			//    reverse side waits briefly so symmetric LAN uses a single connection while
			//    asymmetric firewalls (e.g. Windows Defender blocking inbound TCP) still upgrade.
			outOpen := ps.outSession != nil && !ps.outSession.IsClosed()
			inOpen := ps.inSession != nil && !ps.inSession.IsClosed()
			isAlreadyDirect := (outOpen && ps.outIsDirect) || (inOpen && ps.inIsDirect)
			isConnected := outOpen || inOpen
			alreadyOnTargetCustom := hasOverride &&
				outOpen && ps.outIsDirect &&
				ps.outDirectKind == model.ConnPathCustomDirect &&
				ps.outDirectAddr == newOverride
			if isConnected {
				if hasOverride && !alreadyOnTargetCustom {
					tryUpgrade = true
				} else if !isAlreadyDirect && hasLearned {
					tryUpgrade = true
					if !shouldDial {
						upgradeDelay = 1500 * time.Millisecond
					}
				}
			}
		}
		ps.mu.Unlock()

		if reclassifiedStatus != nil {
			m.updatePeerStatus(ps, *reclassifiedStatus)
		}
		if resetDirect {
			m.resetOutboundPeer(ps)
		}
		if startDial {
			dialCtx, cancel := context.WithCancel(rootCtx)
			ps.mu.Lock()
			if ps.revoked || !ps.dialActive {
				ps.mu.Unlock()
				cancel()
			} else {
				ps.dialCancel = cancel
				ps.mu.Unlock()
				p := ps
				dCtx := dialCtx
				if !m.goSpawn(func() { m.peerDialLoop(dCtx, p) }) {
					cancel()
				}
			}
		}
		if tryUpgrade {
			p := ps
			delay := upgradeDelay
			m.goSpawn(func() {
				if delay > 0 && !sleepCtx(rootCtx, delay) {
					return
				}
				m.tryUpgradeToDirect(rootCtx, p, true)
			})
		}
	}

	return listenerErr
}

// LocalDirectAddrs returns the currently advertised local LAN host:port endpoints.
func (m *TailcatMesh) LocalDirectAddrs() []string {
	m.mu.Lock()
	port := m.directActualPort
	closed := m.closed
	m.mu.Unlock()
	if closed || port <= 0 {
		return nil
	}
	return discoverLocalDirectAddrs(port)
}

// DirectListenAddr returns the bound direct TCP listener address (or "" if disabled).
func (m *TailcatMesh) DirectListenAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.directListener == nil {
		return ""
	}
	return m.directListener.Addr().String()
}

func (m *TailcatMesh) openDirectListener(bindAddr string) (net.Listener, int, error) {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, 0, err
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.Port <= 0 {
		_ = ln.Close()
		return nil, 0, errors.New("direct listener has no TCP port")
	}
	return ln, tcpAddr.Port, nil
}

func (m *TailcatMesh) directAcceptLoop(ln net.Listener) {
	for {
		rawConn, err := ln.Accept()
		if err != nil {
			return
		}
		c := rawConn
		if !m.goSpawn(func() { m.handleDirectInboundConn(c) }) {
			_ = c.Close()
			return
		}
	}
}

func (m *TailcatMesh) handleDirectInboundConn(rawConn net.Conn) {
	tuneDirectTCPConn(rawConn)

	m.mu.Lock()
	if m.closed || !m.started || len(m.directCert.Certificate) == 0 {
		m.mu.Unlock()
		_ = rawConn.Close()
		return
	}
	cert := m.directCert
	groupID := m.id.GroupID
	rootCtx := m.rootCtx
	m.mu.Unlock()

	var verifiedPeerID string
	var verifiedPubKey []byte

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{directALPN},
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("missing client certificate")
			}
			peerCert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parse client certificate: %w", err)
			}
			if len(peerCert.Subject.Organization) == 0 || peerCert.Subject.Organization[0] != groupID {
				return errors.New("direct mTLS: group mismatch")
			}
			peerID := canonicalPeerID(peerCert.Subject.CommonName)
			if peerID == "" {
				return errors.New("direct mTLS: empty peer device id")
			}
			edPub, ok := peerCert.PublicKey.(ed25519.PublicKey)
			if !ok || len(edPub) != ed25519.PublicKeySize {
				return errors.New("direct mTLS: non-ed25519 peer certificate")
			}

			m.mu.Lock()
			_, allowed := m.allowedKeys[peerID]
			expectedSign := m.allowedSignKeys[peerID]
			m.mu.Unlock()

			if !allowed || len(expectedSign) != ed25519.PublicKeySize {
				return errors.New("direct mTLS: peer not in allowlist")
			}
			if subtle.ConstantTimeCompare(edPub, expectedSign) != 1 {
				return errors.New("direct mTLS: peer signing key mismatch")
			}
			verifiedPeerID = peerID
			verifiedPubKey = append([]byte(nil), expectedSign...)
			return nil
		},
	}

	tlsConn := tls.Server(rawConn, tlsCfg)
	hsCtx, hsCancel := context.WithTimeout(rootCtx, directHandshakeTimeout)
	err := tlsConn.HandshakeContext(hsCtx)
	hsCancel()
	if err != nil {
		_ = tlsConn.Close()
		return
	}
	if verifiedPeerID == "" || len(verifiedPubKey) != ed25519.PublicKeySize {
		_ = tlsConn.Close()
		return
	}

	// Re-check allowlist after handshake in case peer was revoked concurrently.
	m.mu.Lock()
	peerNodePub, stillAllowed := m.allowedKeys[verifiedPeerID]
	m.mu.Unlock()
	if !stillAllowed {
		_ = tlsConn.Close()
		return
	}

	sess, err := yamux.Server(tlsConn, newYamuxConfig())
	if err != nil {
		_ = tlsConn.Close()
		return
	}

	remoteAddr := rawConn.RemoteAddr().String()
	inKind := m.classifyInboundDirectKind(verifiedPeerID, remoteAddr)
	ps := m.getOrCreatePeer(verifiedPeerID, peerNodePub)

	var prevIn *yamux.Session
	ps.mu.Lock()
	if ps.revoked {
		ps.mu.Unlock()
		_ = sess.Close()
		return
	}
	prevIn = ps.inSession
	ps.inSession = sess
	ps.inIsDirect = true
	ps.inDirectKind = inKind
	ps.inDirectAddr = remoteAddr
	ps.mu.Unlock()

	if prevIn != nil {
		_ = prevIn.Close()
	}

	log.Printf("tailcat: accepted direct mTLS (%s) connection from peer %s (%s)", inKind, shortIDForLog(verifiedPeerID), remoteAddr)
	m.probeDirectSession(ps, sess, inKind, remoteAddr)

	if !m.goSpawn(func() { m.serveYamuxSession(rootCtx, ps, sess, false) }) {
		_ = sess.Close()
	}
}

func (m *TailcatMesh) classifyInboundDirectKind(deviceID, remoteAddr string) model.ConnPathKind {
	m.mu.Lock()
	override := strings.TrimSpace(m.peerDirectOverrides[deviceID])
	m.mu.Unlock()
	return classifyInboundDirectKindWithOverride(remoteAddr, override)
}

func classifyInboundDirectKindWithOverride(remoteAddr, override string) model.ConnPathKind {
	remoteHost, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		remoteHost = strings.TrimSpace(remoteAddr)
	}
	remoteIP, parseErr := netip.ParseAddr(remoteHost)
	ipOK := parseErr == nil
	if ipOK {
		remoteIP = remoteIP.Unmap()
	}

	override = strings.TrimSpace(override)
	if override != "" {
		if ovHost, _, ovErr := net.SplitHostPort(override); ovErr == nil {
			if ovIP, ovParseErr := netip.ParseAddr(ovHost); ovParseErr == nil && ipOK && ovIP.Unmap() == remoteIP {
				return model.ConnPathCustomDirect
			}
			if strings.EqualFold(ovHost, remoteHost) {
				return model.ConnPathCustomDirect
			}
		}
	}

	if ipOK {
		if remoteIP.IsLoopback() ||
			prefixRFC1918_192.Contains(remoteIP) ||
			prefixRFC1918_10.Contains(remoteIP) ||
			prefixRFC1918_172.Contains(remoteIP) ||
			prefixIPv6ULA.Contains(remoteIP) {
			return model.ConnPathLAN
		}
		return model.ConnPathCustomDirect
	}
	return model.ConnPathLAN
}

func (m *TailcatMesh) hasDirectOverrideLocked(deviceID string) bool {
	return strings.TrimSpace(m.peerDirectOverrides[deviceID]) != ""
}

func (m *TailcatMesh) directCandidatesForPeer(deviceID string) []directCandidate {
	m.mu.Lock()
	override := strings.TrimSpace(m.peerDirectOverrides[deviceID])
	learned := append([]string(nil), m.peerLearnedDirect[deviceID]...)
	m.mu.Unlock()

	var out []directCandidate
	seen := make(map[string]struct{})
	if override != "" {
		seen[override] = struct{}{}
		out = append(out, directCandidate{
			addr:     override,
			timeout:  directCustomDialTimeout,
			isCustom: true,
		})
	}
	learnedCount := 0
	for _, addr := range learned {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, directCandidate{
			addr:     addr,
			timeout:  directLearnedDialTimeout,
			isCustom: false,
		})
		learnedCount++
		if learnedCount >= maxLearnedDirectCandidates {
			break
		}
	}
	return out
}

func (m *TailcatMesh) expectedPeerSignKey(ps *peerState) []byte {
	ps.mu.Lock()
	keyBytes := ps.device.SignPublicKey
	if len(keyBytes) != ed25519.PublicKeySize {
		keyBytes = ps.device.Route.SignPublicKey
	}
	if len(keyBytes) == ed25519.PublicKeySize {
		out := append([]byte(nil), keyBytes...)
		ps.mu.Unlock()
		return out
	}
	ps.mu.Unlock()

	m.mu.Lock()
	k := m.allowedSignKeys[ps.deviceID]
	var out []byte
	if len(k) == ed25519.PublicKeySize {
		out = append([]byte(nil), k...)
	}
	m.mu.Unlock()
	return out
}

func (m *TailcatMesh) ensureDirectCert() (tls.Certificate, string, error) {
	m.mu.Lock()
	if len(m.directCert.Certificate) > 0 {
		cert := m.directCert
		groupID := m.id.GroupID
		m.mu.Unlock()
		return cert, groupID, nil
	}
	id := m.id
	m.mu.Unlock()

	cert, err := buildIdentityTLSCert(id)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	m.mu.Lock()
	if len(m.directCert.Certificate) == 0 {
		m.directCert = cert
	} else {
		cert = m.directCert
	}
	groupID := m.id.GroupID
	m.mu.Unlock()
	return cert, groupID, nil
}

func (m *TailcatMesh) dialDirectPeer(ctx context.Context, ps *peerState) (*yamux.Session, string, model.ConnPathKind, float64, error) {
	candidates := m.directCandidatesForPeer(ps.deviceID)
	if len(candidates) == 0 {
		return nil, "", "", 0, errors.New("no direct candidates")
	}

	expectedSignKey := m.expectedPeerSignKey(ps)
	if len(expectedSignKey) != ed25519.PublicKeySize {
		return nil, "", "", 0, errors.New("peer missing ed25519 signing key for direct mTLS")
	}

	cert, groupID, err := m.ensureDirectCert()
	if err != nil {
		return nil, "", "", 0, err
	}

	var lastErr error
	for _, cand := range candidates {
		if ctx.Err() != nil {
			return nil, "", "", 0, ctx.Err()
		}
		sess, rttMs, dialErr := m.dialDirectCandidate(ctx, ps.deviceID, expectedSignKey, cert, groupID, cand)
		if dialErr == nil {
			kind := model.ConnPathLAN
			if cand.isCustom {
				kind = model.ConnPathCustomDirect
			}
			return sess, cand.addr, kind, rttMs, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("all direct candidates failed")
	}
	return nil, "", "", 0, lastErr
}

func (m *TailcatMesh) dialDirectCandidate(
	ctx context.Context,
	peerDeviceID string,
	expectedSignKey []byte,
	localCert tls.Certificate,
	groupID string,
	cand directCandidate,
) (*yamux.Session, float64, error) {
	dialCtx, cancel := context.WithTimeout(ctx, cand.timeout)
	defer cancel()

	var dialer net.Dialer
	rawConn, err := dialer.DialContext(dialCtx, "tcp", cand.addr)
	if err != nil {
		return nil, 0, fmt.Errorf("direct tcp dial %s: %w", cand.addr, err)
	}
	tuneDirectTCPConn(rawConn)

	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{directALPN},
		Certificates:       []tls.Certificate{localCert},
		InsecureSkipVerify: true, // Verified strictly via VerifyPeerCertificate pinning Ed25519 key + DeviceID + GroupID.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("direct mTLS: missing server certificate")
			}
			peerCert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("direct mTLS: parse server cert: %w", err)
			}
			if canonicalPeerID(peerCert.Subject.CommonName) != peerDeviceID {
				return errors.New("direct mTLS: peer device id mismatch")
			}
			if len(peerCert.Subject.Organization) == 0 || peerCert.Subject.Organization[0] != groupID {
				return errors.New("direct mTLS: group mismatch")
			}
			edPub, ok := peerCert.PublicKey.(ed25519.PublicKey)
			if !ok || len(edPub) != ed25519.PublicKeySize {
				return errors.New("direct mTLS: non-ed25519 server cert")
			}
			if subtle.ConstantTimeCompare(edPub, expectedSignKey) != 1 {
				return errors.New("direct mTLS: server signing key mismatch")
			}
			return nil
		},
	}

	tlsConn := tls.Client(rawConn, tlsCfg)
	hsCtx, hsCancel := context.WithTimeout(ctx, directHandshakeTimeout)
	err = tlsConn.HandshakeContext(hsCtx)
	hsCancel()
	if err != nil {
		_ = tlsConn.Close()
		return nil, 0, fmt.Errorf("direct mTLS handshake %s: %w", cand.addr, err)
	}

	sess, err := yamux.Client(tlsConn, newYamuxConfig())
	if err != nil {
		_ = tlsConn.Close()
		return nil, 0, fmt.Errorf("direct yamux init %s: %w", cand.addr, err)
	}

	// In TLS 1.3, the client's HandshakeContext returns before receiving the server's
	// client-certificate verification alert. Requiring the initial yamux Ping to succeed
	// proves the peer accepted our mTLS identity and initialized yamux end-to-end.
	rtt, pingErr := sess.Ping()
	if pingErr != nil {
		_ = sess.Close()
		return nil, 0, fmt.Errorf("direct yamux ping %s: %w", cand.addr, pingErr)
	}
	rttMs := float64(rtt) / float64(time.Millisecond)
	return sess, rttMs, nil
}

// tryUpgradeToDirect attempts to upgrade a peer currently using Tailcat (P2P/DERP)
// or auto-LAN to the highest-priority direct TCP+mTLS session without interrupting
// existing traffic if direct dialing fails.
func (m *TailcatMesh) tryUpgradeToDirect(ctx context.Context, ps *peerState, force bool) {
	if ctx == nil || ctx.Err() != nil {
		return
	}

	ps.dialMu.Lock()
	defer ps.dialMu.Unlock()

	m.mu.Lock()
	overrideAddr := strings.TrimSpace(m.peerDirectOverrides[ps.deviceID])
	m.mu.Unlock()

	ps.mu.Lock()
	if ps.revoked {
		ps.mu.Unlock()
		return
	}
	outOpen := ps.outSession != nil && !ps.outSession.IsClosed()
	inOpen := ps.inSession != nil && !ps.inSession.IsClosed()
	alreadyDirect := (outOpen && ps.outIsDirect) || (inOpen && ps.inIsDirect)
	alreadySatisfied := alreadyDirect
	if overrideAddr != "" {
		alreadySatisfied = outOpen && ps.outIsDirect &&
			ps.outDirectKind == model.ConnPathCustomDirect &&
			ps.outDirectAddr == overrideAddr
	}
	if alreadySatisfied {
		ps.mu.Unlock()
		return
	}
	if !force && !ps.lastDirectAttempt.IsZero() && time.Since(ps.lastDirectAttempt) < directUpgradeCooldown {
		ps.mu.Unlock()
		return
	}
	ps.lastDirectAttempt = time.Now()
	ps.mu.Unlock()

	sess, addr, directKind, rttMs, err := m.dialDirectPeer(ctx, ps)
	if err != nil {
		if force {
			log.Printf("tailcat: direct mTLS upgrade for peer %s failed: %v", shortIDForLog(ps.deviceID), err)
		}
		return
	}

	m.mu.Lock()
	rootCtx := m.rootCtx
	closed := m.closed
	m.mu.Unlock()
	if closed || rootCtx == nil || rootCtx.Err() != nil {
		_ = sess.Close()
		return
	}

	var oldOutSess *yamux.Session
	var oldOutClient *tailcat.Client

	ps.mu.Lock()
	if ps.revoked {
		ps.mu.Unlock()
		_ = sess.Close()
		return
	}
	oldOutSess = ps.outSession
	oldOutClient = ps.outClient
	ps.outSession = sess
	ps.outClient = nil
	ps.outAddr = ""
	ps.outDERPURL = ""
	ps.outIsDirect = true
	ps.outDirectKind = directKind
	ps.outDirectAddr = addr
	ps.mu.Unlock()

	log.Printf("tailcat: upgraded peer %s from DERP to direct mTLS (%s, %s, %.1fms)", shortIDForLog(ps.deviceID), directKind, addr, rttMs)
	m.updatePeerStatus(ps, model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: true,
		PathKind:  directKind,
		Endpoint:  addr,
		LatencyMs: rttMs,
		UpdatedAt: time.Now().UnixMilli(),
	})

	if !m.goSpawn(func() { m.serveYamuxSession(rootCtx, ps, sess, true) }) {
		_ = sess.Close()
	}

	if oldOutSess != nil {
		_ = oldOutSess.Close()
	}
	if oldOutClient != nil {
		drainAndCloseClient(oldOutClient, defaultDrainTimeout)
	}
}

func (m *TailcatMesh) probeDirectSession(ps *peerState, sess *yamux.Session, kind model.ConnPathKind, endpoint string) {
	if sess == nil || sess.IsClosed() {
		return
	}
	if kind == "" {
		kind = model.ConnPathLAN
	}
	st := model.PeerConnStatus{
		DeviceID:  ps.deviceID,
		Connected: true,
		PathKind:  kind,
		Endpoint:  endpoint,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if rtt, err := sess.Ping(); err == nil {
		st.LatencyMs = float64(rtt) / float64(time.Millisecond)
	}
	m.updatePeerStatus(ps, st)
}

func tuneDirectTCPConn(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
}
