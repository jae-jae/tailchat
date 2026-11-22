package model

import (
	"testing"
	"time"
)

func TestRouteAndMemberSigning(t *testing.T) {
	pub, priv, err := GenerateEd25519Keypair()
	if err != nil {
		t.Fatalf("GenerateEd25519Keypair: %v", err)
	}
	rec := RouteRecord{
		DeviceID:      "nodekey:abc",
		SignPublicKey: pub,
		TailcatAddr:   "tc12345",
		DERP:          DefaultDERPConfig(),
		RouteEpoch:    1,
		UpdatedAt:     time.Now().UnixMilli(),
	}
	if err := SignRouteRecord(&rec, priv); err != nil {
		t.Fatalf("SignRouteRecord: %v", err)
	}
	if !VerifyRouteRecord(rec) {
		t.Fatal("expected valid route record signature")
	}
	rec.RouteEpoch = 2
	if VerifyRouteRecord(rec) {
		t.Fatal("expected tampered route record to fail verification")
	}

	gPub, gPriv, err := GenerateEd25519Keypair()
	if err != nil {
		t.Fatalf("GenerateEd25519Keypair group: %v", err)
	}
	ev := MemberEvent{
		EventID:        NewULID(),
		GroupID:        "grp1",
		Kind:           MemberEventAdd,
		TargetDeviceID: "nodekey:abc",
		Epoch:          1,
		IssuerDeviceID: "nodekey:root",
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := SignMemberEvent(&ev, gPriv); err != nil {
		t.Fatalf("SignMemberEvent: %v", err)
	}
	if !VerifyMemberEvent(ev, gPub) {
		t.Fatal("expected valid member event signature")
	}
	ev.TargetDeviceID = "nodekey:evil"
	if VerifyMemberEvent(ev, gPub) {
		t.Fatal("expected tampered member event to fail verification")
	}
}

func TestPairingTokenRoundTrip(t *testing.T) {
	tok := PairingToken{
		Version:     1,
		GroupID:     "g1",
		HostDevID:   "nodekey:host",
		HostName:    "MacBook",
		TailcatAddr: "tcxyz",
		PairingPort: DefaultPairingPort,
		Secret:      "sec123",
		DERP:        DefaultDERPConfig(),
		ExpiresAt:   time.Now().Add(time.Minute).UnixMilli(),
	}
	code, err := tok.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodePairingToken(code)
	if err != nil {
		t.Fatalf("DecodePairingToken: %v", err)
	}
	if got.HostDevID != tok.HostDevID || got.Secret != tok.Secret || got.TailcatAddr != tok.TailcatAddr {
		t.Fatalf("mismatch: got %+v want %+v", got, tok)
	}
	if got.IsExpired(time.Now()) {
		t.Fatal("expected token not expired")
	}
}

func TestChunkBitmap(t *testing.T) {
	bm := NewChunkBitmap(10)
	bm = SetChunk(bm, 0)
	bm = SetChunk(bm, 3)
	bm = SetChunk(bm, 9)
	if !HasChunk(bm, 0) || !HasChunk(bm, 3) || !HasChunk(bm, 9) || HasChunk(bm, 4) {
		t.Fatal("unexpected chunk bitmap bits")
	}
	if got := CountChunks(bm, 10); got != 3 {
		t.Fatalf("CountChunks = %d, want 3", got)
	}
	missing := MissingChunks(bm, 10)
	if len(missing) != 7 {
		t.Fatalf("MissingChunks len = %d, want 7", len(missing))
	}
}
