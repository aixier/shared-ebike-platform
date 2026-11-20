-- iam 账号与留痕：表结构。

-- +goose Up

-- 岗位模板：开通账号时按岗位给菜单与权限点
CREATE TABLE iam_role_templates (
    role         TEXT PRIMARY KEY,
    post         TEXT NOT NULL,
    client       TEXT NULL CHECK (client IN ('管理端', '执法端')),
    loginable    BOOLEAN NOT NULL,
    id_prefix    TEXT NOT NULL CHECK (id_prefix ~ '^[0-9]{2}$'),
    default_org  TEXT NOT NULL,
    permissions  TEXT[] NOT NULL,
    menus        TEXT[] NOT NULL
);

-- 管理账号（含不可登录的智能体与系统账号）
CREATE TABLE iam_accounts (
    account_id     TEXT PRIMARY KEY CHECK (account_id ~ '^AC[0-9]{4}$'),
    role           TEXT NOT NULL REFERENCES iam_role_templates(role),
    post           TEXT NOT NULL,
    name           TEXT NOT NULL,
    username       TEXT NULL UNIQUE,
    password_hash  TEXT NULL,
    org            TEXT NOT NULL,
    operator_scope TEXT NULL,
    badge_no       TEXT NULL,
    permissions    TEXT[] NOT NULL,
    menus          TEXT[] NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('正常', '停用', '只读（运营商已退出）')),
    created_at     TIMESTAMPTZ NOT NULL,
    created_by     TEXT NULL,
    last_login_at  TIMESTAMPTZ NULL,
    agent_role     TEXT NULL,
    -- 不变量：运营商账号必须带数据范围
    CONSTRAINT operator_scope_required CHECK (role NOT IN ('运营商业务', '运营商技术对接') OR operator_scope IS NOT NULL),
    -- 不变量：智能体与系统账号不可登录（无账号名、无密码）
    CONSTRAINT nonlogin_accounts CHECK ((role IN ('智能体', '系统')) = (username IS NULL AND password_hash IS NULL))
);
CREATE INDEX iam_accounts_scope ON iam_accounts(operator_scope);

-- 登录会话：退出或停用时作废
CREATE TABLE iam_sessions (
    session_id  TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES iam_accounts(account_id),
    client      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ NULL
);
CREATE INDEX iam_sessions_account ON iam_sessions(account_id);

-- 已退出的运营商（来自 fleet.operator.exited）
CREATE TABLE iam_exited_operators (
    operator_id TEXT PRIMARY KEY,
    exited_at   TIMESTAMPTZ NOT NULL,
    reason      TEXT NULL,
    event_id    TEXT NULL
);

-- 留痕：只追加
CREATE TABLE iam_audit (
    seq          BIGSERIAL PRIMARY KEY,
    audit_id     TEXT NOT NULL UNIQUE,
    event_id     TEXT NOT NULL UNIQUE,
    source       TEXT NOT NULL,
    at           TIMESTAMPTZ NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL,
    account_id   TEXT NOT NULL,
    actor_name   TEXT NULL,
    actor_kind   TEXT NOT NULL CHECK (actor_kind IN ('人', '智能体', '系统', '用户')),
    action       TEXT NOT NULL,
    target_type  TEXT NOT NULL,
    target_id    TEXT NOT NULL,
    summary      TEXT NOT NULL,
    reason       TEXT NULL,
    before       JSONB NULL,
    after        JSONB NULL
);
CREATE INDEX iam_audit_at ON iam_audit(at DESC, audit_id DESC);
CREATE INDEX iam_audit_account ON iam_audit(account_id);
CREATE INDEX iam_audit_target ON iam_audit(target_type, target_id);
CREATE INDEX iam_audit_action ON iam_audit(action);

-- +goose StatementBegin
CREATE FUNCTION iam_audit_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'iam_audit 只允许追加';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER iam_audit_no_update BEFORE UPDATE OR DELETE ON iam_audit
    FOR EACH ROW EXECUTE FUNCTION iam_audit_append_only();
CREATE TRIGGER iam_audit_no_truncate BEFORE TRUNCATE ON iam_audit
    FOR EACH STATEMENT EXECUTE FUNCTION iam_audit_append_only();

-- 留痕编号日序号：AU + 日期 + 4 位
CREATE TABLE iam_audit_day_seq (
    day  DATE PRIMARY KEY,
    last INTEGER NOT NULL
);

-- 发件表
CREATE TABLE iam_outbox (
    seq          BIGSERIAL PRIMARY KEY,
    event_id     TEXT NOT NULL UNIQUE,
    subject      TEXT NOT NULL,
    body         JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ NULL
);
CREATE INDEX iam_outbox_pending ON iam_outbox(seq) WHERE published_at IS NULL;

-- 已处理的事件（按 eventId 去重）
CREATE TABLE iam_processed_events (
    event_id     TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL
);

-- 无法解析的事件：留存备查，不阻塞后续事件
CREATE TABLE iam_rejected_events (
    seq         BIGSERIAL PRIMARY KEY,
    subject     TEXT NOT NULL,
    body        TEXT NOT NULL,
    error       TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL
);

-- 幂等键
CREATE TABLE iam_idempotency (
    scope       TEXT NOT NULL,
    key         TEXT NOT NULL,
    status_code INTEGER NULL,
    body        BYTEA NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, key)
);

-- 版本化参数（如统一初始密码的哈希）
CREATE TABLE iam_settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE iam_settings;
DROP TABLE iam_idempotency;
DROP TABLE iam_rejected_events;
DROP TABLE iam_processed_events;
DROP TABLE iam_outbox;
DROP TABLE iam_audit_day_seq;
DROP TRIGGER iam_audit_no_truncate ON iam_audit;
DROP TRIGGER iam_audit_no_update ON iam_audit;
DROP TABLE iam_audit;
DROP FUNCTION iam_audit_append_only();
DROP TABLE iam_exited_operators;
DROP TABLE iam_sessions;
DROP TABLE iam_accounts;
DROP TABLE iam_role_templates;
