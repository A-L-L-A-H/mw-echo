package worldradar

import (
	"database/sql"
	"fmt"
)

const radarSchemaSQL = `
CREATE TABLE IF NOT EXISTS radar_instances (
  id TEXT PRIMARY KEY,
  pack_id TEXT NOT NULL,
  pack_version TEXT NOT NULL,
  name TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'monitor',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'disabled')),
  config_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS radar_watch_targets (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT 'feed',
  canonical_name TEXT NOT NULL,
  connector_id TEXT NOT NULL,
  source_uri TEXT NOT NULL,
  independence_group TEXT NOT NULL,
  etag TEXT NOT NULL DEFAULT '',
  cursor TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_targets_instance ON radar_watch_targets(instance_id, enabled);

CREATE TABLE IF NOT EXISTS radar_collection_runs (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('ok', 'no_new_data', 'connector_failed', 'rate_limited', 'paused')),
  records_acquired INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_runs_instance_started ON radar_collection_runs(instance_id, started_at DESC);

CREATE TABLE IF NOT EXISTS radar_raw_artifacts (
  id TEXT PRIMARY KEY,
  source_uri TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  independence_group TEXT NOT NULL,
  title TEXT NOT NULL,
  language TEXT NOT NULL DEFAULT 'en',
  mime_type TEXT NOT NULL DEFAULT 'text/plain',
  payload_excerpt TEXT NOT NULL DEFAULT '',
  captured_at TEXT NOT NULL,
  UNIQUE(source_uri, content_hash)
);

CREATE TABLE IF NOT EXISTS radar_observations (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  artifact_id TEXT NOT NULL,
  source_type TEXT NOT NULL,
  external_id TEXT NOT NULL,
  title TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  occurred_at TEXT NOT NULL,
  published_at TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  ingested_at TEXT NOT NULL,
  time_precision TEXT NOT NULL DEFAULT 'second',
  payload_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_radar_observations_artifact ON radar_observations(artifact_id);
CREATE INDEX IF NOT EXISTS idx_radar_observations_target ON radar_observations(target_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS radar_entities (
  id TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL DEFAULT 'organization',
  canonical_name TEXT NOT NULL,
  strong_identifier TEXT NOT NULL DEFAULT '',
  aliases_json TEXT NOT NULL DEFAULT '[]',
  review_state TEXT NOT NULL DEFAULT 'resolved',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_entities_name ON radar_entities(canonical_name);

CREATE TABLE IF NOT EXISTS radar_signals (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  lens_id TEXT NOT NULL,
  title TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'active',
  baseline_status TEXT NOT NULL DEFAULT 'warming',
  detected_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_signals_instance ON radar_signals(instance_id, detected_at DESC);

CREATE TABLE IF NOT EXISTS radar_events (
  id TEXT PRIMARY KEY,
  cluster_key TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  primary_domain TEXT NOT NULL,
  domains_json TEXT NOT NULL DEFAULT '[]',
  occurred_at TEXT NOT NULL,
  published_at TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  ingested_at TEXT NOT NULL,
  time_precision TEXT NOT NULL DEFAULT 'second',
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_events_domain_updated ON radar_events(primary_domain, updated_at DESC);

CREATE TABLE IF NOT EXISTS radar_event_evidence (
  event_id TEXT NOT NULL,
  artifact_id TEXT NOT NULL,
  observation_id TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'supporting',
  independence_group TEXT NOT NULL,
  PRIMARY KEY(event_id, artifact_id)
);

CREATE TABLE IF NOT EXISTS radar_relationships (
  id TEXT PRIMARY KEY,
  subject_entity_id TEXT NOT NULL,
  predicate TEXT NOT NULL,
  object_entity_id TEXT NOT NULL,
  confidence REAL NOT NULL DEFAULT 0.5,
  evidence_artifact_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_relationships_subject ON radar_relationships(subject_entity_id);
CREATE INDEX IF NOT EXISTS idx_radar_relationships_object ON radar_relationships(object_entity_id);

CREATE TABLE IF NOT EXISTS radar_assessments (
  id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL,
  impact REAL NOT NULL,
  urgency REAL NOT NULL,
  relevance REAL NOT NULL,
  velocity REAL NOT NULL,
  novelty REAL NOT NULL,
  actionability REAL NOT NULL,
  exposure REAL NOT NULL,
  priority_score REAL NOT NULL,
  priority_tier TEXT NOT NULL,
  confidence_label TEXT NOT NULL,
  independent_sources INTEGER NOT NULL,
  uncertainty_flags_json TEXT NOT NULL DEFAULT '[]',
  rationale TEXT NOT NULL DEFAULT '',
  assessed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_assessments_event ON radar_assessments(event_id, assessed_at DESC);

CREATE TABLE IF NOT EXISTS radar_alerts (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  assessment_id TEXT NOT NULL,
  dedupe_key TEXT NOT NULL UNIQUE,
  priority_tier TEXT NOT NULL,
  confidence_label TEXT NOT NULL,
  alert_kind TEXT NOT NULL CHECK (alert_kind IN ('confirmed_alert', 'verification_required', 'signal_notice')),
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved', 'snoozed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_radar_alerts_status ON radar_alerts(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS radar_reports (
  id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  title TEXT NOT NULL,
  scope TEXT NOT NULL,
  observed_facts_json TEXT NOT NULL DEFAULT '[]',
  reported_claims_json TEXT NOT NULL DEFAULT '[]',
  inferences_json TEXT NOT NULL DEFAULT '[]',
  unknowns_json TEXT NOT NULL DEFAULT '[]',
  evidence_refs_json TEXT NOT NULL DEFAULT '[]',
  model TEXT NOT NULL DEFAULT 'deterministic-evidence-compiler',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS radar_connector_health (
  connector_id TEXT PRIMARY KEY,
  last_attempt_at TEXT NOT NULL DEFAULT '',
  last_success_at TEXT NOT NULL DEFAULT '',
  last_status TEXT NOT NULL DEFAULT 'unknown',
  last_error TEXT NOT NULL DEFAULT '',
  total_runs INTEGER NOT NULL DEFAULT 0,
  failed_runs INTEGER NOT NULL DEFAULT 0,
  rate_limited_until TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS radar_feedback (
  id TEXT PRIMARY KEY,
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  vote TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS radar_audit_logs (
  id TEXT PRIMARY KEY,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  object_type TEXT NOT NULL,
  object_id TEXT NOT NULL,
  outcome TEXT NOT NULL,
  created_at TEXT NOT NULL
);
`

// MigrateSchema creates all normalized evidence-first World Radar Network tables inside hub.DB().
func MigrateSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("worldradar migrate: nil database handle")
	}
	if _, err := db.Exec(radarSchemaSQL); err != nil {
		return fmt.Errorf("worldradar migrate schema: %w", err)
	}
	return nil
}
