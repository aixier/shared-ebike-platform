-- +goose Up
-- 车联网服务商（代接关系）
CREATE TABLE providers (
    provider_id text PRIMARY KEY,
    name        text NOT NULL,
    contact     jsonb
);

-- 运营商
CREATE TABLE operators (
    operator_id        text PRIMARY KEY,
    name               text NOT NULL,
    short_name         text NOT NULL,
    type               text NOT NULL CHECK (type IN ('运营商', '政府自有车队')),
    access_via         text NOT NULL DEFAULT '自行接入' CHECK (access_via IN ('自行接入', '车联网服务商代接')),
    provider_id        text REFERENCES providers (provider_id),
    credit_code        text NOT NULL DEFAULT '',
    joined_at          timestamptz,
    status             text NOT NULL DEFAULT '在营' CHECK (status IN ('在营', '已退出')),
    exited_at          timestamptz,
    contacts           jsonb NOT NULL DEFAULT '[]',
    settlement_account jsonb
);

-- 品牌；接入状态全平台只有这一份
CREATE TABLE brands (
    brand_id      text PRIMARY KEY,
    name          text NOT NULL,
    display_name  text NOT NULL DEFAULT '',
    color         text NOT NULL DEFAULT '',
    vehicle_type  text NOT NULL DEFAULT '电单车',
    operator_id   text NOT NULL REFERENCES operators (operator_id),
    access_status text NOT NULL CHECK (access_status IN ('未接入', '联调中', '已接入', '暂停可租', '已退出')),
    status_since  timestamptz,
    plate_prefix  text
);
CREATE INDEX brands_operator ON brands (operator_id);

-- 接入状态历史，只追加
CREATE TABLE brand_status_history (
    id       bigserial PRIMARY KEY,
    brand_id text NOT NULL REFERENCES brands (brand_id),
    status   text NOT NULL,
    at       timestamptz NOT NULL,
    actor    jsonb,
    note     text
);
CREATE INDEX brand_status_history_brand ON brand_status_history (brand_id, id);

-- 车辆与政府码；一车一码，码不复用（车辆记录不删，政府码全表唯一）
CREATE TABLE vehicles (
    vehicle_id      text PRIMARY KEY,
    brand_id        text NOT NULL REFERENCES brands (brand_id),
    operator_id     text NOT NULL REFERENCES operators (operator_id),
    plate_no        text NOT NULL UNIQUE,
    gov_code        text NOT NULL UNIQUE,
    pile_tag_type   text NOT NULL DEFAULT 'RFID' CHECK (pile_tag_type IN ('RFID', '蓝牙', '视觉车牌', '其他')),
    pile_tag        text NOT NULL,
    vehicle_type    text NOT NULL DEFAULT '电单车',
    model           text NOT NULL,
    listed_at       timestamptz NOT NULL,
    code_issued_at  timestamptz NOT NULL,
    code_status     text NOT NULL CHECK (code_status IN ('已启用', '待启用', '已失效')),
    code_enable_at  timestamptz,
    code_invalid_at timestamptz,
    run_status      text NOT NULL DEFAULT '可用' CHECK (run_status IN ('可用', '骑行中', '低电量', '故障', '离线')),
    battery         integer,
    lng             double precision,
    lat             double precision,
    last_report_at  timestamptz,
    removed_at      timestamptz,
    remove_reason   text
);
CREATE INDEX vehicles_operator ON vehicles (operator_id);
CREATE INDEX vehicles_brand ON vehicles (brand_id);
-- 桩识别用车辆标识：同一识别方式内，在用（码未失效）的车不重复
CREATE UNIQUE INDEX vehicles_pile_tag_live ON vehicles (pile_tag_type, pile_tag) WHERE code_status <> '已失效';

-- 暂停可租记录
CREATE TABLE pauses (
    pause_id        text PRIMARY KEY,
    brand_id        text NOT NULL REFERENCES brands (brand_id),
    paused_at       timestamptz NOT NULL,
    resumed_at      timestamptz,
    metric          text NOT NULL CHECK (metric IN ('开锁成功率', '可用率', '人工')),
    value           double precision,
    threshold       double precision,
    sustain_minutes integer,
    by_actor        jsonb,
    resumed_by      jsonb,
    note            text,
    resume_note     text
);
CREATE INDEX pauses_brand ON pauses (brand_id, paused_at);

-- 发号计数器
CREATE TABLE counters (
    name  text PRIMARY KEY,
    value bigint NOT NULL
);

-- 发件表：业务事务里写入，发布任务补发到事件总线
CREATE TABLE outbox (
    seq          bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    type         text NOT NULL,
    key          text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (seq) WHERE published_at IS NULL;

-- 幂等键：同一调用方、同一接口、同一个键只执行一次
CREATE TABLE idempotency (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     integer NOT NULL,
    body       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- +goose Down
DROP TABLE idempotency;
DROP TABLE outbox;
DROP TABLE counters;
DROP TABLE pauses;
DROP TABLE vehicles;
DROP TABLE brand_status_history;
DROP TABLE brands;
DROP TABLE operators;
DROP TABLE providers;
