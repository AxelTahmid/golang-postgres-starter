-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at () RETURNS TRIGGER AS $$
    BEGIN
        NEW.updated_at = current_timestamp;
        RETURN NEW;
    END;
$$ LANGUAGE plpgsql;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP FUNCTION set_updated_at;

-- +goose StatementEnd
