package migrations

import (
	"log"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/koanf/v2"
	"github.com/knadh/stuffbin"
)

// V6_2_1_KVS_3 persists each campaign's remaining audience and caches sendable list counts.
func V6_2_1_KVS_3(db *sqlx.DB, fs stuffbin.FileSystem, ko *koanf.Koanf, lo *log.Logger) error {
	_, err := db.Exec(`
ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS audience_frozen BOOLEAN NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS campaign_pending_recipients (
    campaign_id INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    subscriber_id INTEGER NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE,
    PRIMARY KEY (campaign_id, subscriber_id)
);
CREATE INDEX IF NOT EXISTS idx_campaign_pending_subscriber ON campaign_pending_recipients(subscriber_id);

-- Shared eligibility rules for the initial snapshot and the final delivery check.
CREATE OR REPLACE VIEW campaign_eligible_subscribers AS
    SELECT cl.campaign_id, sl.subscriber_id
    FROM campaign_lists cl
    JOIN campaigns c ON c.id = cl.campaign_id
    JOIN lists l ON l.id = cl.list_id
    JOIN subscriber_lists sl ON sl.list_id = l.id
    JOIN subscribers s ON s.id = sl.subscriber_id
    WHERE s.status != 'blocklisted' AND CASE
        WHEN c.type = 'optin' THEN l.optin = 'double' AND sl.status = 'unconfirmed'
        WHEN l.optin = 'double' THEN sl.status = 'confirmed'
        ELSE sl.status != 'unsubscribed' END;

DROP MATERIALIZED VIEW IF EXISTS mat_list_subscriber_stats;
CREATE MATERIALIZED VIEW mat_list_subscriber_stats AS
    SELECT NOW() AS updated_at, l.id AS list_id, sl.status, COUNT(sl.status) AS subscriber_count,
           COUNT(sl.status) FILTER (WHERE s.status != 'blocklisted') AS sendable_count
    FROM lists l
    LEFT JOIN subscriber_lists sl ON sl.list_id = l.id
    LEFT JOIN subscribers s ON s.id = sl.subscriber_id
    GROUP BY l.id, sl.status
    UNION ALL
    SELECT NOW(), 0, NULL, COUNT(*), COUNT(*) FILTER (WHERE status != 'blocklisted') FROM subscribers;
CREATE UNIQUE INDEX mat_list_subscriber_stats_idx ON mat_list_subscriber_stats (list_id, status);
`)
	return err
}
