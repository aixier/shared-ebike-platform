-- +goose Up

-- 事件消费去重（按 eventId）
CREATE TABLE processed_events (
    event_id     text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- 发件表：业务事务里写入，发布器补发到事件总线
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- 幂等键：同一个键、同一个操作、同一个账号重复到达返回第一次的结果
CREATE TABLE idempotency (
    idem_key   text NOT NULL,
    op         text NOT NULL,
    account_id text NOT NULL,
    status     int  NOT NULL,
    body       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (idem_key, op, account_id)
);

-- 投影：品牌与运营商（fleet.brand.registered）
CREATE TABLE brands (
    brand_id            text PRIMARY KEY,
    operator_id         text NOT NULL,
    name                text NOT NULL DEFAULT '',
    operator_name       text NOT NULL DEFAULT '',
    operator_short_name text NOT NULL DEFAULT '',
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- 投影：运营商退出（fleet.operator.exited）
CREATE TABLE operator_exits (
    operator_id text PRIMARY KEY,
    exited_at   timestamptz NOT NULL,
    reason      text
);

-- 投影：规则版本（rule.version.published / revoked），版本内容不可改
CREATE TABLE versions (
    version_id    text PRIMARY KEY,
    effective_at  timestamptz NOT NULL,
    basis         text NOT NULL,
    ratios        jsonb NOT NULL,          -- {品牌编号: "0.62"}
    start_fee     bigint NOT NULL,
    start_minutes int NOT NULL,
    per_minute    bigint NOT NULL,
    dispatch_fee  bigint NOT NULL,
    allocation    text NOT NULL DEFAULT '',
    revoked_at    timestamptz
);

-- 投影：订单事实（ride.order.ended + bill.bill.issued）；不含任何用户字段
CREATE TABLE orders (
    order_id           text PRIMARY KEY,
    operator_id        text NOT NULL,
    brand_id           text NOT NULL,
    vehicle_id         text NOT NULL DEFAULT '',
    plate_no           text,
    pricing_version_id text NOT NULL DEFAULT '',
    request_at         timestamptz,
    ride_ended         boolean NOT NULL DEFAULT false,
    unlock_result      text,
    start_at           timestamptz,
    end_at             timestamptz,
    duration_sec       int,
    distance_km        double precision,
    start_location     jsonb,
    end_location       jsonb,
    end_station_id     text,
    end_station_name   text,
    end_method         text,
    in_station         boolean,
    in_station_basis   text,
    lock_report_at     timestamptz,
    pile_in_at         timestamptz,
    report_received_at timestamptz,
    late_report        boolean NOT NULL DEFAULT false,
    degraded           boolean NOT NULL DEFAULT false,
    has_bill           boolean NOT NULL DEFAULT false,
    billed_minutes     int,
    receivable         bigint,
    dispatch_fee       bigint,
    payable            bigint,
    month_card_deduct  bigint,
    settle_month       text
);
CREATE INDEX orders_op_request ON orders (operator_id, request_at DESC);
CREATE INDEX orders_month ON orders (settle_month);

-- 投影：退款（bill.bill.refunded），每次退款一条
CREATE TABLE refunds (
    event_id       text PRIMARY KEY,
    order_id       text NOT NULL,
    amount         bigint NOT NULL,
    refunded_total bigint NOT NULL,
    net_received   bigint NOT NULL,
    receivable     bigint NOT NULL,
    kind           text NOT NULL,
    refunded_at    timestamptz NOT NULL
);
CREATE INDEX refunds_order ON refunds (order_id);
CREATE INDEX refunds_at ON refunds (refunded_at);

-- 投影：月卡购买（bill.card.purchased），按支付时刻归月
CREATE TABLE card_purchases (
    card_id text PRIMARY KEY,
    price   bigint NOT NULL,
    paid_at timestamptz NOT NULL
);

-- 已出单的结算月
CREATE TABLE settle_months (
    month        text PRIMARY KEY,
    generated_at timestamptz NOT NULL,
    trigger      text NOT NULL
);

CREATE TABLE settlements (
    settlement_id  text PRIMARY KEY,
    operator_id    text NOT NULL,
    month          text NOT NULL,
    status         text NOT NULL,
    generated_at   timestamptz NOT NULL,
    due_date       text NOT NULL,
    reviewed_by    jsonb,
    reviewed_at    timestamptz,
    sent_at        timestamptz,
    paid_at        timestamptz,
    bank_trade_no  text,
    invoice_status text,
    month_card     jsonb NOT NULL,
    brand_cards    jsonb NOT NULL,       -- {品牌编号: {count, share}}
    note           text,
    UNIQUE (operator_id, month)
);

-- 逐单明细：出单时写入，之后不再修改（无 UPDATE 语句）
CREATE TABLE settlement_lines (
    settlement_id          text NOT NULL REFERENCES settlements(settlement_id),
    order_id               text NOT NULL UNIQUE,
    brand_id               text NOT NULL,
    start_at               timestamptz,
    end_at                 timestamptz NOT NULL,
    billed_minutes         int,
    distance_km            double precision,
    degraded               boolean NOT NULL,
    pricing_version_id     text NOT NULL,
    receivable             bigint NOT NULL,
    refunded_at_generation bigint NOT NULL,
    base                   bigint NOT NULL,
    ratio                  bigint NOT NULL,   -- 百万分之一
    fee                    bigint NOT NULL,
    remark                 text,
    PRIMARY KEY (settlement_id, order_id)
);

CREATE TABLE adjustments (
    adjustment_id text PRIMARY KEY,
    settlement_id text NOT NULL REFERENCES settlements(settlement_id),
    seq           int NOT NULL,
    type          text NOT NULL,
    brand_id      text NOT NULL,
    order_ids     jsonb NOT NULL DEFAULT '[]',
    base_delta    bigint NOT NULL,
    fee_delta     bigint NOT NULL,
    reason        text NOT NULL,
    dispute_id    text,
    created_by    jsonb NOT NULL,
    created_at    timestamptz NOT NULL,
    confirmed_by  jsonb,
    confirmed_at  timestamptz,
    UNIQUE (settlement_id, seq)
);

CREATE TABLE adjustment_items (
    adjustment_id text NOT NULL REFERENCES adjustments(adjustment_id),
    order_id      text NOT NULL,
    end_at        timestamptz NOT NULL,
    previous_base bigint NOT NULL,
    new_base      bigint NOT NULL,
    base_delta    bigint NOT NULL,
    ratio         bigint NOT NULL,
    fee_delta     bigint NOT NULL,
    PRIMARY KEY (adjustment_id, order_id)
);
CREATE INDEX adjustment_items_order ON adjustment_items (order_id);

-- 每单的结算状态与指针
CREATE TABLE order_settle (
    order_id        text PRIMARY KEY,
    settlement_id   text NOT NULL,
    kind            text NOT NULL,          -- 明细｜调整项
    adjustment_id   text,
    settled_base    bigint NOT NULL,
    settled_fee     bigint NOT NULL,
    last_settled_at timestamptz NOT NULL,
    note            text
);

CREATE TABLE disputes (
    dispute_id       text PRIMARY KEY,
    operator_id      text NOT NULL,
    settlement_id    text NOT NULL,
    order_ids        jsonb NOT NULL,
    field            text NOT NULL,
    operator_claim   text NOT NULL,
    platform_fact    text NOT NULL,
    agent_comparison text NOT NULL DEFAULT '',
    agent_suggestion text,
    status           text NOT NULL,
    submitted_at     timestamptz NOT NULL,
    submitted_by     jsonb NOT NULL,
    decided_at       timestamptz,
    decided_by       jsonb,
    decision_reason  text,
    corrections      jsonb NOT NULL DEFAULT '[]',
    adjustment_id    text
);
CREATE INDEX disputes_orders ON disputes USING gin (order_ids);

-- 采纳异议后的基数更正（只改服务费基数，不向用户补收）
CREATE TABLE corrections (
    dispute_id text NOT NULL,
    order_id   text NOT NULL,
    minutes    int NOT NULL,
    base_delta bigint NOT NULL,
    decided_at timestamptz NOT NULL,
    PRIMARY KEY (dispute_id, order_id)
);

CREATE TABLE seqs (
    name text PRIMARY KEY,
    n    int NOT NULL
);

-- +goose Down
DROP TABLE seqs, corrections, disputes, order_settle, adjustment_items, adjustments, settlement_lines, settlements,
    settle_months, card_purchases, refunds, orders, versions, operator_exits, brands, idempotency, outbox, processed_events;
