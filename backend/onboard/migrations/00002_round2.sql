-- +goose Up

-- 已退出的运营商（订阅 fleet.operator.exited）：之后该运营商 scope 的写操作一律拒绝 READONLY。
CREATE TABLE exited_operators (
    operator_id text PRIMARY KEY,
    exited_at   timestamptz NOT NULL,
    reason      text
);

-- 码状态最近一次变更时刻：车辆入清单事件迟到时不覆盖更新的码状态。
ALTER TABLE fleet_vehicles ADD COLUMN code_status_at timestamptz;

-- +goose Down
ALTER TABLE fleet_vehicles DROP COLUMN code_status_at;
DROP TABLE exited_operators;
