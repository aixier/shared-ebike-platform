-- +goose Up

-- 生产凭证：每家运营商一份；车联网服务商代接时一份覆盖其名下多家
CREATE TABLE credentials (
    credential_id   text PRIMARY KEY,
    app_id          text NOT NULL UNIQUE,
    holder_type     text NOT NULL,              -- 运营商｜车联网服务商
    holder_id       text NOT NULL UNIQUE,       -- 运营商编号或服务商编号
    holder_name     text NOT NULL,
    operator_ids    text[] NOT NULL,
    base_url        text NOT NULL DEFAULT '',
    secret_enc      text NOT NULL,              -- 密钥密文（AES-GCM）
    key_last4       text NOT NULL,
    ip_whitelist    text[] NOT NULL DEFAULT '{}',
    rate_limit      integer NOT NULL,
    status          text NOT NULL,              -- 有效｜停用
    issued_at       timestamptz NOT NULL,
    rotated_at      timestamptz,
    rotated_by      jsonb,
    disabled_at     timestamptz,
    disabled_by     jsonb,
    disabled_reason text,
    last_seen_at    timestamptz
);
CREATE SEQUENCE credential_seq;

-- 品牌投影（来自 fleet.brand.access_changed / fleet.operator.exited）
CREATE TABLE brands (
    brand_id      text PRIMARY KEY,
    operator_id   text NOT NULL,
    access_status text NOT NULL,
    updated_at    timestamptz NOT NULL
);

-- 随机数防重放
CREATE TABLE nonces (
    app_id  text NOT NULL,
    nonce   text NOT NULL,
    seen_at timestamptz NOT NULL,
    PRIMARY KEY (app_id, nonce)
);

-- 限流计数（按凭证、按分钟；多实例共用）
CREATE TABLE rate_counters (
    app_id text NOT NULL,
    minute timestamptz NOT NULL,
    n      integer NOT NULL,
    PRIMARY KEY (app_id, minute)
);

-- 幂等记录
CREATE TABLE idempotency (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     integer NOT NULL,
    body       bytea NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (scope, key)
);

-- 开锁、结束骑行指令（同一订单同一指令只发一次有效答复）
CREATE TABLE commands (
    order_id      text NOT NULL,
    kind          text NOT NULL,                -- 开锁｜结束骑行:<原因>
    operator_id   text NOT NULL,
    brand_id      text NOT NULL,
    vehicle_id    text,
    plate_no      text NOT NULL,
    credential_id text NOT NULL,
    result        jsonb NOT NULL,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,
    PRIMARY KEY (order_id, kind)
);

-- 运营商报告的骑行事件；未送达 ride 的由定时任务重试
CREATE TABLE ride_events (
    credential_id   text NOT NULL,
    event_id        text NOT NULL,
    order_id        text NOT NULL,
    delivery        jsonb NOT NULL,             -- 送达 ride 的报文
    received_at     timestamptz NOT NULL,
    delivered_at    timestamptz,
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    last_error      text,
    PRIMARY KEY (credential_id, event_id)
);
CREATE INDEX ride_events_pending ON ride_events (next_attempt_at) WHERE delivered_at IS NULL;

-- 运营商可达性
CREATE TABLE operator_health (
    operator_id          text PRIMARY KEY,
    consecutive_failures integer NOT NULL DEFAULT 0,
    unreachable          boolean NOT NULL DEFAULT false,
    since                timestamptz,
    last_error           text,
    updated_at           timestamptz NOT NULL
);

-- 平台参数投影（rule.params.changed）
CREATE TABLE params (
    id                        integer PRIMARY KEY CHECK (id = 1),
    backfill_window_hours     integer NOT NULL,
    unlock_report_timeout_sec integer NOT NULL,
    changed_at                timestamptz
);
INSERT INTO params (id, backfill_window_hours, unlock_report_timeout_sec) VALUES (1, 72, 30);

-- 发件表
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    envelope     bytea NOT NULL,
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX outbox_pending ON outbox (id) WHERE published_at IS NULL;

-- 已消费事件（去重）
CREATE TABLE consumed_events (
    event_id    text PRIMARY KEY,
    consumed_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE consumed_events, outbox, params, operator_health, ride_events, commands, idempotency, rate_counters, nonces, brands, credentials;
DROP SEQUENCE credential_seq;
