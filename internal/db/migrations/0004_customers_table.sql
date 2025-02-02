-- +goose Up
-- +goose StatementBegin
CREATE TABLE customers (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id INT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    first_name VARCHAR(128) NOT NULL CHECK (first_name <> ''),
    last_name VARCHAR(128) NOT NULL CHECK (last_name <> ''),
    -- {is_default, name, contact, address_line_1, address_line_2, city, state, postal_code, country, notes}
    address_book JSONB DEFAULT '[]'::JSONB,
    settings JSONB DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp
);

CREATE INDEX idx_customers_tenant_id ON customers (tenant_id);

ALTER TABLE customers ENABLE ROW LEVEL SECURITY;

CREATE POLICY plc_customers_access ON customers USING (
    tenant_id = CURRENT_SETTING('app.current_tenant')::INT
);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_customers_tenant_id;

DROP POLICY plc_customers_access ON customers;

DROP TABLE customers;

-- +goose StatementEnd