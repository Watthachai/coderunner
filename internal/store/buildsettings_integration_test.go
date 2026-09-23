package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// openScratchStore applies every migration into a new schema of the database
// at CRN_TEST_DATABASE_URL and returns a store scoped to it. The schema is
// dropped when the test ends.
func openScratchStore(t *testing.T) domain.Store {
	t.Helper()
	dsn := os.Getenv("CRN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRN_TEST_DATABASE_URL to a disposable Postgres to run store integration tests")
	}
	ctx := context.Background()

	schema := fmt.Sprintf("crn_it_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("CRN_TEST_DATABASE_URL must be a postgres:// URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()

	conn, err := pgx.Connect(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}

	st, err := New(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestBuildSettingsRoundTrip(t *testing.T) {
	st := openScratchStore(t)
	ctx := context.Background()

	if _, err := st.GetBuildSettings(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("before any save: err = %v, want ErrNotFound", err)
	}

	first := &domain.BuildSettings{Model: "claude-opus-5", Effort: "high", UpdatedFrom: "10.0.0.1"}
	if err := st.SaveBuildSettings(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt.IsZero() {
		t.Error("SaveBuildSettings did not read back updated_at")
	}

	// A second save replaces the row — there is only ever one.
	second := &domain.BuildSettings{Model: "claude-opus-5-5", Effort: "xhigh", UpdatedFrom: "10.0.0.2"}
	if err := st.SaveBuildSettings(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetBuildSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-opus-5-5" || got.Effort != "xhigh" || got.UpdatedFrom != "10.0.0.2" {
		t.Errorf("GetBuildSettings = %+v, want the second save", got)
	}
}

func TestClaudeConfigReachesTheDashboardAndTheTrace(t *testing.T) {
	st := openScratchStore(t)
	ctx := context.Background()

	org := &domain.Org{Name: "it"}
	if err := st.EnsureOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	proj := &domain.Project{ID: uuid.New(), OrgID: org.ID, Name: "it-project", Status: domain.ProjectActive}
	if err := st.CreateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}
	newJob := func(buildNo int) *domain.Job {
		t.Helper()
		j := &domain.Job{ID: uuid.New(), ProjectID: proj.ID, OrgID: org.ID, Status: domain.JobQueued,
			BuildNo: buildNo, Payload: []byte(`{}`)}
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		return j
	}

	// Build 1 runs Claude: stamped while building, shown on the dashboard.
	ran := newJob(1)
	if err := st.UpdateJobStatus(ctx, ran.ID, domain.JobBuilding, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetJobClaudeConfig(ctx, ran.ID, "claude-opus-5-5", "xhigh"); err != nil {
		t.Fatal(err)
	}
	snap, err := st.DashboardSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Building) != 1 || snap.Building[0].ClaudeModel != "claude-opus-5-5" || snap.Building[0].ClaudeEffort != "xhigh" {
		t.Errorf("dashboard building = %+v, want one job on claude-opus-5-5/xhigh", snap.Building)
	}

	// Its trace keeps them. Build 2 reused a cached image: no model ran, and
	// its trace must read back empty rather than fail on NULL.
	if err := st.SaveJobTrace(ctx, &domain.BuildTrace{JobID: ran.ID, ProjectID: proj.ID, BuildNo: 1,
		Outcome: "done", Mode: "build", ClaudeModel: "claude-opus-5-5", ClaudeEffort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	reused := newJob(2)
	if err := st.SaveJobTrace(ctx, &domain.BuildTrace{JobID: reused.ID, ProjectID: proj.ID, BuildNo: 2,
		Outcome: "done", Mode: "build"}); err != nil {
		t.Fatal(err)
	}

	traces, err := st.ListBuildTraces(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Fatalf("ListBuildTraces returned %d traces, want 2", len(traces))
	}
	if traces[0].BuildNo != 2 || traces[0].ClaudeModel != "" || traces[0].ClaudeEffort != "" {
		t.Errorf("reused build trace = %+v, want no model recorded", traces[0])
	}
	if traces[1].ClaudeModel != "claude-opus-5-5" || traces[1].ClaudeEffort != "xhigh" {
		t.Errorf("build 1 trace = %+v, want claude-opus-5-5/xhigh", traces[1])
	}

	// The single-trace read appends the events column after the summary — the
	// new columns must not shift it.
	full, err := st.GetBuildTrace(ctx, ran.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.ClaudeModel != "claude-opus-5-5" || full.Events == nil {
		t.Errorf("GetBuildTrace = model %q events %v", full.ClaudeModel, full.Events)
	}
}
