-- +goose Up

-- ========== 基础件 ==========
-- 发件表：业务事务里写入，发布器发到事件总线后记 published_at
CREATE TABLE outbox (
    id           BIGSERIAL PRIMARY KEY,
    event_id     TEXT NOT NULL UNIQUE,
    subject      TEXT NOT NULL,
    body         JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- 收件去重：按事件编号只处理一次
CREATE TABLE inbox (
    event_id    TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 幂等键：同一登录人同一接口同一键返回第一次的结果
CREATE TABLE idempotency (
    scope      TEXT NOT NULL,
    key        TEXT NOT NULL,
    status     INT NOT NULL,
    body       BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- 编号：前缀 + 日期 + 日序号
CREATE TABLE id_seq (
    prefix TEXT NOT NULL,
    day    TEXT NOT NULL,
    n      INT NOT NULL,
    PRIMARY KEY (prefix, day)
);

-- 本模块持有的开关（自动暂停）
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ========== 投影 ==========
CREATE TABLE brands (
    brand_id      TEXT PRIMARY KEY,
    operator_id   TEXT,
    name          TEXT,
    access_status TEXT,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE rule_versions (
    version_id   TEXT PRIMARY KEY,
    effective_at TIMESTAMPTZ NOT NULL,
    ride         JSONB,
    risk_limits  JSONB,
    revoked      BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE platform_params (
    id         INT PRIMARY KEY DEFAULT 1,
    body       JSONB NOT NULL,
    changed_at TIMESTAMPTZ
);

-- 订单事实投影（只存闭合与指标用到的字段）
CREATE TABLE orders (
    order_id               TEXT PRIMARY KEY,
    user_id                TEXT,
    brand_id               TEXT,
    operator_id            TEXT,
    pricing_version_id     TEXT,
    request_at             TIMESTAMPTZ,
    status                 TEXT NOT NULL DEFAULT '开锁中',
    start_at               TIMESTAMPTZ,
    unlock_failed_at       TIMESTAMPTZ,
    end_at                 TIMESTAMPTZ,
    degraded               BOOLEAN NOT NULL DEFAULT false,
    degrade_event_id       TEXT,
    admission              TEXT,
    limit_minutes          INT,
    limit_amount           BIGINT,
    over_limit_reminded_at TIMESTAMPTZ,
    manual_missing         TEXT[],
    user_claimed_end_at    TIMESTAMPTZ,
    receivable             BIGINT,
    order_end_at           TIMESTAMPTZ,
    bill_pushed_at         TIMESTAMPTZ,
    payment                TEXT NOT NULL DEFAULT '未收款',
    payment_id             TEXT,
    payment_unknown_since  TIMESTAMPTZ,
    closed_at              TIMESTAMPTZ,
    note                   TEXT,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX orders_degrade_event ON orders (degrade_event_id);
CREATE INDEX orders_request_at ON orders (request_at);
CREATE INDEX orders_start_at ON orders (start_at);

CREATE TABLE users_realname (
    user_id       TEXT PRIMARY KEY,
    status        TEXT NOT NULL,
    after_degrade BOOLEAN NOT NULL DEFAULT false,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE dependency_states (
    component TEXT NOT NULL,
    instance  TEXT NOT NULL,
    state     TEXT NOT NULL,
    at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (component, instance)
);

CREATE TABLE operator_outages (
    id           BIGSERIAL PRIMARY KEY,
    operator_id  TEXT NOT NULL,
    brand_ids    TEXT[] NOT NULL,
    since        TIMESTAMPTZ NOT NULL,
    recovered_at TIMESTAMPTZ,
    last_error   TEXT
);

-- ========== 本模块持有 ==========
CREATE TABLE degrade_events (
    event_id             TEXT PRIMARY KEY,
    cause                TEXT NOT NULL,
    components           TEXT[] NOT NULL,
    entered_at           TIMESTAMPTZ NOT NULL,
    exited_at            TIMESTAMPTZ,
    entered_by           TEXT NOT NULL,
    entered_by_name      TEXT,
    exited_by            TEXT,
    exited_by_name       TEXT,
    status               TEXT NOT NULL,
    tightened            BOOLEAN NOT NULL DEFAULT false,
    stopped              BOOLEAN NOT NULL DEFAULT false,
    limits_version_id    TEXT NOT NULL DEFAULT '',
    limits               JSONB NOT NULL,
    max_hourly_orders    INT NOT NULL DEFAULT 0,
    max_user_open_orders INT NOT NULL DEFAULT 0
);
-- 同一时刻最多一个进行中的降级事件
CREATE UNIQUE INDEX degrade_events_one_active ON degrade_events ((true)) WHERE status = '进行中';

CREATE TABLE degrade_timeline (
    id       BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES degrade_events (event_id),
    at       TIMESTAMPTZ NOT NULL,
    by       TEXT NOT NULL,
    by_name  TEXT,
    by_kind  TEXT NOT NULL,
    action   TEXT NOT NULL,
    text     TEXT NOT NULL
);

CREATE TABLE brand_metrics_hourly (
    brand_id            TEXT NOT NULL,
    at                  TIMESTAMPTZ NOT NULL,
    requests            INT NOT NULL,
    succeeded           INT NOT NULL,
    unlock_success_rate DOUBLE PRECISION,
    availability        DOUBLE PRECISION,
    below               BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (brand_id, at)
);

CREATE TABLE brand_metrics_daily (
    brand_id            TEXT NOT NULL,
    date                DATE NOT NULL,
    unlock_success_rate DOUBLE PRECISION,
    availability        DOUBLE PRECISION,
    PRIMARY KEY (brand_id, date)
);

CREATE TABLE entry_probes (
    at TIMESTAMPTZ PRIMARY KEY,
    ok BOOLEAN NOT NULL
);

CREATE TABLE platform_daily (
    date               DATE PRIMARY KEY,
    entry_availability DOUBLE PRECISION,
    unlock_p90_sec     DOUBLE PRECISION,
    bill_push_p90_sec  DOUBLE PRECISION
);

-- 品牌持续低于门槛的当前一段（用于告警去重、建议失效与自动恢复）
CREATE TABLE brand_low_state (
    brand_id   TEXT PRIMARY KEY,
    since      TIMESTAMPTZ NOT NULL,
    alerted_at TIMESTAMPTZ
);

CREATE TABLE pauses (
    id              TEXT PRIMARY KEY,
    brand_id        TEXT NOT NULL,
    paused_at       TIMESTAMPTZ NOT NULL,
    resumed_at      TIMESTAMPTZ,
    metric          TEXT NOT NULL,
    value           DOUBLE PRECISION,
    threshold       DOUBLE PRECISION,
    sustain_minutes INT,
    by              TEXT NOT NULL,
    by_name         TEXT,
    note            TEXT
);

CREATE TABLE suspend_suggestions (
    id              TEXT PRIMARY KEY,
    brand_id        TEXT NOT NULL,
    at              TIMESTAMPTZ NOT NULL,
    metric          TEXT NOT NULL,
    value           DOUBLE PRECISION NOT NULL,
    threshold       DOUBLE PRECISION NOT NULL,
    sustain_minutes INT NOT NULL,
    by              TEXT NOT NULL,
    by_name         TEXT,
    note            TEXT NOT NULL,
    status          TEXT NOT NULL,
    decided_by      TEXT,
    decided_by_name TEXT,
    decided_at      TIMESTAMPTZ,
    decision_reason TEXT,
    pause_id        TEXT
);

CREATE TABLE alerts (
    alert_id         TEXT PRIMARY KEY,
    at               TIMESTAMPTZ NOT NULL,
    level            TEXT NOT NULL,
    type             TEXT NOT NULL,
    text             TEXT NOT NULL,
    brand_id         TEXT,
    degrade_event_id TEXT,
    notified         TEXT[] NOT NULL DEFAULT '{}',
    ack_by           TEXT,
    ack_by_name      TEXT,
    ack_at           TIMESTAMPTZ,
    dedup_key        TEXT UNIQUE
);
CREATE INDEX alerts_at ON alerts (at DESC);

-- +goose Down
DROP TABLE alerts, suspend_suggestions, pauses, brand_low_state, platform_daily, entry_probes,
    brand_metrics_daily, brand_metrics_hourly, degrade_timeline, degrade_events, operator_outages,
    dependency_states, users_realname, orders, platform_params, rule_versions, brands, settings,
    id_seq, idempotency, inbox, outbox;
