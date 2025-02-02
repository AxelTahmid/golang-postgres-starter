package db

import (
	"context"
	"net/http"
	"strconv"

	"github.com/AxelTahmid/tinker/internal/db/sqlc"
	"github.com/AxelTahmid/tinker/internal/utils/ctxkeys"
	"github.com/AxelTahmid/tinker/internal/utils/respond"
)

func SetTenantContext(q *sqlc.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if host == "" {
				respond.Error(w, http.StatusUnauthorized, `missing "host" header`)
				return
			}

			// var tenantID string
			id, err := q.GetTenantFromHost(r.Context(), host)
			if err != nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxkeys.TenantID, nil))
			} else {
				// tenantID = strconv.Itoa(int(id))
				r = r.WithContext(context.WithValue(r.Context(), ctxkeys.TenantID, strconv.Itoa(int(id))))
			}

			next.ServeHTTP(w, r)
		})
	}
}
