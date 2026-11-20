-- +goose Up
-- 品牌可单独指定车联网服务商；为空时沿用所属运营商的代接服务商
ALTER TABLE brands ADD COLUMN provider_id text REFERENCES providers (provider_id);

-- +goose Down
ALTER TABLE brands DROP COLUMN provider_id;
