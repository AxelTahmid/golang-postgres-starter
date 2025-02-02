-- +goose Up
-- +goose StatementBegin
CREATE TYPE role_type AS ENUM('admin', 'tenant', 'customer');

CREATE TABLE auth_users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email TEXT NOT NULL DEFAULT '',
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    phone VARCHAR(16) NOT NULL DEFAULT '',
    phone_verified BOOLEAN NOT NULL DEFAULT FALSE,
    password TEXT NOT NULL,
    role role_type NOT NULL,
    tenant_id INT REFERENCES tenants (id) ON DELETE CASCADE,
    customer_id BIGINT REFERENCES customers (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    updated_at TIMESTAMPTZ,
    -- Add a CHECK constraint to enforce role-based tenant_id and customer_id conditions
    CHECK (
        (
            role = 'admin'
            AND tenant_id IS NULL
            AND customer_id IS NULL
        )
        OR (
            role = 'tenant'
            AND tenant_id IS NOT NULL
            AND customer_id IS NULL
        )
        OR (
            role = 'customer'
            AND tenant_id IS NOT NULL
            AND customer_id IS NOT NULL
        )
    )
);

-- Ensure that the email is unique per tenant
CREATE UNIQUE INDEX idx_auth_users_email ON auth_users (email, tenant_id)
WHERE
    email <> '';

-- Ensure that the phone is unique per tenant
CREATE UNIQUE INDEX idx_auth_users_phone ON auth_users (phone, tenant_id)
WHERE
    phone <> '';

CREATE TRIGGER set_user_updated_at BEFORE
UPDATE ON auth_users FOR EACH ROW
EXECUTE PROCEDURE set_updated_at ();

ALTER TABLE auth_users ENABLE ROW LEVEL SECURITY;

-- CREATE POLICY plc_admin_access ON auth_users USING (role = 'admin')
-- WITH
--     CHECK (role = 'admin');
CREATE POLICY plc_auth_users_access ON auth_users USING (
    tenant_id = CURRENT_SETTING('app.current_tenant')::INT
);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TRIGGER set_user_updated_at ON auth_users;

DROP TYPE role_type CASCADE;

DROP INDEX idx_auth_users_email;

DROP INDEX idx_auth_users_phone;

DROP POLICY plc_auth_users_access ON auth_users;

DROP TABLE auth_users;

-- +goose StatementEnd