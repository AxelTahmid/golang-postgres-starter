-- name: GetTenantFromHost :one
SELECT
    id
FROM
    tenants
WHERE
    domain = @host::TEXT;