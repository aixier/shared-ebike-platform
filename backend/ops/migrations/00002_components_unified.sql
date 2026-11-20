-- +goose Up
-- 故障组件统一取值：旧页面名折算为 支付通道｜授权，去重并保持顺序
UPDATE degrade_events SET components = COALESCE((
    SELECT array_agg(c ORDER BY first_pos) FROM (
        SELECT c, min(pos) AS first_pos FROM (
            SELECT CASE x WHEN '微信支付收款' THEN '支付通道' WHEN '支付宝收款' THEN '支付通道'
                          WHEN '微信授权' THEN '授权' WHEN '支付宝授权' THEN '授权' ELSE x END AS c, pos
            FROM unnest(components) WITH ORDINALITY AS t(x, pos)) m
        GROUP BY c) g), components)
WHERE components && ARRAY['微信支付收款','支付宝收款','微信授权','支付宝授权'];

-- +goose Down
SELECT 1;
