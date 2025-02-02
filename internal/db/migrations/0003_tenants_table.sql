-- TODO: stripe_config should be encrypted with `pgcrypto` extension
-- +goose Up
-- +goose StatementBegin
CREATE TYPE tenant_status AS ENUM('trial', 'active', 'suspended', 'disabled');

CREATE TABLE tenants (
    id INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subscription_plan_id INT REFERENCES subscription_plans (id) ON DELETE SET NULL,
    full_name VARCHAR(255) NOT NULL CHECK (full_name <> ''),
    company_name TEXT NOT NULL CHECK (company_name <> ''),
    domain TEXT NOT NULL UNIQUE CHECK (domain <> ''),
    status tenant_status DEFAULT 'trial',
    -- name, address, phone, opening_hours, timezone, country, social_links, allow guest checkout, etc
    store_settings JSONB NOT NULL DEFAULT '{}'::JSONB,
    -- Payment provider configuration for tenant store
    payment_config JSONB NOT NULL DEFAULT '{}'::JSONB,
    -- enabled_pickup, enabled_delivery, enabled_dinein, enabled_on_premise
    admin_settings JSONB,
    suspended_at TIMESTAMPTZ,
    subscription_start_date TIMESTAMPTZ,
    subscription_end_date TIMESTAMPTZ,
    trial_start_date TIMESTAMPTZ,
    trial_end_date TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    updated_at TIMESTAMPTZ
);

CREATE INDEX idx_tenants_status ON tenants (status);

CREATE TRIGGER set_tenant_updated_at BEFORE
UPDATE ON tenants FOR EACH ROW
EXECUTE PROCEDURE set_updated_at ();

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TRIGGER set_tenant_updated_at ON tenants;

DROP TYPE tenant_status CASCADE;

-- index relies on type, so CASCADE drops it
-- DROP INDEX idx_tenants_status;
DROP TABLE tenants;

-- +goose StatementEnd