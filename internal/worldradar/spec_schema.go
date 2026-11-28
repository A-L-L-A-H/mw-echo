package worldradar

import (
	"database/sql"
	"fmt"
)

// SpecSchemaSQL defines the normalized SQLite schema from World Radar Implementation Spec Sections 2.1 and 3.3.
// All tables carry the four temporal fields: created_at, updated_at, valid_from, valid_to (ISO8601 UTC).
const SpecSchemaSQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS radar_instances (
    instance_id      TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    description      TEXT,
    version          TEXT NOT NULL,
    taxonomy_version TEXT NOT NULL,
    config_json      TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('active','paused','archived')),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    valid_from       TEXT NOT NULL,
    valid_to         TEXT
);

CREATE TABLE IF NOT EXISTS radar_sources (
    source_id          TEXT PRIMARY KEY,
    instance_id        TEXT NOT NULL REFERENCES radar_instances(instance_id),
    source_tier        TEXT NOT NULL CHECK (source_tier IN ('A','B','C','D','E','F','G','X')),
    category           TEXT NOT NULL,
    trust_base         REAL NOT NULL CHECK (trust_base BETWEEN 0 AND 1),
    collection_method  TEXT NOT NULL CHECK (collection_method IN ('pull','push','public_api','document_drop','sensor_stream','manual')),
    license_class      TEXT NOT NULL,
    language_hint      TEXT,
    geography_scope    TEXT NOT NULL CHECK (geography_scope IN ('global','regional','local','unknown')),
    status             TEXT NOT NULL CHECK (status IN ('probation','active','suspended','retired')),
    independence_group TEXT,
    config_json        TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    valid_from         TEXT NOT NULL,
    valid_to           TEXT
);

CREATE TABLE IF NOT EXISTS radar_observations (
    observation_id   TEXT PRIMARY KEY,
    source_id        TEXT NOT NULL REFERENCES radar_sources(source_id),
    source_version   TEXT NOT NULL,
    captured_at      TEXT NOT NULL,
    published_at     TEXT,
    observed_at      TEXT,
    canonical_url    TEXT,
    content_hash     TEXT NOT NULL,
    raw_hash         TEXT NOT NULL,
    language         TEXT,
    payload_type     TEXT NOT NULL CHECK (payload_type IN ('text','html','pdf','image','structured_record','sensor_record','other')),
    license_class    TEXT NOT NULL,
    validation_state TEXT NOT NULL CHECK (validation_state IN ('accepted','quarantined','rejected')),
    raw_payload      BLOB,
    metadata_json    TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    valid_from       TEXT NOT NULL,
    valid_to         TEXT
);

CREATE TABLE IF NOT EXISTS radar_claims (
    claim_id          TEXT PRIMARY KEY,
    observation_id    TEXT NOT NULL REFERENCES radar_observations(observation_id),
    claim_type        TEXT NOT NULL,
    claim_text        TEXT NOT NULL,
    entities_json     TEXT NOT NULL,
    metrics_json      TEXT,
    time_range_start  TEXT,
    time_range_end    TEXT,
    place_json        TEXT,
    confidence        REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    confidence_band   TEXT NOT NULL CHECK (confidence_band IN ('high','moderate','low','unverified')),
    extractor_version TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    valid_from        TEXT NOT NULL,
    valid_to          TEXT
);

CREATE TABLE IF NOT EXISTS radar_events (
    event_id         TEXT PRIMARY KEY,
    event_type       TEXT NOT NULL,
    event_status     TEXT NOT NULL CHECK (event_status IN ('reported','confirmed','contested','resolved','retracted')),
    time_range_start TEXT NOT NULL,
    time_range_end   TEXT,
    place_json       TEXT NOT NULL,
    actors_json      TEXT NOT NULL,
    claims_json      TEXT NOT NULL,
    metrics_json     TEXT,
    topics_json      TEXT NOT NULL,
    confidence       REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    confidence_band  TEXT NOT NULL CHECK (confidence_band IN ('high','moderate','low','unverified')),
    version          INTEGER NOT NULL DEFAULT 1,
    scorer_version   TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    valid_from       TEXT NOT NULL,
    valid_to         TEXT
);

CREATE TABLE IF NOT EXISTS radar_provenance (
    provenance_id          TEXT PRIMARY KEY,
    observation_id         TEXT NOT NULL REFERENCES radar_observations(observation_id),
    source_id              TEXT NOT NULL,
    source_version         TEXT NOT NULL,
    capture_method         TEXT NOT NULL,
    captured_at            TEXT NOT NULL,
    canonical_url          TEXT,
    raw_hash               TEXT NOT NULL,
    content_hash           TEXT NOT NULL,
    parser_version         TEXT NOT NULL,
    normalizer_version     TEXT NOT NULL,
    dedupe_version         TEXT NOT NULL,
    scorer_version         TEXT NOT NULL,
    transform_parents_json TEXT,
    extracted_at           TEXT NOT NULL,
    license_class          TEXT NOT NULL,
    privacy_class          TEXT NOT NULL,
    reason_codes_json      TEXT,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    valid_from             TEXT NOT NULL,
    valid_to               TEXT
);

CREATE TABLE IF NOT EXISTS radar_audit_logs (
    audit_id    TEXT PRIMARY KEY,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    action      TEXT NOT NULL,
    actor       TEXT NOT NULL,
    before_json TEXT,
    after_json  TEXT,
    reason      TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    valid_from  TEXT NOT NULL,
    valid_to    TEXT
);

CREATE TABLE IF NOT EXISTS radar_checkpoints (
    source_id      TEXT PRIMARY KEY REFERENCES radar_sources(source_id),
    connector_type TEXT NOT NULL,
    cursor         TEXT NOT NULL,
    last_seen_at   TEXT NOT NULL,
    item_count     INTEGER NOT NULL DEFAULT 0,
    hash           TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    valid_from     TEXT NOT NULL,
    valid_to       TEXT
);

CREATE INDEX IF NOT EXISTS idx_observations_source_captured ON radar_observations(source_id, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_claims_observation ON radar_claims(observation_id);
CREATE INDEX IF NOT EXISTS idx_events_time_range ON radar_events(time_range_start, time_range_end);
CREATE INDEX IF NOT EXISTS idx_events_topics ON radar_events(topics_json);
CREATE INDEX IF NOT EXISTS idx_provenance_observation ON radar_provenance(observation_id);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON radar_audit_logs(entity_type, entity_id, created_at DESC);
`

// MigrateSpecSchema initializes the normalized World Radar SQLite schema (Sections 2.1 & 3.3).
func MigrateSpecSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("worldradar spec migrate: nil database handle")
	}
	if _, err := db.Exec(SpecSchemaSQL); err != nil {
		return fmt.Errorf("worldradar spec migrate schema: %w", err)
	}
	return nil
}
