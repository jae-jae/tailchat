package model

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultChunkSize is 1 MiB for content-addressed file chunking.
	DefaultChunkSize = 1024 * 1024
	// DefaultAutoDownloadDesktopBytes is 20 MiB auto-download threshold on desktop.
	DefaultAutoDownloadDesktopBytes = 20 * 1024 * 1024
	// DefaultAutoDownloadMobileBytes is 0 (thumbnails only by default on mobile).
	DefaultAutoDownloadMobileBytes = 0
	// MaxThumbnailBytes caps inline WebP/PNG preview thumbnails in control messages.
	MaxThumbnailBytes = 32 * 1024
	// DefaultMeshPort is the Tailcat virtual TCP port used for multiplexed mesh traffic.
	DefaultMeshPort uint16 = 19840
	// DefaultPairingPort is the Tailcat virtual TCP port used for short-lived pairing handshakes.
	DefaultPairingPort uint16 = 19841
	// DefaultDirectPort is the default TCP port for LAN / custom IP direct mTLS connections.
	DefaultDirectPort = 19840
)

// DERPMode selects how a node bootstraps and relays Tailcat connections.
type DERPMode string

const (
	// DERPModeDefault uses Tailscale's default public tailcat.dev DERP map (out-of-the-box).
	DERPModeDefault DERPMode = "default_tailcat"
	// DERPModeCustomRegion uses a single self-hosted derper node embedded inline in the route.
	DERPModeCustomRegion DERPMode = "custom_region"
	// DERPModeCustomMapURL fetches a custom DERPMap JSON from a user-provided URL.
	DERPModeCustomMapURL DERPMode = "custom_map_url"
)

// DERPConfig describes the user's DERP relay preference.
type DERPConfig struct {
	Mode          DERPMode `json:"mode"`
	RegionID      int      `json:"region_id,omitempty"`       // Pinned region ID (e.g. 304 Tokyo, or 900+ for custom)
	DERPMapURL    string   `json:"derp_map_url,omitempty"`    // Used when Mode == DERPModeCustomMapURL
	CustomHost    string   `json:"custom_host,omitempty"`     // Used when Mode == DERPModeCustomRegion (e.g. "derp.example.com")
	CustomDERPPort int     `json:"custom_derp_port,omitempty"` // Default 443
	CustomSTUNPort int     `json:"custom_stun_port,omitempty"` // Default 3478
	CustomRegionName string `json:"custom_region_name,omitempty"`
}

// DefaultDERPConfig returns the out-of-the-box DERP configuration.
func DefaultDERPConfig() DERPConfig {
	return DERPConfig{
		Mode:           DERPModeDefault,
		RegionID:       304, // Tokyo (nearest default in tailcat.dev; user can change or auto-pick)
		CustomDERPPort: 443,
		CustomSTUNPort: 3478,
	}
}

// LocalIdentity holds the persisted cryptographic identity of this device and its group.
type LocalIdentity struct {
	DeviceID        string `json:"device_id"`          // Text-marshaled tailscale key.NodePublic
	NodePrivateKey  string `json:"node_private_key"`   // Text-marshaled tailscale key.NodePrivate
	PresharedKey    string `json:"preshared_key"`      // Text-marshaled tailcat.PresharedKey
	SignPublicKey   []byte `json:"sign_public_key"`    // Ed25519 device signing pubkey (32B)
	SignPrivateKey  []byte `json:"sign_private_key"`   // Ed25519 device signing privkey (64B)
	GroupID         string `json:"group_id"`           // Hex/ULID identifier for this device group
	GroupPublicKey  []byte `json:"group_public_key"`   // Ed25519 group root pubkey (32B)
	GroupPrivateKey []byte `json:"group_private_key"`  // Ed25519 group root privkey (64B), shared among trusted devices
	DeviceName      string `json:"device_name"`
	Platform        string `json:"platform"` // macos | windows | linux | ios | android | headless
	RouteEpoch      uint64 `json:"route_epoch"`
}

// RouteRecord decouples mutable transport reachability (DERP + TailcatAddr) from permanent DeviceID.
type RouteRecord struct {
	DeviceID      string     `json:"device_id"`
	SignPublicKey []byte     `json:"sign_public_key"`
	TailcatAddr   string     `json:"tailcat_addr"` // Current tc... address for dialing this device's Server
	DERP          DERPConfig `json:"derp"`
	RouteEpoch    uint64     `json:"route_epoch"`
	UpdatedAt     int64      `json:"updated_at"` // UnixMilli
	Signature     []byte     `json:"signature,omitempty"`
}

// Device represents a paired device in the unified "my devices" group.
type Device struct {
	DeviceID      string      `json:"device_id"`
	Name          string      `json:"name"`
	Platform      string      `json:"platform"`
	SignPublicKey []byte      `json:"sign_public_key"`
	Route         RouteRecord `json:"route"`
	RouteEpoch    uint64      `json:"route_epoch"`
	Revoked       bool        `json:"revoked"`
	AddedAt       int64       `json:"added_at"`
	LastSeenAt    int64       `json:"last_seen_at"`
	IsSelf        bool        `json:"is_self,omitempty"`
	DirectAddrs   []string    `json:"direct_addrs,omitempty"` // Runtime auto-discovered LAN endpoints (not signed)
}

// MemberEventKind enumerates signed membership mutations replicated via gossip.
type MemberEventKind string

const (
	MemberEventAdd    MemberEventKind = "add"
	MemberEventRevoke MemberEventKind = "revoke"
	MemberEventRoute  MemberEventKind = "route_update"
)

// MemberEvent is an immutable, group-signed membership or route state change.
type MemberEvent struct {
	EventID        string          `json:"event_id"` // ULID
	GroupID        string          `json:"group_id"`
	Kind           MemberEventKind `json:"kind"`
	TargetDeviceID string          `json:"target_device_id"`
	DeviceName     string          `json:"device_name,omitempty"`
	Platform       string          `json:"platform,omitempty"`
	SignPublicKey  []byte          `json:"sign_public_key,omitempty"`
	Route          *RouteRecord    `json:"route,omitempty"`
	Epoch          uint64          `json:"epoch"`
	IssuerDeviceID string          `json:"issuer_device_id"`
	CreatedAt      int64           `json:"created_at"` // UnixMilli
	Signature      []byte          `json:"signature,omitempty"`
}

// MessageKind classifies timeline items in the unified chat stream.
type MessageKind string

const (
	MessageKindText      MessageKind = "text"
	MessageKindFile      MessageKind = "file"
	MessageKindTombstone MessageKind = "tombstone"
)

// Message is an immutable timeline entry authored by one device.
type Message struct {
	MsgID          string      `json:"msg_id"`           // ULID (globally sortable)
	AuthorDeviceID string      `json:"author_device_id"` // Sender's permanent DeviceID
	AuthorName     string      `json:"author_name"`      // Cached display name at send time
	AuthorSeq      uint64      `json:"author_seq"`       // Per-author monotonic sequence for Vector Clock
	Kind           MessageKind `json:"kind"`
	TextBody       string      `json:"text_body,omitempty"`
	BlobHash       string      `json:"blob_hash,omitempty"`       // BLAKE3 hex root hash when Kind == file
	TargetMsgID    string      `json:"target_msg_id,omitempty"`   // Target msg_id when Kind == tombstone
	CreatedAt      int64       `json:"created_at"`                // UnixMilli
	File           *FileBlob   `json:"file,omitempty"`            // Hydrated for UI / wire transfer
}

// FileBlob describes content-addressed file metadata and local availability.
type FileBlob struct {
	BlobHash      string   `json:"blob_hash"` // BLAKE3 hex root hash
	FileName      string   `json:"file_name"`
	FileSize      int64    `json:"file_size"`
	MimeType      string   `json:"mime_type"`
	ChunkSize     int      `json:"chunk_size"`
	ChunkCount    int      `json:"chunk_count"`
	ChunkHashes   []string `json:"chunk_hashes"`             // Per-chunk BLAKE3 hex hashes
	ThumbnailWebP []byte   `json:"thumbnail_webp,omitempty"` // Optional <=32KB inline preview
	LocalPath     string   `json:"local_path,omitempty"`     // Non-empty once completely available locally
	Seeders       []string `json:"seeders,omitempty"`        // Known DeviceIDs holding this blob
}

// TransferDirection indicates upload or download.
type TransferDirection string

const (
	TransferUpload   TransferDirection = "upload"
	TransferDownload TransferDirection = "download"
)

// TransferState tracks the lifecycle of a file transfer task.
type TransferState string

const (
	TransferQueued    TransferState = "queued"
	TransferRunning   TransferState = "running"
	TransferPaused    TransferState = "paused"
	TransferDone      TransferState = "done"
	TransferFailed    TransferState = "failed"
	TransferCancelled TransferState = "cancelled"
)

// TransferTask persists resumable chunk download/upload progress.
type TransferTask struct {
	BlobHash    string            `json:"blob_hash"`
	Direction   TransferDirection `json:"direction"`
	State       TransferState     `json:"state"`
	ChunkCount  int               `json:"chunk_count"`
	ChunkBitmap []byte            `json:"chunk_bitmap"` // Bit i == 1 means chunk i verified on disk
	BytesDone   int64             `json:"bytes_done"`
	BytesTotal  int64             `json:"bytes_total"`
	LastError   string            `json:"last_error,omitempty"`
	UpdatedAt   int64             `json:"updated_at"`
}

// Progress returns completion ratio in [0, 1].
func (t TransferTask) Progress() float64 {
	if t.BytesTotal <= 0 {
		if t.State == TransferDone {
			return 1
		}
		return 0
	}
	p := float64(t.BytesDone) / float64(t.BytesTotal)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// PairingToken is a short-lived code encoded as base64url JSON for QR/copy-paste pairing.
type PairingToken struct {
	Version     int        `json:"v"`
	GroupID     string     `json:"group_id"`
	HostDevID   string     `json:"host_device_id"`
	HostName    string     `json:"host_name"`
	TailcatAddr string     `json:"tailcat_addr"`
	PairingPort uint16     `json:"pairing_port"`
	Secret      string     `json:"secret"` // One-time random token
	DERP        DERPConfig `json:"derp"`
	ExpiresAt   int64      `json:"expires_at"` // UnixMilli
}

// Encode serializes PairingToken to a compact "tailchat-pair://" string.
func (p PairingToken) Encode() (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return "tailchat-pair://" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodePairingToken parses a "tailchat-pair://" code or raw base64url payload.
func DecodePairingToken(code string) (PairingToken, error) {
	const prefix = "tailchat-pair://"
	trimmed := code
	if len(trimmed) >= len(prefix) && trimmed[:len(prefix)] == prefix {
		trimmed = trimmed[len(prefix):]
	}
	raw, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return PairingToken{}, errors.New("invalid pairing code encoding")
	}
	var tok PairingToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		return PairingToken{}, errors.New("invalid pairing code payload")
	}
	if tok.HostDevID == "" || tok.TailcatAddr == "" || tok.Secret == "" {
		return PairingToken{}, errors.New("incomplete pairing token")
	}
	return tok, nil
}

// IsExpired reports whether the pairing token has expired.
func (p PairingToken) IsExpired(now time.Time) bool {
	return p.ExpiresAt > 0 && now.UnixMilli() > p.ExpiresAt
}

// Language preference values for AppConfig.Language.
const (
	LanguageAuto    = "auto"
	LanguageEnglish = "en"
	LanguageChinese = "zh"
)

// NormalizeLanguage canonicalizes a stored language preference into "auto", "en", or "zh".
func NormalizeLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case LanguageEnglish, "en-us", "en-gb", "en_us", "en_gb":
		return LanguageEnglish
	case LanguageChinese, "zh-cn", "zh-tw", "zh-hans", "zh-hant", "zh_cn", "zh_tw":
		return LanguageChinese
	default:
		return LanguageAuto
	}
}

// AppConfig holds user-editable runtime settings.
type AppConfig struct {
	DeviceName           string            `json:"device_name"`
	Language             string            `json:"language,omitempty"` // "auto" (default, fallback en), "en", "zh"
	DERP                 DERPConfig        `json:"derp"`
	DirectListenPort     int               `json:"direct_listen_port,omitempty"`     // 0 = DefaultDirectPort (19840)
	DirectListenDisabled bool              `json:"direct_listen_disabled,omitempty"` // true disables LAN/custom TCP direct listener
	PeerDirectAddrs      map[string]string `json:"peer_direct_addrs,omitempty"`      // deviceID -> custom host[:port]
	AutoDownloadMaxBytes int64             `json:"auto_download_max_bytes"`
	MaxDownloadBytesPerS int64             `json:"max_download_bytes_per_s"` // 0 = unlimited
	DownloadDir          string            `json:"download_dir"`
	MinimizeToTray       bool              `json:"minimize_to_tray"`
	GlobalHotkey         string            `json:"global_hotkey"`
}

// Clone returns a deep copy of AppConfig safe for concurrent access across goroutines.
func (c AppConfig) Clone() AppConfig {
	out := c
	if len(c.PeerDirectAddrs) > 0 {
		out.PeerDirectAddrs = make(map[string]string, len(c.PeerDirectAddrs))
		for k, v := range c.PeerDirectAddrs {
			out.PeerDirectAddrs[k] = v
		}
	} else {
		out.PeerDirectAddrs = nil
	}
	return out
}

// EffectiveDirectListenPort returns the active TCP port for direct mTLS connections,
// or 0 if direct listening is explicitly disabled.
func (c AppConfig) EffectiveDirectListenPort() int {
	if c.DirectListenDisabled {
		return 0
	}
	if c.DirectListenPort > 0 && c.DirectListenPort <= 65535 {
		return c.DirectListenPort
	}
	return DefaultDirectPort
}

// NormalizeDirectAddr validates and canonicalizes a user-configured or peer-announced
// direct TCP address into "host:port". Empty input returns ("", nil).
// Supported formats: "192.168.1.5", "192.168.1.5:19840", "[fd00::1]:19840", "fd00::1",
// "mac-mini.local", "mac-mini.local:29840".
func NormalizeDirectAddr(raw string, defaultPort int) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if defaultPort <= 0 || defaultPort > 65535 {
		defaultPort = DefaultDirectPort
	}
	if strings.ContainsAny(s, " \t\r\n/\\@") || strings.Contains(s, "://") {
		return "", fmt.Errorf("invalid direct address %q", raw)
	}

	// Case 1: Pure IP literal (IPv4 or bare IPv6 without brackets/port).
	if addr, err := netip.ParseAddr(s); err == nil {
		return net.JoinHostPort(addr.Unmap().String(), strconv.Itoa(defaultPort)), nil
	}

	// Case 2: Bracketed IPv6 without port (e.g. "[fd00::1]").
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := s[1 : len(s)-1]
		addr, err := netip.ParseAddr(inner)
		if err != nil {
			return "", fmt.Errorf("invalid IPv6 address %q", raw)
		}
		return net.JoinHostPort(addr.Unmap().String(), strconv.Itoa(defaultPort)), nil
	}

	// Case 3: host:port (IPv4:port, [IPv6]:port, hostname:port).
	if host, portStr, err := net.SplitHostPort(s); err == nil {
		host = strings.TrimSpace(host)
		if host == "" {
			return "", fmt.Errorf("missing host in direct address %q", raw)
		}
		port, err := strconv.Atoi(strings.TrimSpace(portStr))
		if err != nil || port <= 0 || port > 65535 {
			return "", fmt.Errorf("invalid port in direct address %q", raw)
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			host = addr.Unmap().String()
		}
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}

	// Case 4: Bare hostname without port (reject stray colons or brackets).
	if strings.ContainsAny(s, ":[]") {
		return "", fmt.Errorf("invalid direct address %q", raw)
	}
	return net.JoinHostPort(s, strconv.Itoa(defaultPort)), nil
}

// ConnPathKind indicates whether traffic to a peer is direct UDP or DERP-relayed.
type ConnPathKind string

const (
	ConnPathDisconnected ConnPathKind = "disconnected"
	ConnPathConnecting   ConnPathKind = "connecting"
	ConnPathDERP         ConnPathKind = "derp"
	ConnPathDirect       ConnPathKind = "direct"
)

// PeerConnStatus reports live connectivity and latency for one peer.
type PeerConnStatus struct {
	DeviceID   string       `json:"device_id"`
	Connected  bool         `json:"connected"`
	PathKind   ConnPathKind `json:"path_kind"`
	Endpoint   string       `json:"endpoint,omitempty"`   // Direct ip:port when direct
	DERPRegion string       `json:"derp_region,omitempty"` // Region code/name when relayed
	LatencyMs  float64      `json:"latency_ms,omitempty"`
	LastError  string       `json:"last_error,omitempty"`
	UpdatedAt  int64        `json:"updated_at"`
}

// EventType enumerates UI/observer notifications emitted by AppService.
type EventType string

const (
	EventMessageAdded    EventType = "message_added"
	EventMessageDeleted  EventType = "message_deleted"
	EventTransferUpdated EventType = "transfer_updated"
	EventDevicesUpdated  EventType = "devices_updated"
	EventPeerStatus      EventType = "peer_status"
	EventConfigUpdated   EventType = "config_updated"
	EventError           EventType = "error"
)

// AppEvent is broadcast to UI subscribers and headless/mobile listeners.
type AppEvent struct {
	Type     EventType       `json:"type"`
	Message  *Message        `json:"message,omitempty"`
	Transfer *TransferTask   `json:"transfer,omitempty"`
	Peer     *PeerConnStatus `json:"peer,omitempty"`
	ErrText  string          `json:"err_text,omitempty"`
}

// VectorClock maps AuthorDeviceID -> highest contiguous/observed AuthorSeq.
type VectorClock map[string]uint64

// VerifyEd25519KeyLength checks raw Ed25519 public key length.
func VerifyEd25519KeyLength(pub []byte) bool {
	return len(pub) == ed25519.PublicKeySize
}
