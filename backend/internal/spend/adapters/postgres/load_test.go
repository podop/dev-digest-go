package postgres_test

// Load harness of the cost ledger (NFR3, AC17): 50 concurrent appends to one workspace against a
// real database, the latency of every append, and the latency of MonthTotal (the cap-check read)
// while that load runs. It is skipped unless -ledger.load is given, so `make check` never runs it.
//
// Run it (from backend/, Go toolchain as in AGENTS.md; -v is needed to see the numbers):
//
//	# 1. against a throwaway pgtest container (Docker needed), small local run
//	go test ./internal/spend/adapters/postgres -run '^TestLedgerLoad$' -count=1 -v -ledger.load
//
//	# 2. against a migrated database of a throwaway stack (S23), at the NFR1 volume
//	LEDGER_LOAD_DSN='postgres://user:pass@localhost:5433/devdigest?sslmode=disable' \
//	go test ./internal/spend/adapters/postgres -run '^TestLedgerLoad$' -count=1 -v -timeout 30m \
//	    -ledger.load -ledger.seed-month=500000 -ledger.seed-earlier=2000000
//
// The database must already carry migration 0005 (the backend migrates on start). Never point
// LEDGER_LOAD_DSN at a database whose data matters: the run appends real ledger rows and seeds
// rows that stay behind (see "Seeding").
//
// Environment:
//
//	LEDGER_LOAD_DSN        database to use; unset = a fresh pgtest database (Docker).
//	LEDGER_LOAD_WORKSPACE  id of an existing workspace to load; unset = a new workspace is created.
//	DB_MAX_OPEN_CONNS      pool cap of the harness, like the backend's (default 20, 1-90).
//
// Flags:
//
//	-ledger.load            enable the test.
//	-ledger.workers=50      concurrent appenders.
//	-ledger.appends=40      measured appends per worker (50 x 40 = 2,000 samples).
//	-ledger.warmup=5        unmeasured appends per worker first, so the pool is warm like in a
//	                        running backend.
//	-ledger.budget=50ms     p95 budget of an append (NFR3, D21); the test fails above it. 0 = report only.
//	-ledger.seed-month=N    first seed N entries into the current UTC month of the workspace.
//	-ledger.seed-earlier=N  and N entries into the 12 months before it.
//
// Reading the output: "append" is the time of Store.Append as the caller sees it, so it includes
// waiting for a pooled connection (50 callers share DB_MAX_OPEN_CONNS connections), which an LLM
// call would also wait for. "MonthTotal under load" is sampled by 5 readers every 5 ms while the
// appends run; "MonthTotal idle" is the same read before the load starts.
//
// Seeding: the entries are written by SQL (generate_series, origin 'backfill', kind 'review_run',
// dedupe keys 'loadseed:<run>:<month|earlier>:<n>', slot 0 of the month totals, kept in step with the entries in
// the same statement), 250,000 per statement. Seeded and appended rows stay in the database; to
// remove them for a workspace the harness created:
//
//	DELETE FROM spend_month_totals WHERE workspace_id = '<id>';
//	DELETE FROM cost_ledger WHERE workspace_id = '<id>';
//
// The test also checks that the month total grew by exactly the sum of the appended costs and
// that every unknown cost was counted, so a lost or doubled update fails the run.

import (
	"cmp"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/spend/adapters/postgres"
	"dev-digest-go/backend/internal/spend/domain"
)

var (
	loadEnabled     = flag.Bool("ledger.load", false, "run the ledger load harness (NFR3); needs a database, see load_test.go")
	loadWorkers     = flag.Int("ledger.workers", 50, "load harness: concurrent appenders")
	loadAppends     = flag.Int("ledger.appends", 40, "load harness: measured appends per worker")
	loadWarmup      = flag.Int("ledger.warmup", 5, "load harness: unmeasured appends per worker first")
	loadBudget      = flag.Duration("ledger.budget", 50*time.Millisecond, "load harness: append p95 budget (0 = report only)")
	loadSeedMonth   = flag.Int("ledger.seed-month", 0, "load harness: entries to seed into the current month first")
	loadSeedEarlier = flag.Int("ledger.seed-earlier", 0, "load harness: entries to seed into the 12 months before it first")
)

const (
	loadReaders      = 5
	loadReadInterval = 5 * time.Millisecond
	seedChunk        = 250_000
	defaultOpenConns = 20
)

// TestLedgerLoad appends concurrently and reports p50/p95/max of Append and MonthTotal.
func TestLedgerLoad(t *testing.T) {
	if !*loadEnabled {
		t.Skip("load harness: pass -ledger.load (and LEDGER_LOAD_DSN for an existing database)")
	}
	if *loadWorkers < 1 || *loadAppends < 1 || *loadWarmup < 0 {
		t.Fatalf("flags: workers=%d appends=%d warmup=%d, want workers>=1, appends>=1, warmup>=0",
			*loadWorkers, *loadAppends, *loadWarmup)
	}
	db, maxOpen := openLoadDB(t)
	store := pgstore.NewStore(db)
	ws := loadWorkspace(t, db)
	now := time.Now().UTC()

	if *loadSeedMonth > 0 || *loadSeedEarlier > 0 {
		start := time.Now()
		run := uuid.NewString()[:8]
		seedLedger(t, db, ws, "loadseed:"+run+":month:", domain.MonthStart(now), *loadSeedMonth, currentMonthAt)
		seedLedger(t, db, ws, "loadseed:"+run+":earlier:", domain.MonthStart(now), *loadSeedEarlier, earlierMonthsAt)
		t.Logf("seeded %d entries into the current month and %d into the 12 before it in %s",
			*loadSeedMonth, *loadSeedEarlier, time.Since(start).Round(time.Millisecond))
	}

	before, err := store.MonthTotal(t.Context(), ws, now)
	if err != nil {
		t.Fatalf("MonthTotal(before) = %v", err)
	}
	idle := make([]time.Duration, 0, 50)
	for range 50 {
		start := time.Now()
		if _, err := store.MonthTotal(t.Context(), ws, now); err != nil {
			t.Fatalf("MonthTotal(idle) = %v", err)
		}
		idle = append(idle, time.Since(start))
	}

	var sum loadSums
	if *loadWarmup > 0 {
		warm := runAppends(t, store, ws, *loadWorkers, *loadWarmup, nil)
		sum.add(warm)
	}

	// Readers run while the measured appends run.
	var reads []time.Duration
	var readMu sync.Mutex
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range loadReaders {
		readers.Go(func() {
			tick := time.NewTicker(loadReadInterval)
			defer tick.Stop()
			var mine []time.Duration
			for {
				select {
				case <-stop:
					readMu.Lock()
					reads = append(reads, mine...)
					readMu.Unlock()
					return
				case <-tick.C:
					start := time.Now()
					if _, err := store.MonthTotal(t.Context(), ws, now); err != nil {
						t.Errorf("MonthTotal(under load) = %v", err)
						return
					}
					mine = append(mine, time.Since(start))
				}
			}
		})
	}
	started := time.Now()
	var appends []time.Duration
	measured := runAppends(t, store, ws, *loadWorkers, *loadAppends, &appends)
	elapsed := time.Since(started)
	close(stop)
	readers.Wait()
	sum.add(measured)

	after, err := store.MonthTotal(t.Context(), ws, now)
	if err != nil {
		t.Fatalf("MonthTotal(after) = %v", err)
	}
	if got, want := after.Spent-before.Spent, sum.spent; got != want {
		t.Errorf("month total grew by %d micros, want %d (sum of the appended costs)", got, want)
	}
	if got, want := after.Unpriced-before.Unpriced, sum.unpriced; got != want {
		t.Errorf("unpriced count grew by %d, want %d", got, want)
	}

	a := summarize(appends)
	t.Logf("pool: DB_MAX_OPEN_CONNS=%d · workers=%d · measured appends=%d (+%d warm-up per worker) in %s = %.0f appends/s",
		maxOpen, *loadWorkers, a.n, *loadWarmup, elapsed.Round(time.Millisecond), float64(a.n)/elapsed.Seconds())
	t.Logf("append                 n=%-6d p50=%-10s p95=%-10s max=%s", a.n, a.p50, a.p95, a.max)
	r := summarize(reads)
	t.Logf("MonthTotal under load  n=%-6d p50=%-10s p95=%-10s max=%s", r.n, r.p50, r.p95, r.max)
	i := summarize(idle)
	t.Logf("MonthTotal idle        n=%-6d p50=%-10s p95=%-10s max=%s", i.n, i.p50, i.p95, i.max)
	verdict := "report only"
	if *loadBudget > 0 {
		verdict = fmt.Sprintf("NFR3 append p95 <= %s: %v", *loadBudget, a.p95 <= *loadBudget)
	}
	t.Logf("verdict: %s", verdict)
	if *loadBudget > 0 && a.p95 > *loadBudget {
		t.Errorf("append p95 = %s, want <= %s (NFR3)", a.p95, *loadBudget)
	}
	if r.n == 0 {
		t.Error("no MonthTotal sample was taken under load; raise -ledger.appends")
	}
}

// loadSums accumulates what the appends should add to the month total.
type loadSums struct {
	spent    domain.Micros
	unpriced int
}

func (s *loadSums) add(o loadSums) {
	s.spent += o.spent
	s.unpriced += o.unpriced
}

// runAppends starts workers goroutines at once, each appending per entries to the workspace, and
// returns the sums of what it appended. When out is not nil, the latency of every append is put
// in it.
func runAppends(t *testing.T, store *pgstore.Store, ws string, workers, per int, out *[]time.Duration) loadSums {
	t.Helper()
	var (
		mu       sync.Mutex
		sum      loadSums
		start    = make(chan struct{})
		wg       sync.WaitGroup
		failures atomic.Int64
	)
	run := uuid.NewString()
	for w := range workers {
		wg.Go(func() {
			lat := make([]time.Duration, 0, per)
			var mine loadSums
			<-start
			for i := range per {
				e := loadEntry(ws, run, w, i)
				if e.Cost != nil {
					mine.spent += *e.Cost
				} else {
					mine.unpriced++
				}
				begin := time.Now()
				stored, err := store.Append(t.Context(), e)
				took := time.Since(begin)
				if err != nil || !stored {
					failures.Add(1)
					t.Errorf("Append(worker %d, entry %d) = %v, %v, want true, nil", w, i, stored, err)
					return
				}
				lat = append(lat, took)
			}
			mu.Lock()
			defer mu.Unlock()
			sum.add(mine)
			if out != nil {
				*out = append(*out, lat...)
			}
		})
	}
	close(start)
	wg.Wait()
	if failures.Load() > 0 {
		t.FailNow()
	}
	return sum
}

// loadEntry builds a live entry: a known cost of 1-1000 micros, unknown for every tenth entry.
func loadEntry(ws, run string, worker, i int) domain.Entry {
	e := domain.Entry{
		WorkspaceID: ws,
		OccurredAt:  time.Now().UTC(),
		Kind:        domain.KindReviewRun,
		Origin:      domain.OriginLive,
		Provider:    "load",
		Model:       "load",
		DedupeKey:   "load:" + run + ":" + strconv.Itoa(worker) + ":" + strconv.Itoa(i),
	}
	if (worker+i)%10 != 0 {
		c := domain.Micros(1 + (worker*31+i)%1000)
		e.Cost = &c
	}
	return e
}

// latencyStats are the order statistics of a set of durations.
type latencyStats struct {
	n             int
	p50, p95, max time.Duration
}

func summarize(d []time.Duration) latencyStats {
	if len(d) == 0 {
		return latencyStats{}
	}
	s := slices.Clone(d)
	slices.SortFunc(s, cmp.Compare[time.Duration])
	return latencyStats{n: len(s), p50: percentile(s, 50), p95: percentile(s, 95), max: s[len(s)-1]}
}

// percentile is the nearest-rank percentile (0 < p <= 100) of the ascending sorted durations.
func percentile(sorted []time.Duration, p int) time.Duration {
	rank := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	return sorted[max(rank, 1)-1]
}

func TestPercentile(t *testing.T) {
	ten := make([]time.Duration, 10)
	for i := range ten {
		ten[i] = time.Duration(i+1) * time.Millisecond
	}
	tests := []struct {
		name   string
		sorted []time.Duration
		p      int
		want   time.Duration
	}{
		{"p50 of ten", ten, 50, 5 * time.Millisecond},
		{"p95 of ten is the largest", ten, 95, 10 * time.Millisecond},
		{"p100", ten, 100, 10 * time.Millisecond},
		{"p1 of ten is the smallest", ten, 1, time.Millisecond},
		{"single sample", ten[:1], 95, time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(tc.sorted, tc.p); got != tc.want {
				t.Errorf("percentile(%v, %d) = %v, want %v", tc.sorted, tc.p, got, tc.want)
			}
		})
	}
}

// openLoadDB opens the pool the harness uses: LEDGER_LOAD_DSN, or a fresh pgtest database, capped
// like the backend (DB_MAX_OPEN_CONNS, default 20).
func openLoadDB(t *testing.T) (*gorm.DB, int) {
	t.Helper()
	dsn := os.Getenv("LEDGER_LOAD_DSN")
	if dsn == "" {
		dsn = pgtest.Migrated(t).DSN
	}
	maxOpen := defaultOpenConns
	if v := os.Getenv("DB_MAX_OPEN_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			t.Fatalf("DB_MAX_OPEN_CONNS = %q, want an integer 1-90", v)
		}
		maxOpen = n
	}
	db, err := platformpg.Open(t.Context(), dsn, slog.New(slog.DiscardHandler), platformpg.WithMaxOpenConns(maxOpen))
	if err != nil {
		t.Fatalf("Open(LEDGER_LOAD_DSN) = %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			if err := sqlDB.Close(); err != nil {
				t.Errorf("close pool: %v", err)
			}
		}
	})
	return db, maxOpen
}

// loadWorkspace returns LEDGER_LOAD_WORKSPACE, or creates a workspace of its own.
func loadWorkspace(t *testing.T, db *gorm.DB) string {
	t.Helper()
	if id := os.Getenv("LEDGER_LOAD_WORKSPACE"); id != "" {
		return id
	}
	var id string
	if err := db.Raw(`INSERT INTO workspaces (name) VALUES (?) RETURNING id`, "ledger-load-"+uuid.NewString()[:8]).
		Scan(&id).Error; err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return id
}

// The occurred_at expression of seeded entry g: spread over the first day of the current month,
// or over the 1-360 days before it (the 12 months before, roughly). Constants, never user input.
const (
	currentMonthAt  = `CAST(@month_start AS timestamptz) + (g % 86400) * interval '1 second'`
	earlierMonthsAt = `CAST(@month_start AS timestamptz) - (1 + g % 360) * interval '1 day'`
)

// seedSQL inserts entries @from..@to (inclusive) and adds them to slot 0 of their months in the
// same statement, like the migration's backfill does.
const seedSQL = `
WITH ins AS (
	INSERT INTO cost_ledger
		(workspace_id, occurred_at, kind, origin, provider, model, tokens_in, tokens_out,
		 cost_micros, ref_id, dedupe_key)
	SELECT CAST(@ws AS uuid), %s, 'review_run', 'backfill', 'load', 'load', NULL, NULL,
	       1000 + g %% 9000, NULL, CAST(@prefix AS text) || CAST(g AS text)
	FROM generate_series(CAST(@from AS bigint), CAST(@to AS bigint)) AS g
	RETURNING workspace_id, occurred_at, cost_micros
)
INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
SELECT workspace_id, date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date, 0,
       SUM(cost_micros), 0, COUNT(*)
FROM ins
GROUP BY workspace_id, date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date
ON CONFLICT (workspace_id, month, slot) DO UPDATE SET
	spent_micros = spend_month_totals.spent_micros + EXCLUDED.spent_micros,
	entry_count  = spend_month_totals.entry_count + EXCLUDED.entry_count`

// seedLedger writes n entries with the given occurred_at expression, seedChunk per statement.
// prefix makes the dedupe keys unique per call and per run.
func seedLedger(t *testing.T, db *gorm.DB, ws, prefix string, monthStart time.Time, n int, at string) {
	t.Helper()
	query := fmt.Sprintf(seedSQL, at)
	for from := 1; from <= n; from += seedChunk {
		to := min(from+seedChunk-1, n)
		err := db.Exec(query, map[string]any{
			"ws": ws, "month_start": monthStart, "prefix": prefix, "from": from, "to": to,
		}).Error
		if err != nil {
			t.Fatalf("seed entries %d-%d: %v", from, to, err)
		}
	}
}
