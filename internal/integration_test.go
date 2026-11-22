package internal_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tailchat/internal/model"
	"tailchat/internal/store"
	"tailchat/internal/syncproto"
	"tailchat/internal/transport"
)

type testNode struct {
	name    string
	dataDir string
	st      *store.SQLiteStore
	cas     *store.FileCAS
	tr      *transport.MemTransport
	svc     *syncproto.Service
}

func startNode(t *testing.T, net *transport.MemNetwork, name, platform, dataDir string, autoDownloadBytes int64) *testNode {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dataDir, err)
	}
	st, err := store.Open(filepath.Join(dataDir, "tailchat.db"))
	if err != nil {
		t.Fatalf("store.Open(%s): %v", name, err)
	}
	cas, err := store.NewCAS(filepath.Join(dataDir, "cas"))
	if err != nil {
		t.Fatalf("store.NewCAS(%s): %v", name, err)
	}
	tr := net.NewTransport()
	svc, err := syncproto.NewService(st, cas, tr, syncproto.Options{
		Platform:          platform,
		DefaultDeviceName: name,
		DownloadDir:       filepath.Join(dataDir, "downloads"),
		IdentityGenerator: transport.GenerateLocalIdentity,
	})
	if err != nil {
		t.Fatalf("NewService(%s): %v", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start(%s): %v", name, err)
	}
	cfg := svc.Config()
	cfg.AutoDownloadMaxBytes = autoDownloadBytes
	if err := svc.UpdateConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateConfig(%s): %v", name, err)
	}
	return &testNode{
		name:    name,
		dataDir: dataDir,
		st:      st,
		cas:     cas,
		tr:      tr,
		svc:     svc,
	}
}

func (n *testNode) stop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := n.svc.Close(ctx); err != nil {
		t.Fatalf("Close(%s): %v", n.name, err)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

func TestFullStackThreeNodeEndToEnd(t *testing.T) {
	net := transport.NewMemNetwork()
	tmp := t.TempDir()

	// Node A (Mac desktop), Node B (Linux headless cache), Node C (iOS mobile, manual download)
	nodeA := startNode(t, net, "MacBook", "macos", filepath.Join(tmp, "nodeA"), model.DefaultAutoDownloadDesktopBytes)
	nodeB := startNode(t, net, "HomeNAS", "headless", filepath.Join(tmp, "nodeB"), 100*1024*1024)
	nodeC := startNode(t, net, "iPhone", "ios", filepath.Join(tmp, "nodeC"), 0)

	defer func() {
		if nodeA != nil {
			nodeA.stop(t)
		}
		if nodeB != nil {
			nodeB.stop(t)
		}
		if nodeC != nil {
			nodeC.stop(t)
		}
	}()

	ctx := context.Background()

	// 1. Pair B to A, then pair C to B -> A must auto-discover and admit C via signed MemberEvent gossip.
	codeAB, _, err := nodeA.svc.CreatePairingCode(ctx, 120)
	if err != nil {
		t.Fatalf("A CreatePairingCode: %v", err)
	}
	if err := nodeB.svc.JoinWithPairingCode(ctx, codeAB); err != nil {
		t.Fatalf("B JoinWithPairingCode: %v", err)
	}
	waitUntil(t, 5*time.Second, "A sees B", func() bool {
		devs, _ := nodeA.svc.ListDevices(false)
		return len(devs) == 2
	})

	codeBC, _, err := nodeB.svc.CreatePairingCode(ctx, 120)
	if err != nil {
		t.Fatalf("B CreatePairingCode: %v", err)
	}
	if err := nodeC.svc.JoinWithPairingCode(ctx, codeBC); err != nil {
		t.Fatalf("C JoinWithPairingCode: %v", err)
	}

	for _, n := range []*testNode{nodeA, nodeB, nodeC} {
		waitUntil(t, 5*time.Second, n.name+" sees all 3 devices", func() bool {
			devs, _ := n.svc.ListDevices(false)
			return len(devs) == 3
		})
	}

	// 2. Node A sends text + 2.3 MiB file (3 chunks).
	if _, err := nodeA.svc.SendText(ctx, "hello tailchat integration test"); err != nil {
		t.Fatalf("A SendText: %v", err)
	}

	payload := make([]byte, 2*1024*1024+300*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	srcFile := filepath.Join(tmp, "release-notes.bin")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileMsg, err := nodeA.svc.SendFile(ctx, srcFile)
	if err != nil {
		t.Fatalf("A SendFile: %v", err)
	}
	if fileMsg.File == nil || fileMsg.File.ChunkCount != 3 {
		t.Fatalf("expected 3-chunk file blob, got %+v", fileMsg.File)
	}

	// Node B (headless auto-download 100 MB) should automatically pull and assemble the file.
	waitUntil(t, 8*time.Second, "B auto-downloads file and syncs text", func() bool {
		msgs, _ := nodeB.svc.ListMessages(20, "")
		if len(msgs) < 2 {
			return false
		}
		task, _ := nodeB.svc.GetTransfer(fileMsg.BlobHash)
		return task != nil && task.State == model.TransferDone
	})

	// Verify FTS5 search works on Node B across SQLite.
	found, err := nodeB.svc.SearchMessages("tailchat", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("B SearchMessages expected 1 result, got %d (err=%v)", len(found), err)
	}

	// 3. Node A goes completely offline! Node C (mobile) must still be able to download the 2.3 MiB file from Node B (headless seeder).
	nodeA.stop(t)
	nodeA = nil

	waitUntil(t, 5*time.Second, "C sees file message metadata", func() bool {
		msgs, _ := nodeC.svc.ListMessages(20, "")
		return len(msgs) >= 2
	})

	if err := nodeC.svc.DownloadFile(ctx, fileMsg.BlobHash); err != nil {
		t.Fatalf("C DownloadFile from B seeder: %v", err)
	}
	waitUntil(t, 8*time.Second, "C completes download from B after A went offline", func() bool {
		task, _ := nodeC.svc.GetTransfer(fileMsg.BlobHash)
		return task != nil && task.State == model.TransferDone
	})

	// Verify downloaded file bytes on C match original payload bit-for-bit.
	cMsgs, err := nodeC.svc.ListMessages(20, "")
	if err != nil {
		t.Fatalf("C ListMessages: %v", err)
	}
	var cLocalPath string
	for _, m := range cMsgs {
		if m.BlobHash == fileMsg.BlobHash && m.File != nil {
			cLocalPath = m.File.LocalPath
		}
	}
	if cLocalPath == "" {
		t.Fatal("expected C file message to have populated LocalPath")
	}
	gotBytes, err := os.ReadFile(cLocalPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", cLocalPath, err)
	}
	if !bytes.Equal(gotBytes, payload) {
		t.Fatal("downloaded payload on C does not match original file bytes")
	}

	// 4. Restart Node A from persisted SQLite + CAS, verify it reloads identity & history and catches up with B/C.
	nodeA = startNode(t, net, "MacBook", "macos", filepath.Join(tmp, "nodeA"), model.DefaultAutoDownloadDesktopBytes)
	if _, err := nodeC.svc.SendText(ctx, "sent while MacBook was restarting"); err != nil {
		t.Fatalf("C SendText: %v", err)
	}
	waitUntil(t, 5*time.Second, "restarted A catches up message from C", func() bool {
		res, _ := nodeA.svc.SearchMessages("restarting", 10)
		return len(res) == 1
	})

	// 5. Hot-switch DERP on Node A -> RouteEpoch increments and propagates to B without losing membership.
	customDERP := model.DERPConfig{
		Mode:             model.DERPModeCustomRegion,
		RegionID:         901,
		CustomHost:       "derp.selfhosted.example",
		CustomDERPPort:   443,
		CustomSTUNPort:   3478,
		CustomRegionName: "HomeDERP",
	}
	if err := nodeA.svc.UpdateDERP(ctx, customDERP); err != nil {
		t.Fatalf("A UpdateDERP: %v", err)
	}
	aID := nodeA.svc.Identity().DeviceID
	waitUntil(t, 5*time.Second, "B receives updated DERP route epoch for A", func() bool {
		devs, _ := nodeB.svc.ListDevices(false)
		for _, d := range devs {
			if d.DeviceID == aID && d.Route.DERP.CustomHost == "derp.selfhosted.example" && d.RouteEpoch >= 2 {
				return true
			}
		}
		return false
	})

	// 6. Revoke C from A -> B marks C revoked.
	cID := nodeC.svc.Identity().DeviceID
	if err := nodeA.svc.RevokeDevice(ctx, cID); err != nil {
		t.Fatalf("A RevokeDevice(C): %v", err)
	}
	waitUntil(t, 5*time.Second, "B marks C revoked", func() bool {
		devs, _ := nodeB.svc.ListDevices(true)
		for _, d := range devs {
			if d.DeviceID == cID && d.Revoked {
				return true
			}
		}
		return false
	})
}
