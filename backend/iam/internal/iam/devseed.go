package iam

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// 本机开发开关 IAM_DEV_SEED_ADMIN=1：种入（或校正）一个只读演示账号 admin，能看全部管理端页面、不能写。
// 仅用于本机给业主看效果，不属于契约，生产配置不得设置此开关。
// 开关关闭时，已种入的 admin（created_by = devSeedMarker）会被置为停用。
const (
	devAdminUsername = "admin"
	devAdminPassword = "Shifan@2026"
	devSeedMarker    = "dev-seed"
	devAdminRole     = "主管部门管理员" // 管理端岗位；权限与菜单以本文件为准，不取岗位模板
)

// devAdminPerms 是管理端所有"查看"类权限点。
// 个别页面的查询接口只认带管理含义的权限点（对账、结算管理、异议处理、退款失败、生产凭证、验收待办、账号管理），
// 为了能看这些页面而必须带上；写操作靠令牌 readonly=true 一律被各服务拒绝（READONLY）。
// 不含：pricing.edit、station.edit、pile.edit、refund.confirm*、deposit.assist、account.cancel.assist、account.unfreeze、
// order.forceEnd、degrade.control、degrade.closure.handle、access.pause、access.setExit、settlement.adjust、
// evidence.export、platformParams.edit、各 *.own（运营商自己范围，管理员无数据范围）。
var devAdminPerms = []string{
	"pricing.view", "access.view", "metrics.unlockRate.view", "evidence.view", "audit.view",
	"order.view", "user.view", "arrears.view", "deposit.view", "ops.monitor", "degrade.closure.view",
	"integration.doc",
	"settlement.manage", "dispute.decide", "recon.manage", "refund.manualTransfer",
	"credential.manage", "acceptance.confirm", "account.manage",
}

// SeedDevAdmin 按开关种入或停用演示账号。幂等：重复启动只校正权限、菜单与密码。
func (s *Service) SeedDevAdmin(ctx context.Context, enabled bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id string
	var createdBy *string
	err = tx.QueryRow(ctx, `SELECT account_id, created_by FROM iam_accounts WHERE username=$1 FOR UPDATE`, devAdminUsername).Scan(&id, &createdBy)
	exists := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if exists && (createdBy == nil || *createdBy != devSeedMarker) {
		return fmt.Errorf("已存在不是开发种子创建的账号 %s，不覆盖", devAdminUsername)
	}
	if !enabled {
		if exists {
			if _, err := tx.Exec(ctx, `UPDATE iam_accounts SET status=$2 WHERE account_id=$1`, id, statusDisabled); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE iam_sessions SET revoked_at=$2 WHERE account_id=$1 AND revoked_at IS NULL`, id, s.now()); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return nil
	}

	// 菜单：全部可登录管理端岗位菜单的并集，保持首次出现的顺序
	var menus []string
	err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(m ORDER BY ord), ARRAY[]::text[]) FROM (
		SELECT m, min(ord) ord FROM (
			SELECT u.m, row_number() OVER () ord FROM iam_role_templates t, unnest(t.menus) u(m)
			WHERE t.client='管理端' AND t.loginable) x GROUP BY m) y`).Scan(&menus)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(devAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if exists {
		_, err = tx.Exec(ctx, `UPDATE iam_accounts SET password_hash=$2, permissions=$3, menus=$4, status=$5 WHERE account_id=$1`,
			id, string(hash), devAdminPerms, menus, statusDemo)
	} else {
		var tpl roleTemplate
		if err = tx.QueryRow(ctx, `SELECT post, id_prefix, default_org FROM iam_role_templates WHERE role=$1`, devAdminRole).
			Scan(&tpl.Post, &tpl.IDPrefix, &tpl.DefaultOrg); err != nil {
			return err
		}
		if id, err = s.nextAccountID(ctx, tx, tpl.IDPrefix); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO iam_accounts(account_id, role, post, name, username, password_hash, org, permissions, menus, status, created_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			id, devAdminRole, tpl.Post, "演示只读管理员", devAdminUsername, string(hash), tpl.DefaultOrg, devAdminPerms, menus, statusDemo, s.now(), devSeedMarker)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
