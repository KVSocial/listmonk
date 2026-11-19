package core

import (
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

func TestCampaignDailyReservations(t *testing.T) {
	dsn := os.Getenv("LISTMONK_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("LISTMONK_TEST_DB_DSN is not set")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.MustExec(`
		CREATE TEMP TABLE campaigns (id int, type text, status text, last_subscriber_id int, max_subscriber_id int,
			daily_limit int, daily_sent int, daily_window_start timestamptz, updated_at timestamptz);
		CREATE TEMP TABLE lists (id int, optin text);
		CREATE TEMP TABLE campaign_lists (campaign_id int, list_id int);
		CREATE TEMP TABLE subscribers (id int, status text);
		CREATE TEMP TABLE subscriber_lists (subscriber_id int, list_id int, status text);
		INSERT INTO campaigns VALUES (1,'optin','running',0,4,2,0,NULL,NULL);
		INSERT INTO lists VALUES (1,'double'),(2,'single');
		INSERT INTO campaign_lists VALUES (1,1);
		INSERT INTO subscribers SELECT n,'enabled' FROM generate_series(1,4) n;
		INSERT INTO subscriber_lists SELECT id,1,'unconfirmed' FROM subscribers;
		INSERT INTO subscriber_lists VALUES (1,2,'confirmed');
		CREATE TEMP VIEW mat_list_subscriber_stats AS
			SELECT list_id,status,COUNT(*) AS subscriber_count FROM subscriber_lists GROUP BY list_id,status;
		CREATE TYPE pg_temp.campaign_type AS ENUM ('optin','regular');
		SET search_path TO pg_temp;
	`)
	prepare := func(name string) *sqlx.Stmt {
		t.Helper()
		stmt, err := db.Preparex(loadNamedTestQuery(t, "campaigns.sql", name))
		if err != nil {
			t.Fatal(err)
		}
		return stmt
	}
	c := &Core{db: db, q: &models.Queries{
		GetRunningCampaign: prepare("get-running-campaign"), NextCampaignSubscribers: prepare("next-campaign-subscribers"),
	}}
	estimate := prepare("get-recipient-estimate")
	for _, tc := range []struct {
		typ     string
		all     bool
		allowed []int
		want    int
	}{
		{"optin", true, nil, 4}, {"regular", true, nil, 1},
		{"optin", false, []int{2}, 0}, {"regular", false, []int{2}, 1},
	} {
		var count int
		err := estimate.Get(&count, pq.Array([]int{1, 2}), tc.typ, tc.all, pq.Array(tc.allowed))
		if err != nil || count != tc.want {
			t.Fatalf("estimate %+v: count=%d err=%v", tc, count, err)
		}
	}

	// A rejected usage update must leave the cursor at the start of the batch.
	db.MustExec("ALTER TABLE campaigns ADD CONSTRAINT reject_charge CHECK (daily_sent < 2)")
	if subs, _, err := c.ReserveCampaignSubscribers(1, 10); err == nil || len(subs) != 0 {
		t.Fatalf("unexpected reservation on failed write: %v, %v", subs, err)
	}
	db.MustExec("ALTER TABLE campaigns DROP CONSTRAINT reject_charge")
	check := func(wantLimited bool, ids ...int) {
		t.Helper()
		subs, limited, err := c.ReserveCampaignSubscribers(1, 10)
		if err != nil || limited != wantLimited || len(subs) != len(ids) {
			t.Fatalf("reserve: subs=%v limited=%v err=%v", subs, limited, err)
		}
		for i, sub := range subs {
			if sub.ID != ids[i] {
				t.Fatalf("subscriber %d: got %d, want %d", i, sub.ID, ids[i])
			}
		}
	}
	check(false, 1, 2)
	check(true)
	db.MustExec("UPDATE campaigns SET daily_window_start = NOW() - INTERVAL '24 hours'")
	check(false, 3, 4)
	check(false) // Exactly exhausted on the last batch.
	db.MustExec("UPDATE campaigns SET last_subscriber_id=0, daily_limit=0")
	check(false, 1, 2, 3, 4)
}
