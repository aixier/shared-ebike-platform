-- +goose Up

-- 管理端录入或修改过地址、白名单、名称的凭证，启动时不再被环境变量覆盖
ALTER TABLE credentials ADD COLUMN settings_managed boolean NOT NULL DEFAULT false;

-- 已退出的运营商（fleet.operator.exited）：其运营商范围的管理端写操作一律拒绝（READONLY）
CREATE TABLE exited_operators (
    operator_id text PRIMARY KEY,
    exited_at   timestamptz NOT NULL,
    reason      text
);

-- +goose Down
DROP TABLE exited_operators;
ALTER TABLE credentials DROP COLUMN settings_managed;
