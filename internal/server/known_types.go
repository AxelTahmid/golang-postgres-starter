package server

import (
	"github.com/AxelTahmid/tinker/internal/db/sqlc"
	"github.com/AxelTahmid/tinker/internal/httpx"
)

// Enum registrations are part of the schema contract. Build fails if a named
// string type reachable from an operation is not registered exhaustively.
func init() {
	httpx.RegisterEnum[sqlc.RoleType](sqlc.RoleTypeAdmin, sqlc.RoleTypeCustomer)
}
