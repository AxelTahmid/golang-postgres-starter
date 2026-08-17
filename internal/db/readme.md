# Database Package - `db`

This package provides database access for the github.com/AxelTahmid/tinker application. It manages PostgreSQL connections, transaction handling, error mapping, and background job processing.

## Architecture

The package follows a layered design:

```
┌────────────────────────────────────────────────────────────┐
│                       High-level DB Interface              │
└───────────┬─────────────────┬───────────────┬──────────────┘
            │                 │               │
┌───────────▼────┐  ┌─────────▼────┐  ┌───────▼─────┐
│   RootStore    │  │  RiverStore  │  │ QueueClient │
└───────┬────────┘  └──────┬───────┘  └──────┬──────┘
        │                  │                 │
┌───────▼──────────────────▼─────────────────▼───────────────┐
│                       PostgreSQL Pools                     │
└────────────────────────────────────────────────────────────┘
```

## Key Components

### Interfaces

-   **DB**: Top-level interface providing access to all database functionality
-   **RootStore**: Admin-level database operations bypassing tenant isolation
-   **RiverStore**: Access to the River job queue database
-   **QueueClient**: Interface for background job processing

### Implementation Classes

-   **PostgresDB**: Main implementation of the DB interface
-   **PostgresRootStore**: Implementation of the RootStore interface
-   **PostgresRiverStore**: Implementation of the RiverStore interface
-   **RiverQueue**: Implementation of the QueueClient interface using River

## Usage Examples

### Creating a New Database Connection

```go
import (
    "context"
    "log/slog"

    "github.com/AxelTahmid/tinker/config"
    "github.com/AxelTahmid/tinker/internal/db"
)

func InitDatabase(ctx context.Context, conf *config.Config, logger *slog.Logger) (db.DB, error) {
    database, err := db.New(ctx, conf.Database, logger)
    if err != nil {
        return nil, err
    }

    // Start the job queue
    if err := database.StartQueue(ctx, conf.Server); err != nil {
        database.Close()
        return nil, err
    }

    return database, nil
}
```

### Using Transactions

```go
func CreateUser(ctx context.Context, store db.Store, user User) error {
    return store.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
        // Perform operations within transaction
        queries := sqlc.New(tx)

        // Create user
        if err := queries.CreateUser(ctx, /* parameters */); err != nil {
            return db.WrapDBError(ctx, err)
        }

        // Create related records
        // ...

        return nil
    })
}
```

### Adding Background Jobs

```go
func ScheduleWelcomeEmail(ctx context.Context, db db.DB, userID int64, email string) error {
    args := db.WelcomeEmailArgs{
        UserID: userID,
        Email:  email,
    }

    _, err := db.Queue().InsertWelcomeEmailJob(ctx, args, nil)
    return err
}
```

## Error Handling

The package provides domain-specific errors and helper functions:

```go
func GetUser(ctx context.Context, queries *sqlc.Queries, id int) (*User, error) {
    user, err := queries.GetUser(ctx, id)
    if err != nil {
        return nil, db.WrapDBError(ctx, err)
    }
    return user, nil
}

// In the calling code:
user, err := GetUser(ctx, db.Queries(), 123)
if db.IsNotFoundError(err) {
    // Handle not found case
} else if err != nil {
    // Handle other errors
}
```
