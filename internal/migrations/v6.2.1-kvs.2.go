package migrations

import (
	"log"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/koanf/v2"
	"github.com/knadh/stuffbin"
)

// V6_2_1_KVS_2 adds per-campaign daily send limits (KVSocial fork).
// The prerelease version sorts after v6.2.0 and before any upstream v6.2.1+,
// so upstream migrations still run afterwards.
func V6_2_1_KVS_2(db *sqlx.DB, fs stuffbin.FileSystem, ko *koanf.Koanf, lo *log.Logger) error {
	_, err := db.Exec(`
		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS daily_limit INT NOT NULL DEFAULT 0;
		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS daily_sent INT NOT NULL DEFAULT 0;
		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS daily_window_start TIMESTAMP WITH TIME ZONE NULL;
	`)
	return err
}
