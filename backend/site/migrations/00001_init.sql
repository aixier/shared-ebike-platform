-- +goose Up
-- 站点与电子围栏
CREATE SEQUENCE station_seq START 1;
CREATE TABLE stations (
    station_id  text PRIMARY KEY,
    name        text NOT NULL,
    district    text NOT NULL,
    type        text NOT NULL CHECK (type IN ('有桩', '无桩')),
    center      jsonb NOT NULL,
    fence       jsonb NOT NULL,
    capacity    integer NOT NULL CHECK (capacity >= 1),
    status      text NOT NULL DEFAULT '正常' CHECK (status IN ('正常', '已停用')),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    updated_by  jsonb,
    removed_at  timestamptz,
    removed_by  jsonb
);

-- 桩：一个桩位同一时刻最多一辆车（occupied_vehicle_id 在全表唯一，一辆车只在一个桩位）
CREATE SEQUENCE pile_seq START 1;
CREATE TABLE piles (
    pile_id             text PRIMARY KEY,
    station_id          text NOT NULL REFERENCES stations(station_id),
    no                  integer NOT NULL,
    location            jsonb NOT NULL,
    status              text NOT NULL CHECK (status IN ('在线', '离线', '故障')),
    manual_fault        boolean NOT NULL DEFAULT false, -- 人工标为故障：不因心跳自动恢复
    occupied            boolean NOT NULL DEFAULT false, -- 桩报告有车在位（含识别不出的车）
    occupied_tag_type   text,
    occupied_tag        text,
    occupied_vehicle_id text,
    occupied_at         timestamptz,
    last_heartbeat_at   timestamptz,
    installed_at        timestamptz NOT NULL,
    fault_note          text,
    UNIQUE (station_id, no)
);
CREATE UNIQUE INDEX piles_vehicle_uq ON piles (occupied_vehicle_id) WHERE occupied_vehicle_id IS NOT NULL;

-- 桩消息去重：deviceId + msgId（旧 HTTP 入口用 pileId + seq）
CREATE TABLE pile_msgs (
    device_id   text NOT NULL,
    msg_id      text NOT NULL,
    kind        text NOT NULL,
    received_at timestamptz NOT NULL,
    PRIMARY KEY (device_id, msg_id)
);

-- 识别不出的车辆标识：只记录，不发事件
CREATE TABLE pile_unrecognized (
    id          bigserial PRIMARY KEY,
    pile_id     text NOT NULL,
    device_id   text NOT NULL,
    tag_type    text NOT NULL,
    tag         text NOT NULL,
    present     boolean NOT NULL,
    at          timestamptz NOT NULL,
    received_at timestamptz NOT NULL
);

-- 投影：识别方式 + 车辆标识 → 车辆编号（来自 fleet.vehicle.listed / removed）
CREATE TABLE vehicle_tags (
    vehicle_id  text PRIMARY KEY,
    plate_no    text,
    tag_type    text NOT NULL,
    tag         text NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX vehicle_tags_tag_uq ON vehicle_tags (tag_type, tag);

-- 投影：订单（来自 ride.order.unlocked / ended），用于站点内进行中骑行数
CREATE TABLE ride_orders (
    order_id         text PRIMARY KEY,
    vehicle_id       text,
    start_station_id text,
    end_station_id   text,
    status           text NOT NULL CHECK (status IN ('骑行中', '已结束'))
);
CREATE INDEX ride_orders_riding ON ride_orders (start_station_id) WHERE status = '骑行中';

-- 投影：在点还车后停在站点内的车辆（开锁即离开）
CREATE TABLE parked_vehicles (
    vehicle_id  text PRIMARY KEY,
    station_id  text NOT NULL,
    since       timestamptz NOT NULL
);
CREATE INDEX parked_vehicles_station ON parked_vehicles (station_id);

-- 发件表
CREATE TABLE outbox (
    seq          bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_pending ON outbox (seq) WHERE published_at IS NULL;

-- 收件去重
CREATE TABLE inbox (
    event_id     text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- 幂等键
CREATE TABLE idempotency (
    scope       text NOT NULL,
    key         text NOT NULL,
    status      integer NOT NULL,
    body        bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- +goose Down
DROP TABLE idempotency, inbox, outbox, parked_vehicles, ride_orders, vehicle_tags, pile_unrecognized, pile_msgs, piles, stations;
DROP SEQUENCE pile_seq, station_seq;
