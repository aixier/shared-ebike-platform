-- +goose Up
-- 已退出的运营商（订阅 fleet.operator.exited）：其账号对工单的写操作拒绝 READONLY
CREATE TABLE exited_operators (
    operator_id    TEXT PRIMARY KEY,
    exited_at      TIMESTAMPTZ NOT NULL,
    readonly_until TIMESTAMPTZ
);

-- +goose Down
DROP TABLE exited_operators;
