-- +goose Up
-- 一键支付两步化：第一步预下单后流水处理中，等渠道回调或查询得到结果。
-- 本表记下调起参数与状态，不改 payments（流水只追加）。
-- trace: R-09 BR-05；contracts/openapi/pay.yaml startOneTapPayment
CREATE TABLE pay_intents (
    payment_id  text PRIMARY KEY REFERENCES payments(payment_id),
    pay_params  jsonb NOT NULL,                 -- 契约 PayParams（原样返回给调用方）
    mode        text NOT NULL CHECK (mode IN ('mock','real')),
    expires_at  timestamptz NOT NULL,           -- 调起参数有效期，到期未支付记失败
    callback_at timestamptz,                    -- 模拟渠道自动回调的时刻；真实渠道与不回调的为空
    state       text NOT NULL CHECK (state IN ('awaiting','called','closed')),
    created_at  timestamptz NOT NULL
);
CREATE INDEX pay_intents_awaiting ON pay_intents (state, expires_at) WHERE state = 'awaiting';

-- +goose Down
DROP TABLE pay_intents;
