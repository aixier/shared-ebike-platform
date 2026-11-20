// 本机桩模拟器：模拟站级控制器 SIM-CTRL-ST0001（管 ST0001 的 4 个桩位），经网关 /device/v1/ 发标准桩消息。
// 只用 node 的 http，只连 127.0.0.1，不监听任何端口。密钥与 extra.tsv 里 site 的 PILE_DEVICES 一致。
//   node pile-sim.mjs run                 常驻：启动先发 snapshot，之后每 30 秒发 heartbeat（每 5 次心跳再发一次 snapshot）
//   node pile-sim.mjs occupy <桩位号> <tag> [tagType]   有车入位，发 occupancy，并记入状态文件
//   node pile-sim.mjs leave <桩位号>                    离位
//   node pile-sim.mjs snapshot | heartbeat              手动发一条
// 状态文件 run/pile-sim-state.json 记各桩位在位情况，常驻进程每次发 snapshot 时读取。
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const GW = new URL(process.env.BIKE_GATEWAY || 'http://127.0.0.1:18300');
if (GW.hostname !== '127.0.0.1' && GW.hostname !== 'localhost') { console.error('只允许连本机'); process.exit(1); }
const DEVICE_ID = 'SIM-CTRL-ST0001';
const SECRET = 'sim-local-dev-key-0001';
const SLOTS = [1, 2, 3, 4];
const INTERVAL_MS = 30000;
const STATE = path.join(here, 'run', 'pile-sim-state.json');
let seq = 0;
const msgId = () => `${Date.now()}-${++seq}`;

const load = () => { try { return JSON.parse(fs.readFileSync(STATE, 'utf8')); } catch { return {}; } };
const save = s => { fs.mkdirSync(path.dirname(STATE), { recursive: true }); fs.writeFileSync(STATE, JSON.stringify(s, null, 1)); };

function post(p, body) {
  return new Promise(resolve => {
    const data = Buffer.from(JSON.stringify(body));
    const req = http.request({ host: GW.hostname, port: GW.port, path: p, method: 'POST', timeout: 8000,
      headers: { 'content-type': 'application/json', 'content-length': data.length, 'x-device-key': SECRET } }, res => {
      let b = ''; res.on('data', c => (b += c)); res.on('end', () => resolve({ status: res.statusCode, body: b }));
    });
    req.on('error', e => resolve({ status: 0, body: String(e.message) }));
    req.on('timeout', () => req.destroy(new Error('超时')));
    req.end(data);
  });
}
const log = (...a) => console.log(new Date().toISOString(), ...a);
const slotsFull = () => { const st = load(); return SLOTS.map(n => { const v = st[n]; return v ? { slotNo: n, present: true, tag: v.tag, tagType: v.tagType } : { slotNo: n, present: false, tag: '', tagType: 'RFID' }; }); };

export const heartbeat = async () => { const r = await post('/device/v1/heartbeat', { msgId: msgId(), deviceId: DEVICE_ID, ts: new Date().toISOString(), slots: SLOTS.map(n => ({ slotNo: n, status: '正常' })), fw: 'sim-1.0', rssi: -60 }); log('heartbeat', r.status, r.body.slice(0, 120)); return r; };
export const snapshot = async () => { const r = await post('/device/v1/snapshot', { msgId: msgId(), deviceId: DEVICE_ID, ts: new Date().toISOString(), slots: slotsFull() }); log('snapshot', r.status, r.body.slice(0, 120)); return r; };
const occupancy = async slots => { const r = await post('/device/v1/occupancy', { msgId: msgId(), deviceId: DEVICE_ID, ts: new Date().toISOString(), slots }); log('occupancy', JSON.stringify(slots), r.status, r.body.slice(0, 120)); return r; };

const [cmd, a1, a2, a3] = process.argv.slice(2);
if (cmd === 'run') {
  fs.mkdirSync(path.dirname(STATE), { recursive: true }); fs.writeFileSync(path.join(here, 'run', 'pile-sim.pid'), String(process.pid));
  await snapshot(); await heartbeat();
  let n = 0;
  setInterval(async () => { await heartbeat(); if (++n % 5 === 0) await snapshot(); }, INTERVAL_MS);
  log(`pile-sim 运行中，网关 ${GW.origin}，设备 ${DEVICE_ID}，间隔 ${INTERVAL_MS / 1000}s`);
} else if (cmd === 'occupy') {
  const n = Number(a1); if (!SLOTS.includes(n) || !a2) { console.error('用法：occupy <1-4> <tag> [tagType]'); process.exit(2); }
  const tagType = a3 || 'RFID'; const st = load(); st[n] = { tag: a2, tagType }; save(st);
  const r = await occupancy([{ slotNo: n, present: true, tag: a2, tagType }]); process.exit(r.status === 200 ? 0 : 1);
} else if (cmd === 'leave') {
  const n = Number(a1); if (!SLOTS.includes(n)) { console.error('用法：leave <1-4>'); process.exit(2); }
  const st = load(); const old = st[n]; delete st[n]; save(st);
  const r = await occupancy([{ slotNo: n, present: false, tag: old ? old.tag : '', tagType: old ? old.tagType : 'RFID' }]); process.exit(r.status === 200 ? 0 : 1);
} else if (cmd === 'snapshot') { const r = await snapshot(); process.exit(r.status === 200 ? 0 : 1); }
else if (cmd === 'heartbeat') { const r = await heartbeat(); process.exit(r.status === 200 ? 0 : 1); }
else { console.error('用法：node pile-sim.mjs run|occupy|leave|snapshot|heartbeat'); process.exit(2); }
