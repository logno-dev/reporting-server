package templates_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/oklog/ulid/v2"
	"reporting-server/internal/database"
	"reporting-server/internal/jobs"
	"reporting-server/internal/templates"
)

func TestTemplateArchiveVisibilityIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEMPLATES_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEMPLATES_TEST_DATABASE_URL to run Postgres template archive tests")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	repository := templates.NewRepository(pool)
	const slug = "certificate-of-analysis"
	if err := repository.SetArchived(ctx, slug, "admin@example.test", true); err != nil {
		t.Fatal(err)
	}
	listed, err := repository.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ArchivedAt == nil || listed[0].ArchivedBy == nil || *listed[0].ArchivedBy != "admin@example.test" {
		t.Fatalf("archived template metadata = %+v", listed)
	}
	catalog, err := repository.PublishedCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 0 {
		t.Fatalf("archived template remained discoverable: %+v", catalog)
	}
	if _, err := repository.GetVersion(ctx, slug, 1); err != nil {
		t.Fatalf("archived pinned version unavailable: %v", err)
	}
	profileID := ulid.Make().String()
	if _, err := pool.Exec(ctx, `INSERT INTO storage_profiles(id,name,backend_type,state,public_config) VALUES($1,$2,'filesystem','active','{}')`, profileID, "template-archive-test-"+profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO storage_defaults(singleton,profile_id) VALUES(true,$1) ON CONFLICT(singleton) DO UPDATE SET profile_id=EXCLUDED.profile_id`, profileID); err != nil {
		t.Fatal(err)
	}
	version := 1
	if _, err := jobs.NewRepository(pool).Create(ctx, ulid.Make().String(), slug, &version, nil, json.RawMessage(`{}`), "api-client:test"); err != nil {
		t.Fatalf("explicit submission to archived version failed: %v", err)
	}

	if err := repository.SetArchived(ctx, slug, "admin@example.test", false); err != nil {
		t.Fatal(err)
	}
	catalog, err = repository.PublishedCatalog(ctx)
	if err != nil || len(catalog) != 1 {
		t.Fatalf("restored catalog = %+v, %v", catalog, err)
	}
}
