package db

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeTx records which terminal operation runInTransaction chose. Embedding
// pgx.Tx satisfies the interface without stubbing the dozen methods the
// helper never calls; touching one of those in a test would nil-panic, which
// is the correct outcome for an unexpected call.
type fakeTx struct {
	pgx.Tx

	committed  bool
	rolledBack bool
}

func (t *fakeTx) Commit(context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(context.Context) error { t.rolledBack = true; return nil }

type fakeBeginner struct {
	tx       *fakeTx
	beginErr error
}

func (b *fakeBeginner) Begin(context.Context) (pgx.Tx, error) {
	if b.beginErr != nil {
		return nil, b.beginErr
	}
	return b.tx, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// TestRunInTransactionRollsBackOnError is the regression test for the bug this
// helper was extracted to fix: with an unnamed return value, the deferred
// closure only ever observed the (nil) error from Begin, so every transaction
// committed — including ones whose callback failed.
func TestRunInTransactionRollsBackOnError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("callback failed")
	tx := &fakeTx{}
	beginner := &fakeBeginner{tx: tx}

	err := runInTransaction(context.Background(), discardLogger(), beginner,
		func(context.Context, pgx.Tx) error { return wantErr })

	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if tx.committed {
		t.Error("transaction committed despite the callback returning an error")
	}
	if !tx.rolledBack {
		t.Error("transaction was not rolled back after the callback failed")
	}
}

func TestRunInTransactionCommitsOnSuccess(t *testing.T) {
	t.Parallel()

	tx := &fakeTx{}
	beginner := &fakeBeginner{tx: tx}

	err := runInTransaction(context.Background(), discardLogger(), beginner,
		func(context.Context, pgx.Tx) error { return nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tx.committed {
		t.Error("transaction was not committed after the callback succeeded")
	}
	if tx.rolledBack {
		t.Error("transaction rolled back despite the callback succeeding")
	}
}

func TestRunInTransactionRollsBackAndRepanicsOnPanic(t *testing.T) {
	t.Parallel()

	tx := &fakeTx{}
	beginner := &fakeBeginner{tx: tx}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("panic did not propagate to the caller")
		}
		if !tx.rolledBack {
			t.Error("transaction was not rolled back before the panic propagated")
		}
		if tx.committed {
			t.Error("transaction committed on the panic path")
		}
	}()

	_ = runInTransaction(context.Background(), discardLogger(), beginner,
		func(context.Context, pgx.Tx) error { panic("boom") })
}

func TestRunInTransactionReportsBeginFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("pool exhausted")
	beginner := &fakeBeginner{beginErr: wantErr}

	called := false
	err := runInTransaction(context.Background(), discardLogger(), beginner,
		func(context.Context, pgx.Tx) error { called = true; return nil })

	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want it to wrap %v", err, wantErr)
	}
	if called {
		t.Error("callback ran even though the transaction never began")
	}
}
