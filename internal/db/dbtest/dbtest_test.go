package dbtest

import "testing"

func TestMain(m *testing.M) { Main(m) }

// TestNewIsMigrated checks that a cloned database carries the full schema, i.e.
// the template was migrated before cloning.
func TestNewIsMigrated(t *testing.T) {
	db := New(t)
	var n int
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if n == 0 {
		t.Fatal("cloned database has no applied migrations")
	}
}

// TestNewIsolatesData checks that two databases from New do not share data.
func TestNewIsolatesData(t *testing.T) {
	a := New(t)
	b := New(t)

	if _, err := a.Exec("CREATE TABLE probe (x int)"); err != nil {
		t.Fatalf("create probe in a: %v", err)
	}

	var leaked bool
	if err := b.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'probe')",
	).Scan(&leaked); err != nil {
		t.Fatalf("check b: %v", err)
	}
	if leaked {
		t.Fatal("table created in one test database is visible in another")
	}
}
