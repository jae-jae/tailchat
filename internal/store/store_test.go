package store

import (
	"bytes"
	"crypto/rand"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tailchat/internal/model"
)

func openTestStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "nested", "tailchat.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dbPath, err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st, dbPath
}

func TestIdentityAndConfigPersistence(t *testing.T) {
	st, dbPath := openTestStore(t)

	// Unset state returns (nil, nil).
	id, err := st.LoadIdentity()
	if err != nil || id != nil {
		t.Fatalf("expected (nil, nil) identity, got (%+v, %v)", id, err)
	}
	cfg, err := st.LoadConfig()
	if err != nil || cfg != nil {
		t.Fatalf("expected (nil, nil) config, got (%+v, %v)", cfg, err)
	}

	wantID := model.LocalIdentity{
		DeviceID:        "nodekey:dev-1",
		NodePrivateKey:  "privkey:secret",
		PresharedKey:    "psk:123",
		SignPublicKey:   bytes.Repeat([]byte{0x01}, 32),
		SignPrivateKey:  bytes.Repeat([]byte{0x02}, 64),
		GroupID:         "grp-001",
		GroupPublicKey:  bytes.Repeat([]byte{0x03}, 32),
		GroupPrivateKey: bytes.Repeat([]byte{0x04}, 64),
		DeviceName:      "MacBook Pro",
		Platform:        "macos",
		RouteEpoch:      7,
	}
	if err := st.SaveIdentity(wantID); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	wantCfg := model.AppConfig{
		DeviceName:           "MacBook Pro",
		DERP:                 model.DefaultDERPConfig(),
		AutoDownloadMaxBytes: model.DefaultAutoDownloadDesktopBytes,
		MaxDownloadBytesPerS: 1024 * 1024,
		DownloadDir:          "/tmp/downloads",
		MinimizeToTray:       true,
		GlobalHotkey:         "Cmd+Shift+T",
	}
	if err := st.SaveConfig(wantCfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// Reopen database to verify true disk persistence.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	gotID, err := st2.LoadIdentity()
	if err != nil || gotID == nil {
		t.Fatalf("LoadIdentity after reopen: (%+v, %v)", gotID, err)
	}
	if gotID.DeviceID != wantID.DeviceID || gotID.RouteEpoch != wantID.RouteEpoch || !bytes.Equal(gotID.SignPublicKey, wantID.SignPublicKey) {
		t.Fatalf("identity mismatch: got %+v, want %+v", gotID, wantID)
	}

	gotCfg, err := st2.LoadConfig()
	if err != nil || gotCfg == nil {
		t.Fatalf("LoadConfig after reopen: (%+v, %v)", gotCfg, err)
	}
	if gotCfg.DeviceName != wantCfg.DeviceName || gotCfg.DERP.RegionID != wantCfg.DERP.RegionID || !gotCfg.MinimizeToTray {
		t.Fatalf("config mismatch: got %+v, want %+v", gotCfg, wantCfg)
	}
}

func TestDevicesAndMemberEvents(t *testing.T) {
	st, _ := openTestStore(t)

	if dev, err := st.GetDevice("missing"); err != nil || dev != nil {
		t.Fatalf("expected (nil, nil) for missing device, got (%+v, %v)", dev, err)
	}

	devSelf := model.Device{
		DeviceID:      "dev-self",
		Name:          "Self Mac",
		Platform:      "macos",
		SignPublicKey: bytes.Repeat([]byte{0xAA}, 32),
		Route: model.RouteRecord{
			DeviceID:    "dev-self",
			TailcatAddr: "tc-self",
			DERP:        model.DefaultDERPConfig(),
			RouteEpoch:  1,
		},
		RouteEpoch: 1,
		AddedAt:    1000,
		LastSeenAt: 2000,
		IsSelf:     true,
	}
	devPeer := model.Device{
		DeviceID:   "dev-peer",
		Name:       "iPhone",
		Platform:   "ios",
		RouteEpoch: 2,
		AddedAt:    1500,
	}
	devRevoked := model.Device{
		DeviceID:   "dev-old",
		Name:       "Old Laptop",
		Platform:   "linux",
		Revoked:    true,
		RouteEpoch: 3,
		AddedAt:    1200,
	}

	for _, d := range []model.Device{devSelf, devPeer, devRevoked} {
		if err := st.UpsertDevice(d); err != nil {
			t.Fatalf("UpsertDevice(%s): %v", d.DeviceID, err)
		}
	}

	// Update peer name and route.
	devPeer.Name = "iPhone 16"
	devPeer.Route.TailcatAddr = "tc-iphone-new"
	devPeer.RouteEpoch = 5
	if err := st.UpsertDevice(devPeer); err != nil {
		t.Fatalf("UpsertDevice update: %v", err)
	}

	gotPeer, err := st.GetDevice("dev-peer")
	if err != nil || gotPeer == nil {
		t.Fatalf("GetDevice: (%+v, %v)", gotPeer, err)
	}
	if gotPeer.Name != "iPhone 16" || gotPeer.Route.TailcatAddr != "tc-iphone-new" || gotPeer.RouteEpoch != 5 {
		t.Fatalf("updated device mismatch: %+v", gotPeer)
	}

	active, err := st.ListDevices(false)
	if err != nil {
		t.Fatalf("ListDevices(false): %v", err)
	}
	if len(active) != 2 || !active[0].IsSelf || active[1].DeviceID != "dev-peer" {
		t.Fatalf("unexpected active devices: %+v", active)
	}

	all, err := st.ListDevices(true)
	if err != nil {
		t.Fatalf("ListDevices(true): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 devices including revoked, got %d", len(all))
	}

	// Member events dedupe and ordering.
	ev1 := model.MemberEvent{
		EventID:        "01EV0001",
		GroupID:        "grp-1",
		Kind:           model.MemberEventAdd,
		TargetDeviceID: "dev-peer",
		DeviceName:     "iPhone",
		Epoch:          1,
		CreatedAt:      100,
	}
	ev2 := model.MemberEvent{
		EventID:        "01EV0002",
		GroupID:        "grp-1",
		Kind:           model.MemberEventRevoke,
		TargetDeviceID: "dev-old",
		Epoch:          2,
		CreatedAt:      200,
	}

	inserted, err := st.SaveMemberEvent(ev1)
	if err != nil || !inserted {
		t.Fatalf("first SaveMemberEvent(ev1): inserted=%v err=%v", inserted, err)
	}
	inserted, err = st.SaveMemberEvent(ev1)
	if err != nil || inserted {
		t.Fatalf("duplicate SaveMemberEvent(ev1): inserted=%v err=%v", inserted, err)
	}
	if inserted, err := st.SaveMemberEvent(ev2); err != nil || !inserted {
		t.Fatalf("SaveMemberEvent(ev2): inserted=%v err=%v", inserted, err)
	}

	evs, err := st.ListMemberEvents()
	if err != nil {
		t.Fatalf("ListMemberEvents: %v", err)
	}
	if len(evs) != 2 || evs[0].EventID != "01EV0001" || evs[1].EventID != "01EV0002" {
		t.Fatalf("unexpected member events: %+v", evs)
	}
}

func TestMessagesPaginationTombstonesAndVectorClock(t *testing.T) {
	st, _ := openTestStore(t)

	// Concurrent NextAuthorSeq must produce unique monotonic values.
	const nConcurrent = 10
	var wg sync.WaitGroup
	seqs := make(chan uint64, nConcurrent)
	for i := 0; i < nConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := st.NextAuthorSeq("dev-concurrent")
			if err != nil {
				t.Errorf("NextAuthorSeq: %v", err)
				return
			}
			seqs <- s
		}()
	}
	wg.Wait()
	close(seqs)

	seenSeq := make(map[uint64]bool)
	for s := range seqs {
		if seenSeq[s] {
			t.Fatalf("duplicate seq %d generated concurrently", s)
		}
		seenSeq[s] = true
	}
	if len(seenSeq) != nConcurrent {
		t.Fatalf("expected %d unique seqs, got %d", nConcurrent, len(seenSeq))
	}

	// Empty vector clock should be non-nil empty map.
	vc, err := st.VectorClock()
	if err != nil || vc == nil || len(vc) != 0 {
		t.Fatalf("expected empty non-nil vector clock, got (%+v, %v)", vc, err)
	}

	blobHash := model.Blake3Hex([]byte("report content"))
	fileBlob := &model.FileBlob{
		BlobHash:    blobHash,
		FileName:    "quarterly_report.pdf",
		FileSize:    14,
		MimeType:    "application/pdf",
		ChunkSize:   model.DefaultChunkSize,
		ChunkCount:  1,
		ChunkHashes: []string{blobHash},
		Seeders:     []string{"dev-A"},
	}

	msgs := []model.Message{
		{MsgID: "01MSG0001", AuthorDeviceID: "dev-A", AuthorName: "Mac", AuthorSeq: 1, Kind: model.MessageKindText, TextBody: "first message", CreatedAt: 1001},
		{MsgID: "01MSG0002", AuthorDeviceID: "dev-A", AuthorName: "Mac", AuthorSeq: 2, Kind: model.MessageKindText, TextBody: "secret to delete", CreatedAt: 1002},
		{MsgID: "01MSG0003", AuthorDeviceID: "dev-B", AuthorName: "Phone", AuthorSeq: 1, Kind: model.MessageKindFile, TextBody: "check pdf", BlobHash: blobHash, File: fileBlob, CreatedAt: 1003},
		{MsgID: "01MSG0004", AuthorDeviceID: "dev-B", AuthorName: "Phone", AuthorSeq: 2, Kind: model.MessageKindText, TextBody: "fourth message", CreatedAt: 1004},
		{MsgID: "01MSG0005", AuthorDeviceID: "dev-A", AuthorName: "Mac", AuthorSeq: 3, Kind: model.MessageKindText, TextBody: "fifth message", CreatedAt: 1005},
		{MsgID: "01MSG0006", AuthorDeviceID: "dev-A", AuthorName: "Mac", AuthorSeq: 4, Kind: model.MessageKindTombstone, TargetMsgID: "01MSG0002", CreatedAt: 1006},
	}

	for _, m := range msgs {
		ins, err := st.SaveMessage(m)
		if err != nil || !ins {
			t.Fatalf("SaveMessage(%s): inserted=%v err=%v", m.MsgID, ins, err)
		}
	}
	// Duplicate insert must return inserted=false.
	if ins, err := st.SaveMessage(msgs[0]); err != nil || ins {
		t.Fatalf("duplicate SaveMessage: inserted=%v err=%v", ins, err)
	}

	// GetMessage hydrates FileBlob.
	gotFileMsg, err := st.GetMessage("01MSG0003")
	if err != nil || gotFileMsg == nil || gotFileMsg.File == nil {
		t.Fatalf("GetMessage(01MSG0003) failed to hydrate file: (%+v, %v)", gotFileMsg, err)
	}
	if gotFileMsg.File.FileName != "quarterly_report.pdf" || len(gotFileMsg.File.Seeders) != 1 {
		t.Fatalf("hydrated file mismatch: %+v", gotFileMsg.File)
	}

	// ListMessages excludes both the tombstoned message (01MSG0002) and the tombstone (01MSG0006),
	// returning newest-capped messages in ascending chronological order.
	page1, err := st.ListMessages(2, "")
	if err != nil {
		t.Fatalf("ListMessages page1: %v", err)
	}
	if len(page1) != 2 || page1[0].MsgID != "01MSG0004" || page1[1].MsgID != "01MSG0005" {
		t.Fatalf("unexpected page1: %+v", page1)
	}

	// Paginate older messages using beforeMsgID.
	page2, err := st.ListMessages(2, page1[0].MsgID)
	if err != nil {
		t.Fatalf("ListMessages page2: %v", err)
	}
	if len(page2) != 2 || page2[0].MsgID != "01MSG0001" || page2[1].MsgID != "01MSG0003" {
		t.Fatalf("unexpected page2 (should skip tombstoned 01MSG0002): %+v", page2)
	}
	if page2[1].File == nil || page2[1].File.FileName != "quarterly_report.pdf" {
		t.Fatalf("ListMessages did not hydrate File: %+v", page2[1])
	}

	// VectorClock reflects highest seq per author including tombstones.
	vc, err = st.VectorClock()
	if err != nil {
		t.Fatalf("VectorClock: %v", err)
	}
	if vc["dev-A"] != 4 || vc["dev-B"] != 2 {
		t.Fatalf("unexpected VectorClock: %+v", vc)
	}

	// MessagesAfterClock MUST include tombstones so deletes propagate across devices!
	delta, err := st.MessagesAfterClock(model.VectorClock{"dev-A": 2, "dev-B": 2}, 10)
	if err != nil {
		t.Fatalf("MessagesAfterClock: %v", err)
	}
	if len(delta) != 2 || delta[0].MsgID != "01MSG0005" || delta[1].MsgID != "01MSG0006" || delta[1].Kind != model.MessageKindTombstone {
		t.Fatalf("unexpected MessagesAfterClock delta: %+v", delta)
	}

	// Partial vector clock missing dev-B should return all of dev-B's messages + newer dev-A messages.
	partialDelta, err := st.MessagesAfterClock(model.VectorClock{"dev-A": 3}, 10)
	if err != nil {
		t.Fatalf("MessagesAfterClock partial: %v", err)
	}
	if len(partialDelta) != 3 || partialDelta[0].MsgID != "01MSG0003" || partialDelta[1].MsgID != "01MSG0004" || partialDelta[2].MsgID != "01MSG0006" {
		t.Fatalf("unexpected partial delta: %+v", partialDelta)
	}
	if partialDelta[0].File == nil || partialDelta[0].File.BlobHash != blobHash {
		t.Fatalf("MessagesAfterClock did not hydrate File: %+v", partialDelta[0])
	}
}

func TestSearchMessagesFTSAndSpecialChars(t *testing.T) {
	st, _ := openTestStore(t)

	hash1 := model.Blake3Hex([]byte("design spec"))
	hash2 := model.Blake3Hex([]byte("late blob"))

	entries := []model.Message{
		{MsgID: "01S0001", AuthorDeviceID: "d1", AuthorName: "A", AuthorSeq: 1, Kind: model.MessageKindText, TextBody: "Deploy tailchat gateway to Tokyo DERP", CreatedAt: 1},
		{MsgID: "01S0002", AuthorDeviceID: "d1", AuthorName: "A", AuthorSeq: 2, Kind: model.MessageKindText, TextBody: "Use C++20 with foo-bar and 100%_exact match", CreatedAt: 2},
		{MsgID: "01S0003", AuthorDeviceID: "d1", AuthorName: "A", AuthorSeq: 3, Kind: model.MessageKindFile, TextBody: "attached mockup", BlobHash: hash1, File: &model.FileBlob{
			BlobHash: hash1, FileName: "architecture_v2.png", FileSize: 11, MimeType: "image/png",
		}, CreatedAt: 3},
		{MsgID: "01S0004", AuthorDeviceID: "d1", AuthorName: "A", AuthorSeq: 4, Kind: model.MessageKindText, TextBody: "deleted secret Tokyo note", CreatedAt: 4},
		{MsgID: "01S0005", AuthorDeviceID: "d1", AuthorName: "A", AuthorSeq: 5, Kind: model.MessageKindTombstone, TargetMsgID: "01S0004", CreatedAt: 5},
		{MsgID: "01S0006", AuthorDeviceID: "d1", AuthorName: "A", AuthorSeq: 6, Kind: model.MessageKindFile, TextBody: "late metadata file", BlobHash: hash2, CreatedAt: 6},
	}
	for _, m := range entries {
		if _, err := st.SaveMessage(m); err != nil {
			t.Fatalf("SaveMessage(%s): %v", m.MsgID, err)
		}
	}

	// Upsert file blob AFTER message save -> FTS index must sync file_name.
	if err := st.UpsertFileBlob(model.FileBlob{
		BlobHash: hash2,
		FileName: "invoice_2026_final.pdf",
		FileSize: 9,
		MimeType: "application/pdf",
	}); err != nil {
		t.Fatalf("UpsertFileBlob: %v", err)
	}

	// Standard FTS5 query matches non-tombstoned message only.
	res, err := st.SearchMessages("Tokyo", 10)
	if err != nil {
		t.Fatalf("SearchMessages(Tokyo): %v", err)
	}
	if len(res) != 1 || res[0].MsgID != "01S0001" {
		t.Fatalf("expected only 01S0001 (01S0004 is tombstoned), got %+v", res)
	}

	// Search by file_name works via FTS5 and hydrates File.
	res, err = st.SearchMessages("architecture_v2", 10)
	if err != nil {
		t.Fatalf("SearchMessages(architecture_v2): %v", err)
	}
	if len(res) != 1 || res[0].MsgID != "01S0003" || res[0].File == nil {
		t.Fatalf("expected hydrated 01S0003, got %+v", res)
	}

	// Search by late-upserted file_name works.
	res, err = st.SearchMessages("invoice_2026_final", 10)
	if err != nil {
		t.Fatalf("SearchMessages(invoice_2026_final): %v", err)
	}
	if len(res) != 1 || res[0].MsgID != "01S0006" {
		t.Fatalf("expected 01S0006 after UpsertFileBlob FTS sync, got %+v", res)
	}

	// Queries with FTS5 syntax chars must NOT error and must match via graceful LIKE fallback.
	specialQueries := []struct {
		query  string
		wantID string
	}{
		{"C++20", "01S0002"},
		{"foo-bar", "01S0002"},
		{"100%_exact", "01S0002"},
		{"architecture_v2.png", "01S0003"},
		{`"unbalanced quote`, ""},
		{`(a:b) OR *`, ""},
	}
	for _, tc := range specialQueries {
		got, err := st.SearchMessages(tc.query, 10)
		if err != nil {
			t.Fatalf("SearchMessages(%q) errored: %v", tc.query, err)
		}
		if tc.wantID == "" {
			if len(got) != 0 {
				t.Fatalf("SearchMessages(%q) expected 0 hits, got %+v", tc.query, got)
			}
		} else if len(got) != 1 || got[0].MsgID != tc.wantID {
			t.Fatalf("SearchMessages(%q) expected %s, got %+v", tc.query, tc.wantID, got)
		}
	}
}

func TestBlobSeedersAndTransferTasks(t *testing.T) {
	st, _ := openTestStore(t)

	h := model.Blake3Hex([]byte("blob-data"))
	if err := st.UpsertFileBlob(model.FileBlob{
		BlobHash:   h,
		FileName:   "video.mp4",
		FileSize:   1024,
		MimeType:   "video/mp4",
		ChunkSize:  model.DefaultChunkSize,
		ChunkCount: 1,
		LocalPath:  "/cas/blobs/xx/yy",
		Seeders:    []string{"dev-1"},
	}); err != nil {
		t.Fatalf("UpsertFileBlob: %v", err)
	}

	// Upserting remote metadata without LocalPath must preserve LocalPath and merge seeders.
	if err := st.UpsertFileBlob(model.FileBlob{
		BlobHash:   h,
		FileName:   "video.mp4",
		FileSize:   1024,
		ChunkCount: 1,
		Seeders:    []string{"dev-2"},
	}); err != nil {
		t.Fatalf("UpsertFileBlob merge: %v", err)
	}

	if err := st.AddBlobSeeder(h, "dev-1"); err != nil {
		t.Fatalf("AddBlobSeeder dup: %v", err)
	}
	if err := st.AddBlobSeeder(h, "dev-3"); err != nil {
		t.Fatalf("AddBlobSeeder new: %v", err)
	}

	blob, err := st.GetFileBlob(h)
	if err != nil || blob == nil {
		t.Fatalf("GetFileBlob: (%+v, %v)", blob, err)
	}
	if blob.LocalPath != "/cas/blobs/xx/yy" {
		t.Fatalf("LocalPath was overwritten: %q", blob.LocalPath)
	}
	if len(blob.Seeders) != 3 {
		t.Fatalf("expected 3 deduplicated seeders, got %v", blob.Seeders)
	}

	// Transfer tasks round-trip.
	bm := model.NewChunkBitmap(5)
	bm = model.SetChunk(bm, 0)
	bm = model.SetChunk(bm, 3)

	task := model.TransferTask{
		BlobHash:    h,
		Direction:   model.TransferDownload,
		State:       model.TransferRunning,
		ChunkCount:  5,
		ChunkBitmap: bm,
		BytesDone:   2048,
		BytesTotal:  5120,
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if err := st.UpsertTransferTask(task); err != nil {
		t.Fatalf("UpsertTransferTask: %v", err)
	}
	gotTask, err := st.GetTransferTask(h)
	if err != nil || gotTask == nil {
		t.Fatalf("GetTransferTask: (%+v, %v)", gotTask, err)
	}
	if !model.HasChunk(gotTask.ChunkBitmap, 0) || model.HasChunk(gotTask.ChunkBitmap, 1) || !model.HasChunk(gotTask.ChunkBitmap, 3) {
		t.Fatalf("chunk bitmap did not round-trip: %x", gotTask.ChunkBitmap)
	}

	tasks, err := st.ListTransferTasks()
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTransferTasks: (%+v, %v)", tasks, err)
	}
}

func TestCASIngestMultiChunkAndDeleteSource(t *testing.T) {
	casDir := filepath.Join(t.TempDir(), "cas")
	cas, err := NewCAS(casDir)
	if err != nil {
		t.Fatalf("NewCAS: %v", err)
	}

	// Create a >2.5 MiB random payload (3 chunks: 1 MiB, 1 MiB, 512 KiB + 123 bytes).
	size := 2*model.DefaultChunkSize + 512*1024 + 123
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	srcPath := filepath.Join(t.TempDir(), "archive.bin")
	if err := os.WriteFile(srcPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	blob, err := cas.IngestFile(srcPath)
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if blob.FileSize != int64(size) {
		t.Fatalf("FileSize = %d, want %d", blob.FileSize, size)
	}
	if blob.ChunkCount != 3 || len(blob.ChunkHashes) != 3 {
		t.Fatalf("ChunkCount = %d (hashes=%d), want 3", blob.ChunkCount, len(blob.ChunkHashes))
	}
	if wantHash := model.Blake3Hex(payload); blob.BlobHash != wantHash {
		t.Fatalf("BlobHash = %s, want %s", blob.BlobHash, wantHash)
	}
	if p, ok := cas.HasCompleteBlob(blob.BlobHash); !ok || p == "" {
		t.Fatalf("HasCompleteBlob returned (%q, %v)", p, ok)
	}

	// Delete the original source file and confirm ReadChunk still returns every chunk accurately.
	if err := os.Remove(srcPath); err != nil {
		t.Fatalf("Remove source: %v", err)
	}

	var reassembled []byte
	for i := 0; i < blob.ChunkCount; i++ {
		chunk, err := cas.ReadChunk(blob.BlobHash, i)
		if err != nil {
			t.Fatalf("ReadChunk(%d): %v", i, err)
		}
		if model.Blake3Hex(chunk) != blob.ChunkHashes[i] {
			t.Fatalf("chunk %d hash mismatch", i)
		}
		reassembled = append(reassembled, chunk...)
	}
	if !bytes.Equal(reassembled, payload) {
		t.Fatalf("reassembled chunks differ from original payload")
	}
}

func TestCASZeroByteFile(t *testing.T) {
	cas, err := NewCAS(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("NewCAS: %v", err)
	}

	emptyFile := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	blob, err := cas.IngestFile(emptyFile)
	if err != nil {
		t.Fatalf("IngestFile(empty): %v", err)
	}
	emptyHash := model.Blake3Hex(nil)
	if blob.BlobHash != emptyHash || blob.FileSize != 0 || blob.ChunkCount != 0 || len(blob.ChunkHashes) != 0 {
		t.Fatalf("unexpected empty FileBlob: %+v", blob)
	}
	if _, ok := cas.HasCompleteBlob(emptyHash); !ok {
		t.Fatalf("HasCompleteBlob(empty) should be true")
	}

	// Also verify IngestBytes + AssembleFile on 0-byte blob on a fresh receiver CAS.
	recvCAS, err := NewCAS(filepath.Join(t.TempDir(), "recv-cas"))
	if err != nil {
		t.Fatalf("NewCAS recv: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), "downloads")
	outPath, err := recvCAS.AssembleFile(*blob, outDir)
	if err != nil {
		t.Fatalf("AssembleFile(empty): %v", err)
	}
	gotBytes, err := os.ReadFile(outPath)
	if err != nil || len(gotBytes) != 0 {
		t.Fatalf("assembled empty file read: len=%d err=%v", len(gotBytes), err)
	}

	blob2, err := recvCAS.IngestBytes("zero.dat", "", nil)
	if err != nil || blob2.BlobHash != emptyHash || blob2.ChunkCount != 0 {
		t.Fatalf("IngestBytes(empty): (%+v, %v)", blob2, err)
	}
}

func TestCASThumbnailGeneration(t *testing.T) {
	cas, err := NewCAS(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("NewCAS: %v", err)
	}

	// Generate a 600x400 noisy PNG (high entropy forces scaling down to stay <= 32KB).
	img := image.NewNRGBA(image.Rect(0, 0, 600, 400))
	noise := make([]byte, 600*400*3)
	_, _ = rand.Read(noise)
	for y := 0; y < 400; y++ {
		for x := 0; x < 600; x++ {
			off := (y*600 + x) * 3
			img.SetNRGBA(x, y, color.NRGBA{R: noise[off], G: noise[off+1], B: noise[off+2], A: 255})
		}
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	pngPath := filepath.Join(t.TempDir(), "noisy.png")
	if err := os.WriteFile(pngPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile png: %v", err)
	}

	blob, err := cas.IngestFile(pngPath)
	if err != nil {
		t.Fatalf("IngestFile(png): %v", err)
	}
	if len(blob.ThumbnailWebP) == 0 {
		t.Fatalf("expected non-empty PNG thumbnail")
	}
	if len(blob.ThumbnailWebP) > model.MaxThumbnailBytes {
		t.Fatalf("thumbnail size %d exceeds MaxThumbnailBytes %d", len(blob.ThumbnailWebP), model.MaxThumbnailBytes)
	}
	thumbImg, err := png.Decode(bytes.NewReader(blob.ThumbnailWebP))
	if err != nil {
		t.Fatalf("thumbnail is not valid PNG: %v", err)
	}
	if thumbImg.Bounds().Dx() > 256 || thumbImg.Bounds().Dy() > 256 {
		t.Fatalf("thumbnail dimensions too large: %v", thumbImg.Bounds())
	}

	// Also verify JPEG via IngestBytes.
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	jpgBlob, err := cas.IngestBytes("photo.jpg", "image/jpeg", jpgBuf.Bytes())
	if err != nil {
		t.Fatalf("IngestBytes(jpg): %v", err)
	}
	if len(jpgBlob.ThumbnailWebP) == 0 || len(jpgBlob.ThumbnailWebP) > model.MaxThumbnailBytes {
		t.Fatalf("unexpected jpg thumbnail len: %d", len(jpgBlob.ThumbnailWebP))
	}
}

func TestCASOutOfOrderWriteChunkVerificationAndSanitization(t *testing.T) {
	senderCAS, err := NewCAS(filepath.Join(t.TempDir(), "sender"))
	if err != nil {
		t.Fatalf("NewCAS sender: %v", err)
	}
	recvCAS, err := NewCAS(filepath.Join(t.TempDir(), "recv"))
	if err != nil {
		t.Fatalf("NewCAS recv: %v", err)
	}

	// Prepare a 2.3 MiB payload (3 chunks).
	payload := make([]byte, 2*model.DefaultChunkSize+300*1024)
	_, _ = rand.Read(payload)

	blob, err := senderCAS.IngestBytes("photo.png", "application/octet-stream", payload)
	if err != nil {
		t.Fatalf("IngestBytes: %v", err)
	}

	// Chunk hash mismatch must be rejected without creating the chunk file.
	chunk0, err := senderCAS.ReadChunk(blob.BlobHash, 0)
	if err != nil {
		t.Fatalf("ReadChunk(0): %v", err)
	}
	corrupted := append([]byte(nil), chunk0...)
	corrupted[0] ^= 0xFF
	if err := recvCAS.WriteChunk(blob.BlobHash, 0, blob.ChunkHashes[0], corrupted); err == nil {
		t.Fatalf("expected WriteChunk to reject corrupted data")
	}
	if _, err := recvCAS.ReadChunk(blob.BlobHash, 0); err == nil {
		t.Fatalf("corrupted chunk must not be persisted on disk")
	}

	// Write chunks out-of-order: 2, 0, 1.
	for _, idx := range []int{2, 0, 1} {
		data, err := senderCAS.ReadChunk(blob.BlobHash, idx)
		if err != nil {
			t.Fatalf("ReadChunk(%d): %v", idx, err)
		}
		if err := recvCAS.WriteChunk(blob.BlobHash, idx, blob.ChunkHashes[idx], data); err != nil {
			t.Fatalf("WriteChunk(%d): %v", idx, err)
		}
	}

	// Assemble with path-traversal malicious filename -> must stay strictly inside outputDir.
	outDir := filepath.Join(t.TempDir(), "downloads")
	traversalBlob := *blob
	traversalBlob.FileName = "../../etc/passwd"
	outPath1, err := recvCAS.AssembleFile(traversalBlob, outDir)
	if err != nil {
		t.Fatalf("AssembleFile(traversal): %v", err)
	}
	if filepath.Dir(outPath1) != filepath.Clean(outDir) || filepath.Base(outPath1) != "passwd" {
		t.Fatalf("path traversal not sanitized: got %q (outDir=%q)", outPath1, outDir)
	}
	got1, err := os.ReadFile(outPath1)
	if err != nil || !bytes.Equal(got1, payload) {
		t.Fatalf("assembled file content mismatch")
	}
	if _, ok := recvCAS.HasCompleteBlob(blob.BlobHash); !ok {
		t.Fatalf("HasCompleteBlob should be true after AssembleFile")
	}

	// Windows-style traversal & NUL/control chars.
	winBlob := *blob
	winBlob.FileName = `..\..\evil\..\photo` + "\x00" + `.png`
	outPathWin, err := recvCAS.AssembleFile(winBlob, outDir)
	if err != nil {
		t.Fatalf("AssembleFile(win): %v", err)
	}
	if filepath.Dir(outPathWin) != filepath.Clean(outDir) || filepath.Base(outPathWin) != "photo.png" {
		t.Fatalf("unexpected windows sanitized path: %q", outPathWin)
	}

	// Collision deduplication: photo.png -> photo (1).png -> photo (2).png.
	dedupBlob := *blob
	dedupBlob.FileName = "photo.png"
	outPathDup1, err := recvCAS.AssembleFile(dedupBlob, outDir)
	if err != nil {
		t.Fatalf("AssembleFile(dup1): %v", err)
	}
	if filepath.Base(outPathDup1) != "photo (1).png" {
		t.Fatalf("expected 'photo (1).png', got %q", filepath.Base(outPathDup1))
	}
	outPathDup2, err := recvCAS.AssembleFile(dedupBlob, outDir)
	if err != nil {
		t.Fatalf("AssembleFile(dup2): %v", err)
	}
	if filepath.Base(outPathDup2) != "photo (2).png" {
		t.Fatalf("expected 'photo (2).png', got %q", filepath.Base(outPathDup2))
	}

	// Degenerate ".." filename falls back safely.
	dotBlob := *blob
	dotBlob.FileName = "../.."
	outPathDot, err := recvCAS.AssembleFile(dotBlob, outDir)
	if err != nil {
		t.Fatalf("AssembleFile(..): %v", err)
	}
	if filepath.Dir(outPathDot) != filepath.Clean(outDir) || !strings.HasPrefix(filepath.Base(outPathDot), "file-") {
		t.Fatalf("unexpected fallback path for '..': %q", outPathDot)
	}

	// AssembleFile with all valid chunks written under a mismatched root hash must fail
	// whole-file BLAKE3 verification and leave no complete blob behind.
	fakeRootHash := strings.Repeat("a", 64)
	for idx := 0; idx < blob.ChunkCount; idx++ {
		data, err := senderCAS.ReadChunk(blob.BlobHash, idx)
		if err != nil {
			t.Fatalf("ReadChunk(%d): %v", idx, err)
		}
		if err := recvCAS.WriteChunk(fakeRootHash, idx, blob.ChunkHashes[idx], data); err != nil {
			t.Fatalf("WriteChunk(fakeRoot, %d): %v", idx, err)
		}
	}
	badRootBlob := *blob
	badRootBlob.BlobHash = fakeRootHash
	if _, err := recvCAS.AssembleFile(badRootBlob, outDir); err == nil || !strings.Contains(err.Error(), "root hash mismatch") {
		t.Fatalf("expected root hash mismatch error, got: %v", err)
	}
	if _, ok := recvCAS.HasCompleteBlob(fakeRootHash); ok {
		t.Fatalf("HasCompleteBlob must remain false after failed AssembleFile")
	}
}
