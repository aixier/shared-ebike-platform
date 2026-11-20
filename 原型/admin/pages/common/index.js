/* 通用页面：所有岗位都有「我的账号」；有相应权限的账号可见「操作留痕」「账号与授权」 */
import audit from './audit.js';
import me from './me.js';
import accounts from './accounts.js';

export default [
  { path: '#/common/audit', title: '操作留痕', menu: '操作留痕', perm: 'audit.view', sub: '谁、何时、对什么做了什么，配置、退款、结算、验收、降级与解冻都在这里', mount: audit.mount },
  { path: '#/common/accounts', title: '账号与授权', menu: '账号与授权', perm: 'account.manage', sub: '开通账号、查看授权的岗位与运营商范围、停用与启用', mount: accounts.mount },
  { path: '#/common/me', title: '我的账号', menu: '我的账号', mount: me.mount },
];
