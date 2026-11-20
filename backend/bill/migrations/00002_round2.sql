-- +goose Up
-- 一键支付两步化：未了结的调起参数与时刻，重复调用返回同一份
ALTER TABLE bills ADD COLUMN pending_params jsonb;
ALTER TABLE bills ADD COLUMN pending_at timestamptz;

-- 月卡购买款待支付时的调起参数
ALTER TABLE month_cards ADD COLUMN pay_params jsonb;

-- 用户注册（来自 acct.user.registered）：注册券、渠道券是否已发
ALTER TABLE user_profiles ADD COLUMN registered_at timestamptz;
ALTER TABLE user_profiles ADD COLUMN reg_granted_at timestamptz;

-- 订单开锁状态（来自 ride.order.unlocked / unlock_failed / pending_manual）
CREATE TABLE order_unlock_states (
    order_id   text PRIMARY KEY,
    state      text NOT NULL, -- 已开锁｜开锁失败｜待人工处理
    reason     text,
    at         timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 已退出的运营商（来自 fleet.operator.exited）
CREATE TABLE exited_operators (
    operator_id    text PRIMARY KEY,
    exited_at      timestamptz NOT NULL,
    readonly_until timestamptz
);

-- 一次性批量发券（系统券、定时券）：每个模板只发一批
CREATE TABLE coupon_batches (
    template_id text PRIMARY KEY,
    kind        text NOT NULL,
    issued      int NOT NULL,
    done_at     timestamptz NOT NULL
);

-- 发券去重：同一用户同一模板的"注册类"发放只发一次
CREATE TABLE coupon_grants (
    user_id     text NOT NULL,
    template_id text NOT NULL,
    kind        text NOT NULL,
    at          timestamptz NOT NULL,
    PRIMARY KEY (user_id, template_id, kind)
);

-- +goose Down
ALTER TABLE user_profiles DROP COLUMN reg_granted_at, DROP COLUMN registered_at;
DROP TABLE coupon_grants, coupon_batches, exited_operators, order_unlock_states;
ALTER TABLE month_cards DROP COLUMN pay_params;
ALTER TABLE bills DROP COLUMN pending_at, DROP COLUMN pending_params;
