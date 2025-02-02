-- +goose Up
-- +goose StatementBegin
CREATE TYPE billing_cycle AS ENUM('monthly', 'yearly');

CREATE TABLE subscription_plans (
    id INT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL CHECK (name <> ''),
    description TEXT,
    price NUMERIC(10, 2) NOT NULL CHECK (price >= 0),
    billing_cycle billing_cycle NOT NULL,
    max_orders INT,
    -- e.g., {"analytics": true, "custom_domain": false, "dine_in: true, delivery: true"}
    features JSONB NOT NULL DEFAULT '{}'::JSONB,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT current_timestamp,
    updated_at TIMESTAMPTZ
);

CREATE TRIGGER set_subscription_updated_at BEFORE
UPDATE ON subscription_plans FOR EACH ROW
EXECUTE PROCEDURE set_updated_at ();

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TRIGGER set_subscription_updated_at ON subscription_plans;

DROP TYPE billing_cycle CASCADE;

DROP TABLE subscription_plans;

-- +goose StatementEnd
