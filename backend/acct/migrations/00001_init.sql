-- +goose Up
-- 骑行用户。姓名、手机号、证件号只在本库保存（NFR-11）
CREATE SEQUENCE user_no_seq START 100001;

CREATE TABLE users (
    user_id                  text PRIMARY KEY,
    phone                    text NOT NULL,
    channel                  text NOT NULL,
    registered_at            timestamptz NOT NULL,
    last_active_at           timestamptz,
    during_degrade           boolean NOT NULL DEFAULT false,
    status                   text NOT NULL DEFAULT '正常',
    cancelled_at             timestamptz,
    tokens_valid_after       timestamptz,
    real_name                text,
    id_no                    text,
    rn_status                text NOT NULL DEFAULT '未实名',
    rn_verified_at           timestamptz,
    rn_adult                 boolean,
    rn_method                text,
    rn_fail_reason           text,
    rn_deadline              timestamptz,
    degraded_signup_event_id text,
    has_ridden               boolean NOT NULL DEFAULT false,
    freeze_kind              text,
    freeze_reason            text,
    freeze_at                timestamptz,
    freeze_how               text
);
-- 一个手机号一个账户；已注销的手机号可重新注册
CREATE UNIQUE INDEX users_phone_active ON users (phone) WHERE status <> '已注销';
CREATE INDEX users_phone ON users (phone);
CREATE INDEX users_frozen ON users (freeze_at) WHERE status = '冻结';

-- 账户状态变更记录，只追加
CREATE TABLE account_history (
    id       bigserial PRIMARY KEY,
    user_id  text NOT NULL,
    action   text NOT NULL,
    at       timestamptz NOT NULL,
    reason   text,
    by_actor text NOT NULL
);
CREATE INDEX account_history_user ON account_history (user_id, id);

-- 已作废的令牌（退出登录）
CREATE TABLE revoked_tokens (
    jti        text PRIMARY KEY,
    user_id    text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE SEQUENCE notice_no_seq;
CREATE TABLE notices (
    notice_id text PRIMARY KEY,
    user_id   text NOT NULL,
    at        timestamptz NOT NULL,
    kind      text NOT NULL,
    title     text NOT NULL,
    text      text NOT NULL,
    order_id  text,
    read      boolean NOT NULL DEFAULT false
);
CREATE INDEX notices_user ON notices (user_id, at DESC);

-- 幂等键：同一个键重复到达返回第一次的结果
CREATE TABLE idempotency (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     integer NOT NULL DEFAULT 0,
    body       bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- 发件表
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- 已处理的事件（按 eventId 去重）
CREATE TABLE processed_events (
    event_id     text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- 以下为订阅事件得到的投影
CREATE TABLE riding_orders (
    order_id   text PRIMARY KEY,
    user_id    text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX riding_orders_user ON riding_orders (user_id);

-- 已结束的订单号（防止"已结束"先于"已创建"到达时重新记成进行中）
CREATE TABLE closed_orders (
    order_id text PRIMARY KEY
);

CREATE TABLE order_bills (
    order_id       text PRIMARY KEY,
    user_id        text NOT NULL,
    collect_by     text NOT NULL,
    payable        bigint NOT NULL,
    billed_minutes integer NOT NULL,
    pay_deadline   timestamptz,
    paid           boolean NOT NULL DEFAULT false,
    in_arrears     boolean NOT NULL DEFAULT false
);
CREATE INDEX order_bills_user ON order_bills (user_id);

CREATE TABLE arrears_orders (
    order_id   text PRIMARY KEY,
    user_id    text NOT NULL,
    amount     bigint NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX arrears_orders_user ON arrears_orders (user_id);

CREATE TABLE user_deposit (
    user_id    text PRIMARY KEY,
    mode       text NOT NULL,
    status     text,
    balance    bigint,
    changed_at timestamptz NOT NULL
);

CREATE TABLE degrade_state (
    id         integer PRIMARY KEY DEFAULT 1,
    event_id   text,
    components text[] NOT NULL DEFAULT '{}',
    active     boolean NOT NULL DEFAULT false,
    entered_at timestamptz,
    exited_at  timestamptz
);

CREATE TABLE rule_versions (
    version_id   text PRIMARY KEY,
    effective_at timestamptz NOT NULL,
    revoked      boolean NOT NULL DEFAULT false,
    tight_minutes integer,
    tight_amount  bigint,
    body         jsonb NOT NULL
);

-- +goose Down
DROP TABLE rule_versions, degrade_state, user_deposit, arrears_orders, order_bills, closed_orders, riding_orders,
    processed_events, outbox, idempotency, notices, revoked_tokens, account_history, users;
DROP SEQUENCE notice_no_seq;
DROP SEQUENCE user_no_seq;
