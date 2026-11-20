// 执法人员手机页：扫码核查、拍照取证、我的取证记录（R-44、R-45）。看不到用户、订单与资金。
import { ApiError } from './errors.js';
import { ms, paginate, clone, dist } from './util.js';
import * as R from './rules.js';
import { evidenceHash } from './admin.js';

export function createEnforcementApi(ctx, D, V) {
  const db = () => ctx.db;
  const officer = () => {
    const s = ctx.session.get('Officer');
    const a = s && D.account(s.accountId);
    if (!a) throw new ApiError('ADMIN_NOT_LOGGED_IN', '请先登录执法账号');
    if (a.status !== '正常') throw new ApiError('ACCOUNT_DISABLED');
    ctx._actor = a.accountId;
    return a;
  };
  const call = (name, fn, opts) => ctx.call('enforcement.' + name, fn, opts);
  const view = e => ({ ...clone(e), brandName: e.brandId ? D.brand(e.brandId).name : null, operatorName: e.operatorId ? D.operator(e.operatorId).shortName : null, plateNo: e.vehicleId ? (D.vehicle(e.vehicleId) || {}).plateNo : null });

  return {
    login: (username, password) => call('login', () => {
      const a = db().accounts.accounts.find(x => x.username === String(username || '').trim() && x.role === '执法人员');
      if (!a || a.password !== password) throw new ApiError('LOGIN_FAILED');
      if (a.status !== '正常') throw new ApiError('ACCOUNT_DISABLED');
      a.lastLoginAt = ctx.now(); ctx.dirty('accounts');
      ctx.session.set('Officer', { accountId: a.accountId });
      return { accountId: a.accountId, name: a.name, username: a.username, org: a.org, badgeNo: a.username.replace(/^zf/, '') };
    }),
    logout: () => call('logout', () => { ctx.session.set('Officer', null); return { ok: true }; }),
    me: () => call('me', () => { const a = officer(); return { accountId: a.accountId, name: a.name, username: a.username, org: a.org, badgeNo: a.username.replace(/^zf/, ''), evidenceCount: db().evidence.evidence.filter(e => e.officerId === a.accountId).length }; }, { latency: [80, 200] }),
    /**
     * 扫码核查：是否政府码、所属运营商、接入状态。
     * 返回 scanResult：有效政府码 | 码未启用 | 码已失效 | 无政府码（无政府码时 needCapture=true，直接进入取证）
     */
    scan: code => call('scan', () => {
      officer();
      const m = /SF\d{8}/i.exec(String(code || ''));
      const v = m ? ctx.find(db().vehicles, 'govCode', m[0].toUpperCase()) : null;
      if (!v) return { isGovCode: false, scanResult: '无政府码', needCapture: true, text: '未识别到政府码，请拍照取证', vehicle: null };
      const b = D.brand(v.brandId), op = D.operator(v.operatorId);
      const scanResult = v.codeStatus === '已启用' ? '有效政府码' : v.codeStatus === '待启用' ? '码未启用' : '码已失效';
      const text = scanResult === '有效政府码' ? `政府码有效，属于${op.shortName}（${b.name}），接入状态：${b.accessStatus}`
        : scanResult === '码未启用' ? `政府码尚未启用，属于${op.shortName}（${b.name}），接入状态：${b.accessStatus}`
          : `政府码已失效，${op.shortName}${op.status === '已退出' ? '已退出' : '已将该车移出清单'}，可拍照留证`;
      return { isGovCode: true, scanResult, needCapture: scanResult !== '有效政府码', text, vehicle: { vehicleId: v.vehicleId, govCode: v.govCode, plateNo: v.plateNo, model: v.model, codeStatus: v.codeStatus, codeIssuedAt: v.codeIssuedAt, codeEnableAt: v.codeEnableAt, codeInvalidAt: v.codeInvalidAt, brandId: b.brandId, brandName: b.name, operatorId: op.operatorId, operatorName: op.name, operatorShortName: op.shortName, operatorStatus: op.status, accessStatus: b.accessStatus } };
    }),
    /** 定位（原型模拟手机定位：在本市范围内给出位置、地址与精度） */
    locate: () => call('locate', () => {
      officer();
      const [[w, s], [e, n]] = db().stations.region.bounds;
      const loc = [+(w + ctx.rnd() * (e - w)).toFixed(6), +(s + ctx.rnd() * (n - s)).toFixed(6)];
      const near = R.nearestStation(db().stations.stations, loc);
      return { location: loc, accuracyM: ctx.randInt(4, 15), address: `${db().stations.region.city}${near.station.district}${near.station.name}附近` };
    }, { latency: [300, 700] }),
    /**
     * 提交取证（提交后不可修改）。payload: {scanResult, govCode?, location, address, accuracyM, vehicleDescription, brandId?, photos[], note?}
     * photos 为图片地址或 data URL，至少 1 张
     */
    capture: (p = {}) => call('capture', async () => {
      const a = officer();
      if (!['无政府码', '码已失效', '码未启用'].includes(p.scanResult)) throw new ApiError('INVALID', '核查结果只能是无政府码、码已失效或码未启用');
      if (!Array.isArray(p.photos) || !p.photos.length) throw new ApiError('INVALID', '请至少拍 1 张照片');
      if (!Array.isArray(p.location) || p.location.length !== 2) throw new ApiError('INVALID', '没有取得位置，请打开定位后重试');
      if (!String(p.vehicleDescription || '').trim()) throw new ApiError('INVALID', '请描述车辆（品牌字样、车身编号、颜色等）');
      const v = p.govCode ? ctx.find(db().vehicles, 'govCode', String(p.govCode).toUpperCase()) : null;
      const t = ctx.now();
      const e = {
        evidenceId: ctx.ids.evidence(t), officerId: a.accountId, badgeNo: a.username.replace(/^zf/, ''), capturedAt: p.capturedAt || t, location: p.location.map(x => +(+x).toFixed(6)),
        address: p.address || '', accuracyM: p.accuracyM ?? null, scanResult: p.scanResult, vehicleId: v ? v.vehicleId : null, brandId: v ? v.brandId : (p.brandId || null),
        operatorId: v ? v.operatorId : (p.brandId && D.brand(p.brandId) ? D.brand(p.brandId).operatorId : null), codeStatus: v ? v.codeStatus : null,
        vehicleDescription: String(p.vehicleDescription).trim(), photos: [...p.photos], note: p.note || null, sha256: null, submittedAt: t, locked: true,
      };
      e.sha256 = await evidenceHash(e, ctx.label);
      db().evidence.evidence.push(e);
      ctx.dirty('evidence');
      ctx.emit('evidence.created', { evidenceId: e.evidenceId, officerId: a.accountId });
      return view(e);
    }),
    /** 我的取证记录（只看本人） */
    mine: (f = {}) => call('mine', () => {
      const a = officer();
      const list = db().evidence.evidence.filter(e => e.officerId === a.accountId).sort((x, y) => ms(y.capturedAt) - ms(x.capturedAt)).map(view);
      return paginate(list, f);
    }),
    get: id => call('get', () => {
      const a = officer();
      const e = ctx.get(db().evidence.evidence, 'evidenceId', id, '取证记录');
      if (e.officerId !== a.accountId) throw new ApiError('FORBIDDEN', '只能查看本人提交的取证记录');
      return view(e);
    }),
    /** 修改已提交的证据：一律拒绝（R-45） */
    update: id => call('update', () => { officer(); ctx.get(db().evidence.evidence, 'evidenceId', id, '取证记录'); throw new ApiError('EVIDENCE_LOCKED'); }),
    remove: id => call('remove', () => { officer(); ctx.get(db().evidence.evidence, 'evidenceId', id, '取证记录'); throw new ApiError('EVIDENCE_LOCKED'); }),
  };
}
