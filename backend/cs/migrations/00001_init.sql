-- +goose Up
-- 工单。订单事实快照建单时写入，之后不改（只写一次，见 facts 触发器）。
CREATE TABLE tickets (
    ticket_id                TEXT PRIMARY KEY,
    user_id                  TEXT NOT NULL,
    order_id                 TEXT,
    operator_id              TEXT,
    category                 TEXT NOT NULL,
    entry                    TEXT NOT NULL,
    status                   TEXT NOT NULL CHECK (status IN ('待处理', '处理中', '待主管确认', '已解决')),
    created_at               TIMESTAMPTZ NOT NULL,
    closed_at                TIMESTAMPTZ,
    assignee                 TEXT,
    assignee_name            TEXT,
    facts                    JSONB,
    refund_proposal          JSONB,
    force_end_proposal       JSONB,
    force_end                JSONB,
    awaiting_payee           BOOLEAN NOT NULL DEFAULT FALSE,
    refund_failed_payment_id TEXT,
    refund_pending           BOOLEAN NOT NULL DEFAULT FALSE,
    refund_seq               INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX tickets_user_idx ON tickets (user_id, created_at DESC);
CREATE INDEX tickets_order_idx ON tickets (order_id, created_at DESC);
CREATE INDEX tickets_status_idx ON tickets (status, created_at DESC);

-- +goose StatementBegin
CREATE FUNCTION tickets_facts_immutable() RETURNS trigger AS $$
BEGIN
    IF OLD.facts IS DISTINCT FROM NEW.facts THEN
        RAISE EXCEPTION '订单事实快照建单后不可修改';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER tickets_facts_immutable BEFORE UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION tickets_facts_immutable();

-- 工单号日序号
CREATE TABLE ticket_day_seq (
    day TEXT PRIMARY KEY,
    n   INTEGER NOT NULL
);

-- 对话消息，只追加
CREATE TABLE ticket_messages (
    id        BIGSERIAL PRIMARY KEY,
    ticket_id TEXT NOT NULL REFERENCES tickets (ticket_id),
    sender    TEXT NOT NULL CHECK (sender IN ('用户', '客服智能体', '客服')),
    at        TIMESTAMPTZ NOT NULL,
    text      TEXT NOT NULL
);
CREATE INDEX ticket_messages_ticket_idx ON ticket_messages (ticket_id, id);

-- 处理动作，只追加
CREATE TABLE ticket_actions (
    id         BIGSERIAL PRIMARY KEY,
    ticket_id  TEXT NOT NULL REFERENCES tickets (ticket_id),
    type       TEXT NOT NULL CHECK (type IN ('退款', '超额退款确认', '人工转账', '强制结束', '人工解冻')),
    payment_id TEXT,
    at         TIMESTAMPTZ NOT NULL,
    by_account TEXT NOT NULL
);
CREATE INDEX ticket_actions_ticket_idx ON ticket_actions (ticket_id, id);
CREATE INDEX ticket_actions_payment_idx ON ticket_actions (payment_id);

-- 平台参数投影（rule.params.changed）
CREATE TABLE platform_params (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    refund_limit BIGINT NOT NULL,
    changed_at   TIMESTAMPTZ
);

-- 发件表
CREATE TABLE outbox (
    id           BIGSERIAL PRIMARY KEY,
    event_id     TEXT NOT NULL UNIQUE,
    subject      TEXT NOT NULL,
    body         JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;

-- 已处理的事件（按 eventId 去重）
CREATE TABLE inbox (
    event_id     TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 幂等键：同一主体、同一接口、同一键返回第一次的结果
CREATE TABLE idempotency (
    scope      TEXT NOT NULL,
    key        TEXT NOT NULL,
    status     INTEGER,
    body       BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- +goose Down
DROP TABLE idempotency;
DROP TABLE inbox;
DROP TABLE outbox;
DROP TABLE platform_params;
DROP TABLE ticket_actions;
DROP TABLE ticket_messages;
DROP TABLE ticket_day_seq;
DROP TRIGGER tickets_facts_immutable ON tickets;
DROP FUNCTION tickets_facts_immutable();
DROP TABLE tickets;
