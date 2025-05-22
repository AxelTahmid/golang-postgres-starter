-- +goose Up
-- +goose StatementBegin
CREATE TYPE role_type AS ENUM('admin', 'customer');

CREATE TABLE auth_users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email TEXT NOT NULL DEFAULT '',
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    phone VARCHAR(15) NOT NULL DEFAULT '',
    phone_verified BOOLEAN NOT NULL DEFAULT FALSE,
    password TEXT NOT NULL,
    role role_type NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Unique indexes for tenant and customer rows:
CREATE UNIQUE INDEX idx_unq_auth_users_email_tenant ON auth_users (email, tenant_id)
WHERE
    tenant_id IS NOT NULL
    AND email <> '';

CREATE UNIQUE INDEX idx_unq_auth_users_phone_tenant ON auth_users (phone, tenant_id)
WHERE
    tenant_id IS NOT NULL
    AND phone <> '';

CREATE TRIGGER set_user_updated_at BEFORE
UPDATE ON auth_users FOR EACH ROW
EXECUTE PROCEDURE set_updated_at ();

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TRIGGER set_user_updated_at ON auth_users;

DROP INDEX idx_unq_auth_users_email_tenant;

DROP INDEX idx_unq_auth_users_phone_tenant;

DROP TABLE auth_users;

DROP TYPE role_type;

-- +goose StatementEnd