package syncproto

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"tailchat/internal/model"
)

const (
	msgBatchLimit       = 50
	maxSyncMessagesOnce = 2000
	streamIOTimeout     = 15 * time.Second
)

// goBackground safely spawns a goroutine tracked by s.wg without racing Close().
func (s *Service) goBackground(fn func(ctx context.Context)) {
	s.mu.RLock()
	if !s.started || s.closed || s.ctx == nil {
		s.mu.RUnlock()
		return
	}
	ctx := s.ctx
	s.wg.Add(1)
	s.mu.RUnlock()

	go func() {
		defer s.wg.Done()
		fn(ctx)
	}()
}

func (s *Service) signalResume() {
	select {
	case s.resumeWakeCh <- struct{}{}:
	default:
	}
}

func (s *Service) antiEntropyLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.opts.AntiEntropyInterval)
	defer ticker.Stop()

	// Check for any queued/interrupted transfers shortly after start.
	s.resumePendingTransfers(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.resumeWakeCh:
			s.resumePendingTransfers(ctx)
		case <-ticker.C:
			s.syncAllPeers(ctx)
			s.resumePendingTransfers(ctx)
		}
	}
}

func (s *Service) handlePeerStatus(status model.PeerConnStatus) {
	if status.DeviceID == "" {
		return
	}

	s.peerMu.Lock()
	wasConnected := s.connectedPeers[status.DeviceID]
	s.connectedPeers[status.DeviceID] = status.Connected
	s.peerMu.Unlock()

	stCopy := status
	s.emit(model.AppEvent{
		Type: model.EventPeerStatus,
		Peer: &stCopy,
	})

	if status.Connected {
		s.memberMu.Lock()
		allowed := false
		if dev, err := s.store.GetDevice(status.DeviceID); err == nil && dev != nil && !dev.Revoked {
			dev.LastSeenAt = time.Now().UnixMilli()
			_ = s.store.UpsertDevice(*dev)
			allowed = true
		}
		s.memberMu.Unlock()

		if allowed && !wasConnected {
			peerID := status.DeviceID
			s.goBackground(func(ctx context.Context) {
				s.syncWithPeer(ctx, peerID)
			})
			s.signalResume()
		}
	}
}

func (s *Service) handleStream(ctx context.Context, peerDeviceID string, kind model.StreamKind, stream io.ReadWriteCloser) {
	defer stream.Close()

	if err := s.requireStarted(); err != nil {
		return
	}
	if !s.isPeerAllowed(peerDeviceID) {
		return
	}

	switch kind {
	case model.StreamKindControl:
		s.handleControlStream(ctx, peerDeviceID, stream)
	case model.StreamKindChunkData:
		s.handleChunkStream(ctx, peerDeviceID, stream)
	}
}

func (s *Service) handleControlStream(ctx context.Context, peerDeviceID string, stream io.ReadWriteCloser) {
	for {
		var frame ControlFrame
		if err := ReadFrame(stream, &frame); err != nil {
			return
		}
		if !s.isPeerAllowed(peerDeviceID) {
			return
		}

		switch frame.Type {
		case FrameHello:
			if frame.Hello == nil {
				return
			}
			s.handleHelloFrame(ctx, peerDeviceID, frame.Hello, stream)
			// Close stream cleanly after responding to Hello so the initiator's ReadFrame loop gets EOF.
			return

		case FrameMemberEvents:
			if frame.MemberEvents != nil && len(frame.MemberEvents.Events) > 0 {
				s.applyMemberEvents(ctx, frame.MemberEvents.Events, peerDeviceID)
			}

		case FrameMsgSyncRequest:
			if frame.MsgSyncRequest == nil {
				return
			}
			s.writeMessagesAfterClock(stream, frame.MsgSyncRequest.VectorClock)
			return

		case FrameMsgBatch:
			if frame.MsgBatch != nil && len(frame.MsgBatch.Messages) > 0 {
				s.ingestRemoteMessages(frame.MsgBatch.Messages, peerDeviceID)
			}

		case FrameBlobHave:
			if frame.BlobHave != nil && frame.BlobHave.BlobHash != "" {
				_ = s.store.AddBlobSeeder(frame.BlobHave.BlobHash, peerDeviceID)
				s.signalResume()
			}
		}
	}
}

func (s *Service) handleHelloFrame(ctx context.Context, peerDeviceID string, hello *HelloPayload, stream io.ReadWriteCloser) {
	id := s.Identity()
	if hello.GroupID != id.GroupID {
		return
	}

	// Update peer LastSeenAt and Route if newer and validly signed.
	s.memberMu.Lock()
	dev, err := s.store.GetDevice(peerDeviceID)
	if err != nil || dev == nil || dev.Revoked {
		s.memberMu.Unlock()
		return
	}
	dev.LastSeenAt = time.Now().UnixMilli()
	if hello.Route.DeviceID == peerDeviceID &&
		bytes.Equal(hello.Route.SignPublicKey, dev.SignPublicKey) &&
		model.VerifyRouteRecord(hello.Route) &&
		hello.Route.RouteEpoch > dev.RouteEpoch {
		dev.Route = hello.Route
		dev.RouteEpoch = hello.Route.RouteEpoch
	}
	_ = s.store.UpsertDevice(*dev)
	s.memberMu.Unlock()

	// Send member events this peer doesn't have yet.
	missingEvents := s.missingEventsFor(id.GroupID, hello.MemberEventIDs)
	if len(missingEvents) > 0 {
		_ = WriteFrame(stream, ControlFrame{
			Type: FrameMemberEvents,
			MemberEvents: &MemberEventsPayload{
				Events: missingEvents,
			},
		})
	}

	// Send messages after peer's VectorClock.
	s.writeMessagesAfterClock(stream, hello.VectorClock)

	// Reply with our own Hello so initiator can push anything we are missing.
	if !hello.IsReply {
		myHello, err := s.buildHelloPayload(true)
		if err == nil {
			_ = WriteFrame(stream, ControlFrame{
				Type:  FrameHello,
				Hello: myHello,
			})
		}
	}
}

func (s *Service) writeMessagesAfterClock(w io.Writer, vc model.VectorClock) {
	msgs, err := s.store.MessagesAfterClock(vc, maxSyncMessagesOnce)
	if err != nil || len(msgs) == 0 {
		return
	}
	for i := 0; i < len(msgs); i += msgBatchLimit {
		end := i + msgBatchLimit
		if end > len(msgs) {
			end = len(msgs)
		}
		batch := make([]model.Message, 0, end-i)
		for _, m := range msgs[i:end] {
			batch = append(batch, s.prepareMessageForWire(m))
		}
		if err := WriteFrame(w, ControlFrame{
			Type:     FrameMsgBatch,
			MsgBatch: &MsgBatchPayload{Messages: batch},
		}); err != nil {
			return
		}
	}
}

func (s *Service) buildHelloPayload(isReply bool) (*HelloPayload, error) {
	id := s.Identity()
	cfg := s.Config()
	vc, err := s.store.VectorClock()
	if err != nil {
		vc = model.VectorClock{}
	}
	events, err := s.store.ListMemberEvents()
	if err != nil {
		events = nil
	}
	eventIDs := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.GroupID == id.GroupID {
			eventIDs = append(eventIDs, ev.EventID)
		}
	}
	return &HelloPayload{
		DeviceID:       id.DeviceID,
		DeviceName:     cfg.DeviceName,
		Platform:       id.Platform,
		GroupID:        id.GroupID,
		Route:          s.currentRoute(),
		VectorClock:    vc,
		MemberEventIDs: eventIDs,
		IsReply:        isReply,
	}, nil
}

func (s *Service) syncAllPeers(ctx context.Context) {
	peers := s.onlinePeerIDs("")
	var wg sync.WaitGroup
	for _, peerID := range peers {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(pid string) {
			defer wg.Done()
			s.syncWithPeer(ctx, pid)
		}(peerID)
	}
	wg.Wait()
}

func (s *Service) syncWithPeer(ctx context.Context, peerDeviceID string) {
	if ctx.Err() != nil || !s.isPeerAllowed(peerDeviceID) {
		return
	}
	id := s.Identity()
	if peerDeviceID == id.DeviceID {
		return
	}

	syncCtx, cancel := context.WithTimeout(ctx, streamIOTimeout)
	defer cancel()

	stream, err := s.tr.OpenStream(syncCtx, peerDeviceID, model.StreamKindControl)
	if err != nil {
		return
	}
	stopClose := context.AfterFunc(syncCtx, func() { _ = stream.Close() })

	hello, err := s.buildHelloPayload(false)
	if err != nil {
		stopClose()
		_ = stream.Close()
		return
	}
	if err := WriteFrame(stream, ControlFrame{
		Type:  FrameHello,
		Hello: hello,
	}); err != nil {
		stopClose()
		_ = stream.Close()
		return
	}

	var peerHello *HelloPayload
	for {
		var frame ControlFrame
		if err := ReadFrame(stream, &frame); err != nil {
			break
		}
		switch frame.Type {
		case FrameMemberEvents:
			if frame.MemberEvents != nil && len(frame.MemberEvents.Events) > 0 {
				s.applyMemberEvents(ctx, frame.MemberEvents.Events, peerDeviceID)
			}
		case FrameMsgBatch:
			if frame.MsgBatch != nil && len(frame.MsgBatch.Messages) > 0 {
				s.ingestRemoteMessages(frame.MsgBatch.Messages, peerDeviceID)
			}
		case FrameHello:
			if frame.Hello != nil {
				peerHello = frame.Hello
			}
		}
	}
	stopClose()
	_ = stream.Close()

	if peerHello == nil || peerHello.GroupID != id.GroupID || !s.isPeerAllowed(peerDeviceID) {
		return
	}

	// Push any events or messages the remote peer is still missing.
	missingEvents := s.missingEventsFor(id.GroupID, peerHello.MemberEventIDs)
	missingMsgs, _ := s.store.MessagesAfterClock(peerHello.VectorClock, maxSyncMessagesOnce)
	if len(missingEvents) == 0 && len(missingMsgs) == 0 {
		return
	}

	pushCtx, pushCancel := context.WithTimeout(ctx, streamIOTimeout)
	defer pushCancel()

	pushStream, err := s.tr.OpenStream(pushCtx, peerDeviceID, model.StreamKindControl)
	if err != nil {
		return
	}
	defer pushStream.Close()
	stopPush := context.AfterFunc(pushCtx, func() { _ = pushStream.Close() })
	defer stopPush()

	if len(missingEvents) > 0 {
		_ = WriteFrame(pushStream, ControlFrame{
			Type: FrameMemberEvents,
			MemberEvents: &MemberEventsPayload{
				Events: missingEvents,
			},
		})
	}
	for i := 0; i < len(missingMsgs); i += msgBatchLimit {
		end := i + msgBatchLimit
		if end > len(missingMsgs) {
			end = len(missingMsgs)
		}
		batch := make([]model.Message, 0, end-i)
		for _, m := range missingMsgs[i:end] {
			batch = append(batch, s.prepareMessageForWire(m))
		}
		if err := WriteFrame(pushStream, ControlFrame{
			Type:     FrameMsgBatch,
			MsgBatch: &MsgBatchPayload{Messages: batch},
		}); err != nil {
			return
		}
	}
}

func (s *Service) handlePairingConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	var req PairRequest
	if err := ReadFrame(conn, &req); err != nil {
		return
	}

	writeErr := func(msg string) {
		_ = WriteFrame(conn, PairResponse{OK: false, Error: msg})
	}

	s.pairMu.Lock()
	sess := s.pair
	if sess == nil || time.Now().UnixMilli() > sess.expiresAt {
		s.pairMu.Unlock()
		writeErr("no active pairing session or pairing code expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Secret), []byte(sess.secret)) != 1 {
		s.pairMu.Unlock()
		writeErr("invalid pairing secret")
		return
	}
	s.pairMu.Unlock()

	if req.DeviceID == "" || !model.VerifyEd25519KeyLength(req.SignPublicKey) {
		writeErr("invalid device identity in pair request")
		return
	}
	if req.Route.DeviceID != req.DeviceID ||
		!bytes.Equal(req.Route.SignPublicKey, req.SignPublicKey) ||
		!model.VerifyRouteRecord(req.Route) {
		writeErr("invalid route record signature in pair request")
		return
	}

	// Consume the one-time pairing secret now that request is verified.
	s.pairMu.Lock()
	if s.pair == nil || subtle.ConstantTimeCompare([]byte(req.Secret), []byte(s.pair.secret)) != 1 {
		s.pairMu.Unlock()
		writeErr("pairing code already used")
		return
	}
	s.pair = nil
	s.pairMu.Unlock()

	id := s.Identity()
	epoch := req.Route.RouteEpoch
	if epoch == 0 {
		epoch = 1
	}
	routeCopy := req.Route
	ev := model.MemberEvent{
		EventID:        model.NewULID(),
		GroupID:        id.GroupID,
		Kind:           model.MemberEventAdd,
		TargetDeviceID: req.DeviceID,
		DeviceName:     req.DeviceName,
		Platform:       req.Platform,
		SignPublicKey:  cloneBytes(req.SignPublicKey),
		Route:          &routeCopy,
		Epoch:          epoch,
		IssuerDeviceID: id.DeviceID,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := model.SignMemberEvent(&ev, ed25519.PrivateKey(id.GroupPrivateKey)); err != nil {
		writeErr("failed to sign membership event")
		return
	}

	if _, err := s.applyMemberEvent(ev, ""); err != nil {
		writeErr(fmt.Sprintf("failed to persist new member: %v", err))
		return
	}

	devices, err := s.store.ListDevices(true)
	if err != nil {
		writeErr("failed to load devices")
		return
	}
	allEvents, err := s.store.ListMemberEvents()
	if err != nil {
		writeErr("failed to load member events")
		return
	}
	groupEvents := make([]model.MemberEvent, 0, len(allEvents))
	for _, e := range allEvents {
		if e.GroupID == id.GroupID {
			groupEvents = append(groupEvents, e)
		}
	}

	resp := PairResponse{
		OK:              true,
		GroupID:         id.GroupID,
		GroupPublicKey:  cloneBytes(id.GroupPublicKey),
		GroupPrivateKey: cloneBytes(id.GroupPrivateKey),
		Devices:         devices,
		MemberEvents:    groupEvents,
	}
	_ = WriteFrame(conn, resp)

	// Close the temporary pairing listener and gossip the new member to all other online peers.
	_ = s.tr.StopPairing()
	s.goBackground(func(bgCtx context.Context) {
		s.gossipMemberEvents(bgCtx, []model.MemberEvent{ev}, req.DeviceID)
	})
}

func (s *Service) applyMemberEvents(ctx context.Context, events []model.MemberEvent, fromPeerID string) {
	sorted := append([]model.MemberEvent(nil), events...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt != sorted[j].CreatedAt {
			return sorted[i].CreatedAt < sorted[j].CreatedAt
		}
		return sorted[i].EventID < sorted[j].EventID
	})

	var newlyApplied []model.MemberEvent
	for _, ev := range sorted {
		applied, err := s.applyMemberEvent(ev, fromPeerID)
		if err == nil && applied {
			newlyApplied = append(newlyApplied, ev)
		}
	}

	if len(newlyApplied) > 0 && fromPeerID != "" {
		s.gossipMemberEvents(ctx, newlyApplied, fromPeerID)
	}
}

func (s *Service) applyMemberEvent(ev model.MemberEvent, fromPeerID string) (bool, error) {
	s.memberMu.Lock()
	defer s.memberMu.Unlock()

	id := s.Identity()
	if ev.GroupID != id.GroupID {
		return false, errors.New("member event group mismatch")
	}
	if !model.VerifyMemberEvent(ev, ed25519.PublicKey(id.GroupPublicKey)) {
		return false, errors.New("invalid member event signature")
	}
	if ev.Route != nil {
		if ev.Route.DeviceID != ev.TargetDeviceID || !model.VerifyRouteRecord(*ev.Route) {
			return false, errors.New("invalid route signature in member event")
		}
	}

	inserted, err := s.store.SaveMemberEvent(ev)
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}

	existing, err := s.store.GetDevice(ev.TargetDeviceID)
	if err != nil {
		return false, err
	}

	changed := false
	switch ev.Kind {
	case model.MemberEventAdd:
		dev := model.Device{
			DeviceID:      ev.TargetDeviceID,
			Name:          ev.DeviceName,
			Platform:      ev.Platform,
			SignPublicKey: cloneBytes(ev.SignPublicKey),
			AddedAt:       ev.CreatedAt,
			IsSelf:        ev.TargetDeviceID == id.DeviceID,
		}
		if existing != nil {
			dev = *existing
			if ev.DeviceName != "" {
				dev.Name = ev.DeviceName
			}
			if ev.Platform != "" {
				dev.Platform = ev.Platform
			}
			if len(ev.SignPublicKey) > 0 {
				dev.SignPublicKey = cloneBytes(ev.SignPublicKey)
			}
			if dev.AddedAt == 0 {
				dev.AddedAt = ev.CreatedAt
			}
			dev.IsSelf = ev.TargetDeviceID == id.DeviceID
		}
		if ev.Route != nil && (ev.Route.RouteEpoch > dev.RouteEpoch || (ev.Route.RouteEpoch == dev.RouteEpoch && ev.Route.UpdatedAt >= dev.Route.UpdatedAt)) {
			dev.Route = *ev.Route
			dev.RouteEpoch = ev.Route.RouteEpoch
		}
		// Check if any higher-epoch route_update event arrived out-of-order earlier.
		if allEvents, err := s.store.ListMemberEvents(); err == nil {
			for _, prior := range allEvents {
				if prior.GroupID == id.GroupID &&
					prior.Kind == model.MemberEventRoute &&
					prior.TargetDeviceID == dev.DeviceID &&
					prior.Route != nil &&
					bytes.Equal(prior.Route.SignPublicKey, dev.SignPublicKey) &&
					prior.Route.RouteEpoch > dev.RouteEpoch {
					dev.Route = *prior.Route
					dev.RouteEpoch = prior.Route.RouteEpoch
				}
			}
		}
		if err := s.store.UpsertDevice(dev); err != nil {
			return false, err
		}
		changed = true

	case model.MemberEventRevoke:
		dev := model.Device{
			DeviceID: ev.TargetDeviceID,
			Revoked:  true,
			AddedAt:  ev.CreatedAt,
			IsSelf:   ev.TargetDeviceID == id.DeviceID,
		}
		if existing != nil {
			dev = *existing
			dev.Revoked = true
		}
		if err := s.store.UpsertDevice(dev); err != nil {
			return false, err
		}
		if ev.TargetDeviceID != id.DeviceID {
			s.peerMu.Lock()
			delete(s.connectedPeers, ev.TargetDeviceID)
			s.peerMu.Unlock()
			s.tr.RevokePeer(ev.TargetDeviceID)
		}
		changed = true

	case model.MemberEventRoute:
		if existing != nil && !existing.Revoked && ev.Route != nil {
			if len(existing.SignPublicKey) > 0 && !bytes.Equal(ev.Route.SignPublicKey, existing.SignPublicKey) {
				return false, errors.New("route update signer mismatch")
			}
			if ev.Route.RouteEpoch > existing.RouteEpoch ||
				(ev.Route.RouteEpoch == existing.RouteEpoch && ev.Route.UpdatedAt > existing.Route.UpdatedAt) {
				existing.Route = *ev.Route
				existing.RouteEpoch = ev.Route.RouteEpoch
				if ev.DeviceName != "" {
					existing.Name = ev.DeviceName
				}
				if err := s.store.UpsertDevice(*existing); err != nil {
					return false, err
				}
				changed = true
			}
		}
	}

	if changed {
		s.refreshTransportPeers()
		s.emit(model.AppEvent{Type: model.EventDevicesUpdated})
	}
	return true, nil
}

func (s *Service) refreshTransportPeers() {
	id := s.Identity()
	devices, err := s.store.ListDevices(false)
	if err != nil {
		return
	}
	activePeers := make([]model.Device, 0, len(devices))
	for _, d := range devices {
		if d.Revoked || d.DeviceID == id.DeviceID {
			continue
		}
		activePeers = append(activePeers, d)
	}
	s.tr.SetAllowedPeers(activePeers)
	s.tr.SyncPeers(activePeers)
}

func (s *Service) missingEventsFor(groupID string, remoteIDs []string) []model.MemberEvent {
	known := make(map[string]struct{}, len(remoteIDs))
	for _, eid := range remoteIDs {
		known[eid] = struct{}{}
	}
	all, err := s.store.ListMemberEvents()
	if err != nil {
		return nil
	}
	var missing []model.MemberEvent
	for _, ev := range all {
		if ev.GroupID != groupID {
			continue
		}
		if _, ok := known[ev.EventID]; !ok {
			missing = append(missing, ev)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].CreatedAt != missing[j].CreatedAt {
			return missing[i].CreatedAt < missing[j].CreatedAt
		}
		return missing[i].EventID < missing[j].EventID
	})
	return missing
}

func (s *Service) gossipMemberEvents(ctx context.Context, events []model.MemberEvent, excludePeerID string) {
	if len(events) == 0 {
		return
	}
	frame := ControlFrame{
		Type: FrameMemberEvents,
		MemberEvents: &MemberEventsPayload{
			Events: events,
		},
	}
	s.broadcastControlFrame(ctx, frame, excludePeerID)
}

func (s *Service) broadcastMessages(ctx context.Context, msgs []model.Message) {
	if len(msgs) == 0 {
		return
	}
	for i := 0; i < len(msgs); i += msgBatchLimit {
		end := i + msgBatchLimit
		if end > len(msgs) {
			end = len(msgs)
		}
		prepared := make([]model.Message, 0, end-i)
		for _, m := range msgs[i:end] {
			prepared = append(prepared, s.prepareMessageForWire(m))
		}
		frame := ControlFrame{
			Type:     FrameMsgBatch,
			MsgBatch: &MsgBatchPayload{Messages: prepared},
		}
		s.broadcastControlFrame(ctx, frame, "")
	}
}

func (s *Service) broadcastBlobHave(ctx context.Context, blobHash string) {
	id := s.Identity()
	frame := ControlFrame{
		Type: FrameBlobHave,
		BlobHave: &BlobHavePayload{
			BlobHash: blobHash,
			DeviceID: id.DeviceID,
		},
	}
	s.broadcastControlFrame(ctx, frame, "")
}

func (s *Service) broadcastControlFrame(ctx context.Context, frame ControlFrame, excludePeerID string) {
	peers := s.onlinePeerIDs(excludePeerID)
	if len(peers) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, peerID := range peers {
		wg.Add(1)
		go func(pid string) {
			defer wg.Done()
			_ = s.sendControlFrame(ctx, pid, frame)
		}(peerID)
	}
	wg.Wait()
}

func (s *Service) sendControlFrame(ctx context.Context, peerDeviceID string, frame ControlFrame) error {
	if !s.isPeerAllowed(peerDeviceID) {
		return errors.New("peer not allowed")
	}
	sendCtx, cancel := context.WithTimeout(ctx, streamIOTimeout)
	defer cancel()

	stream, err := s.tr.OpenStream(sendCtx, peerDeviceID, model.StreamKindControl)
	if err != nil {
		return err
	}
	defer stream.Close()
	stop := context.AfterFunc(sendCtx, func() { _ = stream.Close() })
	defer stop()

	return WriteFrame(stream, frame)
}

func (s *Service) ingestRemoteMessages(msgs []model.Message, fromPeerID string) {
	cfg := s.Config()

	for _, msg := range msgs {
		var autoDownloadHash string

		if msg.Kind == model.MessageKindFile && msg.File != nil {
			blob := *cloneFileBlob(msg.File)
			blob.LocalPath = "" // Never trust remote filesystem paths.

			if existing, err := s.store.GetFileBlob(blob.BlobHash); err == nil && existing != nil {
				if isUsableLocalFile(existing.LocalPath, blob.BlobHash) {
					blob.LocalPath = existing.LocalPath
				}
				for _, seeder := range existing.Seeders {
					blob.Seeders = appendUniqueString(blob.Seeders, seeder)
				}
			}
			if blob.LocalPath == "" {
				if p, ok := s.cas.HasCompleteBlob(blob.BlobHash); ok && isUsableLocalFile(p, blob.BlobHash) {
					blob.LocalPath = p
				}
			}
			if msg.AuthorDeviceID != "" {
				blob.Seeders = appendUniqueString(blob.Seeders, msg.AuthorDeviceID)
			}
			if err := s.store.UpsertFileBlob(blob); err == nil {
				for _, seeder := range blob.Seeders {
					_ = s.store.AddBlobSeeder(blob.BlobHash, seeder)
				}
			}
			msg.File = &blob

			if blob.LocalPath == "" && cfg.AutoDownloadMaxBytes > 0 && blob.FileSize <= cfg.AutoDownloadMaxBytes {
				autoDownloadHash = blob.BlobHash
			}
		}

		inserted, err := s.store.SaveMessage(msg)
		if err != nil || !inserted {
			continue
		}

		evType := model.EventMessageAdded
		if msg.Kind == model.MessageKindTombstone {
			evType = model.EventMessageDeleted
		}
		msgCopy := cloneMessage(msg)
		s.emit(model.AppEvent{
			Type:    evType,
			Message: &msgCopy,
		})

		if autoDownloadHash != "" {
			hash := autoDownloadHash
			s.goBackground(func(ctx context.Context) {
				_ = s.DownloadFile(ctx, hash)
			})
		}
	}
}

func (s *Service) prepareMessageForWire(msg model.Message) model.Message {
	out := cloneMessage(msg)
	if out.Kind == model.MessageKindFile {
		if out.File == nil && out.BlobHash != "" {
			if b, err := s.store.GetFileBlob(out.BlobHash); err == nil && b != nil {
				out.File = cloneFileBlob(b)
			}
		}
		if out.File != nil {
			out.File.LocalPath = ""
		}
	}
	return out
}

func (s *Service) handleChunkStream(ctx context.Context, peerDeviceID string, stream io.ReadWriteCloser) {
	for {
		var req ChunkRequest
		if err := ReadFrame(stream, &req); err != nil {
			return
		}
		if !s.isPeerAllowed(peerDeviceID) {
			return
		}

		resp := ChunkResponse{
			BlobHash: req.BlobHash,
			Index:    req.Index,
		}
		data, err := s.cas.ReadChunk(req.BlobHash, req.Index)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Data = data
			resp.Hash = model.Blake3Hex(data)
		}
		if err := WriteFrame(stream, resp); err != nil {
			return
		}
	}
}

func (s *Service) runDownload(ctx context.Context, blobHash string) error {
	blob, err := s.store.GetFileBlob(blobHash)
	if err != nil {
		return err
	}
	if blob == nil {
		return fmt.Errorf("blob %s not found", blobHash)
	}

	// Fast path if already complete locally with a usable user-facing path.
	if isUsableLocalFile(blob.LocalPath, blob.BlobHash) {
		return s.markBlobComplete(ctx, blob, blob.LocalPath)
	}
	if localPath, ok := s.cas.HasCompleteBlob(blobHash); ok {
		if isUsableLocalFile(localPath, blob.BlobHash) {
			return s.markBlobComplete(ctx, blob, localPath)
		}
		downloadDir := s.effectiveDownloadDir()
		if err := os.MkdirAll(downloadDir, 0o755); err == nil {
			if assembledPath, err := s.cas.AssembleFile(*blob, downloadDir); err == nil && isUsableLocalFile(assembledPath, blob.BlobHash) {
				return s.markBlobComplete(ctx, blob, assembledPath)
			}
		}
	}

	// Register single active transfer controller per blobHash.
	dlCtx, dlCancel := context.WithCancel(ctx)
	at := &activeTransfer{
		cancel: dlCancel,
		done:   make(chan struct{}),
	}

	s.transfersMu.Lock()
	if existing, ok := s.activeTransfers[blobHash]; ok {
		s.transfersMu.Unlock()
		dlCancel()
		select {
		case <-existing.done:
			t, _ := s.store.GetTransferTask(blobHash)
			if t != nil && t.State == model.TransferDone {
				return nil
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.activeTransfers[blobHash] = at
	s.transfersMu.Unlock()

	defer func() {
		dlCancel()
		s.transfersMu.Lock()
		delete(s.activeTransfers, blobHash)
		s.transfersMu.Unlock()
		close(at.done)
	}()

	chunkSize := blob.ChunkSize
	if chunkSize <= 0 {
		chunkSize = model.DefaultChunkSize
	}

	task, err := s.store.GetTransferTask(blobHash)
	if err != nil {
		return err
	}
	if task == nil {
		task = &model.TransferTask{
			BlobHash:    blobHash,
			Direction:   model.TransferDownload,
			State:       model.TransferRunning,
			ChunkCount:  blob.ChunkCount,
			ChunkBitmap: model.NewChunkBitmap(blob.ChunkCount),
			BytesDone:   0,
			BytesTotal:  blob.FileSize,
			UpdatedAt:   time.Now().UnixMilli(),
		}
	} else {
		if task.State == model.TransferCancelled {
			task.ChunkBitmap = model.NewChunkBitmap(blob.ChunkCount)
			task.BytesDone = 0
		}
		if len(task.ChunkBitmap) < (blob.ChunkCount+7)/8 {
			grown := model.NewChunkBitmap(blob.ChunkCount)
			copy(grown, task.ChunkBitmap)
			task.ChunkBitmap = grown
		}
		task.ChunkCount = blob.ChunkCount
		task.BytesTotal = blob.FileSize
		task.BytesDone = computeBytesDone(task.ChunkBitmap, blob.ChunkCount, chunkSize, blob.FileSize)
		task.State = model.TransferRunning
		task.LastError = ""
		task.UpdatedAt = time.Now().UnixMilli()
	}

	if err := s.store.UpsertTransferTask(*task); err != nil {
		return err
	}
	{
		tc := cloneTransferTask(*task)
		s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
	}

	missing := model.MissingChunks(task.ChunkBitmap, blob.ChunkCount)
	if len(missing) > 0 {
		candidates := s.selectCandidatePeers(blob.Seeders)
		if len(candidates) == 0 {
			task.State = model.TransferQueued
			task.LastError = "waiting for online seeder peers"
			task.UpdatedAt = time.Now().UnixMilli()
			_ = s.store.UpsertTransferTask(*task)
			tc := cloneTransferTask(*task)
			s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
			return errors.New("no online peers available for download")
		}

		workers := s.opts.DownloadWorkers
		if workers > len(missing) {
			workers = len(missing)
		}

		jobs := make(chan int, len(missing))
		for _, idx := range missing {
			jobs <- idx
		}
		close(jobs)

		var (
			taskMu   sync.Mutex
			errMu    sync.Mutex
			firstErr error
			wg       sync.WaitGroup
		)

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					if dlCtx.Err() != nil {
						return
					}
					var expectedHash string
					if idx >= 0 && idx < len(blob.ChunkHashes) {
						expectedHash = blob.ChunkHashes[idx]
					}
					data, fetchErr := s.fetchChunkFromPeers(dlCtx, blobHash, idx, candidates)
					if fetchErr != nil {
						if dlCtx.Err() != nil {
							return
						}
						errMu.Lock()
						if firstErr == nil {
							firstErr = fetchErr
						}
						errMu.Unlock()
						continue
					}

					if err := s.limiter.Wait(dlCtx, len(data)); err != nil {
						return
					}

					if writeErr := s.cas.WriteChunk(blobHash, idx, expectedHash, data); writeErr != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = writeErr
						}
						errMu.Unlock()
						continue
					}

					taskMu.Lock()
					task.ChunkBitmap = model.SetChunk(task.ChunkBitmap, idx)
					task.BytesDone = computeBytesDone(task.ChunkBitmap, blob.ChunkCount, chunkSize, blob.FileSize)
					task.UpdatedAt = time.Now().UnixMilli()
					_ = s.store.UpsertTransferTask(*task)
					snap := cloneTransferTask(*task)
					taskMu.Unlock()

					s.emit(model.AppEvent{
						Type:     model.EventTransferUpdated,
						Transfer: &snap,
					})
				}
			}()
		}
		wg.Wait()

		// Handle pause/cancel/context termination before attempting assembly.
		switch at.getAction() {
		case stopPause:
			task.State = model.TransferPaused
			task.UpdatedAt = time.Now().UnixMilli()
			_ = s.store.UpsertTransferTask(*task)
			tc := cloneTransferTask(*task)
			s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
			return ErrTransferPaused

		case stopCancel:
			task.State = model.TransferCancelled
			task.UpdatedAt = time.Now().UnixMilli()
			_ = s.store.UpsertTransferTask(*task)
			tc := cloneTransferTask(*task)
			s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
			return ErrTransferCancelled
		}

		if dlCtx.Err() != nil {
			task.State = model.TransferPaused
			task.UpdatedAt = time.Now().UnixMilli()
			_ = s.store.UpsertTransferTask(*task)
			tc := cloneTransferTask(*task)
			s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
			return dlCtx.Err()
		}

		if firstErr != nil || len(model.MissingChunks(task.ChunkBitmap, blob.ChunkCount)) > 0 {
			if firstErr == nil {
				firstErr = errors.New("incomplete chunk download")
			}
			task.State = model.TransferFailed
			task.LastError = firstErr.Error()
			task.UpdatedAt = time.Now().UnixMilli()
			_ = s.store.UpsertTransferTask(*task)
			tc := cloneTransferTask(*task)
			s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
			return firstErr
		}
	}

	// All chunks verified on disk; assemble into DownloadDir.
	downloadDir := s.effectiveDownloadDir()
	_ = os.MkdirAll(downloadDir, 0o755)
	localPath, err := s.cas.AssembleFile(*blob, downloadDir)
	if err != nil {
		task.State = model.TransferFailed
		task.LastError = err.Error()
		task.UpdatedAt = time.Now().UnixMilli()
		_ = s.store.UpsertTransferTask(*task)
		tc := cloneTransferTask(*task)
		s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})
		return fmt.Errorf("assemble file: %w", err)
	}

	return s.markBlobComplete(ctx, blob, localPath)
}

func (s *Service) markBlobComplete(ctx context.Context, blob *model.FileBlob, localPath string) error {
	id := s.Identity()
	blob.LocalPath = localPath
	blob.Seeders = appendUniqueString(blob.Seeders, id.DeviceID)
	if err := s.store.UpsertFileBlob(*blob); err != nil {
		return err
	}
	_ = s.store.AddBlobSeeder(blob.BlobHash, id.DeviceID)

	bitmap := model.NewChunkBitmap(blob.ChunkCount)
	for i := 0; i < blob.ChunkCount; i++ {
		bitmap = model.SetChunk(bitmap, i)
	}
	task := model.TransferTask{
		BlobHash:    blob.BlobHash,
		Direction:   model.TransferDownload,
		State:       model.TransferDone,
		ChunkCount:  blob.ChunkCount,
		ChunkBitmap: bitmap,
		BytesDone:   blob.FileSize,
		BytesTotal:  blob.FileSize,
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if err := s.store.UpsertTransferTask(task); err != nil {
		return err
	}
	tc := cloneTransferTask(task)
	s.emit(model.AppEvent{Type: model.EventTransferUpdated, Transfer: &tc})

	s.goBackground(func(bgCtx context.Context) {
		s.broadcastBlobHave(bgCtx, blob.BlobHash)
	})
	return nil
}

func (s *Service) fetchChunkFromPeers(ctx context.Context, blobHash string, index int, candidates []string) ([]byte, error) {
	if len(candidates) == 0 {
		return nil, errors.New("no candidate peers")
	}
	start := index % len(candidates)
	var lastErr error

	for attempt := 0; attempt < len(candidates); attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		peerID := candidates[(start+attempt)%len(candidates)]

		reqCtx, cancel := context.WithTimeout(ctx, streamIOTimeout)
		stream, err := s.tr.OpenStream(reqCtx, peerID, model.StreamKindChunkData)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		stop := context.AfterFunc(reqCtx, func() { _ = stream.Close() })

		err = WriteFrame(stream, ChunkRequest{
			BlobHash: blobHash,
			Index:    index,
		})
		if err != nil {
			stop()
			_ = stream.Close()
			cancel()
			lastErr = err
			continue
		}

		var resp ChunkResponse
		err = ReadFrame(stream, &resp)
		stop()
		_ = stream.Close()
		cancel()

		if err != nil {
			lastErr = err
			continue
		}
		if resp.Error != "" {
			lastErr = errors.New(resp.Error)
			continue
		}
		if resp.BlobHash != blobHash || resp.Index != index {
			lastErr = errors.New("mismatched chunk response metadata")
			continue
		}
		return resp.Data, nil
	}

	if lastErr == nil {
		lastErr = errors.New("failed to fetch chunk from all peers")
	}
	return nil, lastErr
}

func (s *Service) resumePendingTransfers(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	tasks, err := s.store.ListTransferTasks()
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.State != model.TransferQueued && t.State != model.TransferRunning {
			continue
		}
		s.transfersMu.Lock()
		_, running := s.activeTransfers[t.BlobHash]
		s.transfersMu.Unlock()
		if running {
			continue
		}
		if len(s.onlinePeerIDs("")) == 0 {
			return
		}
		hash := t.BlobHash
		s.goBackground(func(bgCtx context.Context) {
			_ = s.DownloadFile(bgCtx, hash)
		})
	}
}

func (s *Service) pauseAllActiveTransfers() {
	s.transfersMu.Lock()
	active := make([]*activeTransfer, 0, len(s.activeTransfers))
	for _, at := range s.activeTransfers {
		at.setAction(stopPause)
		at.cancel()
		active = append(active, at)
	}
	s.transfersMu.Unlock()

	for _, at := range active {
		<-at.done
	}
}

func (s *Service) isPeerAllowed(peerDeviceID string) bool {
	if peerDeviceID == "" {
		return false
	}
	id := s.Identity()
	if peerDeviceID == id.DeviceID {
		return false
	}
	dev, err := s.store.GetDevice(peerDeviceID)
	if err != nil || dev == nil || dev.Revoked {
		return false
	}
	return true
}

func (s *Service) onlinePeerIDs(excludePeerID string) []string {
	id := s.Identity()
	devices, err := s.store.ListDevices(false)
	if err != nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(devices))
	for _, d := range devices {
		if !d.Revoked && d.DeviceID != id.DeviceID && d.DeviceID != excludePeerID {
			allowed[d.DeviceID] = struct{}{}
		}
	}

	connected := make(map[string]struct{})
	s.peerMu.RLock()
	for pid, ok := range s.connectedPeers {
		if ok {
			if _, isAllowed := allowed[pid]; isAllowed {
				connected[pid] = struct{}{}
			}
		}
	}
	s.peerMu.RUnlock()

	for _, st := range s.tr.PeerStatuses() {
		if st.Connected {
			if _, isAllowed := allowed[st.DeviceID]; isAllowed {
				connected[st.DeviceID] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(connected))
	for pid := range connected {
		out = append(out, pid)
	}
	sort.Strings(out)
	return out
}

func (s *Service) selectCandidatePeers(seeders []string) []string {
	online := s.onlinePeerIDs("")
	if len(online) == 0 {
		return nil
	}
	onlineSet := make(map[string]struct{}, len(online))
	for _, pid := range online {
		onlineSet[pid] = struct{}{}
	}

	preferred := make([]string, 0, len(seeders))
	seen := make(map[string]struct{}, len(online))
	for _, sid := range seeders {
		if _, ok := onlineSet[sid]; ok {
			if _, dup := seen[sid]; !dup {
				seen[sid] = struct{}{}
				preferred = append(preferred, sid)
			}
		}
	}
	// Append remaining online peers as fallback.
	for _, pid := range online {
		if _, dup := seen[pid]; !dup {
			preferred = append(preferred, pid)
		}
	}
	return preferred
}

func computeBytesDone(bitmap []byte, chunkCount, chunkSize int, fileSize int64) int64 {
	if chunkCount <= 0 || fileSize <= 0 {
		return 0
	}
	var done int64
	for i := 0; i < chunkCount; i++ {
		if !model.HasChunk(bitmap, i) {
			continue
		}
		if i < chunkCount-1 {
			done += int64(chunkSize)
		} else {
			rem := fileSize - int64(chunkCount-1)*int64(chunkSize)
			if rem > 0 {
				done += rem
			}
		}
	}
	if done > fileSize {
		done = fileSize
	}
	return done
}

func cloneFileBlob(b *model.FileBlob) *model.FileBlob {
	if b == nil {
		return nil
	}
	cp := *b
	if len(b.ChunkHashes) > 0 {
		cp.ChunkHashes = append([]string(nil), b.ChunkHashes...)
	}
	cp.ThumbnailWebP = cloneBytes(b.ThumbnailWebP)
	if len(b.Seeders) > 0 {
		cp.Seeders = append([]string(nil), b.Seeders...)
	}
	return &cp
}

func cloneMessage(m model.Message) model.Message {
	cp := m
	cp.File = cloneFileBlob(m.File)
	return cp
}

func cloneTransferTask(t model.TransferTask) model.TransferTask {
	cp := t
	cp.ChunkBitmap = cloneBytes(t.ChunkBitmap)
	return cp
}

// rateLimiter is a lightweight stdlib token-bucket limiter for download throttling.
type rateLimiter struct {
	mu           sync.Mutex
	bytesPerSec  int64
	tokens       float64
	lastRefillAt time.Time
}

func newRateLimiter(bytesPerSec int64) *rateLimiter {
	return &rateLimiter{
		bytesPerSec:  bytesPerSec,
		lastRefillAt: time.Now(),
	}
}

func (r *rateLimiter) SetLimit(bytesPerSec int64) {
	r.mu.Lock()
	r.bytesPerSec = bytesPerSec
	if bytesPerSec > 0 {
		r.tokens = float64(bytesPerSec)
	}
	r.lastRefillAt = time.Now()
	r.mu.Unlock()
}

func (r *rateLimiter) Wait(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	for {
		r.mu.Lock()
		limit := r.bytesPerSec
		if limit <= 0 {
			r.mu.Unlock()
			return nil
		}
		now := time.Now()
		elapsed := now.Sub(r.lastRefillAt).Seconds()
		r.lastRefillAt = now

		burst := float64(limit)
		if float64(n) > burst {
			burst = float64(n)
		}
		r.tokens += elapsed * float64(limit)
		if r.tokens > burst {
			r.tokens = burst
		}
		if r.tokens >= float64(n) {
			r.tokens -= float64(n)
			r.mu.Unlock()
			return nil
		}
		deficit := float64(n) - r.tokens
		waitDur := time.Duration((deficit / float64(limit)) * float64(time.Second))
		if waitDur < time.Millisecond {
			waitDur = time.Millisecond
		}
		r.mu.Unlock()

		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
