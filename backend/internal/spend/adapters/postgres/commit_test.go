package postgres_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// recordCommitMode makes the database write down the value of synchronous_commit that every
// transaction touching the month totals has at its commit. A deferred constraint trigger runs
// when the transaction starts to commit, before the commit record (and its flush) is written,
// so it sees exactly the setting the commit uses.
func (l *ledger) recordCommitMode() {
	l.t.Helper()
	l.exec(`CREATE TABLE commit_mode (mode text NOT NULL)`)
	l.exec(`CREATE FUNCTION record_commit_mode() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO commit_mode VALUES (current_setting('synchronous_commit')); RETURN NULL; END $$`)
	l.exec(`CREATE CONSTRAINT TRIGGER record_commit_mode AFTER INSERT OR UPDATE ON spend_month_totals
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION record_commit_mode()`)
}

func (l *ledger) commitModes() []string {
	l.t.Helper()
	var modes []string
	if err := l.db.Raw(`SELECT mode FROM commit_mode`).Scan(&modes).Error; err != nil {
		l.t.Fatalf("read commit_mode: %v", err)
	}
	return modes
}

// TestAppend_commitsAsynchronously (NFR3, D22): a standalone append commits with
// synchronous_commit = off, the setting does not leak to the pooled connection, and an append
// that joins a caller's transaction leaves the caller's durability alone.
func TestAppend_commitsAsynchronously(t *testing.T) {
	l := newLedger(t)
	tn := l.newTenant("a")
	l.recordCommitMode()

	if !l.append(entryAt(tn.ws, "standalone", micros(7), mid)) {
		t.Fatal("standalone Append stored = false, want true")
	}
	if diff := cmp.Diff([]string{"off"}, l.commitModes()); diff != "" {
		t.Errorf("synchronous_commit at the commit of a standalone Append mismatch (-want +got):\n%s", diff)
	}

	// SET LOCAL ends with the append's transaction: the pooled connection is back to the default.
	var after string
	if err := l.db.Raw("SHOW synchronous_commit").Scan(&after).Error; err != nil {
		t.Fatalf("SHOW synchronous_commit: %v", err)
	}
	if after != "on" {
		t.Errorf("synchronous_commit after the append = %q, want on (the setting must not outlive the transaction)", after)
	}

	err := platformpg.NewTx(l.db).InTx(context.Background(), func(ctx context.Context) error {
		_, err := l.store.Append(ctx, entryAt(tn.ws, "in-tx", micros(3), mid))
		return err
	})
	if err != nil {
		t.Fatalf("InTx(Append): %v", err)
	}
	if diff := cmp.Diff([]string{"off", "on"}, l.commitModes()); diff != "" {
		t.Errorf("synchronous_commit at the commit of a caller's transaction with an Append mismatch (-want +got):\n%s", diff)
	}

	// A duplicate stores nothing and a stored entry still matches its total.
	if l.append(entryAt(tn.ws, "standalone", micros(7), mid)) {
		t.Error("duplicate Append stored = true, want false")
	}
	if got := l.total(tn.ws, mid); got.Spent != 10 {
		t.Errorf("MonthTotal = %+v, want 10 spent", got)
	}
	l.assertTotalsMatchEntries(tn.ws)
}
