-- +goose Up

-- ========== 投影（来自 fleet 事件） ==========
CREATE TABLE operators (
    operator_id  text PRIMARY KEY,
    name         text NOT NULL DEFAULT '',
    short_name   text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT '在营',
    exited_at    timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE brands (
    brand_id      text PRIMARY KEY,
    operator_id   text NOT NULL DEFAULT '',
    name          text NOT NULL DEFAULT '',
    access_status text NOT NULL DEFAULT '未接入',
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE vehicles (
    vehicle_id      text PRIMARY KEY,
    gov_code        text NOT NULL,
    plate_no        text NOT NULL DEFAULT '',
    model           text,
    brand_id        text NOT NULL DEFAULT '',
    operator_id     text NOT NULL DEFAULT '',
    code_status     text NOT NULL,
    code_issued_at  timestamptz,
    code_enable_at  timestamptz,
    code_invalid_at timestamptz,
    removed         boolean NOT NULL DEFAULT false,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vehicles_gov_code ON vehicles (gov_code);

-- ========== 事件收发 ==========
CREATE TABLE processed_events (
    event_id     text PRIMARY KEY,
    type         text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- ========== 幂等与发号 ==========
CREATE TABLE idempotency (
    scope        text NOT NULL,
    key          text NOT NULL,
    status       integer NOT NULL,
    body         bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

CREATE TABLE id_seq (
    kind text NOT NULL,
    day  text NOT NULL,
    n    integer NOT NULL,
    PRIMARY KEY (kind, day)
);

-- ========== 业务 ==========
CREATE TABLE scan_records (
    scan_id     bigserial PRIMARY KEY,
    officer_id  text NOT NULL,
    raw_code    text NOT NULL,
    gov_code    text,
    scan_result text NOT NULL,
    vehicle_id  text,
    scanned_at  timestamptz NOT NULL
);

CREATE TABLE photos (
    photo_id     text PRIMARY KEY,
    officer_id   text NOT NULL,
    sha256       text NOT NULL,
    size         bigint NOT NULL,
    content_type text NOT NULL,
    file_name    text NOT NULL,
    uploaded_at  timestamptz NOT NULL
);

CREATE TABLE evidence (
    evidence_id         text PRIMARY KEY,
    officer_id          text NOT NULL,
    officer_name        text NOT NULL DEFAULT '',
    badge_no            text NOT NULL DEFAULT '',
    captured_at         timestamptz NOT NULL,
    captured_at_text    text NOT NULL,
    submitted_at        timestamptz NOT NULL,
    lng                 double precision NOT NULL,
    lat                 double precision NOT NULL,
    address             text NOT NULL DEFAULT '',
    accuracy_m          double precision,
    scan_result         text NOT NULL,
    gov_code            text,
    vehicle_id          text,
    plate_no            text,
    brand_id            text,
    operator_id         text,
    code_status         text,
    vehicle_description text NOT NULL,
    note                text,
    sha256              text NOT NULL
);
CREATE INDEX evidence_officer ON evidence (officer_id, captured_at DESC);
CREATE INDEX evidence_captured ON evidence (captured_at DESC);

CREATE TABLE evidence_photos (
    evidence_id text NOT NULL REFERENCES evidence (evidence_id) ON DELETE RESTRICT,
    seq         integer NOT NULL,
    photo_id    text NOT NULL UNIQUE REFERENCES photos (photo_id) ON DELETE RESTRICT,
    PRIMARY KEY (evidence_id, seq)
);

CREATE TABLE evidence_exports (
    export_id    text PRIMARY KEY,
    at           timestamptz NOT NULL,
    by_id        text NOT NULL,
    by_name      text NOT NULL DEFAULT '',
    filter       jsonb NOT NULL,
    evidence_ids text[] NOT NULL,
    count        integer NOT NULL,
    file_name    text NOT NULL,
    reason       text
);

-- ========== 防改防删（数据库层面，不只靠接口拒绝） ==========
-- +goose StatementBegin
CREATE FUNCTION enforce_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% 只能追加，不能修改或删除', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION enforce_photo_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'photos 不能修改' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF EXISTS (SELECT 1 FROM evidence_photos WHERE photo_id = OLD.photo_id) THEN
        RAISE EXCEPTION '照片 % 已被证据引用，不能删除', OLD.photo_id USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER evidence_append_only BEFORE UPDATE OR DELETE ON evidence
    FOR EACH ROW EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER evidence_no_truncate BEFORE TRUNCATE ON evidence
    FOR EACH STATEMENT EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER evidence_photos_append_only BEFORE UPDATE OR DELETE ON evidence_photos
    FOR EACH ROW EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER evidence_photos_no_truncate BEFORE TRUNCATE ON evidence_photos
    FOR EACH STATEMENT EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER evidence_exports_append_only BEFORE UPDATE OR DELETE ON evidence_exports
    FOR EACH ROW EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER evidence_exports_no_truncate BEFORE TRUNCATE ON evidence_exports
    FOR EACH STATEMENT EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER scan_records_append_only BEFORE UPDATE OR DELETE ON scan_records
    FOR EACH ROW EXECUTE FUNCTION enforce_append_only();
CREATE TRIGGER photos_guard BEFORE UPDATE OR DELETE ON photos
    FOR EACH ROW EXECUTE FUNCTION enforce_photo_guard();
CREATE TRIGGER photos_no_truncate BEFORE TRUNCATE ON photos
    FOR EACH STATEMENT EXECUTE FUNCTION enforce_append_only();

-- 其他账号不得改删（部署若另设只读或运维账号，也不会被授予改删权）
REVOKE UPDATE, DELETE, TRUNCATE ON evidence, evidence_photos, evidence_exports, scan_records, photos FROM PUBLIC;

-- +goose Down
DROP TABLE IF EXISTS evidence_exports, evidence_photos, evidence, photos, scan_records, id_seq, idempotency, outbox, processed_events, vehicles, brands, operators;
DROP FUNCTION IF EXISTS enforce_append_only();
DROP FUNCTION IF EXISTS enforce_photo_guard();
