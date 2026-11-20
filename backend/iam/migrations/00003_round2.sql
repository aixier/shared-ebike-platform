-- +goose Up
-- 第二轮：账号状态加"只读（演示账号）"，与"只读（运营商已退出）"区分
ALTER TABLE iam_accounts DROP CONSTRAINT iam_accounts_status_check;
ALTER TABLE iam_accounts ADD CONSTRAINT iam_accounts_status_check CHECK (status IN ('正常', '停用', '只读（运营商已退出）', '只读（演示账号）'));
UPDATE iam_accounts SET status='只读（演示账号）' WHERE created_by='dev-seed' AND status='只读（运营商已退出）';

-- +goose Down
UPDATE iam_accounts SET status='只读（运营商已退出）' WHERE status='只读（演示账号）';
ALTER TABLE iam_accounts DROP CONSTRAINT iam_accounts_status_check;
ALTER TABLE iam_accounts ADD CONSTRAINT iam_accounts_status_check CHECK (status IN ('正常', '停用', '只读（运营商已退出）'));
