package transport

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/tailscale/tailcat"
	"go4.org/mem"
	"lukechampine.com/blake3"
	"tailscale.com/types/key"

	"tailchat/internal/model"
)

// GenerateLocalIdentity creates a new persistent device identity and root group keypair.
func GenerateLocalIdentity(deviceName, platform string) (model.LocalIdentity, error) {
	nodePriv := key.NewNode()
	nodePub := nodePriv.Public()

	nodePrivText, err := nodePriv.MarshalText()
	if err != nil {
		return model.LocalIdentity{}, fmt.Errorf("marshal node private key: %w", err)
	}
	nodePubText, err := nodePub.MarshalText()
	if err != nil {
		return model.LocalIdentity{}, fmt.Errorf("marshal node public key: %w", err)
	}

	psk := tailcat.NewPresharedKey()
	pskText, err := psk.MarshalText()
	if err != nil {
		return model.LocalIdentity{}, fmt.Errorf("marshal preshared key: %w", err)
	}

	signPub, signPriv, err := model.GenerateEd25519Keypair()
	if err != nil {
		return model.LocalIdentity{}, fmt.Errorf("generate device signing keypair: %w", err)
	}

	groupPub, groupPriv, err := model.GenerateEd25519Keypair()
	if err != nil {
		return model.LocalIdentity{}, fmt.Errorf("generate group keypair: %w", err)
	}

	name := strings.TrimSpace(deviceName)
	if name == "" {
		if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
			name = strings.TrimSpace(h)
		} else {
			name = "tailchat-device"
		}
	}

	plat := strings.TrimSpace(platform)
	if plat == "" {
		plat = normalizePlatform(runtime.GOOS)
	}

	return model.LocalIdentity{
		DeviceID:        string(nodePubText),
		NodePrivateKey:  string(nodePrivText),
		PresharedKey:    string(pskText),
		SignPublicKey:   signPub,
		SignPrivateKey:  signPriv,
		GroupID:         model.NewULID(),
		GroupPublicKey:  groupPub,
		GroupPrivateKey: groupPriv,
		DeviceName:      name,
		Platform:        plat,
		RouteEpoch:      1,
	}, nil
}

func normalizePlatform(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows", "linux", "ios", "android":
		return goos
	default:
		return "headless"
	}
}

// ParseNodePrivate parses a text-marshaled Tailscale NodePrivate key.
func ParseNodePrivate(text string) (key.NodePrivate, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return key.NodePrivate{}, errors.New("empty node private key")
	}
	var k key.NodePrivate
	if err := k.UnmarshalText([]byte(trimmed)); err != nil {
		return key.NodePrivate{}, fmt.Errorf("parse node private key: %w", err)
	}
	if k.IsZero() {
		return key.NodePrivate{}, errors.New("zero node private key")
	}
	return k, nil
}

// ParseNodePublic parses a text-marshaled Tailscale NodePublic key.
func ParseNodePublic(text string) (key.NodePublic, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return key.NodePublic{}, errors.New("empty node public key")
	}
	var k key.NodePublic
	if err := k.UnmarshalText([]byte(trimmed)); err != nil {
		return key.NodePublic{}, fmt.Errorf("parse node public key: %w", err)
	}
	if k.IsZero() {
		return key.NodePublic{}, errors.New("zero node public key")
	}
	return k, nil
}

// ParsePresharedKey parses a text-marshaled Tailcat PresharedKey.
func ParsePresharedKey(text string) (tailcat.PresharedKey, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return tailcat.PresharedKey{}, errors.New("empty preshared key")
	}
	var k tailcat.PresharedKey
	if err := k.UnmarshalText([]byte(trimmed)); err != nil {
		return tailcat.PresharedKey{}, fmt.Errorf("parse preshared key: %w", err)
	}
	if k.IsZero() {
		return tailcat.PresharedKey{}, errors.New("zero preshared key")
	}
	return k, nil
}

// FormatNodePublic returns the canonical text-marshaled form of a NodePublic key.
func FormatNodePublic(k key.NodePublic) string {
	b, err := k.MarshalText()
	if err != nil {
		return k.String()
	}
	return string(b)
}

// DerivePairClientKey deterministically derives a unique Tailcat Client NodePrivate
// for outbound dials from callerDeviceID to calleeDeviceID within the same group.
// Tailscale DERP routes by NodePublic and evicts duplicate connections using the same
// key on a region, so every outbound Client MUST have a distinct key from its local
// Server and from other peer Clients.
func DerivePairClientKey(groupSecret []byte, callerDeviceID, calleeDeviceID string) key.NodePrivate {
	h := blake3.New(32, nil)
	_, _ = h.Write([]byte("tailchat-pair-client-v1|"))
	if len(groupSecret) >= 32 {
		_, _ = h.Write(groupSecret[:32])
	} else {
		_, _ = h.Write(groupSecret)
	}
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(callerDeviceID))
	_, _ = h.Write([]byte("->"))
	_, _ = h.Write([]byte(calleeDeviceID))
	var raw [32]byte
	copy(raw[:], h.Sum(nil))
	return key.NodePrivateFromRaw32(mem.B(raw[:]))
}

