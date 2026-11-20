-- +goose Up
-- 规则版本投影（来自 rule.version.published，版本不可变）
CREATE TABLE pricing_versions (
    version_id   text PRIMARY KEY,
    effective_at timestamptz NOT NULL,
    revoked_at   timestamptz,
    body         jsonb NOT NULL
);

-- 平台参数投影（来自 rule.params.changed）：只存 bill 用到的
CREATE TABLE platform_params (
    id                  int PRIMARY KEY CHECK (id = 1),
    pay_window_hours    int NOT NULL,
    arrears_freeze_days int NOT NULL,
    changed_at          timestamptz NOT NULL
);

-- 用户投影：押金方式、实名、冻结、注销、付款入口
CREATE TABLE user_profiles (
    user_id         text PRIMARY KEY,
    deposit_mode    text,
    deposit_status  text,
    deposit_balance bigint,
    realname_status text,
    frozen          boolean NOT NULL DEFAULT false,
    cancelled       boolean NOT NULL DEFAULT false,
    channel         text,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- 账单：一单一张
CREATE TABLE bills (
    bill_id                  text PRIMARY KEY,
    order_id                 text NOT NULL UNIQUE,
    user_id                  text NOT NULL,
    brand_id                 text NOT NULL,
    operator_id              text NOT NULL,
    channel                  text NOT NULL,
    pricing_version_id       text NOT NULL,
    start_at                 timestamptz NOT NULL,
    end_at                   timestamptz NOT NULL,
    duration_sec             bigint NOT NULL,
    in_station               boolean NOT NULL,
    distance_to_station_m    int,
    zero_unlock              boolean NOT NULL,
    degraded                 boolean NOT NULL,
    billed_minutes           int NOT NULL,
    ride_fee                 bigint NOT NULL,
    dispatch_fee             bigint NOT NULL,
    receivable               bigint NOT NULL,
    month_card_id            text,
    month_card_deduct        bigint NOT NULL,
    coupon_id                text,
    coupon_name              text,
    coupon_deduct            bigint NOT NULL,
    payable                  bigint NOT NULL,
    channel_paid             bigint NOT NULL DEFAULT 0,
    deposit_deducted         bigint NOT NULL DEFAULT 0,
    refunded                 bigint NOT NULL DEFAULT 0,
    pay_status               text NOT NULL,
    pay_method               text,
    collect_by               text NOT NULL,
    pay_deadline             timestamptz,
    bill_pushed_at           timestamptz NOT NULL,
    arrears_at               timestamptz,
    arrears_cause            text,
    arrears_paid_at          timestamptz,
    pending_kind             text,
    pending_payment_id       text,
    refund_failed_payment_id text,
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CHECK (payable = receivable - month_card_deduct - coupon_deduct)
);
CREATE INDEX bills_user ON bills (user_id);
CREATE INDEX bills_status ON bills (pay_status);
CREATE INDEX bills_pushed ON bills (bill_pushed_at);

-- 每一笔动钱只记一次（键为流水号或抵扣键）
CREATE TABLE receipts (
    receipt_key text PRIMARY KEY,
    bill_id     text NOT NULL REFERENCES bills (bill_id),
    kind        text NOT NULL,
    amount      bigint NOT NULL,
    at          timestamptz NOT NULL
);

-- 退款命令
CREATE TABLE refunds (
    idem_key   text PRIMARY KEY,
    bill_id    text NOT NULL REFERENCES bills (bill_id),
    amount     bigint NOT NULL,
    status     text NOT NULL,
    payment_id text,
    ticket_id  text,
    reason     text NOT NULL,
    actor      jsonb NOT NULL,
    result     jsonb,
    created_at timestamptz NOT NULL
);
CREATE INDEX refunds_payment ON refunds (payment_id);

-- 月卡
CREATE SEQUENCE month_card_seq START 1001;
CREATE TABLE month_cards (
    card_id            text PRIMARY KEY,
    user_id            text NOT NULL,
    pricing_version_id text NOT NULL,
    price              bigint NOT NULL,
    per_order_cap      bigint NOT NULL,
    start_at           timestamptz NOT NULL,
    end_at             timestamptz NOT NULL,
    payment_id         text,
    state              text NOT NULL, -- 支付中｜已支付｜支付失败
    paid_at            timestamptz,
    created_at         timestamptz NOT NULL
);
CREATE INDEX month_cards_user ON month_cards (user_id);

-- 优惠券
CREATE SEQUENCE coupon_seq START 50000001;
CREATE TABLE coupons (
    coupon_id     text PRIMARY KEY,
    user_id       text NOT NULL,
    template_id   text NOT NULL,
    name          text NOT NULL,
    amount        bigint NOT NULL,
    issued_at     timestamptz NOT NULL,
    expire_at     timestamptz NOT NULL,
    obtained_by   text NOT NULL,
    used_order_id text,
    used_at       timestamptz
);
CREATE INDEX coupons_user ON coupons (user_id);

-- 当前欠费周期已发"欠费超期"的用户；结清时删除
CREATE TABLE arrears_overdue (
    user_id     text PRIMARY KEY,
    notified_at timestamptz NOT NULL
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

-- 收件去重
CREATE TABLE inbox (
    event_id     text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- 幂等键
CREATE TABLE idempotency (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     int,
    body       bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- +goose Down
DROP TABLE idempotency, inbox, outbox, arrears_overdue, coupons, month_cards, refunds, receipts, bills, user_profiles, platform_params, pricing_versions;
DROP SEQUENCE coupon_seq, month_card_seq;
