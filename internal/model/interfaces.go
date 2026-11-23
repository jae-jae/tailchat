package model

import (
	"context"
	"io"
	"net"
)

// StreamKind identifies multiplexed yamux stream roles over a peer tunnel.
type StreamKind byte

const (
	// StreamKindControl carries JSON/framed control, gossip, and chat metadata messages.
	StreamKindControl StreamKind = 1
	// StreamKindChunkData carries on-demand content-addressed file chunk requests/responses.
	StreamKindChunkData StreamKind = 2
	// StreamKindPairing carries one-time pairing handshakes on the pairing channel.
	StreamKindPairing StreamKind = 3
)

// Store defines the persistence contract implemented by internal/store.
type Store interface {
	Close() error

	// Identity & Config
	LoadIdentity() (*LocalIdentity, error)
	SaveIdentity(id LocalIdentity) error
	LoadConfig() (*AppConfig, error)
	SaveConfig(cfg AppConfig) error

	// Devices & Member Events
	UpsertDevice(dev Device) error
	GetDevice(deviceID string) (*Device, error)
	ListDevices(includeRevoked bool) ([]Device, error)
	SaveMemberEvent(ev MemberEvent) (inserted bool, err error)
	ListMemberEvents() ([]MemberEvent, error)

	// Messages & Search
	NextAuthorSeq(authorDeviceID string) (uint64, error)
	SaveMessage(msg Message) (inserted bool, err error)
	GetMessage(msgID string) (*Message, error)
	ListMessages(limit int, beforeMsgID string) ([]Message, error)
	SearchMessages(query string, limit int) ([]Message, error)
	MessagesAfterClock(vc VectorClock, limit int) ([]Message, error)
	VectorClock() (VectorClock, error)

	// File Blobs & Transfers
	UpsertFileBlob(blob FileBlob) error
	GetFileBlob(blobHash string) (*FileBlob, error)
	AddBlobSeeder(blobHash, deviceID string) error
	UpsertTransferTask(task TransferTask) error
	GetTransferTask(blobHash string) (*TransferTask, error)
	ListTransferTasks() ([]TransferTask, error)
}

// CASStore manages 1 MiB content-addressed file chunks and resumable .part assembly.
type CASStore interface {
	// IngestFile splits a local file into verified 1 MiB chunks, computes BLAKE3 hashes,
	// optionally generates a <=32KB preview thumbnail for images, and returns FileBlob metadata.
	IngestFile(srcPath string) (*FileBlob, error)
	// IngestBytes writes in-memory bytes (e.g. pasted clipboard image) as a CAS file.
	IngestBytes(fileName, mimeType string, data []byte) (*FileBlob, error)
	// ReadChunk returns chunk index i for blobHash.
	ReadChunk(blobHash string, index int) ([]byte, error)
	// WriteChunk verifies chunk i against expectedHash, writes it to .part / chunk storage,
	// and updates/returns whether the chunk succeeded.
	WriteChunk(blobHash string, index int, expectedHash string, data []byte) error
	// AssembleFile verifies all chunks exist and match blob.BlobHash, writes/moves to outputDir,
	// and returns the final assembled file path.
	AssembleFile(blob FileBlob, outputDir string) (string, error)
	// HasCompleteBlob checks if a local complete file exists for blobHash.
	HasCompleteBlob(blobHash string) (string, bool)
}

// PeerStreamHandler handles an incoming multiplexed stream from a verified remote peer.
type PeerStreamHandler func(ctx context.Context, peerDeviceID string, kind StreamKind, stream io.ReadWriteCloser)

// PairingHandler handles an incoming pairing connection on the pairing listener.
type PairingHandler func(ctx context.Context, conn net.Conn)

// PeerTransportProvider abstracts Tailcat (and future LAN-direct) peer-to-peer mesh networking.
type PeerTransportProvider interface {
	// Start initializes the local Server with the persisted identity and DERP settings.
	Start(ctx context.Context, id LocalIdentity, derp DERPConfig) (RouteRecord, error)
	// UpdateDERP hot-switches the local Server's DERP configuration and returns the updated signed RouteRecord.
	UpdateDERP(ctx context.Context, derp DERPConfig) (RouteRecord, error)
	// CurrentRoute returns the local node's current signed RouteRecord.
	CurrentRoute() RouteRecord
	// SetAllowedPeers updates the server allowlist and disconnects any peer removed from it.
	SetAllowedPeers(peers []Device)
	// RevokePeer removes a peer from allowlist and immediately drops active tunnels/clients to it.
	RevokePeer(deviceID string)
	// SyncPeers ensures mesh connectivity toward all non-revoked peers using deterministic tie-breaking.
	SyncPeers(peers []Device)
	// OpenStream opens a multiplexed yamux stream of the given kind to peerDeviceID.
	OpenStream(ctx context.Context, peerDeviceID string, kind StreamKind) (io.ReadWriteCloser, error)
	// SetStreamHandler registers the callback for incoming multiplexed streams from peers.
	SetStreamHandler(h PeerStreamHandler)
	// SetPeerStatusHandler registers a callback invoked whenever peer path/connectivity changes.
	SetPeerStatusHandler(h func(PeerConnStatus))
	// PeerStatuses returns the current snapshot of all known peers' connection statuses.
	PeerStatuses() []PeerConnStatus
	// StartPairing opens the temporary pairing listener and invokes h for inbound pairing connections.
	StartPairing(ctx context.Context, h PairingHandler) (tailcatAddr string, port uint16, err error)
	// StopPairing closes the temporary pairing listener.
	StopPairing() error
	// DialPairing dials a remote host's pairing endpoint directly (used by new device before joining allowlist).
	DialPairing(ctx context.Context, tailcatAddr string, port uint16, derp DERPConfig) (net.Conn, error)
	// Close gracefully drains TCP and shuts down all servers and clients.
	Close(ctx context.Context) error
}

// AppAPI is the UI-facing / headless-facing application facade implemented by internal/syncproto.
type AppAPI interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error

	Identity() LocalIdentity
	Config() AppConfig
	UpdateConfig(ctx context.Context, cfg AppConfig) error
	UpdateDERP(ctx context.Context, derp DERPConfig) error

	// Messaging & Files
	SendText(ctx context.Context, text string) (*Message, error)
	SendFile(ctx context.Context, filePath string) (*Message, error)
	SendBytes(ctx context.Context, fileName, mimeType string, data []byte) (*Message, error)
	DeleteMessage(ctx context.Context, msgID string) error
	ClearAllMessages(ctx context.Context) error
	ResolveLocalFile(ctx context.Context, blobHash string) (string, error)
	ListMessages(limit int, beforeMsgID string) ([]Message, error)
	SearchMessages(query string, limit int) ([]Message, error)

	// Transfers
	DownloadFile(ctx context.Context, blobHash string) error
	PauseTransfer(blobHash string) error
	CancelTransfer(blobHash string) error
	GetTransfer(blobHash string) (*TransferTask, error)
	ListTransfers() ([]TransferTask, error)
	HasActiveTransfers() bool

	// Devices & Pairing
	ListDevices(includeRevoked bool) ([]Device, error)
	PeerStatuses() []PeerConnStatus
	CreatePairingCode(ctx context.Context, ttlSeconds int) (string, PairingToken, error)
	CancelPairing() error
	JoinWithPairingCode(ctx context.Context, code string) error
	RevokeDevice(ctx context.Context, deviceID string) error

	// Events
	Subscribe() (ch <-chan AppEvent, unsubscribe func())
}
