-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS metrics (
    id    text             NOT NULL,
    mtype text             NOT NULL CHECK (mtype IN ('gauge', 'counter')),
    delta bigint,
    value double precision,
    PRIMARY KEY (id, mtype)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS metrics;
-- +goose StatementEnd