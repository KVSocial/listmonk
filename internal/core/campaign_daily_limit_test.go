package core

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

// Exercise the shipping schema, including the snapshot, eligibility view and cached estimates.
func TestCampaignAudienceAndClaims(t *testing.T) {
	dsn := os.Getenv("LISTMONK_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("LISTMONK_TEST_DB_DSN is not set")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx := db.MustBegin()
	defer tx.Rollback()
	schema := fmt.Sprintf("campaign_audience_test_%d", time.Now().UnixNano())
	tx.MustExec("CREATE SCHEMA " + schema)
	tx.MustExec("SET LOCAL search_path TO " + schema)
	ddl, err := os.ReadFile(filepath.Join("..", "..", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tx.MustExec(string(ddl))
	tx.MustExec(`
		INSERT INTO lists (uuid,name,type,optin) VALUES (gen_random_uuid(),'Opt-in','private','double'),(gen_random_uuid(),'Regular','private','single');
		INSERT INTO subscribers (uuid,email,name) SELECT gen_random_uuid(), i||'@example.invalid','Test' FROM generate_series(1,6) i;
		UPDATE subscribers SET status='blocklisted' WHERE id=5;
		INSERT INTO subscriber_lists (subscriber_id,list_id,status) SELECT id,1,'unconfirmed' FROM subscribers WHERE id>1;
		INSERT INTO subscriber_lists (subscriber_id,list_id,status) VALUES (2,2,'confirmed');
		INSERT INTO campaigns (uuid,name,subject,from_email,body,messenger,status,type,daily_limit)
		  VALUES (gen_random_uuid(),'Test','Test','test@example.invalid','Body','email','running','optin',2);
		INSERT INTO campaign_lists(campaign_id,list_id) VALUES(1,1),(1,2);
		REFRESH MATERIALIZED VIEW mat_list_subscriber_stats;
	`)
	estimate := loadNamedTestQuery(t, "campaigns.sql", "get-recipient-estimate")
	for _, tc := range []struct {
		typ       string
		all       bool
		permitted []int
		want      int
	}{
		{"optin", true, nil, 4}, {"regular", true, nil, 1}, {"optin", false, []int{2}, 0}, {"regular", false, []int{2}, 1},
	} {
		var got int
		if err := tx.Get(&got, estimate, pq.Array([]int{1, 2}), tc.typ, tc.all, pq.Array(tc.permitted)); err != nil || got != tc.want {
			t.Fatalf("estimate %+v: %d, %v", tc, got, err)
		}
	}
	scan := func() {
		t.Helper()
		if _, err := tx.Exec(loadNamedTestQuery(t, "campaigns.sql", "next-campaigns"), pq.Array([]int{}), pq.Array([]int{})); err != nil {
			t.Fatal(err)
		}
	}
	checkIDs := func(want string) {
		t.Helper()
		var ids []int
		if err := tx.Select(&ids, "SELECT subscriber_id FROM campaign_pending_recipients ORDER BY subscriber_id"); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(ids) != want {
			t.Fatalf("pending recipients %v, want %s", ids, want)
		}
	}
	claimSQL := loadNamedTestQuery(t, "campaigns.sql", "claim-campaign-recipient")
	claim := func(id int, want string) {
		t.Helper()
		var got string
		if err := tx.Get(&got, claimSQL, 1, id); err != nil || got != want {
			t.Fatalf("claim %d: got %q, want %q, err %v", id, got, want, err)
		}
	}
	scan()
	checkIDs("[2 3 4 6]")
	tx.MustExec("INSERT INTO subscriber_lists(subscriber_id,list_id,status) VALUES (1,1,'unconfirmed')")
	tx.MustExec("UPDATE subscribers SET status='blocklisted' WHERE id=6")
	// A rejected usage update must roll back the recipient claim too.
	tx.MustExec("ALTER TABLE campaigns ADD CONSTRAINT reject_charge CHECK (daily_sent=0); SAVEPOINT before_claim")
	var result string
	if err := tx.Get(&result, claimSQL, 1, 2); err == nil {
		t.Fatal("expected the quota update to fail")
	}
	tx.MustExec("ROLLBACK TO before_claim; ALTER TABLE campaigns DROP CONSTRAINT reject_charge")
	claim(2, "attempt")
	claim(2, "skip")
	tx.MustExec("UPDATE campaigns SET status='paused' WHERE id=1")
	claim(3, "inactive")
	checkIDs("[3 4 6]")
	tx.MustExec("UPDATE campaigns SET status='running' WHERE id=1")
	scan()
	checkIDs("[3 4 6]")
	claim(3, "attempt")
	claim(4, "limited")
	claim(6, "skip")
	tx.MustExec("UPDATE campaigns SET daily_window_start=NOW()-INTERVAL '24 hours'")
	claim(4, "attempt")
	checkIDs("[]")
	var total int
	if err := tx.Get(&total, "SELECT to_send FROM campaigns WHERE id=1"); err != nil || total != 4 {
		t.Fatalf("initial audience: %d, %v", total, err)
	}
	// On upgrade an in-flight campaign has a legacy cursor, not an initial member list.
	// Only materialize the still-eligible, unprocessed IDs inside its recorded bounds.
	tx.MustExec(`
		UPDATE campaigns SET status='finished' WHERE id=1;
		INSERT INTO campaigns (id,uuid,name,subject,from_email,body,messenger,type,status,started_at,
			last_subscriber_id,max_subscriber_id,to_send)
		VALUES (2,gen_random_uuid(),'Legacy','Test','test@example.invalid','Body','email','optin','running',NOW(),2,3,5);
		INSERT INTO campaign_lists(campaign_id,list_id) VALUES(2,1);
	`)
	scan()
	checkIDs("[3]")
	var action string
	if err := tx.Get(&action, claimSQL, 2, 3); err != nil || action != "attempt" {
		t.Fatalf("unlimited legacy campaign: %s, %v", action, err)
	}
	if err := tx.Get(&total, "SELECT to_send FROM campaigns WHERE id=2"); err != nil || total != 5 {
		t.Fatalf("legacy total: %d, %v", total, err)
	}
	checkIDs("[]")

}
