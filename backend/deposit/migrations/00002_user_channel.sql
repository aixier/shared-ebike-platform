-- +goose Up
-- 注册渠道（acct.user.registered）：开通免押缺省的信用服务与押金收款渠道按它取。
ALTER TABLE proj_users ADD COLUMN channel text;

-- +goose Down
ALTER TABLE proj_users DROP COLUMN channel;
