package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if db == nil {
		t.Fatal("Open() returned nil DB")
	}
}

func TestMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.Migrate(); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
}

func TestUpdateIdleConns(t *testing.T) {
	db := openTestDB(t)

	tests := []struct {
		name        string
		numAccounts int
	}{
		{"zero accounts", 0},
		{"three accounts", 3},
		{"ten accounts", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify no panic
			db.UpdateIdleConns(tt.numAccounts)
		})
	}
}

func TestCheckpoint(t *testing.T) {
	db := openTestDB(t)

	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
}

// TestMigrationV29_OAuthCompositeKey verifies the Phase 1 extension-system
// migration: oauth_tokens now uses composite PK (account_id, client_config_id)
// so a single account can hold separate token rows for Mail vs extension-
// scoped OAuth clients.
func TestMigrationV29_OAuthCompositeKey(t *testing.T) {
	db := openTestDB(t)

	// Insert a test account row (oauth_tokens.account_id FK to accounts.id).
	// Schema defaults handle most columns; only NOT NULL non-default fields are explicit.
	if _, err := db.Exec(`
		INSERT INTO accounts (id, name, email, imap_host, smtp_host, username)
		VALUES ('acct-1', 'Test', 'user@example.com', 'imap.example.com', 'smtp.example.com', 'user@example.com')
	`); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Insert mail-config token row
	if _, err := db.Exec(`
		INSERT INTO oauth_tokens (account_id, client_config_id, provider, expires_at, scopes)
		VALUES ('acct-1', 'google-mail', 'google', CURRENT_TIMESTAMP, '[]')
	`); err != nil {
		t.Fatalf("insert mail token row: %v", err)
	}

	// Insert extension-config token row for same account — should succeed
	if _, err := db.Exec(`
		INSERT INTO oauth_tokens (account_id, client_config_id, provider, expires_at, scopes)
		VALUES ('acct-1', 'google-extensions', 'google', CURRENT_TIMESTAMP, '[]')
	`); err != nil {
		t.Fatalf("insert extension token row failed (composite PK should allow it): %v", err)
	}

	// Verify both rows exist
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_tokens WHERE account_id = 'acct-1'`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 token rows for account, got %d", count)
	}

	// Duplicate (account_id, client_config_id) must violate the composite PK
	if _, err := db.Exec(`
		INSERT INTO oauth_tokens (account_id, client_config_id, provider, expires_at, scopes)
		VALUES ('acct-1', 'google-mail', 'google', CURRENT_TIMESTAMP, '[]')
	`); err == nil {
		t.Fatal("expected composite PK conflict on duplicate (account_id, client_config_id), got no error")
	}
}

// TestMigrationV32_LocalRecordIDsRewrittenToUUIDs verifies that migration 32
// transforms "local-<email>" record IDs into canonical UUIDv4s while keeping
// the contact_emails references intact. Simulates the upgrade path for a user
// who applied migration 31 (id format was "local-X@Y") and is now upgrading
// to the schema that uses UUIDs.
func TestMigrationV32_LocalRecordIDsRewrittenToUUIDs(t *testing.T) {
	db := openTestDB(t)

	// Seed legacy v31-shape data: a local record with the "local-<email>"
	// synthetic id. Delete the migration 32 marker so it re-applies and
	// rewrites this row.
	if _, err := db.Exec(`
		INSERT INTO contact_records (id, source, kind, fn)
		VALUES ('local-alice@example.com', 'local', 'collected', 'Alice')
	`); err != nil {
		t.Fatalf("seed contact_records: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO contact_emails (record_id, email, send_count, is_primary)
		VALUES ('local-alice@example.com', 'alice@example.com', 5, 1)
	`); err != nil {
		t.Fatalf("seed contact_emails: %v", err)
	}
	// Clear v32 AND any later markers so Migrate() sees v32 as pending.
	// Migrate compares against MAX(version), so leaving a later marker
	// (e.g., v33) would cause v32 to be skipped on the re-run.
	if _, err := db.Exec(`DELETE FROM migrations WHERE version >= 32`); err != nil {
		t.Fatalf("clear migration 32+ markers: %v", err)
	}
	// Drop v34's photo columns so its re-application's ADD COLUMNs don't
	// collide. SQLite supports DROP COLUMN since 3.35; modernc.org/sqlite
	// is well past that.
	for _, col := range []string{"photo_data", "photo_media_type", "photo_url"} {
		if _, err := db.Exec(`ALTER TABLE contact_records DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s for re-migrate: %v", col, err)
		}
	}
	// Drop v36's encrypted fallback columns on oauth_tokens for the same
	// reason — re-running v36 ADDs them again.
	for _, col := range []string{"encrypted_access_token", "encrypted_refresh_token"} {
		if _, err := db.Exec(`ALTER TABLE oauth_tokens DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop oauth_tokens.%s for re-migrate: %v", col, err)
		}
	}
	// Drop v37's SMTP-receive-only / SMTP-creds columns + v38's
	// reply_forward_identity_id + v41's smtp_auth_mechanism + v44's
	// imap_require_id on accounts so the re-application's ADD COLUMNs
	// don't collide.
	for _, col := range []string{"no_outgoing_server", "smtp_username", "encrypted_smtp_password", "reply_forward_identity_id", "oauth_stable_id", "imap_auth_mechanism", "smtp_auth_mechanism", "imap_require_id"} {
		if _, err := db.Exec(`ALTER TABLE accounts DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop accounts.%s for re-migrate: %v", col, err)
		}
	}
	// Same for v39's body_failed on messages.
	if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN body_failed`); err != nil {
		t.Fatalf("drop messages.body_failed for re-migrate: %v", err)
	}
	// And v53's body_truncated on messages.
	if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN body_truncated`); err != nil {
		t.Fatalf("drop messages.body_truncated for re-migrate: %v", err)
	}
	// And v45's is_hidden on messages.
	if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN is_hidden`); err != nil {
		t.Fatalf("drop messages.is_hidden for re-migrate: %v", err)
	}
	// And v46's enrichment columns on messages (indexes first: SQLite
	// refuses DROP COLUMN while an index references the column).
	for _, idx := range []string{"idx_messages_category", "idx_messages_extract"} {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			t.Fatalf("drop index %s for re-migrate: %v", idx, err)
		}
	}
	for _, col := range []string{"list_unsubscribe", "list_unsubscribe_post", "category", "sub_category", "extract_kind", "extract_value", "extract_rule"} {
		if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop messages.%s for re-migrate: %v", col, err)
		}
	}
	// And v43's reply_to on drafts.
	if _, err := db.Exec(`ALTER TABLE drafts DROP COLUMN reply_to`); err != nil {
		t.Fatalf("drop drafts.reply_to for re-migrate: %v", err)
	}
	// And v49's body_hash on message_translations.
	if _, err := db.Exec(`ALTER TABLE message_translations DROP COLUMN body_hash`); err != nil {
		t.Fatalf("drop message_translations.body_hash for re-migrate: %v", err)
	}
	// And v52's kind / raw_message / reaction_message_id on send_queue.
	for _, col := range []string{"kind", "raw_message", "reaction_message_id"} {
		if _, err := db.Exec(`ALTER TABLE send_queue DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop send_queue.%s for re-migrate: %v", col, err)
		}
	}
	// And v55's writable on contact_source_addressbooks.
	if _, err := db.Exec(`ALTER TABLE contact_source_addressbooks DROP COLUMN writable`); err != nil {
		t.Fatalf("drop contact_source_addressbooks.writable for re-migrate: %v", err)
	}

	// Re-run migrations — migration 32 should rewrite the seeded local- id.
	if err := db.Migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	// Record id should now be a UUID (length 36, 4 dashes, hex elsewhere).
	var id string
	if err := db.QueryRow(`SELECT id FROM contact_records WHERE source = 'local'`).Scan(&id); err != nil {
		t.Fatalf("query rewritten id: %v", err)
	}
	if len(id) != 36 {
		t.Errorf("id length = %d, want 36 (UUID)", len(id))
	}
	if id == "local-alice@example.com" {
		t.Errorf("id still has the legacy 'local-' shape: %q", id)
	}

	// contact_emails reference should point at the NEW id, not the old one.
	var refCount, emailCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_emails WHERE record_id = ?`, id).Scan(&refCount); err != nil {
		t.Fatalf("count refs to new id: %v", err)
	}
	if refCount != 1 {
		t.Errorf("contact_emails row pointing at new id: got %d, want 1", refCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_emails WHERE record_id = 'local-alice@example.com'`).Scan(&emailCount); err != nil {
		t.Fatalf("count orphan refs: %v", err)
	}
	if emailCount != 0 {
		t.Errorf("contact_emails still references old id: got %d orphan refs", emailCount)
	}

	// Email content + autocomplete metadata are unchanged.
	var email string
	var sendCount int
	if err := db.QueryRow(`SELECT email, send_count FROM contact_emails WHERE record_id = ?`, id).Scan(&email, &sendCount); err != nil {
		t.Fatalf("query preserved fields: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", email)
	}
	if sendCount != 5 {
		t.Errorf("send_count = %d, want 5 (preserved through migration)", sendCount)
	}
}

// TestMigrationV33_AddsAddressbookFK verifies migration 33 cleans existing
// orphans AND wires the new FK so future addressbook deletes cascade to
// state rows automatically.
func TestMigrationV33_AddsAddressbookFK(t *testing.T) {
	db := openTestDB(t)

	// Seed a source + addressbook + record so we have a row to chain through.
	if _, err := db.Exec(`
		INSERT INTO contact_sources (id, name, type, url, username, enabled, sync_interval)
		VALUES ('src-1', 'Test', 'carddav', 'https://x', 'u', 1, 60)
	`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO contact_source_addressbooks (id, source_id, path, name, enabled)
		VALUES ('ab-1', 'src-1', '/dav/', 'ab', 1)
	`); err != nil {
		t.Fatalf("seed addressbook: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO contact_records (id, source, source_ref, fn)
		VALUES ('rec-1', 'carddav', 'ab-1', 'Test')
	`); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO carddav_record_state (record_id, addressbook_id, href, etag)
		VALUES ('rec-1', 'ab-1', '/dav/rec-1.vcf', 'etag')
	`); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// Schema-level invariant: deleting the addressbook now cascades to state.
	// Pre-migration this would have left the state row as a zombie.
	if _, err := db.Exec(`DELETE FROM contact_source_addressbooks WHERE id = 'ab-1'`); err != nil {
		t.Fatalf("delete addressbook: %v", err)
	}

	var stateRows, recordRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM carddav_record_state WHERE record_id = 'rec-1'`).Scan(&stateRows); err != nil {
		t.Fatalf("count state: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_records WHERE id = 'rec-1'`).Scan(&recordRows); err != nil {
		t.Fatalf("count records: %v", err)
	}
	if stateRows != 0 {
		t.Errorf("expected 0 state rows after addressbook delete, got %d", stateRows)
	}
	// contact_records does NOT cascade from state (cascade goes the other
	// direction: record→state). So the record row survives. That's expected.
	// (The application-level DeleteSource path handles record cleanup.)
	if recordRows != 1 {
		t.Errorf("expected record to survive addressbook delete (no cascade in that direction); got %d rows", recordRows)
	}
}

// TestMigrationV33_CleansExistingOrphans verifies the pre-step that scrubs
// orphan state rows + records before the FK is added. Simulates the v32
// state by rebuilding carddav_record_state WITHOUT the FK, seeding orphans,
// then re-running migration 33 — which must pre-clean orphans before the
// table rebuild's INSERT.
func TestMigrationV33_CleansExistingOrphans(t *testing.T) {
	db := openTestDB(t)

	// Roll back the v33 schema: drop the FK by rebuilding the table without
	// it. This mimics what an install at v32 would have looked like.
	if _, err := db.Exec(`
		PRAGMA foreign_keys = OFF;
		DROP TABLE carddav_record_state;
		CREATE TABLE carddav_record_state (
			record_id       TEXT PRIMARY KEY REFERENCES contact_records(id) ON DELETE CASCADE,
			addressbook_id  TEXT NOT NULL,
			href            TEXT NOT NULL UNIQUE,
			etag            TEXT,
			synced_at       DATETIME
		);
		CREATE INDEX idx_carddav_record_state_addressbook
			ON carddav_record_state(addressbook_id);
		DELETE FROM migrations WHERE version >= 33;
		PRAGMA foreign_keys = ON;
	`); err != nil {
		t.Fatalf("rewind to v32 schema: %v", err)
	}
	// Drop v34's photo columns so re-application's ADD COLUMNs don't collide.
	for _, col := range []string{"photo_data", "photo_media_type", "photo_url"} {
		if _, err := db.Exec(`ALTER TABLE contact_records DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s for re-migrate: %v", col, err)
		}
	}
	// Same for v36's encrypted oauth_tokens fallback columns.
	for _, col := range []string{"encrypted_access_token", "encrypted_refresh_token"} {
		if _, err := db.Exec(`ALTER TABLE oauth_tokens DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop oauth_tokens.%s for re-migrate: %v", col, err)
		}
	}
	// Same for v37 + v38 + v40 + v41 + v42's accounts columns.
	for _, col := range []string{"no_outgoing_server", "smtp_username", "encrypted_smtp_password", "reply_forward_identity_id", "oauth_stable_id", "imap_auth_mechanism", "smtp_auth_mechanism", "imap_require_id"} {
		if _, err := db.Exec(`ALTER TABLE accounts DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop accounts.%s for re-migrate: %v", col, err)
		}
	}
	// And v39's body_failed on messages.
	if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN body_failed`); err != nil {
		t.Fatalf("drop messages.body_failed for re-migrate: %v", err)
	}
	// And v53's body_truncated on messages.
	if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN body_truncated`); err != nil {
		t.Fatalf("drop messages.body_truncated for re-migrate: %v", err)
	}
	// And v45's is_hidden on messages.
	if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN is_hidden`); err != nil {
		t.Fatalf("drop messages.is_hidden for re-migrate: %v", err)
	}
	// And v46's enrichment columns on messages (indexes first: SQLite
	// refuses DROP COLUMN while an index references the column).
	for _, idx := range []string{"idx_messages_category", "idx_messages_extract"} {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			t.Fatalf("drop index %s for re-migrate: %v", idx, err)
		}
	}
	for _, col := range []string{"list_unsubscribe", "list_unsubscribe_post", "category", "sub_category", "extract_kind", "extract_value", "extract_rule"} {
		if _, err := db.Exec(`ALTER TABLE messages DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop messages.%s for re-migrate: %v", col, err)
		}
	}
	// And v43's reply_to on drafts.
	if _, err := db.Exec(`ALTER TABLE drafts DROP COLUMN reply_to`); err != nil {
		t.Fatalf("drop drafts.reply_to for re-migrate: %v", err)
	}
	// And v49's body_hash on message_translations.
	if _, err := db.Exec(`ALTER TABLE message_translations DROP COLUMN body_hash`); err != nil {
		t.Fatalf("drop message_translations.body_hash for re-migrate: %v", err)
	}
	// And v52's kind / raw_message / reaction_message_id on send_queue.
	for _, col := range []string{"kind", "raw_message", "reaction_message_id"} {
		if _, err := db.Exec(`ALTER TABLE send_queue DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop send_queue.%s for re-migrate: %v", col, err)
		}
	}
	// And v55's writable on contact_source_addressbooks.
	if _, err := db.Exec(`ALTER TABLE contact_source_addressbooks DROP COLUMN writable`); err != nil {
		t.Fatalf("drop contact_source_addressbooks.writable for re-migrate: %v", err)
	}

	// Seed: orphan state row whose addressbook doesn't exist. Pre-migration,
	// this insert succeeds because the FK isn't there.
	if _, err := db.Exec(`
		INSERT INTO contact_records (id, source, source_ref, fn)
		VALUES ('zombie-rec', 'carddav', 'dead-ab-id', 'Zombie')
	`); err != nil {
		t.Fatalf("seed zombie record: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO carddav_record_state (record_id, addressbook_id, href, etag)
		VALUES ('zombie-rec', 'dead-ab-id', '/dav/zombie.vcf', 'etag')
	`); err != nil {
		t.Fatalf("seed zombie state: %v", err)
	}

	// Seed: contact_records (carddav) with no state row — bloat the
	// migration should drop in pre-step 2.
	if _, err := db.Exec(`
		INSERT INTO contact_records (id, source, fn)
		VALUES ('orphan-record', 'carddav', 'OrphanRec')
	`); err != nil {
		t.Fatalf("seed orphan record: %v", err)
	}

	// Re-run migrations; v33 should pre-clean both before rebuilding the
	// table with the FK.
	if err := db.Migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	var stateCount, recordCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM carddav_record_state WHERE record_id = 'zombie-rec'`).Scan(&stateCount); err != nil {
		t.Fatalf("count zombie state: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_records WHERE id IN ('zombie-rec','orphan-record')`).Scan(&recordCount); err != nil {
		t.Fatalf("count orphan records: %v", err)
	}
	if stateCount != 0 {
		t.Errorf("zombie state row should have been pre-cleaned; got %d", stateCount)
	}
	if recordCount != 0 {
		t.Errorf("orphan records (no state) should have been pre-cleaned; got %d", recordCount)
	}

	// FK should now be enforcing — try inserting another orphan, expect FK violation.
	_, err := db.Exec(`
		INSERT INTO contact_records (id, source, fn) VALUES ('post-rec', 'carddav', 'Post');
	`)
	if err != nil {
		t.Fatalf("insert valid record: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO carddav_record_state (record_id, addressbook_id, href, etag)
		VALUES ('post-rec', 'still-dead', '/dav/post.vcf', 'etag')
	`)
	if err == nil {
		t.Error("expected FK violation when inserting state pointing at dead addressbook_id post-migration, got nil")
	}
}

func TestPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if got := db.Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}

// TestMigrationV34_AddsPhotoColumns verifies migration 34 adds the three
// PHOTO columns to contact_records and that existing rows survive with NULL
// values for the new fields.
func TestMigrationV34_AddsPhotoColumns(t *testing.T) {
	db := openTestDB(t)

	// Seed a record before migration 34 (well — migration 34 already ran via
	// openTestDB, but we check that the columns exist + are queryable).
	if _, err := db.Exec(`
		INSERT INTO contact_records (id, source, fn, photo_data, photo_media_type, photo_url)
		VALUES ('rec-1', 'local', 'Alice', 'BASE64DATA', 'image/jpeg', NULL)
	`); err != nil {
		t.Fatalf("insert record with photo: %v", err)
	}

	var photoData, mediaType, photoURL sql.NullString
	if err := db.QueryRow(`
		SELECT photo_data, photo_media_type, photo_url
		FROM contact_records WHERE id = 'rec-1'
	`).Scan(&photoData, &mediaType, &photoURL); err != nil {
		t.Fatalf("read photo columns: %v", err)
	}
	if photoData.String != "BASE64DATA" {
		t.Errorf("photo_data = %q, want BASE64DATA", photoData.String)
	}
	if mediaType.String != "image/jpeg" {
		t.Errorf("photo_media_type = %q, want image/jpeg", mediaType.String)
	}
	if photoURL.Valid {
		t.Errorf("photo_url should be NULL, got %q", photoURL.String)
	}

	// Record with no photo: all three columns NULL.
	if _, err := db.Exec(`
		INSERT INTO contact_records (id, source, fn)
		VALUES ('rec-2', 'local', 'Bob')
	`); err != nil {
		t.Fatalf("insert record without photo: %v", err)
	}
	var dataNull, typeNull, urlNull sql.NullString
	if err := db.QueryRow(`
		SELECT photo_data, photo_media_type, photo_url
		FROM contact_records WHERE id = 'rec-2'
	`).Scan(&dataNull, &typeNull, &urlNull); err != nil {
		t.Fatalf("read NULL photo columns: %v", err)
	}
	if dataNull.Valid || typeNull.Valid || urlNull.Valid {
		t.Errorf("expected all NULL, got %v/%v/%v", dataNull, typeNull, urlNull)
	}
}

// Regression for #289: upgrading from v0.2.5 with a "messy" CardDAV collection
// that lists the same address twice for one vCard must not crash the app inside
// migration 31. The CardDAV backfill collapses fanned-out rows onto one
// record_id; a plain INSERT tripped PRIMARY KEY(record_id, email) and rolled the
// whole startup migration back. INSERT OR IGNORE dedupes instead.
func TestMigration31_DuplicateCardDAVEmailDoesNotFailMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// applyMigration records into the migrations table, which Migrate() normally
	// creates first — make it here since we drive applyMigration directly.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS migrations (version INTEGER PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create migrations table: %v", err)
	}

	// Build the pre-unification (v30) schema so the legacy tables exist.
	for _, m := range migrations {
		if m.Version <= 30 {
			if err := db.applyMigration(m); err != nil {
				t.Fatalf("apply migration %d: %v", m.Version, err)
			}
		}
	}

	// Seed one CardDAV vCard (single href) whose old schema fanned the SAME
	// address out across two rows — the exact #289 trigger.
	if _, err := db.Exec(`
		INSERT INTO contact_sources (id, name, type, url) VALUES ('src1','Test','carddav','https://example.com/dav');
		INSERT INTO contact_source_addressbooks (id, source_id, path) VALUES ('ab1','src1','/');
		INSERT INTO carddav_contacts (id, addressbook_id, email, display_name, href, synced_at) VALUES
			('c1','ab1','dup@example.com','Dup Person','/contacts/1.vcf',CURRENT_TIMESTAMP),
			('c2','ab1','dup@example.com','Dup Person','/contacts/1.vcf',CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatalf("seed legacy carddav_contacts: %v", err)
	}

	// Apply migration 31 and everything after — must NOT fail on the duplicate.
	for _, m := range migrations {
		if m.Version > 30 {
			if err := db.applyMigration(m); err != nil {
				t.Fatalf("migration %d failed on duplicate CardDAV email (#289): %v", m.Version, err)
			}
		}
	}

	// The duplicate collapsed to exactly one contact_emails row.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_emails WHERE email = 'dup@example.com'`).Scan(&n); err != nil {
		t.Fatalf("count contact_emails: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 deduped contact_emails row, got %d", n)
	}
}

// TestMigrationV51_DedupesAttachments verifies the Nov 21 duplicate-
// attachment cleanup: exact-duplicate rows (same message, filename,
// type, size, content-id, inline flag) collapse to one, preferring the
// row with a downloaded file; genuinely different files that share a
// filename are untouched.
func TestMigrationV51_DedupesAttachments(t *testing.T) {
	db := openTestDB(t)

	// Rewind just migration 51's record (the schema stays), so
	// applying it again below records cleanly.
	if _, err := db.Exec(`DELETE FROM migrations WHERE version = 51`); err != nil {
		t.Fatalf("rewind migration 51: %v", err)
	}

	// Seed with FKs off: only the attachment rows matter here.
	// m1 mirrors the Nov 21 report: 5 files, each stored twice (10
	// rows). The second copy of one file has a local_path (downloaded);
	// the kept row must be that one. m2 has two genuinely different
	// files sharing a filename (different size, different content-id);
	// both must survive.
	if _, err := db.Exec(`
		PRAGMA foreign_keys = OFF;
		INSERT INTO attachments (id, message_id, filename, content_type, size, content_id, is_inline, local_path) VALUES
			('a1', 'm1', '1000210286.jpg', 'image/jpeg', 1400000, '<img1@x>', 1, NULL),
			('a2', 'm1', '1000210286.jpg', 'image/jpeg', 1400000, '<img1@x>', 1, NULL),
			('a3', 'm1', '1000210287.jpg', 'image/jpeg', 1200000, NULL, 1, NULL),
			('a4', 'm1', '1000210287.jpg', 'image/jpeg', 1200000, NULL, 1, NULL),
			('a5', 'm1', '1000212485.jpg', 'image/jpeg', 377600, NULL, 1, NULL),
			('a6', 'm1', '1000212485.jpg', 'image/jpeg', 377600, NULL, 1, NULL),
			('a7', 'm1', 'Muhammad Umar CV-1.pdf', 'application/pdf', 336500, NULL, 1, NULL),
			('a8', 'm1', 'Muhammad Umar CV-1.pdf', 'application/pdf', 336500, NULL, 1, '/tmp/cv.pdf'),
			('a9', 'm1', 'douctoment.pdf', 'application/pdf', 968000, NULL, 1, NULL),
			('aa', 'm1', 'douctoment.pdf', 'application/pdf', 968000, NULL, 1, NULL),
			('b1', 'm2', 'photo.jpg', 'image/jpeg', 100, '<cid-a@x>', 0, NULL),
			('b2', 'm2', 'photo.jpg', 'image/jpeg', 200, '<cid-b@x>', 0, NULL);
		PRAGMA foreign_keys = ON;
	`); err != nil {
		t.Fatalf("seed attachments: %v", err)
	}

	// Apply migration 51 directly. (Do not rewind + Migrate():
	// later migrations such as 52's ALTER TABLE ADD COLUMN are not
	// re-runnable on a DB that already has the column, and Migrate
	// keys off MAX(version) — 52 — so 51 would never re-run.)
	var m51 *Migration
	for i := range migrations {
		if migrations[i].Version == 51 {
			m51 = &migrations[i]
			break
		}
	}
	if m51 == nil {
		t.Fatal("migration 51 not found")
	}
	if err := db.applyMigration(*m51); err != nil {
		t.Fatalf("apply migration 51: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE message_id = 'm1'`).Scan(&n); err != nil {
		t.Fatalf("count m1: %v", err)
	}
	if n != 5 {
		t.Fatalf("m1 attachments = %d, want 5 (each file once)", n)
	}
	var keptPath sql.NullString
	if err := db.QueryRow(`SELECT local_path FROM attachments WHERE message_id = 'm1' AND filename = 'Muhammad Umar CV-1.pdf'`).Scan(&keptPath); err != nil {
		t.Fatalf("kept CV row: %v", err)
	}
	if !keptPath.Valid || keptPath.String != "/tmp/cv.pdf" {
		t.Fatalf("kept CV local_path = %q, want /tmp/cv.pdf (downloaded copy wins)", keptPath.String)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE message_id = 'm2'`).Scan(&n); err != nil {
		t.Fatalf("count m2: %v", err)
	}
	if n != 2 {
		t.Fatalf("m2 attachments = %d, want 2 (different files sharing a name are kept)", n)
	}
}

// TestMigrationV54_DedupesDuplicateActiveDraftRows verifies migration
// 54's pre-step: a DB that already holds two live rows for the same
// draft (queued before the fix) must migrate cleanly, keeping only
// the oldest live row, and the partial unique index must then exist
// and enforce itself.
func TestMigrationV54_DedupesDuplicateActiveDraftRows(t *testing.T) {
	db := openTestDB(t)

	// Rewind to the v53 schema: drop the v54 index and its
	// migration record, so Migrate() re-applies v54 below. Clear the
	// v55 marker too — Migrate keys off MAX(version), so leaving it
	// would skip v54 — and drop v55's writable column so its
	// re-application's ADD COLUMN doesn't collide.
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_send_queue_draft_active`); err != nil {
		t.Fatalf("drop v54 index for re-migrate: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM migrations WHERE version >= 54`); err != nil {
		t.Fatalf("delete v54+ migration records: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE contact_source_addressbooks DROP COLUMN writable`); err != nil {
		t.Fatalf("drop contact_source_addressbooks.writable for re-migrate: %v", err)
	}

	// Seed two live rows for the same draft, plus a failed row for
	// it (untouched by the dedupe) and a live row for another draft.
	seed := func(id, draftID, status, createdAt string) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO send_queue
				(id, account_id, draft_id, message_json, status,
				 send_at, due_at, created_at)
			VALUES (?, 'acc-1', ?, '', ?, '2026-11-25T00:00:00Z',
				'2026-11-25T00:00:00Z', ?)
		`, id, draftID, status, createdAt); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("dup-old", "draft-1", "pending", "2026-11-25T00:00:00Z")
	seed("dup-new", "draft-1", "pending", "2026-11-25T00:01:00Z")
	seed("dup-failed", "draft-1", "failed", "2026-11-25T00:02:00Z")
	seed("other-live", "draft-2", "pending", "2026-11-25T00:00:00Z")

	if err := db.Migrate(); err != nil {
		t.Fatalf("re-migrate to v54: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE draft_id = 'draft-1' AND status = 'pending'`).Scan(&n); err != nil {
		t.Fatalf("count live draft-1 rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("live draft-1 rows = %d, want 1 (oldest kept)", n)
	}
	var kept string
	if err := db.QueryRow(`SELECT id FROM send_queue WHERE draft_id = 'draft-1' AND status = 'pending'`).Scan(&kept); err != nil {
		t.Fatalf("kept live row: %v", err)
	}
	if kept != "dup-old" {
		t.Fatalf("kept live row = %q, want dup-old (oldest)", kept)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE id = 'dup-failed'`).Scan(&n); err != nil {
		t.Fatalf("count failed row: %v", err)
	}
	if n != 1 {
		t.Fatalf("failed row survived = %d, want 1 (dedupe touches live rows only)", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_send_queue_draft_active'`).Scan(&n); err != nil {
		t.Fatalf("count v54 index: %v", err)
	}
	if n != 1 {
		t.Fatalf("v54 index present = %d, want 1", n)
	}
	// And the index enforces itself after the migration.
	if _, err := db.Exec(`
		INSERT INTO send_queue
			(id, account_id, draft_id, message_json, status,
			 send_at, due_at, created_at)
		VALUES ('dup-third', 'acc-1', 'draft-1', '', 'pending',
			'2026-11-25T00:00:00Z', '2026-11-25T00:00:00Z', '2026-11-25T00:03:00Z')
	`); err == nil {
		t.Fatalf("inserting a second live row for draft-1 succeeded, want a UNIQUE constraint failure")
	}
}

// TestMigrationV55_AddsAddressbookWritable (re-audit R22): the
// per-address-book writability column exists after migration, and
// pre-existing addressbook rows default to writable — the old
// behaviour is preserved until a discovery probe or a 403 PUT
// proves a book read-only.
func TestMigrationV55_AddsAddressbookWritable(t *testing.T) {
	db := openTestDB(t)

	// Rewind v55: drop the column and its marker, so Migrate()
	// re-applies it below.
	if _, err := db.Exec(`ALTER TABLE contact_source_addressbooks DROP COLUMN writable`); err != nil {
		t.Fatalf("drop writable for re-migrate: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM migrations WHERE version = 55`); err != nil {
		t.Fatalf("delete v55 migration record: %v", err)
	}

	// Seed a pre-v55 addressbook row (no writable value to insert).
	if _, err := db.Exec(`
		INSERT INTO contact_sources (id, name, type, url, enabled, sync_interval, created_at)
		VALUES ('src-55', 'Test DAV', 'carddav', 'https://example/', 1, 60, '2026-11-25T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO contact_source_addressbooks (id, source_id, path, name, enabled)
		VALUES ('ab-55', 'src-55', '/ab/', 'Book', 1)
	`); err != nil {
		t.Fatalf("seed addressbook: %v", err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("re-migrate to v55: %v", err)
	}

	var writable int
	if err := db.QueryRow(`SELECT writable FROM contact_source_addressbooks WHERE id = 'ab-55'`).Scan(&writable); err != nil {
		t.Fatalf("query writable: %v", err)
	}
	if writable != 1 {
		t.Fatalf("pre-existing addressbook writable = %d, want 1 (default writable)", writable)
	}
}
