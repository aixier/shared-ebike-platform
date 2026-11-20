-- +goose Up
-- 电量、位置、最近上报不属于车辆清单，本服务不再保存。
ALTER TABLE vehicles DROP COLUMN battery, DROP COLUMN lng, DROP COLUMN lat, DROP COLUMN last_report_at;

-- +goose Down
ALTER TABLE vehicles ADD COLUMN battery integer, ADD COLUMN lng double precision, ADD COLUMN lat double precision, ADD COLUMN last_report_at timestamptz;
