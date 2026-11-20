// 本机联调用的运营商模拟器（只在 127.0.0.1 监听）。平台 opgw 把开锁 / 结束骑行请求发到这里（M-01、M-02），
// 本进程答复"已受理"，稍后按 M-03 签名后向网关 /open/v1/ride-events 报告开锁结果与锁车。
// 凭证（appId -> 密钥）读 secrets/extra/mock-operator-creds.json，由 seed.sh 写入。
// 控制接口（只在本机）：
//   POST /mock/scenario   {"plateNo":"QH000005","unlock":"失败","failReason":"电量不足"}  指定某车下一次开锁结果；unlock 取 成功|失败|无响应
//   POST /mock/park       {"plateNo":"QH000004","location":[104.0657,30.6595],"inStation":true}  指定某车下次锁车的位置与车端在点判定
//   GET  /mock/state      看当前记录
import http from 'node:http';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const PORT = Number(process.env.MOCK_OP_PORT || 18390);
const HOST = '127.0.0.1';
const GATEWAY = process.env.MOCK_OP_GATEWAY || 'http://127.0.0.1:18300';
const CREDS_FILE = process.env.MOCK_OP_CREDS || path.join(HERE, 'secrets', 'extra', 'mock-operator-creds.json');
const UNLOCK_DELAY_MS = Number(process.env.MOCK_OP_UNLOCK_DELAY_MS || 900);
const LOCK_DELAY_MS = Number(process.env.MOCK_OP_LOCK_DELAY_MS || 600);
const DEFAULT_STATION = [104.066, 30.596];
// 没指定位置时，在站点中心附近按车号做固定的小偏移（约 ±25 米，仍在示例围栏内），地图上不会叠成一点
const defaultLoc = plate => { let h = 0; for (const ch of String(plate)) h = (h * 31 + ch.charCodeAt(0)) >>> 0; return [+(DEFAULT_STATION[0] + ((h % 7) - 3) * 0.00008).toFixed(6), +(DEFAULT_STATION[1] + (((h >> 3) % 7) - 3) * 0.00008).toFixed(6)]; };

const state = { rides: {}, scenario: {}, park: {}, log: [] };
const note = (...a) => { const s = `${new Date().toISOString()} ${a.join(' ')}`; state.log.push(s); if (state.log.length > 200) state.log.shift(); console.log(s); };

function creds() {
  try { return JSON.parse(fs.readFileSync(CREDS_FILE, 'utf8')); } catch { return {}; }
}
const sha = b => crypto.createHash('sha256').update(b).digest('hex');
function sign(method, p, appId, ts, nonce, body, secret) {
  const s = [method, p, appId, ts, nonce, sha(body)].join('\n');
  return crypto.createHmac('sha256', secret).update(s).digest('hex');
}
const rfc3339 = (d = new Date()) => {
  const t = new Date(d.getTime() + 8 * 3600e3).toISOString().slice(0, 19);
  return `${t}+08:00`;
};

async function report(appId, event) {
  const secret = creds()[appId];
  if (!secret) { note('没有应用', appId, '的密钥，无法报告', event.type, event.orderId); return; }
  const body = JSON.stringify(event);
  const ts = String(Math.floor(Date.now() / 1000));
  const nonce = crypto.randomBytes(12).toString('hex');
  const p = '/open/v1/ride-events';
  try {
    const r = await fetch(GATEWAY + p, {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'x-app-id': appId, 'x-timestamp': ts, 'x-nonce': nonce, 'x-signature': sign('POST', p, appId, ts, nonce, body, secret) },
      body,
    });
    note('报告', event.type, event.orderId, '->', r.status, (await r.text()).slice(0, 120));
  } catch (e) { note('报告失败', e.message); }
}

const readBody = req => new Promise(res => { const c = []; req.on('data', d => c.push(d)); req.on('end', () => res(Buffer.concat(c).toString('utf8'))); });
const send = (res, code, obj) => { res.writeHead(code, { 'content-type': 'application/json; charset=utf-8' }); res.end(JSON.stringify(obj)); };

http.createServer(async (req, res) => {
  const raw = await readBody(req);
  let body = {}; try { body = raw ? JSON.parse(raw) : {}; } catch { /* 忽略 */ }
  const appId = req.headers['x-app-id'] || '';
  if (req.method === 'GET' && req.url === '/mock/state') return send(res, 200, state);
  if (req.method === 'POST' && req.url === '/mock/scenario') { state.scenario[body.plateNo] = body; return send(res, 200, { ok: true }); }
  if (req.method === 'POST' && req.url === '/mock/park') { state.park[body.plateNo] = body; return send(res, 200, { ok: true }); }

  if (req.method === 'POST' && req.url === '/v1/unlock') {
    const { orderId, plateNo } = body;
    const sc = state.scenario[plateNo]; delete state.scenario[plateNo];
    note('开锁请求', orderId, plateNo, sc ? `（场景 ${sc.unlock}）` : '');
    if (sc && sc.unlock === '无响应') return send(res, 200, { result: '已受理' }); // 受理后不报告，平台按时限判失败
    send(res, 200, { result: '已受理' });
    const failed = sc && sc.unlock === '失败';
    state.rides[orderId] = { plateNo, appId, unlockAt: Date.now() };
    setTimeout(() => report(appId, {
      eventId: 'MOCK-U-' + orderId, type: '开锁结果', orderId, plateNo, occurredAt: rfc3339(),
      unlock: failed ? { result: '失败', failReason: sc.failReason || '车辆故障' } : { result: '成功', location: state.park[plateNo]?.location || defaultLoc(plateNo) },
    }), UNLOCK_DELAY_MS);
    return;
  }
  if (req.method === 'POST' && req.url === '/v1/end-ride') {
    const { orderId, plateNo } = body;
    note('结束骑行请求', orderId, plateNo, body.cause || '');
    send(res, 200, { result: '已受理' });
    const r = state.rides[orderId] || { appId, unlockAt: Date.now() - 600e3 };
    const pk = state.park[plateNo] || {};
    const durationSec = Math.max(1, Math.round((Date.now() - r.unlockAt) / 1000));
    setTimeout(() => report(r.appId || appId, {
      eventId: 'MOCK-L-' + orderId, type: '锁车', orderId, plateNo, occurredAt: rfc3339(),
      lock: { location: pk.location || defaultLoc(plateNo), inStation: pk.inStation !== false, durationSec, distanceKm: Math.round(durationSec * 0.004 * 100) / 100 },
    }), LOCK_DELAY_MS);
    return;
  }
  send(res, 404, { code: 'NOT_FOUND', message: '没有这个接口' });
}).listen(PORT, HOST, () => note(`mock-operator 已启动 ${HOST}:${PORT}`));
