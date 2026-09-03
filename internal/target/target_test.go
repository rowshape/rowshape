package target

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/hydrate"
)

// testAdminEnv holds a Postgres admin connection (CREATE/DROP DATABASE) for the
// disposable-target tests. Without it the DB-backed tests skip; CI sets it
// against a real Postgres service.
const testAdminEnv = "ROWSHAPE_TEST_PG_DSN"

func adminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(testAdminEnv)
	if dsn == "" {
		t.Skipf("set %s to a Postgres admin connection to run target tests", testAdminEnv)
	}
	return dsn
}

func dbExists(t *testing.T, adminDSN, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), adminDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var exists bool
	if err := conn.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("check db: %v", err)
	}
	return exists
}

// TestEphemeralLifecycle: NewEphemeral creates a disposable database that exists
// and accepts connections, and Close drops it (RFC §1 hydrate — thrown away).
func TestEphemeralLifecycle(t *testing.T) {
	dsn := adminDSN(t)
	ctx := context.Background()

	e, err := NewEphemeral(ctx, dsn)
	if err != nil {
		t.Fatalf("NewEphemeral: %v", err)
	}
	if !e.Disposable() {
		t.Errorf("ephemeral target must report Disposable() == true")
	}
	if !dbExists(t, dsn, e.Name()) {
		t.Fatalf("disposable database %q was not created", e.Name())
	}

	conn, err := e.Connect(ctx)
	if err != nil {
		t.Fatalf("connect to disposable db: %v", err)
	}
	_ = conn.Close(ctx)

	name := e.Name()
	if err := e.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if dbExists(t, dsn, name) {
		t.Errorf("disposable database %q was not dropped on Close", name)
	}
	// Close is idempotent.
	if err := e.Close(ctx); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
}

// TestCloseDropsWhenContextCancelled: teardown must survive cancellation of the
// run's context. On Ctrl-C the run's context is cancelled and THEN the deferred
// Close runs — if Close honored that cancellation it would fail to connect and
// orphan the disposable database on the admin server. Close detaches from the
// caller's cancellation for exactly this reason.
func TestCloseDropsWhenContextCancelled(t *testing.T) {
	dsn := adminDSN(t)

	e, err := NewEphemeral(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewEphemeral: %v", err)
	}
	name := e.Name()
	if !dbExists(t, dsn, name) {
		t.Fatalf("disposable database %q was not created", name)
	}

	// Simulate the interrupted run: the context handed to Close is already dead.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := e.Close(ctx); err != nil {
		t.Fatalf("Close with a cancelled context must still drop the database, got: %v", err)
	}
	if dbExists(t, dsn, name) {
		t.Errorf("disposable database %q was orphaned — Close honored the cancelled context", name)
	}
}

// TestLoadIntoEphemeral: a full hydration into a disposable database loads the
// right row counts and satisfies unique/NOT NULL constraints (RFC §13).
func TestLoadIntoEphemeral(t *testing.T) {
	dsn := adminDSN(t)
	ctx := context.Background()

	e, err := NewEphemeral(ctx, dsn)
	if err != nil {
		t.Fatalf("NewEphemeral: %v", err)
	}
	defer func() { _ = e.Close(ctx) }()

	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"app.users": {
				Rows: fixture.Fact[int64]{Value: 300, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id":     {Type: "bigint", Nullable: false, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"}},
					"email":  {Type: "text", Nullable: true, NullFraction: &fixture.Fact[float64]{Value: 0.1, Confidence: fixture.Estimated}, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Format: "email"},
					"status": {Type: "text", Nullable: false, Distinct: &fixture.Fact[int64]{Value: 3, Confidence: fixture.Estimated}, Format: "enum_like", Values: []string{"active", "trialing", "canceled"}},
					"joined": {Type: "timestamp with time zone", Nullable: false},
				},
				Constraints: []fixture.Constraint{
					{Name: "users_pkey", Kind: "primary_key", Columns: []string{"id"}},
					{Name: "users_email_key", Kind: "unique", Columns: []string{"email"}},
				},
			},
		},
	}

	report, err := Load(ctx, e, f, hydrate.Options{Seed: 42})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if report.Tables["app.users"] != 300 {
		t.Errorf("loaded %d rows, want 300", report.Tables["app.users"])
	}

	conn, err := e.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var total, distinctEmail, distinctStatus, nullEmail int64
	err = conn.QueryRow(ctx, `SELECT count(*), count(distinct email), count(distinct status), count(*) FILTER (WHERE email IS NULL) FROM app.users`).
		Scan(&total, &distinctEmail, &distinctStatus, &nullEmail)
	if err != nil {
		t.Fatalf("query loaded data: %v", err)
	}
	if total != 300 {
		t.Errorf("total rows = %d, want 300", total)
	}
	if distinctStatus != 3 {
		t.Errorf("distinct statuses = %d, want 3", distinctStatus)
	}
	// null_fraction 0.1 -> ~30 null emails; the rest unique.
	if nullEmail < 27 || nullEmail > 33 {
		t.Errorf("null emails = %d, want ~30", nullEmail)
	}
	if distinctEmail != total-nullEmail {
		t.Errorf("non-null emails not all unique: %d distinct of %d non-null", distinctEmail, total-nullEmail)
	}
}

// TestProvidedNotDisposable: a provided target loads data and is never dropped.
func TestProvidedNotDisposable(t *testing.T) {
	dsn := adminDSN(t)
	ctx := context.Background()

	// Use a disposable database as the "provided" target so the test cleans up,
	// but drive it through the Provided (non-disposable) path.
	e, err := NewEphemeral(ctx, dsn)
	if err != nil {
		t.Fatalf("NewEphemeral: %v", err)
	}
	defer func() { _ = e.Close(ctx) }()

	providedDSN := providedDSNFor(dsn, e.Name())
	p := NewProvided(providedDSN)
	if p.Disposable() {
		t.Errorf("provided target must report Disposable() == false")
	}

	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.t": {
				Rows:    fixture.Fact[int64]{Value: 50, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"id": {Type: "bigint", Nullable: false, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}}},
			},
		},
	}
	if _, err := Load(ctx, p, f, hydrate.Options{Seed: 1}); err != nil {
		t.Fatalf("Load into provided: %v", err)
	}
	// Provided.Close must NOT drop the database.
	if err := p.Close(ctx); err != nil {
		t.Fatalf("Provided.Close: %v", err)
	}
	if !dbExists(t, dsn, e.Name()) {
		t.Errorf("Provided.Close must not drop the database")
	}
}

// providedDSNFor rewrites an admin DSN to point at a specific database.
func providedDSNFor(adminDSN, db string) string {
	cfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return adminDSN
	}
	cfg.Database = db
	// Rebuild a keyword DSN pgx understands.
	parts := []string{"host=" + cfg.Host, "user=" + cfg.User, "dbname=" + db}
	if cfg.Port != 0 {
		parts = append(parts, "port="+itoa(int(cfg.Port)))
	}
	if cfg.Password != "" {
		parts = append(parts, "password="+cfg.Password)
	}
	return strings.Join(parts, " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestContainerLifecycle exercises the Docker-based disposable target when a
// Docker daemon is available; it skips otherwise (the Ephemeral target is the
// dependency-light default, DECISIONS D-005).
func TestContainerLifecycle(t *testing.T) {
	if !ContainerAvailable() {
		t.Skip("no Docker daemon; Container target is a swap-in, Ephemeral is the default")
	}
	ctx := context.Background()
	c, err := NewContainer(ctx, "postgres:16")
	if err != nil {
		t.Fatalf("NewContainer: %v", err)
	}
	conn, err := c.Connect(ctx)
	if err != nil {
		_ = c.Close(ctx)
		t.Fatalf("connect to container: %v", err)
	}
	_ = conn.Close(ctx)
	if err := c.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// --- CR-T18: cleanup must not depend on receiving the container id ----------
//
// `docker run -d` prints the id on stdout, but context cancellation kills the
// docker CLI rather than what the daemon has already launched. In the window
// between "container started" and "id read", a cancel left a running container
// that nothing referenced — narrow and Docker-only, but it accumulates on a
// developer machine or a CI runner with nothing pointing at the cause.
//
// The container is now named BEFORE it is asked for, so cleanup has a handle
// either way, and labelled so a stray is findable. These tests need no Docker;
// the end-to-end cancel path is covered by TestContainerLifecycle where a daemon
// is available.

func TestContainerNameIsUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		n := containerName()
		if !strings.HasPrefix(n, "rowshape-") {
			t.Fatalf("name %q must be identifiable as ours", n)
		}
		if seen[n] {
			t.Fatalf("duplicate container name %q — two concurrent runs would collide", n)
		}
		seen[n] = true
	}
}

// TestContainerLabelIsQueryable pins the label an operator greps for; changing
// it silently would break the stray-cleanup recipe documented in container.go.
func TestContainerLabelIsQueryable(t *testing.T) {
	if containerLabel != "rowshape=1" {
		t.Errorf("containerLabel = %q; the cleanup recipe and "+
			"`docker ps --filter label=rowshape` depend on this value", containerLabel)
	}
}
