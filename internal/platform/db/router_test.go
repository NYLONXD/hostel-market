package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/platform/reqctx"
)

type fakeRecent struct {
	users map[uuid.UUID]bool
	err   error
	marks []uuid.UUID
}

func (f *fakeRecent) Mark(_ context.Context, id uuid.UUID) error {
	f.marks = append(f.marks, id)
	return nil
}

func (f *fakeRecent) Has(_ context.Context, id uuid.UUID) (bool, error) {
	return f.users[id], f.err
}

// lazyPool builds a pool without connecting; pgxpool only dials on first use.
func lazyPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

func TestRouterReader(t *testing.T) {
	primary := lazyPool(t, "postgres://u:p@127.0.0.1:1/primary")
	replica := lazyPool(t, "postgres://u:p@127.0.0.1:1/replica")
	withReplica := &Pools{Primary: primary, Replica: replica}
	single := &Pools{Primary: primary, Replica: primary}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	writer, reader := uuid.New(), uuid.New()
	userCtx := func(id uuid.UUID) context.Context { return reqctx.SetUserID(context.Background(), id) }

	tests := []struct {
		name   string
		pools  *Pools
		recent *fakeRecent
		ctx    context.Context
		c      Consistency
		want   *pgxpool.Pool
	}{
		{"strong always primary", withReplica, &fakeRecent{}, userCtx(reader), Strong, primary},
		{"anonymous browse uses replica", withReplica, &fakeRecent{}, context.Background(), ReplicaOK, replica},
		{"user without recent write uses replica", withReplica, &fakeRecent{users: map[uuid.UUID]bool{writer: true}}, userCtx(reader), ReplicaOK, replica},
		{"user with recent write uses primary", withReplica, &fakeRecent{users: map[uuid.UUID]bool{writer: true}}, userCtx(writer), ReplicaOK, primary},
		{"redis error fails safe to primary", withReplica, &fakeRecent{err: errors.New("down")}, userCtx(reader), ReplicaOK, primary},
		{"no replica configured", single, &fakeRecent{}, context.Background(), ReplicaOK, primary},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter(tc.pools, tc.recent, log)
			assert.Same(t, tc.want, r.Reader(tc.ctx, tc.c))
		})
	}
}

func TestRouterMarkWrite(t *testing.T) {
	primary := lazyPool(t, "postgres://u:p@127.0.0.1:1/primary")
	recent := &fakeRecent{}
	r := NewRouter(&Pools{Primary: primary, Replica: primary}, recent, slog.New(slog.NewTextHandler(io.Discard, nil)))

	r.MarkWrite(context.Background()) // anonymous: nothing to mark
	assert.Empty(t, recent.marks)

	id := uuid.New()
	r.MarkWrite(reqctx.SetUserID(context.Background(), id))
	assert.Equal(t, []uuid.UUID{id}, recent.marks)
}
