-- +goose Up
-- pay 收付款的库 bike_pay。流水只追加不修改：流水头写一次，状态变化记进 payment_results 新行。
-- trace: R-09 R-11 R-18 R-25 R-34 BR-06 R-47

-- 资金流水头（不可修改）
CREATE TABLE payments (
    payment_id          text PRIMARY KEY,
    biz_no              text NOT NULL,           -- 调用方业务单号；事件的保序键
    type                text NOT NULL,
    account             text NOT NULL CHECK (account IN ('骑行收入户','押金专户')),
    direction           text NOT NULL CHECK (direction IN ('收','付','转')),
    user_id             text,
    order_id            text,
    card_id             text,
    amount              bigint NOT NULL CHECK (amount > 0),
    channel             text NOT NULL CHECK (channel IN ('微信支付','支付宝','银行转账','押金专户划转')),
    credit              boolean NOT NULL DEFAULT false, -- 免押后扣款（支付分、芝麻信用）
    related_payment_id  text REFERENCES payments(payment_id),
    ticket_id           text,
    operator_account_id text,
    payee               jsonb,                   -- 人工转账收款人（脱敏）
    note                text,
    created_at          timestamptz NOT NULL,
    seq                 bigserial                -- 同一时刻内的稳定顺序
);
CREATE INDEX payments_biz ON payments (biz_no);
CREATE INDEX payments_order ON payments (order_id);
CREATE INDEX payments_user ON payments (user_id, created_at);
CREATE INDEX payments_related ON payments (related_payment_id);

-- 流水状态记录（只追加；当前状态取最新一行）
CREATE TABLE payment_results (
    id               bigserial PRIMARY KEY,
    payment_id       text NOT NULL REFERENCES payments(payment_id),
    status           text NOT NULL CHECK (status IN ('成功','失败','处理中')),
    fail_code        text,
    fail_reason      text,
    channel_trade_no text,
    completed_at     timestamptz,
    source           text NOT NULL,  -- 渠道、查询、人工确认、记账
    actor            jsonb,
    reason           text,
    recorded_at      timestamptz NOT NULL
);
CREATE INDEX payment_results_pid ON payment_results (payment_id, id DESC);

CREATE VIEW payment_current AS
SELECT p.*, r.status, r.fail_code, r.fail_reason, r.channel_trade_no, r.completed_at, r.recorded_at AS status_at
FROM payments p
JOIN LATERAL (
    SELECT * FROM payment_results x WHERE x.payment_id = p.payment_id ORDER BY x.id DESC LIMIT 1
) r ON true;

-- 用户提交的本人收款账户（只追加，取最新）
CREATE TABLE payee_submissions (
    id           bigserial PRIMARY KEY,
    payment_id   text NOT NULL REFERENCES payments(payment_id),
    user_id      text NOT NULL,
    name_masked  text NOT NULL,
    bank         text NOT NULL,
    account_tail text NOT NULL,
    submitted_at timestamptz NOT NULL
);
CREATE INDEX payee_submissions_pid ON payee_submissions (payment_id, id DESC);

-- 渠道对账单行（事实采集，按渠道交易去重）
CREATE TABLE channel_bill_lines (
    channel      text NOT NULL,
    merchant     text NOT NULL,
    out_no       text NOT NULL,
    direction    text NOT NULL,
    trade_no     text,
    amount       bigint NOT NULL,
    settled_date date NOT NULL,
    fetched_at   timestamptz NOT NULL,
    PRIMARY KEY (channel, merchant, out_no, direction)
);

-- 已出具的日对账（出具后冻结，不修改）
CREATE TABLE recon_reports (
    recon_id     text PRIMARY KEY,
    date         date NOT NULL UNIQUE,
    generated_at timestamptz NOT NULL,
    body         jsonb NOT NULL
);

-- 差异项处理（只追加，一项只处理一次）
CREATE TABLE recon_diff_handlings (
    recon_id   text NOT NULL REFERENCES recon_reports(recon_id),
    idx        int NOT NULL,
    handled_by text NOT NULL,
    note       text NOT NULL,
    handled_at timestamptz NOT NULL,
    PRIMARY KEY (recon_id, idx)
);

-- 幂等键
CREATE TABLE idempotency_keys (
    scope       text NOT NULL,
    key         text NOT NULL,
    payment_id  text,
    status_code int,
    body        jsonb,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (scope, key)
);

-- 发件表
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- 模拟渠道的交易（只在 PAY_CHANNEL_MODE=mock 时使用）
CREATE TABLE mock_channel_trades (
    channel      text NOT NULL,
    out_no       text NOT NULL,
    kind         text NOT NULL,
    merchant     text NOT NULL,
    user_id      text,
    trade_no     text,
    direction    text NOT NULL,
    amount       bigint NOT NULL,
    status       text NOT NULL,
    fail_reason  text,
    behavior     text NOT NULL,
    completed_at timestamptz,
    settled_date date,
    PRIMARY KEY (channel, out_no)
);

-- +goose Down
DROP TABLE mock_channel_trades;
DROP TABLE outbox;
DROP TABLE idempotency_keys;
DROP TABLE recon_diff_handlings;
DROP TABLE recon_reports;
DROP TABLE channel_bill_lines;
DROP TABLE payee_submissions;
DROP VIEW payment_current;
DROP TABLE payment_results;
DROP TABLE payments;
