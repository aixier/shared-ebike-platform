-- +goose Up
-- 站点状态统一为 启用｜停用
ALTER TABLE stations DROP CONSTRAINT IF EXISTS stations_status_check;
UPDATE stations SET status = '启用' WHERE status = '正常';
UPDATE stations SET status = '停用' WHERE status = '已停用';
ALTER TABLE stations ALTER COLUMN status SET DEFAULT '启用';
ALTER TABLE stations ADD CONSTRAINT stations_status_check CHECK (status IN ('启用', '停用'));

-- 桩位的骑行事件临时判断：车辆已离开（开锁事件先到）、车辆已回来（还车事件先到），桩的报告到达后清除
ALTER TABLE piles
    ADD COLUMN ride_left_order      text,        -- 已离开：开锁订单
    ADD COLUMN ride_left_vehicle_id text,        -- 已离开：桩上记着的那辆车
    ADD COLUMN ride_left_at         timestamptz, -- 已离开：开锁时刻
    ADD COLUMN ride_back_order      text,        -- 已回来：还车订单
    ADD COLUMN ride_back_vehicle_id text,        -- 已回来：还车的车
    ADD COLUMN ride_back_at         timestamptz; -- 已回来：还车时刻

-- 订单投影补开锁时刻（乱序到达时判断先后）
ALTER TABLE ride_orders ADD COLUMN start_at timestamptz, ADD COLUMN end_at timestamptz;

-- 管理端登记的桩控制器；密钥只存摘要
CREATE TABLE controllers (
    device_id     text PRIMARY KEY,
    access_mode   text NOT NULL CHECK (access_mode IN ('MQTT', 'HTTP', '厂商适配')),
    vendor        text,
    station_id    text REFERENCES stations(station_id),
    pile_id       text REFERENCES piles(pile_id),
    secret_sha256 text NOT NULL,
    secret_tail   text NOT NULL,
    status        text NOT NULL DEFAULT '启用' CHECK (status IN ('启用', '停用')),
    created_at    timestamptz NOT NULL,
    created_by    jsonb,
    disabled_at   timestamptz,
    CHECK ((station_id IS NULL) <> (pile_id IS NULL))
);

-- +goose Down
DROP TABLE controllers;
ALTER TABLE ride_orders DROP COLUMN start_at, DROP COLUMN end_at;
ALTER TABLE piles DROP COLUMN ride_left_order, DROP COLUMN ride_left_vehicle_id, DROP COLUMN ride_left_at,
    DROP COLUMN ride_back_order, DROP COLUMN ride_back_vehicle_id, DROP COLUMN ride_back_at;
ALTER TABLE stations DROP CONSTRAINT IF EXISTS stations_status_check;
UPDATE stations SET status = '正常' WHERE status = '启用';
UPDATE stations SET status = '已停用' WHERE status = '停用';
ALTER TABLE stations ALTER COLUMN status SET DEFAULT '正常';
ALTER TABLE stations ADD CONSTRAINT stations_status_check CHECK (status IN ('正常', '已停用'));
