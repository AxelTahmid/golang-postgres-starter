package ctxkeys

type (
	tenant_key string
	user_key   string
)

const (
	TenantID tenant_key = "tenant_id"
	AuthUser user_key   = "AuthenticatedUserInfo"
)
