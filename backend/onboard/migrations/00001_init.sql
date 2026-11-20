-- +goose Up

-- 接入记录：每个品牌一条。status 等是订阅 fleet 事件得到的投影，只用于显示与前置判断。
CREATE TABLE integrations (
    integration_id    text PRIMARY KEY,
    brand_id          text NOT NULL UNIQUE,
    operator_id       text NOT NULL,
    brand_name        text,
    access_via        text NOT NULL DEFAULT '自行接入',
    provider_id       text,
    status            text NOT NULL,
    status_since      timestamptz NOT NULL,
    started_at        timestamptz,
    operator_base_url text,
    pending_start     boolean NOT NULL DEFAULT false,
    run_seq           integer NOT NULL DEFAULT 0,
    report_seq        integer NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE status_history (
    id             bigserial PRIMARY KEY,
    integration_id text NOT NULL,
    status         text NOT NULL,
    at             timestamptz NOT NULL,
    by_id          text NOT NULL DEFAULT '',
    note           text
);
CREATE INDEX status_history_integration ON status_history (integration_id, id);

-- 联调凭证：每个品牌一份。secret 是 HMAC 密钥，验签需要原文。
CREATE TABLE credentials (
    brand_id    text PRIMARY KEY,
    app_id      text NOT NULL UNIQUE,
    secret      text NOT NULL,
    issued_at   timestamptz NOT NULL,
    status      text NOT NULL DEFAULT '有效',
    disabled_at timestamptz,
    disabled_by text,
    rotated_at  timestamptz,
    rotated_by  text,
    delivered   boolean NOT NULL DEFAULT false
);

CREATE TABLE sandbox_settings (
    operator_id  text PRIMARY KEY,
    payment_mode text NOT NULL DEFAULT '正常'
);

-- fleet 车辆清单投影（车辆已入清单、车辆已移除）
CREATE TABLE fleet_vehicles (
    vehicle_id     text PRIMARY KEY,
    brand_id       text NOT NULL,
    operator_id    text NOT NULL,
    plate_no       text NOT NULL,
    gov_code       text,
    model          text,
    pile_tag_type  text,
    pile_tag       text,
    code_status    text,
    code_enable_at timestamptz,
    listed_at      timestamptz NOT NULL,
    listed_by      text
);
CREATE INDEX fleet_vehicles_brand ON fleet_vehicles (brand_id);

-- 平台参数（rule.params.changed 的投影）。初始值是需求文档建议值，收到事件即覆盖。
CREATE TABLE params (
    id                    integer PRIMARY KEY CHECK (id = 1),
    real_vehicles         integer NOT NULL,
    real_orders           integer NOT NULL,
    target_days           integer NOT NULL,
    unlock_timeout_sec    integer NOT NULL,
    backfill_window_hours integer NOT NULL,
    changed_at            timestamptz
);
INSERT INTO params (id, real_vehicles, real_orders, target_days, unlock_timeout_sec, backfill_window_hours)
VALUES (1, 20, 60, 21, 30, 72);

-- 符合性测试轮次与用例结果
CREATE TABLE runs (
    run_id         text PRIMARY KEY,
    integration_id text NOT NULL,
    brand_id       text NOT NULL,
    at             timestamptz NOT NULL,
    total          integer NOT NULL,
    passed         integer NOT NULL,
    failed         integer NOT NULL
);
CREATE INDEX runs_brand ON runs (brand_id, at DESC);

CREATE TABLE run_cases (
    run_id       text NOT NULL,
    case_id      text NOT NULL,
    seq          integer NOT NULL,
    grp          text NOT NULL,
    name         text NOT NULL,
    result       text NOT NULL,
    duration_ms  integer NOT NULL,
    fail_reason  text,
    facts        jsonb,
    suggestion   text,
    suggested_by text,
    PRIMARY KEY (run_id, case_id)
);

-- 联调环境订单（符合性测试与真车联调共用，只在本模块内，不进生产）
CREATE TABLE sandbox_orders (
    order_id           text PRIMARY KEY,
    brand_id           text NOT NULL,
    kind               text NOT NULL,          -- 符合性｜真车
    run_id             text,
    plate_no           text NOT NULL,
    sim_user_id        text,
    degraded           boolean NOT NULL DEFAULT false,
    unlock_requested_at timestamptz,
    unlock_reply       text,                   -- 已受理｜拒绝｜无答复｜验签失败 等
    unlock_reject      text,
    unlock_reply_ms    integer,
    end_requested_at   timestamptz,
    end_cause          text,
    end_reply          text,
    reject_first_lock  boolean NOT NULL DEFAULT false,
    pay_result         text,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sandbox_orders_brand ON sandbox_orders (brand_id, kind);
CREATE SEQUENCE sandbox_order_seq;

-- 运营商在联调环境里的 M-03 每次投递（含重复与被拒收的）
CREATE TABLE sandbox_deliveries (
    id          bigserial PRIMARY KEY,
    brand_id    text NOT NULL,
    order_id    text NOT NULL,
    event_id    text NOT NULL,
    type        text NOT NULL,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL,
    outcome     text NOT NULL,                 -- 已收到｜重复｜拒收
    body        jsonb NOT NULL
);
CREATE INDEX sandbox_deliveries_order ON sandbox_deliveries (order_id, id);
CREATE UNIQUE INDEX sandbox_deliveries_once ON sandbox_deliveries (brand_id, event_id) WHERE outcome = '已收到';

-- 运营商在联调环境里的 M-04 每次提交
CREATE TABLE vehicle_submissions (
    id           bigserial PRIMARY KEY,
    brand_id     text NOT NULL,
    submitted_at timestamptz NOT NULL,
    ok           boolean NOT NULL,
    rows         jsonb NOT NULL,
    errors       jsonb NOT NULL
);
CREATE INDEX vehicle_submissions_brand ON vehicle_submissions (brand_id, id);

CREATE TABLE sandbox_nonces (
    app_id  text NOT NULL,
    nonce   text NOT NULL,
    seen_at timestamptz NOT NULL,
    PRIMARY KEY (app_id, nonce)
);

-- 验收报告。数字由测试结果与真车联调记录决定；summary 由接入智能体写。
CREATE TABLE reports (
    report_id              text PRIMARY KEY,
    integration_id         text NOT NULL,
    real_vehicles_required integer NOT NULL,
    real_vehicles_ridden   integer NOT NULL,
    real_orders            integer NOT NULL,
    matched_orders         integer NOT NULL,
    lock_reports_matched   integer NOT NULL,
    time_limit_ok          boolean NOT NULL,
    run_id                 text NOT NULL,
    submitted_at           timestamptz NOT NULL,
    submitted_by           text NOT NULL,
    result                 text NOT NULL,      -- 待确认｜通过
    confirmed_by           text,
    confirmed_at           timestamptz,
    summary                text,
    summary_by             text
);
CREATE INDEX reports_integration ON reports (integration_id, submitted_at DESC);

CREATE TABLE idempotency (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     integer NOT NULL,
    body       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

CREATE TABLE processed_events (
    event_id     text PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE processed_events, outbox, idempotency, reports, sandbox_nonces, vehicle_submissions,
    sandbox_deliveries, sandbox_orders, run_cases, runs, params, fleet_vehicles, sandbox_settings,
    credentials, status_history, integrations;
DROP SEQUENCE sandbox_order_seq;
