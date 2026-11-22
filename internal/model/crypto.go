package model

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"lukechampine.com/blake3"
)

var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

// NewULID returns a lexicographically sortable, globally unique identifier.
func NewULID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now().UTC()), ulidEntropy).String()
}

// RandomSecret returns a URL-safe random token of n random bytes.
func RandomSecret(n int) (string, error) {
	if n <= 0 {
		n = 24
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateEd25519Keypair generates an Ed25519 signing keypair.
func GenerateEd25519Keypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// Blake3Hex returns the lowercase hex-encoded 256-bit BLAKE3 digest of data.
func Blake3Hex(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SignRouteRecord signs r with the device's Ed25519 private key.
func SignRouteRecord(r *RouteRecord, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("invalid ed25519 private key size")
	}
	payload, err := canonicalRoutePayload(*r)
	if err != nil {
		return err
	}
	r.Signature = ed25519.Sign(priv, payload)
	return nil
}

// VerifyRouteRecord verifies r's signature against r.SignPublicKey.
func VerifyRouteRecord(r RouteRecord) bool {
	if len(r.SignPublicKey) != ed25519.PublicKeySize || len(r.Signature) != ed25519.SignatureSize {
		return false
	}
	payload, err := canonicalRoutePayload(r)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(r.SignPublicKey), payload, r.Signature)
}

func canonicalRoutePayload(r RouteRecord) ([]byte, error) {
	r.Signature = nil
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append([]byte("tailchat-route-v1:"), raw...), nil
}

// SignMemberEvent signs ev with the group Ed25519 private key.
func SignMemberEvent(ev *MemberEvent, groupPriv ed25519.PrivateKey) error {
	if len(groupPriv) != ed25519.PrivateKeySize {
		return errors.New("invalid group ed25519 private key size")
	}
	payload, err := canonicalMemberEventPayload(*ev)
	if err != nil {
		return err
	}
	ev.Signature = ed25519.Sign(groupPriv, payload)
	return nil
}

// VerifyMemberEvent verifies ev's signature against the group Ed25519 public key.
func VerifyMemberEvent(ev MemberEvent, groupPub ed25519.PublicKey) bool {
	if len(groupPub) != ed25519.PublicKeySize || len(ev.Signature) != ed25519.SignatureSize {
		return false
	}
	payload, err := canonicalMemberEventPayload(ev)
	if err != nil {
		return false
	}
	return ed25519.Verify(groupPub, payload, ev.Signature)
}

func canonicalMemberEventPayload(ev MemberEvent) ([]byte, error) {
	ev.Signature = nil
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	return append([]byte("tailchat-member-event-v1:"), raw...), nil
}

// NewChunkBitmap allocates a zeroed bitmap capable of tracking chunkCount chunks.
func NewChunkBitmap(chunkCount int) []byte {
	if chunkCount <= 0 {
		return nil
	}
	return make([]byte, (chunkCount+7)/8)
}

// HasChunk reports whether bit index i is set in bitmap.
func HasChunk(bitmap []byte, i int) bool {
	if i < 0 || i/8 >= len(bitmap) {
		return false
	}
	return (bitmap[i/8] & (1 << uint(i%8))) != 0
}

// SetChunk marks bit index i as present in bitmap, growing if needed.
func SetChunk(bitmap []byte, i int) []byte {
	if i < 0 {
		return bitmap
	}
	need := i/8 + 1
	if len(bitmap) < need {
		grown := make([]byte, need)
		copy(grown, bitmap)
		bitmap = grown
	}
	bitmap[i/8] |= 1 << uint(i%8)
	return bitmap
}

// CountChunks returns how many bits in [0, chunkCount) are set.
func CountChunks(bitmap []byte, chunkCount int) int {
	n := 0
	for i := 0; i < chunkCount; i++ {
		if HasChunk(bitmap, i) {
			n++
		}
	}
	return n
}

// MissingChunks returns chunk indices in [0, chunkCount) not yet set in bitmap.
func MissingChunks(bitmap []byte, chunkCount int) []int {
	var missing []int
	for i := 0; i < chunkCount; i++ {
		if !HasChunk(bitmap, i) {
			missing = append(missing, i)
		}
	}
	return missing
}
