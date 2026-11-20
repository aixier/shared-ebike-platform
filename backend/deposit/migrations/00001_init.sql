-- +goose Up
-- deposit 本模块库 bike_deposit。金额一律整数分。

-- 押金账户：每个用户一行，记当前形态
CREATE TABLE accounts (
    user_id          text PRIMARY KEY,
    mode             text NOT NULL DEFAULT '未开通' CHECK (mode IN ('免押', '未开通', '押金')),
    deposit_id       text,          -- 当前押金（mode=押金）
    prev_deposit_id  text,          -- 改开免押之前的旧押金
    credit_provider  text,
    credit_score     integer,
    credit_opened_at timestamptz,
    updated_at       timestamptz NOT NULL
);

-- 押金缴纳：一次缴纳一行；按缴纳时金额退还（BR-25）
CREATE TABLE deposits (
    deposit_id         text PRIMARY KEY,
    user_id            text NOT NULL,
    amount_paid        bigint NOT NULL CHECK (amount_paid > 0),
    balance            bigint NOT NULL CHECK (balance >= 0),
    status             text NOT NULL CHECK (status IN ('已缴', '押金不足', '退押中', '已退')),
    paid_at            timestamptz NOT NULL,
    payment_id         text NOT NULL,
    channel            text NOT NULL,
    pricing_version_id text,
    created_at         timestamptz NOT NULL
);
CREATE INDEX deposits_user ON deposits (user_id);

-- 缴纳与补缴的收款记录（向 pay 发起的业务单号 bizNo = charge_id）
CREATE TABLE charges (
    charge_id          text PRIMARY KEY,
    user_id            text NOT NULL,
    kind               text NOT NULL CHECK (kind IN ('押金缴纳', '押金补缴')),
    deposit_id         text,
    amount             bigint NOT NULL CHECK (amount > 0),
    channel            text NOT NULL,
    pricing_version_id text,
    status             text NOT NULL CHECK (status IN ('处理中', '成功', '失败')),
    payment_id         text,
    fail_reason        text,
    idem_key           text NOT NULL,
    created_at         timestamptz NOT NULL,
    completed_at       timestamptz,
    UNIQUE (user_id, idem_key)
);

-- 押金抵扣（bill 的命令；bizNo = deduction_id）
CREATE TABLE deductions (
    deduction_id text PRIMARY KEY,
    caller       text NOT NULL,
    idem_key     text NOT NULL,
    user_id      text NOT NULL,
    order_id     text NOT NULL,
    deposit_id   text NOT NULL,
    requested    bigint NOT NULL,
    amount       bigint NOT NULL,
    status       text NOT NULL CHECK (status IN ('处理中', '成功', '失败')),
    payment_id   text,
    fail_reason  text,
    created_at   timestamptz NOT NULL,
    completed_at timestamptz,
    UNIQUE (caller, idem_key)
);
CREATE INDEX deductions_deposit ON deductions (deposit_id);

-- 退押申请（requestId；bizNo = request_id）
CREATE TABLE refund_requests (
    request_id        text PRIMARY KEY,
    user_id           text NOT NULL,
    deposit_id        text NOT NULL,
    amount            bigint NOT NULL,
    applied_at        timestamptz NOT NULL,
    expected_at       timestamptz NOT NULL,
    stopped_at        timestamptz,
    payment_id        text,
    manual_payment_id text,
    awaiting_payee    boolean NOT NULL DEFAULT false,
    status            text NOT NULL CHECK (status IN ('待阻止开单', '退款中', '已退', '已拒绝')),
    pay_mode          text,
    requested_by      jsonb NOT NULL,
    reason            text,
    ticket_id         text,
    reject_reason     text,
    refunded_at       timestamptz,
    created_at        timestamptz NOT NULL
);
CREATE UNIQUE INDEX refund_requests_open ON refund_requests (deposit_id) WHERE status IN ('待阻止开单', '退款中');
CREATE INDEX refund_requests_user ON refund_requests (user_id);

-- 专户台账：pay 的每一笔押金专户流水（状态只能从处理中改为成功或失败）
CREATE TABLE ledger (
    payment_id         text PRIMARY KEY,
    type               text NOT NULL CHECK (type IN ('押金缴纳', '押金补缴', '押金抵扣', '退押', '人工转账退押')),
    direction          text NOT NULL CHECK (direction IN ('收', '付', '转')),
    user_id            text NOT NULL,
    order_id           text,
    amount             bigint NOT NULL CHECK (amount > 0),
    channel            text,
    status             text NOT NULL CHECK (status IN ('成功', '失败', '处理中')),
    fail_reason        text,
    biz_no             text,
    related_payment_id text,
    created_at         timestamptz NOT NULL,
    completed_at       timestamptz
);
CREATE INDEX ledger_created ON ledger (created_at DESC);
CREATE INDEX ledger_user ON ledger (user_id);

-- 投影：骑行中订单（ride）
CREATE TABLE proj_riding (
    order_id   text PRIMARY KEY,
    user_id    text NOT NULL,
    created_at timestamptz
);
CREATE INDEX proj_riding_user ON proj_riding (user_id);

-- 投影：账单（bill）。state：待支付｜欠费｜已付清｜其他
CREATE TABLE proj_bills (
    order_id text PRIMARY KEY,
    user_id  text NOT NULL,
    unpaid   bigint NOT NULL DEFAULT 0,
    state    text NOT NULL
);
CREATE INDEX proj_bills_user ON proj_bills (user_id);

-- 投影：实名与账户状态（acct）
CREATE TABLE proj_users (
    user_id         text PRIMARY KEY,
    realname_status text,
    frozen          boolean NOT NULL DEFAULT false,
    cancelled       boolean NOT NULL DEFAULT false
);

-- 投影：规则版本中与押金有关的部分（rule）
CREATE TABLE proj_rule_versions (
    version_id       text PRIMARY KEY,
    effective_at     timestamptz NOT NULL,
    deposit_amount   bigint NOT NULL,
    credit_threshold integer NOT NULL,
    providers        jsonb,
    revoked          boolean NOT NULL DEFAULT false
);

-- 投影：平台参数（rule）
CREATE TABLE proj_params (
    id              integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    refund_workdays integer NOT NULL,
    changed_at      timestamptz
);

-- 发件表
CREATE TABLE outbox (
    seq          bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX outbox_pending ON outbox (seq) WHERE published_at IS NULL;

-- 收件去重
CREATE TABLE inbox (
    event_id     text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL
);

-- 幂等键
CREATE TABLE idempotency (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     integer,
    body       bytea,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (scope, key)
);

CREATE SEQUENCE seq_deposit;
CREATE SEQUENCE seq_charge;
CREATE SEQUENCE seq_deduction;
CREATE SEQUENCE seq_refund;

-- +goose Down
DROP TABLE accounts, deposits, charges, deductions, refund_requests, ledger, proj_riding, proj_bills,
    proj_users, proj_rule_versions, proj_params, outbox, inbox, idempotency;
DROP SEQUENCE seq_deposit, seq_charge, seq_deduction, seq_refund;
