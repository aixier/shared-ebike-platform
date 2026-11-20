-- +goose Up

-- 规则版本：只追加。内容一经写入不可修改，唯一允许的更新是把尚未生效的版本标为已撤销（BR-15）。
CREATE TABLE pricing_versions (
    version_id    text PRIMARY KEY,
    seq           integer NOT NULL UNIQUE,
    name          text NOT NULL,
    effective_at  timestamptz NOT NULL,
    created_at    timestamptz NOT NULL,
    created_by    jsonb NOT NULL,
    note          text,
    content       jsonb NOT NULL,
    revoked_at    timestamptz,
    revoked_by    jsonb,
    revoke_reason text,
    CONSTRAINT pricing_versions_effective_not_before_created CHECK (effective_at >= created_at)
);

-- +goose StatementBegin
CREATE FUNCTION pricing_versions_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION '规则版本不可删除';
    END IF;
    IF OLD.revoked_at IS NOT NULL THEN
        RAISE EXCEPTION '已撤销的规则版本不可修改';
    END IF;
    IF NEW.version_id <> OLD.version_id OR NEW.seq <> OLD.seq OR NEW.name <> OLD.name
       OR NEW.effective_at <> OLD.effective_at OR NEW.created_at <> OLD.created_at
       OR NEW.created_by <> OLD.created_by OR NEW.note IS DISTINCT FROM OLD.note
       OR NEW.content <> OLD.content THEN
        RAISE EXCEPTION '规则版本内容不可修改';
    END IF;
    IF NEW.revoked_at IS NULL OR NEW.revoked_by IS NULL THEN
        RAISE EXCEPTION '规则版本只允许撤销';
    END IF;
    IF OLD.effective_at <= NEW.revoked_at THEN
        RAISE EXCEPTION '已生效的规则版本不可撤销';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER pricing_versions_guard BEFORE UPDATE OR DELETE ON pricing_versions
    FOR EACH ROW EXECUTE FUNCTION pricing_versions_guard();

-- 平台公司参数现值（单行）。
CREATE TABLE platform_params (
    id         smallint PRIMARY KEY CHECK (id = 1),
    items      jsonb NOT NULL,
    updated_at timestamptz NOT NULL,
    updated_by jsonb NOT NULL
);

-- 平台公司参数历史：只追加。
CREATE TABLE platform_params_history (
    id         bigserial PRIMARY KEY,
    changed_at timestamptz NOT NULL,
    changed_by jsonb NOT NULL,
    reason     text,
    before     jsonb,
    after      jsonb NOT NULL,
    items      jsonb NOT NULL
);

-- +goose StatementBegin
CREATE FUNCTION append_only_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% 只追加，不可修改或删除', TG_TABLE_NAME;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER platform_params_history_guard BEFORE UPDATE OR DELETE ON platform_params_history
    FOR EACH ROW EXECUTE FUNCTION append_only_guard();

-- 幂等键：同一键重复到达返回第一次的结果。
CREATE TABLE idempotency_keys (
    scope      text NOT NULL,
    key        text NOT NULL,
    status     integer NOT NULL,
    body       bytea NOT NULL, -- 原样保存第一次的响应字节
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

-- 发件表：业务事务里写入，由发布循环送到事件总线。
CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL UNIQUE,
    subject      text NOT NULL,
    body         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- +goose Down
DROP TABLE outbox;
DROP TABLE idempotency_keys;
DROP TRIGGER platform_params_history_guard ON platform_params_history;
DROP FUNCTION append_only_guard();
DROP TABLE platform_params_history;
DROP TABLE platform_params;
DROP TRIGGER pricing_versions_guard ON pricing_versions;
DROP FUNCTION pricing_versions_guard();
DROP TABLE pricing_versions;
