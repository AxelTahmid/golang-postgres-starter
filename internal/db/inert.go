package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/AxelTahmid/tinker/config"
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
)

// NewInert returns a DB for OpenAPI docs generation: every construction-time
// dereference the route wiring performs — RootStore().Queries(), Pool(),
// Queue(), Close() — succeeds without a database, while any attempt to
// actually execute a query, transaction, ping or job insert panics loudly.
//
// Construction-safe, execution-loud. The generator only walks and reflects the
// compiled router; nothing may run SQL during that walk. Passing a nil DB
// instead would work only for as long as no service dereferences it at
// construction, and would fail as a nil-pointer panic with no explanation the
// moment one did.
func NewInert() DB {
	return &inertDB{
		root:  &inertStore{queries: sqlc.New(inertDBTX{})},
		river: &inertRiverStore{},
		queue: inertQueue{},
	}
}

const (
	inertQueryMsg = "inert db: query executed during docs generation"
	inertTxMsg    = "inert db: transaction started during docs generation"
	inertPingMsg  = "inert db: ping executed during docs generation"
	inertQueueMsg = "inert db: job enqueued during docs generation"
)

// inertDBTX implements sqlc.DBTX so services can extract *sqlc.Queries at
// construction. Executing anything through it is a bug in the docs-generation
// path and panics.
type inertDBTX struct{}

func (inertDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic(inertQueryMsg)
}

func (inertDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic(inertQueryMsg)
}

func (inertDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	panic(inertQueryMsg)
}

// inertPool satisfies Pool without holding a connection.
type inertPool struct{}

func (inertPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { panic(inertTxMsg) }

func (inertPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic(inertQueryMsg)
}
func (inertPool) Ping(context.Context) error { panic(inertPingMsg) }
func (inertPool) Close()                     {}

// inertStore satisfies RootStore.
type inertStore struct {
	queries *sqlc.Queries
}

func (s *inertStore) Queries() *sqlc.Queries { return s.queries }
func (*inertStore) Pool() Pool               { return inertPool{} }

func (*inertStore) WithTransaction(context.Context, TransactionFunc) error { panic(inertTxMsg) }
func (*inertStore) Ping(context.Context) error                             { panic(inertPingMsg) }
func (*inertStore) Close()                                                 {}

// inertRiverStore satisfies RiverStore.
type inertRiverStore struct{}

func (*inertRiverStore) Pool() Pool                 { return inertPool{} }
func (*inertRiverStore) Ping(context.Context) error { panic(inertPingMsg) }
func (*inertRiverStore) Close()                     {}

// inertQueue satisfies QueueClient.
type inertQueue struct{}

func (inertQueue) Insert(
	context.Context, river.JobArgs, *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	panic(inertQueueMsg)
}

func (inertQueue) InsertTx(
	context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	panic(inertQueueMsg)
}
func (inertQueue) Start(context.Context) error { panic(inertQueueMsg) }
func (inertQueue) Stop(context.Context) error  { return nil }

// inertDB satisfies DB.
type inertDB struct {
	root  *inertStore
	river *inertRiverStore
	queue inertQueue
}

func (d *inertDB) RootStore() RootStore   { return d.root }
func (d *inertDB) RiverStore() RiverStore { return d.river }
func (d *inertDB) Queue() QueueClient     { return d.queue }
func (*inertDB) Ping(context.Context) error {
	panic(inertPingMsg)
}
func (*inertDB) Close() {}

func (*inertDB) StartQueue(context.Context, *config.Server) error { panic(inertQueueMsg) }
func (*inertDB) StopQueue(context.Context) error                  { return nil }
