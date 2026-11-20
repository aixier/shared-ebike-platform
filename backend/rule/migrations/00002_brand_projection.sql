-- +goose Up

-- 品牌投影：来自 fleet.brand.registered，用来校验规则版本里引用的品牌存在。
CREATE TABLE brands (
    brand_id    text PRIMARY KEY,
    operator_id text NOT NULL,
    name        text NOT NULL,
    updated_at  timestamptz NOT NULL
);

-- 已处理的订阅事件，按 eventId 去重。
CREATE TABLE consumed_events (
    event_id    text PRIMARY KEY,
    consumed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE consumed_events;
DROP TABLE brands;
