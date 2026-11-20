-- +goose Up
-- 准入快照（投影）：用户、车辆、品牌、站点、桩、规则版本、平台参数、降级控制。doc 为投影内容，rev 用于各实例增量刷新内存快照。
CREATE SEQUENCE snap_rev;
CREATE TABLE snap (
  kind text NOT NULL,
  id   text NOT NULL,
  doc  jsonb NOT NULL,
  rev  bigint NOT NULL DEFAULT nextval('snap_rev'),
  PRIMARY KEY (kind, id)
);
CREATE INDEX snap_rev_idx ON snap (rev);

-- 骑行订单。doc 为订单全部字段；列只放查询与唯一约束要用的。
CREATE TABLE orders (
  order_id    text PRIMARY KEY,
  user_id     text NOT NULL,
  vehicle_id  text NOT NULL,
  plate_no    text NOT NULL,
  brand_id    text NOT NULL,
  operator_id text NOT NULL,
  status      text NOT NULL,
  request_at  timestamptz NOT NULL,
  start_at    timestamptz,
  degraded    boolean NOT NULL DEFAULT false,
  fee_settled boolean NOT NULL DEFAULT false,
  rev         integer NOT NULL,
  doc         jsonb NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now()
);
-- 一车一单、一人一单（BR-04、NFR-08）
CREATE UNIQUE INDEX orders_vehicle_active ON orders (vehicle_id) WHERE status IN ('开锁中', '骑行中');
CREATE UNIQUE INDEX orders_user_active ON orders (user_id) WHERE status IN ('开锁中', '骑行中');
CREATE INDEX orders_user_req ON orders (user_id, request_at DESC);
CREATE INDEX orders_plate ON orders (plate_no);
CREATE INDEX orders_status ON orders (status);
CREATE INDEX orders_req ON orders (request_at DESC);

-- 订单号日序号
CREATE TABLE order_day_seq (
  day text PRIMARY KEY,
  seq integer NOT NULL
);

-- 运营商骑行事件（opgw 送达），按运营商事件号去重；未匹配的保留待匹配
CREATE TABLE ride_reports (
  operator_event_id text PRIMARY KEY,
  order_id          text,
  plate_no          text NOT NULL,
  brand_id          text NOT NULL,
  kind              text NOT NULL,
  body              jsonb NOT NULL,
  received_at       timestamptz NOT NULL,
  matched_order_id  text,
  applied           boolean NOT NULL DEFAULT false,
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ride_reports_unapplied ON ride_reports (created_at) WHERE NOT applied;

-- 发件表
CREATE TABLE outbox (
  id           bigserial PRIMARY KEY,
  event_id     text NOT NULL UNIQUE,
  subject      text NOT NULL,
  body         jsonb NOT NULL,
  not_before   timestamptz NOT NULL DEFAULT now(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX outbox_pending ON outbox (id) WHERE published_at IS NULL;

-- 收件去重
CREATE TABLE inbox (
  event_id     text PRIMARY KEY,
  type         text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now()
);

-- 幂等键
CREATE TABLE idem (
  scope      text NOT NULL,
  key        text NOT NULL,
  done       boolean NOT NULL DEFAULT false,
  status     integer NOT NULL DEFAULT 0,
  body       bytea,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, key)
);

-- +goose Down
DROP TABLE idem;
DROP TABLE inbox;
DROP TABLE outbox;
DROP TABLE ride_reports;
DROP TABLE order_day_seq;
DROP TABLE orders;
DROP TABLE snap;
DROP SEQUENCE snap_rev;
