-- +goose Up

-- 投影：每单的开锁状态（ride.order.unlocked / unlock_failed / pending_manual / ended）。
-- 开锁失败的订单不计入结算；不含任何用户字段。
CREATE TABLE order_unlock (
    order_id   text PRIMARY KEY,
    state      text NOT NULL,           -- 已开锁｜开锁失败｜待人工处理｜已结束
    reason     text,                    -- 开锁失败原因
    missing    jsonb,                   -- 待人工处理时缺的事实
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_unlock_failed ON order_unlock (order_id) WHERE state = '开锁失败';

-- 投影：每单的收款结果（bill.bill.paid），作为结算事实留存
CREATE TABLE order_pay (
    order_id   text PRIMARY KEY,
    pay_status text NOT NULL,
    paid_at    timestamptz
);

-- +goose Down
DROP TABLE order_pay, order_unlock;
