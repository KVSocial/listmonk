package core

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/knadh/listmonk/models"
	"github.com/lib/pq"
)

const queryListsTestSchema = `
CREATE TYPE list_type AS ENUM ('public', 'private', 'temporary');
CREATE TYPE list_optin AS ENUM ('single', 'double');
CREATE TYPE list_status AS ENUM ('active', 'archived');
CREATE TABLE lists (
	id SERIAL PRIMARY KEY,
	uuid UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
	name TEXT NOT NULL,
	type list_type NOT NULL DEFAULT 'private',
	optin list_optin NOT NULL DEFAULT 'single',
	status list_status NOT NULL DEFAULT 'active',
	tags VARCHAR(100)[],
	description TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);
CREATE TABLE mat_list_subscriber_stats (
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
	list_id INTEGER NOT NULL,
	status TEXT NULL,
	subscriber_count BIGINT NOT NULL
);`

func newQueryListsTestDB(t *testing.T) *sqlx.DB {
	t.Helper()

	dsn := os.Getenv("LISTMONK_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("LISTMONK_TEST_DB_DSN is not set")
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(1)

	schema := fmt.Sprintf("query_lists_test_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatalf("create test schema: %v", err)
	}
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		db.Close()
		t.Fatalf("select test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = db.Close()
	})

	if _, err := db.Exec(queryListsTestSchema); err != nil {
		t.Fatalf("create query lists test tables: %v", err)
	}

	return db
}

// queryListsPage runs query-lists the same way Core.QueryLists does and returns
// the subscriber counts of one page.
func queryListsPage(t *testing.T, db *sqlx.DB, orderBy, order string, offset, limit int) []int {
	t.Helper()

	_, stmt := makeSearchQuery("", orderBy, order, loadNamedTestQuery(t, "lists.sql", "query-lists"), listQuerySortFields)

	var out []models.List
	if err := db.Select(&out, stmt, 0, "", "", "", "", "", pq.StringArray{}, true, pq.Array([]int{}), offset, limit); err != nil {
		t.Fatalf("query lists: %v", err)
	}

	counts := make([]int, len(out))
	for i, l := range out {
		counts[i] = l.SubscriberCount
	}
	return counts
}

func TestQueryListsSortsBeforePaginating(t *testing.T) {
	db := newQueryListsTestDB(t)

	// Insert lists so that ID order differs from subscriber count order, with
	// ties to check that equal values are not repeated or skipped across pages.
	counts := []int{5, 100, 1, 50, 5, 0, 20, 5}
	for i, n := range counts {
		var id int
		if err := db.Get(&id, `INSERT INTO lists (name) VALUES ($1) RETURNING id`, fmt.Sprintf("list-%d", i)); err != nil {
			t.Fatalf("insert list: %v", err)
		}
		if n > 0 {
			if _, err := db.Exec(`INSERT INTO mat_list_subscriber_stats (list_id, status, subscriber_count) VALUES ($1, 'confirmed', $2)`, id, n); err != nil {
				t.Fatalf("insert list stats: %v", err)
			}
		}
	}

	cases := []struct {
		order string
		want  [][]int
	}{
		{SortDesc, [][]int{{100, 50, 20}, {5, 5, 5}, {1, 0}}},
		{SortAsc, [][]int{{0, 1, 5}, {5, 5, 20}, {50, 100}}},
	}
	for _, c := range cases {
		for page, want := range c.want {
			got := queryListsPage(t, db, "subscriber_count", c.order, page*3, 3)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("order %s page %d: got %v, want %v", c.order, page+1, got, want)
			}
		}
	}

	// Tied rows must not be repeated or skipped across page boundaries.
	var ids []int
	_, stmt := makeSearchQuery("", "subscriber_count", SortDesc, loadNamedTestQuery(t, "lists.sql", "query-lists"), listQuerySortFields)
	for offset := 0; offset < len(counts); offset += 2 {
		var out []models.List
		if err := db.Select(&out, stmt, 0, "", "", "", "", "", pq.StringArray{}, true, pq.Array([]int{}), offset, 2); err != nil {
			t.Fatalf("query lists: %v", err)
		}
		for _, l := range out {
			ids = append(ids, l.ID)
		}
	}
	seen := map[int]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("list %d returned on more than one page: %v", id, ids)
		}
		seen[id] = true
	}
	if len(seen) != len(counts) {
		t.Fatalf("got %d distinct lists across pages, want %d: %v", len(seen), len(counts), ids)
	}
}
