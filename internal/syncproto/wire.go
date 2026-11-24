package syncproto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"tailchat/internal/model"
)

const (
	// MaxFrameSize caps any single length-prefixed JSON frame at 4 MiB.
	MaxFrameSize = 4 * 1024 * 1024
)

// FrameType identifies the payload kind carried inside a ControlFrame.
type FrameType string

const (
	FrameHello          FrameType = "hello"
	FrameMemberEvents   FrameType = "member_events"
	FrameMsgSyncRequest FrameType = "msg_sync_request"
	FrameMsgBatch       FrameType = "msg_batch"
	FrameBlobHave       FrameType = "blob_have"
)

// ControlFrame is the top-level JSON envelope sent over StreamKindControl.
type ControlFrame struct {
	Type           FrameType              `json:"type"`
	Hello          *HelloPayload          `json:"hello,omitempty"`
	MemberEvents   *MemberEventsPayload   `json:"member_events,omitempty"`
	MsgSyncRequest *MsgSyncRequestPayload `json:"msg_sync_request,omitempty"`
	MsgBatch       *MsgBatchPayload       `json:"msg_batch,omitempty"`
	BlobHave       *BlobHavePayload       `json:"blob_have,omitempty"`
}

// HelloPayload initiates or completes anti-entropy handshake on peer connection.
type HelloPayload struct {
	DeviceID       string            `json:"device_id"`
	DeviceName     string            `json:"device_name"`
	Platform       string            `json:"platform"`
	GroupID        string            `json:"group_id"`
	Route          model.RouteRecord `json:"route"`
	VectorClock    model.VectorClock `json:"vector_clock"`
	MemberEventIDs []string          `json:"member_event_ids,omitempty"`
	DirectAddrs    []string          `json:"direct_addrs,omitempty"`
	IsReply        bool              `json:"is_reply,omitempty"`
}

// MemberEventsPayload carries a batch of group-signed membership/route events.
type MemberEventsPayload struct {
	Events []model.MemberEvent `json:"events"`
}

// MsgSyncRequestPayload requests all messages newer than VectorClock.
type MsgSyncRequestPayload struct {
	VectorClock model.VectorClock `json:"vector_clock"`
}

// MsgBatchPayload carries a batch of chat messages (including FileBlob metadata for file messages).
type MsgBatchPayload struct {
	Messages []model.Message `json:"messages"`
}

// BlobHavePayload announces that the sender holds the complete verified blob.
type BlobHavePayload struct {
	BlobHash string `json:"blob_hash"`
	DeviceID string `json:"device_id"`
}

// ChunkRequest requests a single 1 MiB chunk over StreamKindChunkData.
type ChunkRequest struct {
	BlobHash string `json:"blob_hash"`
	Index    int    `json:"index"`
}

// ChunkResponse returns the requested chunk bytes and BLAKE3 hash (or an error).
type ChunkResponse struct {
	BlobHash string `json:"blob_hash"`
	Index    int    `json:"index"`
	Hash     string `json:"hash,omitempty"`
	Data     []byte `json:"data,omitempty"`
	Error    string `json:"error,omitempty"`
}

// PairRequest is sent by a joining device over the temporary PairingPort connection.
type PairRequest struct {
	Secret        string            `json:"secret"`
	DeviceID      string            `json:"device_id"`
	DeviceName    string            `json:"device_name"`
	Platform      string            `json:"platform"`
	SignPublicKey []byte            `json:"sign_public_key"`
	Route         model.RouteRecord `json:"route"`
}

// PairResponse is returned by the pairing host upon verifying PairRequest.
type PairResponse struct {
	OK              bool                `json:"ok"`
	Error           string              `json:"error,omitempty"`
	GroupID         string              `json:"group_id,omitempty"`
	GroupPublicKey  []byte              `json:"group_public_key,omitempty"`
	GroupPrivateKey []byte              `json:"group_private_key,omitempty"`
	Devices         []model.Device      `json:"devices,omitempty"`
	MemberEvents    []model.MemberEvent `json:"member_events,omitempty"`
}

// WriteFrame encodes v as a 4-byte big-endian length-prefixed JSON frame.
func WriteFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(payload) == 0 {
		return errors.New("empty frame payload")
	}
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("frame size %d exceeds max %d", len(payload), MaxFrameSize)
	}

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if err := writeAll(w, hdr[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

// ReadFrame reads a 4-byte big-endian length-prefixed JSON frame into dst.
func ReadFrame(r io.Reader, dst any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return errors.New("zero-length frame")
	}
	if n > MaxFrameSize {
		return fmt.Errorf("frame size %d exceeds max %d", n, MaxFrameSize)
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	if err := json.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("unmarshal frame: %w", err)
	}
	return nil
}

type readDeadlineSetter interface {
	SetReadDeadline(time.Time) error
}

// ReadFrameWithTimeouts reads a length-prefixed JSON frame while enforcing a fast
// headerTimeout on the 4-byte length prefix (to detect stalled/zombie sessions quickly)
// and a separate bodyTimeout for reading the payload once bytes start flowing.
func ReadFrameWithTimeouts(r io.Reader, dst any, headerTimeout, bodyTimeout time.Duration) error {
	ds, hasDeadline := r.(readDeadlineSetter)
	if hasDeadline && headerTimeout > 0 {
		_ = ds.SetReadDeadline(time.Now().Add(headerTimeout))
	}
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return errors.New("zero-length frame")
	}
	if n > MaxFrameSize {
		return fmt.Errorf("frame size %d exceeds max %d", n, MaxFrameSize)
	}

	if hasDeadline && bodyTimeout > 0 {
		_ = ds.SetReadDeadline(time.Now().Add(bodyTimeout))
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	if hasDeadline {
		_ = ds.SetReadDeadline(time.Time{})
	}
	if err := json.Unmarshal(buf, dst); err != nil {
		return fmt.Errorf("unmarshal frame: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
