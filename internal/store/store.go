package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"tailchat/internal/model"
)

var _ model.Store = (*SQLiteStore)(nil)

// SQLiteStore implements model.Store backed by SQLite (modernc.org/sqlite).
type SQLiteStore struct {
	db      *sql.DB
	mu      sync.Mutex
	lastSeq map[string]uint64
}

// Open opens or initializes the SQLite database at dbPath.
func Open(dbPath string) (*SQLiteStore, error) {
	if dbPath == "" {
		return nil, errors.New("dbPath must not be empty")
	}
	if dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file:") {
		dir := filepath.Dir(dbPath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db directory: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single open connection avoids SQLITE_BUSY lock contention across goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &SQLiteStore{
		db:      db,
		lastSeq: make(map[string]uint64),
	}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) initSchema() error {
	pragmas := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA busy_timeout=5000;`,
		`PRAGMA foreign_keys=ON;`,
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("exec pragma %q: %w", p, err)
		}
	}

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS kv_meta (
			key TEXT PRIMARY KEY,
			value BLOB
		);`,
		`CREATE TABLE IF NOT EXISTS devices (
			device_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			platform TEXT NOT NULL,
			sign_public_key BLOB,
			route_json BLOB,
			route_epoch INTEGER NOT NULL DEFAULT 0,
			revoked INTEGER NOT NULL DEFAULT 0,
			added_at INTEGER NOT NULL DEFAULT 0,
			last_seen_at INTEGER NOT NULL DEFAULT 0,
			is_self INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS member_events (
			event_id TEXT PRIMARY KEY,
			group_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			target_device_id TEXT NOT NULL,
			epoch INTEGER NOT NULL DEFAULT 0,
			payload_json BLOB NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS messages (
			msg_id TEXT PRIMARY KEY,
			author_device_id TEXT NOT NULL,
			author_name TEXT NOT NULL,
			author_seq INTEGER NOT NULL,
			kind TEXT NOT NULL,
			text_body TEXT NOT NULL DEFAULT '',
			blob_hash TEXT NOT NULL DEFAULT '',
			target_msg_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE INDEX IF NOT EXISTS idx_messages_author_seq ON messages(author_device_id, author_seq);`,
		`CREATE INDEX IF NOT EXISTS idx_messages_target_msg ON messages(target_msg_id) WHERE target_msg_id != '';`,
		`CREATE INDEX IF NOT EXISTS idx_messages_blob_hash ON messages(blob_hash) WHERE blob_hash != '';`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(msg_id UNINDEXED, text_body, file_name);`,
		`CREATE TABLE IF NOT EXISTS file_blobs (
			blob_hash TEXT PRIMARY KEY,
			file_name TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			mime_type TEXT NOT NULL DEFAULT '',
			chunk_size INTEGER NOT NULL DEFAULT 0,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			chunk_hashes_json BLOB,
			thumbnail_webp BLOB,
			local_path TEXT NOT NULL DEFAULT '',
			seeders_json BLOB
		);`,
		`CREATE TABLE IF NOT EXISTS transfer_tasks (
			blob_hash TEXT PRIMARY KEY,
			direction TEXT NOT NULL,
			state TEXT NOT NULL,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			chunk_bitmap BLOB,
			bytes_done INTEGER NOT NULL DEFAULT 0,
			bytes_total INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0
		);`,
	}

	for _, stmt := range ddl {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
}

// Close closes the underlying SQLite database handle.
func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// --- Identity & Config ---

const (
	kvKeyIdentity = "identity"
	kvKeyConfig   = "config"
)

func (s *SQLiteStore) LoadIdentity() (*model.LocalIdentity, error) {
	raw, err := s.getKV(kvKeyIdentity)
	if err != nil || raw == nil {
		return nil, err
	}
	var id model.LocalIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("unmarshal identity: %w", err)
	}
	return &id, nil
}

func (s *SQLiteStore) SaveIdentity(id model.LocalIdentity) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}
	return s.setKV(kvKeyIdentity, raw)
}

func (s *SQLiteStore) LoadConfig() (*model.AppConfig, error) {
	raw, err := s.getKV(kvKeyConfig)
	if err != nil || raw == nil {
		return nil, err
	}
	var cfg model.AppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

func (s *SQLiteStore) SaveConfig(cfg model.AppConfig) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return s.setKV(kvKeyConfig, raw)
}

func (s *SQLiteStore) getKV(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var val []byte
	err := s.db.QueryRow(`SELECT value FROM kv_meta WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return val, nil
}

func (s *SQLiteStore) setKV(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO kv_meta(key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// --- Devices & Member Events ---

func (s *SQLiteStore) UpsertDevice(dev model.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	routeJSON, err := json.Marshal(dev.Route)
	if err != nil {
		return fmt.Errorf("marshal route: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO devices(
			device_id, name, platform, sign_public_key, route_json,
			route_epoch, revoked, added_at, last_seen_at, is_self
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			name = excluded.name,
			platform = excluded.platform,
			sign_public_key = excluded.sign_public_key,
			route_json = excluded.route_json,
			route_epoch = excluded.route_epoch,
			revoked = excluded.revoked,
			added_at = excluded.added_at,
			last_seen_at = excluded.last_seen_at,
			is_self = excluded.is_self`,
		dev.DeviceID, dev.Name, dev.Platform, dev.SignPublicKey, routeJSON,
		dev.RouteEpoch, boolToInt(dev.Revoked), dev.AddedAt, dev.LastSeenAt, boolToInt(dev.IsSelf),
	)
	return err
}

func (s *SQLiteStore) GetDevice(deviceID string) (*model.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT device_id, name, platform, sign_public_key, route_json,
		        route_epoch, revoked, added_at, last_seen_at, is_self
		 FROM devices WHERE device_id = ?`,
		deviceID,
	)
	dev, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dev, nil
}

func (s *SQLiteStore) ListDevices(includeRevoked bool) ([]model.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `SELECT device_id, name, platform, sign_public_key, route_json,
	                 route_epoch, revoked, added_at, last_seen_at, is_self
	          FROM devices`
	if !includeRevoked {
		query += ` WHERE revoked = 0`
	}
	query += ` ORDER BY is_self DESC, added_at ASC, device_id ASC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []model.Device
	for rows.Next() {
		dev, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, dev)
	}
	return devices, rows.Err()
}

func (s *SQLiteStore) SaveMemberEvent(ev model.MemberEvent) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := json.Marshal(ev)
	if err != nil {
		return false, fmt.Errorf("marshal member event: %w", err)
	}

	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO member_events(
			event_id, group_id, kind, target_device_id, epoch, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.EventID, ev.GroupID, string(ev.Kind), ev.TargetDeviceID, ev.Epoch, payload, ev.CreatedAt,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *SQLiteStore) ListMemberEvents() ([]model.MemberEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT payload_json FROM member_events ORDER BY epoch ASC, created_at ASC, event_id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []model.MemberEvent
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var ev model.MemberEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return nil, fmt.Errorf("unmarshal member event: %w", err)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// --- Messages & Search ---

func (s *SQLiteStore) NextAuthorSeq(authorDeviceID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var maxDB uint64
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(author_seq), 0) FROM messages WHERE author_device_id = ?`,
		authorDeviceID,
	).Scan(&maxDB)
	if err != nil {
		return 0, err
	}

	cur := maxDB
	if mem := s.lastSeq[authorDeviceID]; mem > cur {
		cur = mem
	}
	next := cur + 1
	s.lastSeq[authorDeviceID] = next
	return next, nil
}

func (s *SQLiteStore) SaveMessage(msg model.Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if msg.File != nil {
		if msg.BlobHash == "" && msg.File.BlobHash != "" {
			msg.BlobHash = msg.File.BlobHash
		}
		if err := upsertFileBlobTx(tx, *msg.File); err != nil {
			return false, err
		}
	}

	res, err := tx.Exec(
		`INSERT OR IGNORE INTO messages(
			msg_id, author_device_id, author_name, author_seq, kind,
			text_body, blob_hash, target_msg_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.MsgID, msg.AuthorDeviceID, msg.AuthorName, msg.AuthorSeq, string(msg.Kind),
		msg.TextBody, msg.BlobHash, msg.TargetMsgID, msg.CreatedAt,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	inserted := n > 0

	if inserted {
		if msg.Kind != model.MessageKindTombstone {
			fileName := ""
			if msg.File != nil && msg.File.FileName != "" {
				fileName = msg.File.FileName
			} else if msg.BlobHash != "" {
				_ = tx.QueryRow(`SELECT file_name FROM file_blobs WHERE blob_hash = ?`, msg.BlobHash).Scan(&fileName)
			}
			if _, err := tx.Exec(
				`INSERT INTO messages_fts(msg_id, text_body, file_name) VALUES (?, ?, ?)`,
				msg.MsgID, msg.TextBody, fileName,
			); err != nil {
				return false, fmt.Errorf("index message fts: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	if inserted && msg.AuthorSeq > s.lastSeq[msg.AuthorDeviceID] {
		s.lastSeq[msg.AuthorDeviceID] = msg.AuthorSeq
	}
	return inserted, nil
}

func (s *SQLiteStore) GetMessage(msgID string) (*model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT msg_id, author_device_id, author_name, author_seq, kind,
		        text_body, blob_hash, target_msg_id, created_at
		 FROM messages WHERE msg_id = ?`,
		msgID,
	)
	msg, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.hydrateMessageFileLocked(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// ListMessages returns up to limit newest visible messages in chronological ascending order (msg_id ASC).
// If beforeMsgID is non-empty, only messages with msg_id < beforeMsgID are considered.
// Tombstone entries and messages targeted by a tombstone are excluded from timeline listings.
func (s *SQLiteStore) ListMessages(limit int, beforeMsgID string) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = -1
	}

	rows, err := s.db.Query(
		`SELECT msg_id, author_device_id, author_name, author_seq, kind,
		        text_body, blob_hash, target_msg_id, created_at
		 FROM (
		     SELECT m.msg_id, m.author_device_id, m.author_name, m.author_seq, m.kind,
		            m.text_body, m.blob_hash, m.target_msg_id, m.created_at
		     FROM messages m
		     WHERE m.kind != ?
		       AND NOT EXISTS (
		           SELECT 1 FROM messages t
		           WHERE t.kind = ? AND t.target_msg_id = m.msg_id
		       )
		       AND (? = '' OR m.msg_id < ?)
		     ORDER BY m.msg_id DESC
		     LIMIT ?
		 ) sub
		 ORDER BY msg_id ASC`,
		string(model.MessageKindTombstone),
		string(model.MessageKindTombstone),
		beforeMsgID, beforeMsgID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanAndHydrateMessagesLocked(rows)
}

// SearchMessages searches visible messages across text_body and file_name using FTS5,
// falling back to case-insensitive LIKE when the query contains FTS5 syntax characters
// or yields no FTS5 token match. Results are ordered newest-first (msg_id DESC).
func (s *SQLiteStore) SearchMessages(query string, limit int) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = -1
	}

	msgs, err := s.searchMessagesFTSLocked(q, limit)
	if err == nil && len(msgs) > 0 {
		return msgs, nil
	}
	// Gracefully fall back to LIKE on FTS syntax errors (e.g., quotes, operators, punctuation)
	// or substring/punctuation queries that FTS5 tokenization does not match.
	return s.searchMessagesLIKELocked(q, limit)
}

func (s *SQLiteStore) searchMessagesFTSLocked(q string, limit int) ([]model.Message, error) {
	rows, err := s.db.Query(
		`SELECT m.msg_id, m.author_device_id, m.author_name, m.author_seq, m.kind,
		        m.text_body, m.blob_hash, m.target_msg_id, m.created_at
		 FROM messages_fts f
		 JOIN messages m ON m.msg_id = f.msg_id
		 WHERE messages_fts MATCH ?
		   AND m.kind != ?
		   AND NOT EXISTS (
		       SELECT 1 FROM messages t
		       WHERE t.kind = ? AND t.target_msg_id = m.msg_id
		   )
		 ORDER BY m.msg_id DESC
		 LIMIT ?`,
		q,
		string(model.MessageKindTombstone),
		string(model.MessageKindTombstone),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanAndHydrateMessagesLocked(rows)
}

func (s *SQLiteStore) searchMessagesLIKELocked(q string, limit int) ([]model.Message, error) {
	pattern := "%" + escapeLIKE(q) + "%"
	rows, err := s.db.Query(
		`SELECT m.msg_id, m.author_device_id, m.author_name, m.author_seq, m.kind,
		        m.text_body, m.blob_hash, m.target_msg_id, m.created_at
		 FROM messages m
		 LEFT JOIN file_blobs fb ON fb.blob_hash = m.blob_hash
		 WHERE m.kind != ?
		   AND NOT EXISTS (
		       SELECT 1 FROM messages t
		       WHERE t.kind = ? AND t.target_msg_id = m.msg_id
		   )
		   AND (m.text_body LIKE ? ESCAPE '\' OR COALESCE(fb.file_name, '') LIKE ? ESCAPE '\')
		 ORDER BY m.msg_id DESC
		 LIMIT ?`,
		string(model.MessageKindTombstone),
		string(model.MessageKindTombstone),
		pattern, pattern,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanAndHydrateMessagesLocked(rows)
}

// MessagesAfterClock returns messages where author_seq > vc[author_device_id],
// ordered by msg_id ASC. Tombstone messages ARE included so deletes propagate across devices.
func (s *SQLiteStore) MessagesAfterClock(vc model.VectorClock, limit int) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = -1
	}

	var (
		queryBuilder strings.Builder
		args         []any
	)

	queryBuilder.WriteString(
		`SELECT msg_id, author_device_id, author_name, author_seq, kind,
		        text_body, blob_hash, target_msg_id, created_at
		 FROM messages
		 WHERE author_seq > `,
	)

	if len(vc) == 0 {
		queryBuilder.WriteString(`0`)
	} else {
		keys := make([]string, 0, len(vc))
		for k := range vc {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		queryBuilder.WriteString(`CASE author_device_id`)
		for _, devID := range keys {
			queryBuilder.WriteString(` WHEN ? THEN ?`)
			args = append(args, devID, vc[devID])
		}
		queryBuilder.WriteString(` ELSE 0 END`)
	}

	queryBuilder.WriteString(` ORDER BY msg_id ASC LIMIT ?`)
	args = append(args, limit)

	rows, err := s.db.Query(queryBuilder.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanAndHydrateMessagesLocked(rows)
}

func (s *SQLiteStore) VectorClock() (model.VectorClock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT author_device_id, MAX(author_seq) FROM messages GROUP BY author_device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	vc := make(model.VectorClock)
	for rows.Next() {
		var devID string
		var maxSeq uint64
		if err := rows.Scan(&devID, &maxSeq); err != nil {
			return nil, err
		}
		vc[devID] = maxSeq
	}
	return vc, rows.Err()
}

// --- File Blobs & Transfers ---

func (s *SQLiteStore) UpsertFileBlob(blob model.FileBlob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertFileBlobTx(tx, blob); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertFileBlobTx(tx *sql.Tx, blob model.FileBlob) error {
	// Merge seeders with any existing record so incoming metadata updates don't wipe known seeders.
	var existingSeedersJSON []byte
	var existingFileName string
	err := tx.QueryRow(
		`SELECT file_name, seeders_json FROM file_blobs WHERE blob_hash = ?`,
		blob.BlobHash,
	).Scan(&existingFileName, &existingSeedersJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var existingSeeders []string
	if len(existingSeedersJSON) > 0 {
		_ = json.Unmarshal(existingSeedersJSON, &existingSeeders)
	}
	mergedSeeders := mergeUniqueStrings(existingSeeders, blob.Seeders)

	chunkHashesJSON, err := json.Marshal(blob.ChunkHashes)
	if err != nil {
		return fmt.Errorf("marshal chunk hashes: %w", err)
	}
	seedersJSON, err := json.Marshal(mergedSeeders)
	if err != nil {
		return fmt.Errorf("marshal seeders: %w", err)
	}

	_, err = tx.Exec(
		`INSERT INTO file_blobs(
			blob_hash, file_name, file_size, mime_type, chunk_size, chunk_count,
			chunk_hashes_json, thumbnail_webp, local_path, seeders_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(blob_hash) DO UPDATE SET
			file_name = CASE WHEN excluded.file_name != '' THEN excluded.file_name ELSE file_blobs.file_name END,
			file_size = CASE WHEN excluded.file_size > 0 THEN excluded.file_size ELSE file_blobs.file_size END,
			mime_type = CASE WHEN excluded.mime_type != '' THEN excluded.mime_type ELSE file_blobs.mime_type END,
			chunk_size = CASE WHEN excluded.chunk_size > 0 THEN excluded.chunk_size ELSE file_blobs.chunk_size END,
			chunk_count = CASE WHEN excluded.chunk_count > 0 THEN excluded.chunk_count ELSE file_blobs.chunk_count END,
			chunk_hashes_json = CASE WHEN excluded.chunk_hashes_json IS NOT NULL AND excluded.chunk_hashes_json != 'null' AND excluded.chunk_hashes_json != '[]' THEN excluded.chunk_hashes_json ELSE file_blobs.chunk_hashes_json END,
			thumbnail_webp = CASE WHEN length(excluded.thumbnail_webp) > 0 THEN excluded.thumbnail_webp ELSE file_blobs.thumbnail_webp END,
			local_path = CASE WHEN excluded.local_path != '' THEN excluded.local_path ELSE file_blobs.local_path END,
			seeders_json = excluded.seeders_json`,
		blob.BlobHash, blob.FileName, blob.FileSize, blob.MimeType, blob.ChunkSize, blob.ChunkCount,
		chunkHashesJSON, blob.ThumbnailWebP, blob.LocalPath, seedersJSON,
	)
	if err != nil {
		return err
	}

	// Keep messages_fts synchronized when a file's name is inserted or updated.
	finalFileName := blob.FileName
	if finalFileName == "" {
		finalFileName = existingFileName
	}
	if finalFileName != "" && finalFileName != existingFileName {
		if _, err := tx.Exec(
			`DELETE FROM messages_fts WHERE msg_id IN (SELECT msg_id FROM messages WHERE blob_hash = ?)`,
			blob.BlobHash,
		); err != nil {
			return fmt.Errorf("refresh fts delete: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO messages_fts(msg_id, text_body, file_name)
			 SELECT msg_id, text_body, ? FROM messages WHERE blob_hash = ? AND kind != ?`,
			finalFileName, blob.BlobHash, string(model.MessageKindTombstone),
		); err != nil {
			return fmt.Errorf("refresh fts insert: %w", err)
		}
	}

	return nil
}

func (s *SQLiteStore) GetFileBlob(blobHash string) (*model.FileBlob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getFileBlobLocked(blobHash)
}

func (s *SQLiteStore) getFileBlobLocked(blobHash string) (*model.FileBlob, error) {
	row := s.db.QueryRow(
		`SELECT blob_hash, file_name, file_size, mime_type, chunk_size, chunk_count,
		        chunk_hashes_json, thumbnail_webp, local_path, seeders_json
		 FROM file_blobs WHERE blob_hash = ?`,
		blobHash,
	)
	blob, err := scanFileBlob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &blob, nil
}

func (s *SQLiteStore) AddBlobSeeder(blobHash, deviceID string) error {
	if deviceID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var seedersJSON []byte
	err = tx.QueryRow(`SELECT seeders_json FROM file_blobs WHERE blob_hash = ?`, blobHash).Scan(&seedersJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("file blob %q not found: %w", blobHash, sql.ErrNoRows)
	}
	if err != nil {
		return err
	}

	var seeders []string
	if len(seedersJSON) > 0 {
		if err := json.Unmarshal(seedersJSON, &seeders); err != nil {
			return fmt.Errorf("unmarshal seeders: %w", err)
		}
	}
	merged := mergeUniqueStrings(seeders, []string{deviceID})
	updatedJSON, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal seeders: %w", err)
	}

	if _, err := tx.Exec(`UPDATE file_blobs SET seeders_json = ? WHERE blob_hash = ?`, updatedJSON, blobHash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertTransferTask(task model.TransferTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO transfer_tasks(
			blob_hash, direction, state, chunk_count, chunk_bitmap,
			bytes_done, bytes_total, last_error, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(blob_hash) DO UPDATE SET
			direction = excluded.direction,
			state = excluded.state,
			chunk_count = excluded.chunk_count,
			chunk_bitmap = excluded.chunk_bitmap,
			bytes_done = excluded.bytes_done,
			bytes_total = excluded.bytes_total,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		task.BlobHash, string(task.Direction), string(task.State), task.ChunkCount, task.ChunkBitmap,
		task.BytesDone, task.BytesTotal, task.LastError, task.UpdatedAt,
	)
	return err
}

func (s *SQLiteStore) GetTransferTask(blobHash string) (*model.TransferTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT blob_hash, direction, state, chunk_count, chunk_bitmap,
		        bytes_done, bytes_total, last_error, updated_at
		 FROM transfer_tasks WHERE blob_hash = ?`,
		blobHash,
	)
	task, err := scanTransferTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *SQLiteStore) ListTransferTasks() ([]model.TransferTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT blob_hash, direction, state, chunk_count, chunk_bitmap,
		        bytes_done, bytes_total, last_error, updated_at
		 FROM transfer_tasks
		 ORDER BY updated_at DESC, blob_hash ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []model.TransferTask
	for rows.Next() {
		task, err := scanTransferTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// --- Scanning & Helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(sc scanner) (model.Device, error) {
	var (
		dev       model.Device
		routeJSON []byte
		revoked   int
		isSelf    int
	)
	err := sc.Scan(
		&dev.DeviceID, &dev.Name, &dev.Platform, &dev.SignPublicKey, &routeJSON,
		&dev.RouteEpoch, &revoked, &dev.AddedAt, &dev.LastSeenAt, &isSelf,
	)
	if err != nil {
		return model.Device{}, err
	}
	if len(routeJSON) > 0 {
		if err := json.Unmarshal(routeJSON, &dev.Route); err != nil {
			return model.Device{}, fmt.Errorf("unmarshal route: %w", err)
		}
	}
	dev.Revoked = revoked != 0
	dev.IsSelf = isSelf != 0
	return dev, nil
}

func scanMessage(sc scanner) (model.Message, error) {
	var (
		msg  model.Message
		kind string
	)
	err := sc.Scan(
		&msg.MsgID, &msg.AuthorDeviceID, &msg.AuthorName, &msg.AuthorSeq, &kind,
		&msg.TextBody, &msg.BlobHash, &msg.TargetMsgID, &msg.CreatedAt,
	)
	if err != nil {
		return model.Message{}, err
	}
	msg.Kind = model.MessageKind(kind)
	return msg, nil
}

func scanFileBlob(sc scanner) (model.FileBlob, error) {
	var (
		blob            model.FileBlob
		chunkHashesJSON []byte
		seedersJSON     []byte
	)
	err := sc.Scan(
		&blob.BlobHash, &blob.FileName, &blob.FileSize, &blob.MimeType, &blob.ChunkSize, &blob.ChunkCount,
		&chunkHashesJSON, &blob.ThumbnailWebP, &blob.LocalPath, &seedersJSON,
	)
	if err != nil {
		return model.FileBlob{}, err
	}
	if len(chunkHashesJSON) > 0 {
		if err := json.Unmarshal(chunkHashesJSON, &blob.ChunkHashes); err != nil {
			return model.FileBlob{}, fmt.Errorf("unmarshal chunk hashes: %w", err)
		}
	}
	if len(seedersJSON) > 0 {
		if err := json.Unmarshal(seedersJSON, &blob.Seeders); err != nil {
			return model.FileBlob{}, fmt.Errorf("unmarshal seeders: %w", err)
		}
	}
	return blob, nil
}

func scanTransferTask(sc scanner) (model.TransferTask, error) {
	var (
		task      model.TransferTask
		direction string
		state     string
	)
	err := sc.Scan(
		&task.BlobHash, &direction, &state, &task.ChunkCount, &task.ChunkBitmap,
		&task.BytesDone, &task.BytesTotal, &task.LastError, &task.UpdatedAt,
	)
	if err != nil {
		return model.TransferTask{}, err
	}
	task.Direction = model.TransferDirection(direction)
	task.State = model.TransferState(state)
	return task, nil
}

func (s *SQLiteStore) scanAndHydrateMessagesLocked(rows *sql.Rows) ([]model.Message, error) {
	var msgs []model.Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close rows before issuing hydration queries on the single SQLite connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range msgs {
		if err := s.hydrateMessageFileLocked(&msgs[i]); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

func (s *SQLiteStore) hydrateMessageFileLocked(msg *model.Message) error {
	if msg.BlobHash == "" {
		return nil
	}
	blob, err := s.getFileBlobLocked(msg.BlobHash)
	if err != nil {
		return err
	}
	msg.File = blob
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mergeUniqueStrings(existing, added []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(added))
	out := make([]string, 0, len(existing)+len(added))
	for _, s := range existing {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	for _, s := range added {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func escapeLIKE(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return r.Replace(s)
}
