# -*- coding: utf-8 -*-
"""共享电单车监管平台 全真原型 mock 数据生成器。

固定种子，可重复生成。锚点"现在" = 2026-12-01 12:00:00（Asia/Shanghai）。
用法：python generate.py   （输出到上一级 mockdata 目录）
金额内部一律用"分"整数计算，输出时换成元。
"""
import json, random, math, hashlib, os
from datetime import datetime as DT, timedelta as TD

SEED = 20261201
R = random.Random(SEED)
NOW = DT(2026, 12, 1, 12, 0, 0)
START = DT(2026, 9, 1, 0, 0, 0)
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.normpath(os.path.join(HERE, '..'))


def F(d):
    return None if d is None else d.strftime('%Y-%m-%d %H:%M:%S')


def Y(fen):
    return None if fen is None else round(fen / 100.0, 2)


def S(n):
    return TD(seconds=n)


def rt(a, b):
    return a + TD(seconds=R.randint(0, max(0, int((b - a).total_seconds()))))


def hav(lat1, lng1, lat2, lng2):
    p = math.pi / 180
    a = math.sin((lat2 - lat1) * p / 2) ** 2 + math.cos(lat1 * p) * math.cos(lat2 * p) * math.sin((lng2 - lng1) * p / 2) ** 2
    return 2 * 6371000 * math.asin(math.sqrt(a))


def off(lat, lng, dn, de):
    """在 (lat,lng) 处向北 dn 米、向东 de 米偏移。"""
    return lat + dn / 111320.0, lng + de / (111320.0 * math.cos(lat * math.pi / 180))


def half_up(x):
    return int(math.floor(x + 0.5 + 1e-9))


def eod(d, days):
    x = (d + TD(days=days)).replace(hour=23, minute=59, second=59)
    return x


def add_workdays(d, n):
    x = d
    while n > 0:
        x += TD(days=1)
        if x.weekday() < 5:
            n -= 1
    return x.replace(hour=18, minute=0, second=0)


AUDIT = []


# 留痕动作统一名：以 contracts/events/README.md 第 3 节「动作清单」为准；下面把调用处的旧写法归并成统一名
SIMPLE={"账号开通":"开通账号","权限调整":"调整权限","桩登记":"登记桩","桩状态变更":"设置桩状态","政府码发放":"发放政府码","运营商退出设置":"设置运营商退出","验收确认":"确认验收","降级进入":"进入降级","降级收紧":"收紧降级","降级退出":"退出降级","降级停止":"停止降级","降级放宽":"放宽降级","押金抵扣":"抵扣押金","退款确认":"确认退款","超额退款确认":"确认超额退款","退款驳回":"驳回退款","强制结束":"强制结束订单","账户冻结":"冻结账户","自动解冻":"解冻账户","人工解冻":"解冻账户","账户注销":"注销账户","结算单生成":"生成结算单","结算调整":"调整结算单","结算单审核":"审核结算单","结算单发送":"发送结算单","到账登记":"登记到账","异议处理":"处理异议","异议提交":"提交异议","证据导出":"导出证据","对账差异处理":"处理对账差异","恢复闭合处理":"闭合订单","暂停建议驳回":"驳回暂停建议","联调凭证轮换":"轮换联调凭证","联调凭证吊销":"吊销联调凭证","联调凭证签发":"签发联调凭证","联调设置":"设置联调环境","政府码发放":"发放政府码","退款执行":"执行退款","月卡购买":"购买月卡","取证":"提交取证记录","品牌上线":"上线品牌","用户注册":"注册用户","实名核验":"核验实名","站点调整":None,"配置录入":None,"车辆清单提交":None,"退押处理":None}
def ren(action,ttype,tid,summary):
    if action in ('暂停可租','恢复可租','人工转账','修改密码','查看用户信息','开始联调'): return action
    if action=='配置录入':
        if ttype=='pricing':
            if tid=='platformParams': return '设置自动暂停' if '自动暂停' in summary else '修改平台参数'
            return '撤销规则版本' if summary.startswith('撤销') else '新增规则版本'
        return {'operator':'登记运营商','brand':'登记品牌','provider':'登记服务商'}[ttype]
    if action=='站点调整':
        return '新建站点' if summary.startswith('新建') else '删除站点' if summary.startswith('删除') else '调整站点'
    if action=='车辆清单提交':
        return '编辑车辆' if '编辑' in summary else '移出车辆' if ('移' in summary) else '提交车辆清单'
    if action=='退押处理':
        return '退还押金' if ('到账' in summary or '已退' in summary) else '申请退押'
    return SIMPLE.get(action,action)


def audit(at, actor, action, ttype, tid, summary, reason=None, before=None, after=None):
    summary = summary
    action = ren(action, ttype, tid, summary)
    AUDIT.append(dict(at=at, accountId=actor, action=action, targetType=ttype, targetId=tid,
                      summary=summary, reason=reason, before=before, after=after))


# ---------------------------------------------------------------- 账号（先定义编号，供各处引用）
ACC = dict(gov1='AC0001', gov2='AC0002', op1biz='AC0101', op2biz='AC0102', op3biz='AC0103',
           op1tech='AC0111', sp1tech='AC0112', op4tech='AC0114', op4biz='AC0104',
           fin1='AC0201', fin2='AC0202', cs1='AC0301', cs2='AC0302', cs3='AC0304', csLead='AC0303',
           duty1='AC0401', duty2='AC0402', accLead='AC0403', admin='AC0404',
           law1='AC0501', law2='AC0502', law3='AC0503', law4='AC0504',
           agOps='AC0901', agRecon='AC0902', agAcc='AC0903', agCs='AC0904', sys='AC0900')

# ---------------------------------------------------------------- 运营商与品牌
OPERATORS = [
    dict(operatorId='OP01', name='青禾出行科技有限公司', shortName='青禾出行', type='运营商', accessVia='自行接入',
         providerId=None, creditCode='91510100MA6X3K7Q2B', joinedAt=DT(2026, 6, 18, 10, 0, 0), status='在营',
         contacts=[dict(name='何＊＊', role='业务负责人', phone='139****2716'), dict(name='邓＊', role='技术负责人', phone='186****0543'),
                   dict(name='罗＊＊', role='财务', phone='135****8810')],
         settlementAccount=dict(bank='示范市农商银行高新支行', accountName='青禾出行科技有限公司', account='6230 **** **** 4417')),
    dict(operatorId='OP02', name='驰路智行科技有限公司', shortName='驰路智行', type='运营商', accessVia='车联网服务商代接',
         providerId='SP01', creditCode='91510107MA62QH8L5N', joinedAt=DT(2026, 7, 2, 10, 0, 0), status='在营',
         contacts=[dict(name='沈＊＊', role='业务负责人', phone='138****6629'), dict(name='汪＊', role='财务', phone='159****3307')],
         settlementAccount=dict(bank='示范市商业银行滨河支行', accountName='驰路智行科技有限公司', account='6214 **** **** 0928')),
    dict(operatorId='OP03', name='简途共享出行有限公司', shortName='简途共享', type='运营商', accessVia='自行接入',
         providerId=None, creditCode='91510104MA6C9TW03R', joinedAt=DT(2026, 6, 25, 10, 0, 0), status='已退出',
         contacts=[dict(name='韦＊＊', role='业务负责人', phone='187****5502'), dict(name='孔＊', role='技术负责人', phone='133****7190')],
         settlementAccount=dict(bank='示范市农商银行东城支行', accountName='简途共享出行有限公司', account='6230 **** **** 7761')),
    dict(operatorId='OP04', name='示范市公共交通集团有限公司', shortName='公交集团车队', type='政府自有车队', accessVia='自行接入',
         providerId=None, creditCode='12510100MB0F28734K', joinedAt=DT(2026, 11, 3, 10, 0, 0), status='在营',
         contacts=[dict(name='龚＊＊', role='车队负责人', phone='180****4471'), dict(name='黎＊', role='技术负责人', phone='177****2068')],
         settlementAccount=dict(bank='示范市商业银行营业部', accountName='示范市公共交通集团有限公司', account='6214 **** **** 3105')),
    dict(operatorId='OP05', name='途迈出行科技有限公司', shortName='途迈出行', type='运营商', accessVia='自行接入',
         providerId=None, creditCode='91510112MA7E5UJ64W', joinedAt=None, status='在营',
         contacts=[dict(name='侯＊＊', role='业务联系人', phone='199****0835')],
         settlementAccount=None),
]
PROVIDERS = [dict(providerId='SP01', name='联泰车联网科技有限公司', operatorIds=['OP02'],
                  contact=dict(name='闫＊＊', role='接入工程师', phone='182****9156'))]

BRANDS = {
    'BR01': dict(name='青禾', op='OP01', n=480, color='#2E7D5B', model='青禾 Q3 电单车', plate='QH', status='已接入', since=DT(2026, 7, 20, 16, 30, 0)),
    'BR02': dict(name='木棉', op='OP01', n=220, color='#B83B5E', model='木棉 M2 电单车', plate='MM', status='暂停可租', since=DT(2026, 11, 30, 8, 0, 0)),
    'BR03': dict(name='驰路', op='OP02', n=360, color='#2563EB', model='驰路 C5 电单车', plate='CL', status='已接入', since=DT(2026, 8, 12, 11, 0, 0)),
    'BR04': dict(name='简途', op='OP03', n=90, color='#7C3AED', model='简途 J1 电单车', plate='JT', status='已退出', since=DT(2026, 11, 20, 10, 0, 0)),
    'BR05': dict(name='市民骑行', op='OP04', n=60, color='#0F766E', model='公交 G1 电单车', plate='GJ', status='联调中', since=DT(2026, 11, 10, 9, 30, 0)),
    'BR06': dict(name='途迈', op='OP05', n=0, color='#6B7280', model=None, plate='TM', status='未接入', since=DT(2026, 10, 30, 14, 0, 0)),
}
# 新单可开的时间窗（品牌已接入且未暂停）
BRAND_WIN = {
    'BR01': [(START, NOW)],
    'BR02': [(START, DT(2026, 10, 22, 9, 5, 0)), (DT(2026, 10, 22, 12, 40, 0), DT(2026, 11, 30, 8, 0, 0))],
    'BR03': [(START, NOW)],
    'BR04': [(START, DT(2026, 11, 8, 14, 10, 0)), (DT(2026, 11, 9, 10, 20, 0), DT(2026, 11, 20, 10, 0, 0))],
}
BRAND_W = {'BR01': 0.42, 'BR02': 0.18, 'BR03': 0.32, 'BR04': 0.08}


def brand_open(b, t):
    return any(a <= t < z for a, z in BRAND_WIN.get(b, []))


# ---------------------------------------------------------------- 计价规则版本
COUPON_TPL = {
    'CPT01': dict(name='新用户骑行券', amount=200, audience='新注册并完成实名的用户', validDays=7, issue='注册后自动发放', perUser=1),
    'CPT02': dict(name='周末骑行券', amount=200, audience='全部实名用户', validDays=3, issue='用户在券中心领取（每周五 17:00 开放）', perUser=1),
    'CPT03': dict(name='绿色出行券', amount=300, audience='2026-10-15 前已实名的用户', validDays=14, issue='系统一次性发放', perUser=1),
    'CPT04': dict(name='开锁失败关怀券', amount=100, audience='当日遇到开锁失败的用户', validDays=7, issue='开锁失败后自动发放', perUser=1),
    'CPT05': dict(name='元旦骑行券', amount=200, audience='全部实名用户', validDays=3, issue='2027-01-01 00:00 自动发放', perUser=1),
}
LIM1 = dict(perUserOpenDegradedOrders=1, normal=dict(maxMinutes=120, maxAmount=6000), tight=dict(maxMinutes=45, maxAmount=2000),
            hourlyDegradedOrders=300, degradeDurationAlertMinutes=30)
VERS = [
    dict(id='PR0001', name='2026年7月计价规则', eff=DT(2026, 7, 1), created=DT(2026, 6, 22, 15, 12, 40), by=ACC['gov1'],
         startFee=150, startMin=3, perMin=40, dispatch=300, deposit=9900, threshold=550, card=2000, cardCap=300,
         ratios={'BR01': 0.62, 'BR02': 0.58, 'BR03': 0.60, 'BR04': 0.55, 'BR05': 0.60},
         coupons=['CPT01', 'CPT02', 'CPT04'], limits=json.loads(json.dumps(LIM1)),
         note='首版计价规则：起步 3 分钟 1.5 元，之后每分钟 0.4 元。'),
    dict(id='PR0002', name='2026年10月调价', eff=DT(2026, 10, 15), created=DT(2026, 10, 8, 10, 26, 5), by=ACC['gov1'],
         startFee=150, startMin=3, perMin=50, dispatch=500, deposit=19900, threshold=550, card=2500, cardCap=300,
         ratios={'BR01': 0.62, 'BR02': 0.60, 'BR03': 0.60, 'BR04': 0.55, 'BR05': 0.60},
         coupons=['CPT01', 'CPT02', 'CPT03', 'CPT04'], limits=json.loads(json.dumps(LIM1)),
         note='分钟价调为 0.5 元，调度费 5 元，押金 199 元，月卡 25 元；木棉服务费比例调为 60%；新增绿色出行券。'),
    dict(id='PR0003', name='2026年12月限额与比例调整', eff=DT(2026, 12, 1), created=DT(2026, 11, 24, 16, 40, 18), by=ACC['gov2'],
         startFee=150, startMin=3, perMin=50, dispatch=500, deposit=19900, threshold=550, card=2500, cardCap=300,
         ratios={'BR01': 0.62, 'BR02': 0.60, 'BR03': 0.61, 'BR04': 0.55, 'BR05': 0.60},
         coupons=['CPT01', 'CPT02', 'CPT03', 'CPT04'],
         limits=dict(perUserOpenDegradedOrders=1, normal=dict(maxMinutes=120, maxAmount=6000), tight=dict(maxMinutes=30, maxAmount=1500),
                     hourlyDegradedOrders=200, degradeDurationAlertMinutes=30),
         note='收紧先骑后验与押金状态未知用户的降级额度，每小时降级单上限调为 200 单；驰路服务费比例调为 61%。'),
    dict(id='PR0004', name='2027年1月免押门槛调整', eff=DT(2027, 1, 1), created=DT(2026, 11, 28, 9, 52, 31), by=ACC['gov1'],
         startFee=150, startMin=3, perMin=50, dispatch=500, deposit=19900, threshold=600, card=2500, cardCap=300,
         ratios={'BR01': 0.62, 'BR02': 0.60, 'BR03': 0.61, 'BR04': 0.55, 'BR05': 0.60},
         coupons=['CPT01', 'CPT02', 'CPT03', 'CPT04', 'CPT05'],
         limits=dict(perUserOpenDegradedOrders=1, normal=dict(maxMinutes=120, maxAmount=6000), tight=dict(maxMinutes=30, maxAmount=1500),
                     hourlyDegradedOrders=200, degradeDurationAlertMinutes=30),
         note='信用免押门槛由 550 分调为 600 分；新增元旦骑行券。'),
]
VMAP = {v['id']: v for v in VERS}


def ver_at(t):
    cur = None
    for v in VERS:
        if v['eff'] <= t:
            cur = v
    return cur


def ride_fee(v, secs):
    m = max(1, math.ceil(secs / 60))
    return m, v['startFee'] + max(0, m - v['startMin']) * v['perMin']


# ---------------------------------------------------------------- 站点与桩
CENTER = (30.5960, 104.0660)
ROADS = ['和平路', '建设大道', '文昌街', '锦绣路', '滨河路', '青年路', '科创大道', '迎宾大道', '学府路', '人民南路', '长青街', '丰泽路',
         '望江路', '新华大街', '桂溪路', '天府东街', '金沙路', '东升街', '云锦路', '银杏大道']
POIS = ['地铁站A口', '地铁站C口', '人民医院东门', '市民中心', '体育中心北门', '第一中学', '市图书馆', '万象汇', '火车站南广场', '科技园二期',
        '滨河公园', '大学城南门', '汽车客运站', '妇幼保健院', '文化宫', '中心菜市场', '政务服务中心', '实验小学', '国际会展中心', '湿地公园东门',
        '软件园C区', '金融城', '老城区商业街', '社区卫生服务中心', '公交枢纽站']
DISTRICTS = {('N', 'W'): '西城区', ('N', 'E'): '东城区', ('S', 'W'): '滨河区', ('S', 'E'): '高新区'}

STATIONS, PILES = [], []
pts = []
names = set()
while len(pts) < 60:
    lat = CENTER[0] + R.uniform(-0.024, 0.024)
    lng = CENTER[1] + R.uniform(-0.032, 0.032)
    if all(hav(lat, lng, a, b) > 420 for a, b in pts):
        pts.append((lat, lng))
pile_station_idx = set(R.sample(range(60), 45))
pile_counts = {}
remaining = 402
idxs = sorted(pile_station_idx)
for k, i in enumerate(idxs):
    left = len(idxs) - k
    if left == 1:
        c = remaining
    else:
        c = max(6, min(12, half_up(remaining / left + R.uniform(-3, 3))))
    pile_counts[i] = c
    remaining -= c
pid = 0
for i, (lat, lng) in enumerate(pts):
    while True:
        if R.random() < 0.55:
            nm = R.choice(ROADS) + R.choice(POIS)
        else:
            a, b = R.sample(ROADS, 2)
            nm = a + '与' + b + '路口'
        if nm not in names:
            names.add(nm)
            break
    sid = 'ST%04d' % (i + 1)
    has = i in pile_station_idx
    ang = R.uniform(0, math.pi)
    hl = R.uniform(16, 34)
    hw = R.uniform(6, 11)
    ca, sa = math.cos(ang), math.sin(ang)
    poly = []
    for (x, y) in [(-hl, -hw), (hl, -hw), (hl, hw), (-hl, hw)]:
        dn, de = x * sa + y * ca, x * ca - y * sa
        la, ln = off(lat, lng, dn, de)
        poly.append([round(ln, 6), round(la, 6)])
    poly.append(poly[0])
    dist = DISTRICTS[('N' if lat >= CENTER[0] else 'S', 'E' if lng >= CENTER[1] else 'W')]
    created = DT(2026, 6, 10, 9, 0, 0) + TD(minutes=37 * i + R.randint(0, 30))
    st = dict(stationId=sid, name=nm, district=dist, type='有桩' if has else '无桩',
              center=[round(lng, 6), round(lat, 6)], fence=poly, capacity=0, pileIds=[],
              createdAt=F(created), updatedAt=F(created), updatedBy=ACC['gov1'], hl=hl, ang=ang, lat=lat, lng=lng)
    if has:
        n = pile_counts[i]
        st['capacity'] = n + R.randint(2, 6)
        for j in range(n):
            pid += 1
            x = -hl * 0.8 + (1.6 * hl) * j / max(1, n - 1)
            dn, de = x * sa, x * ca
            la, ln = off(lat, lng, dn, de)
            r = R.random()
            status = '在线' if r < 0.92 else ('离线' if r < 0.97 else '故障')
            hb = NOW - S(R.randint(3, 50)) if status == '在线' else NOW - TD(hours=R.randint(3, 70), minutes=R.randint(0, 59))
            p = dict(pileId='PL%05d' % pid, stationId=sid, no=j + 1, location=[round(ln, 6), round(la, 6)], status=status,
                     occupiedVehicleId=None, lastHeartbeatAt=F(hb), installedAt=F(created + TD(days=R.randint(5, 20), hours=2)),
                     faultNote=None)
            if status == '故障':
                p['faultNote'] = R.choice(['识别模块无响应，已通知桩运维方', '在位传感器误报，待巡检'])
            if status == '离线':
                p['faultNote'] = '心跳中断，疑似供电故障，已通知桩运维方'
            PILES.append(p)
            st['pileIds'].append(p['pileId'])
    else:
        st['capacity'] = R.randint(10, 24)
    STATIONS.append(st)
ST_BY = {s['stationId']: s for s in STATIONS}
PILE_BY = {p['pileId']: p for p in PILES}
PILE_STS = [s for s in STATIONS if s['type'] == '有桩']
NOPILE_STS = [s for s in STATIONS if s['type'] == '无桩']
FAULT_STS = [s for s in PILE_STS if any(PILE_BY[x]['status'] != '在线' for x in s['pileIds'])]

# ---------------------------------------------------------------- 车辆
VEHICLES = []
used_codes, used_tags, used_plates = set(), set(), set()
vid = 0
for b, info in BRANDS.items():
    for k in range(info['n']):
        vid += 1
        while True:
            gc = 'SF%08d' % R.randint(10000000, 99999999)
            if gc not in used_codes:
                used_codes.add(gc)
                break
        while True:
            tag = 'E28011' + ''.join(R.choice('0123456789ABCDEF') for _ in range(18))
            if tag not in used_tags:
                used_tags.add(tag)
                break
        while True:
            pl = info['plate'] + '%06d' % R.randint(100000, 999999)
            if pl not in used_plates:
                used_plates.add(pl)
                break
        listed = {'BR01': DT(2026, 7, 8, 14, 20, 11), 'BR02': DT(2026, 7, 8, 14, 20, 11), 'BR03': DT(2026, 7, 28, 10, 5, 42),
                  'BR04': DT(2026, 7, 3, 16, 41, 9), 'BR05': DT(2026, 11, 14, 15, 2, 37)}[b]
        issued = listed + TD(days=1, hours=R.randint(1, 5))
        v = dict(vehicleId='VH%06d' % vid, brandId=b, operatorId=info['op'], plateNo=pl, govCode=gc, pileTag=tag,
                 vehicleType='电单车', model=info['model'], listedAt=F(listed), codeIssuedAt=F(issued),
                 codeStatus='已启用', codeEnableAt=None, codeInvalidAt=None, battery=0, status='可用',
                 location=None, stationId=None, pileId=None, lastReportAt=None)
        if b == 'BR04':
            v['codeStatus'] = '已失效'
            v['codeInvalidAt'] = F(DT(2026, 11, 20, 10, 0, 0))
        if b == 'BR05':
            v['codeStatus'] = '待启用'
            v['codeEnableAt'] = F(DT(2026, 12, 8, 9, 0, 0))
        VEHICLES.append(v)
VH_BY = {v['vehicleId']: v for v in VEHICLES}
VH_BY_BRAND = {}
for v in VEHICLES:
    VH_BY_BRAND.setdefault(v['brandId'], []).append(v['vehicleId'])

# ---------------------------------------------------------------- 用户
SURN = list('王李张刘陈杨黄赵吴周徐孙马朱胡郭何高林罗郑梁谢宋唐许韩冯邓曹彭曾肖田董袁潘于蒋蔡余杜叶程苏魏吕丁任沈姚卢姜崔钟谭陆汪范金石廖贾夏韦付方白邹孟熊秦邱江尹薛闫段雷侯龙史陶黎贺顾毛郝龚邵万钱严覃武戴莫孔向汤')
PHONE_PFX = ['138', '139', '135', '136', '137', '150', '151', '152', '158', '159', '182', '183', '187', '188', '130', '131',
             '132', '155', '156', '186', '133', '153', '180', '189', '177', '199', '198', '166', '173', '181']
PERSONAS = [('old_free', 165), ('old_deposit', 65), ('new_none', 12), ('new_unverified', 10), ('arrears', 13), ('arrears_frozen', 5),
            ('dg3_failed', 2), ('dg3_pending', 8), ('dg3_verified', 2), ('dep_refunding', 4), ('dep_refunded', 8),
            ('cancelled', 3), ('dep_short', 3)]
PERSONA_LABEL = {'old_free': '老用户·免押', 'old_deposit': '老用户·押金', 'new_none': '新用户·未开通免押或押金',
                 'new_unverified': '新用户·未实名', 'arrears': '有欠费', 'arrears_frozen': '冻结·欠费追缴未结',
                 'dg3_failed': '冻结·先骑后验未通过', 'dg3_pending': '先骑后验·待核验', 'dg3_verified': '老用户·免押',
                 'dep_refunding': '退押中', 'dep_refunded': '已退押', 'cancelled': '已注销', 'dep_short': '押金不足'}
order_list = []
for p, n in PERSONAS:
    order_list += [p] * n
R.shuffle(order_list)
USERS = []
phones = set()
DG = [  # 降级事件
    dict(id='OPS-DG-20261018-01', enter=DT(2026, 10, 18, 19, 41, 26), exit=DT(2026, 10, 18, 20, 14, 3),
         cause='支付通道故障（微信支付回调持续超时）', components=['微信支付收款'], n=18, tighten=None),
    dict(id='OPS-DG-20261112-01', enter=DT(2026, 11, 12, 7, 58, 12), exit=DT(2026, 11, 12, 8, 52, 40),
         cause='支付通道故障（支付宝扣款接口返回系统繁忙）', components=['支付宝收款', '信用免押扣款'], n=22,
         tighten=DT(2026, 11, 12, 8, 31, 5)),
    dict(id='OPS-DG-20261127-01', enter=DT(2026, 11, 27, 21, 6, 44), exit=DT(2026, 11, 27, 21, 47, 19),
         cause='实名核验服务不可用（第三方核验接口连续超时）', components=['实名核验'], n=15, tighten=None),
]
DG_BY = {d['id']: d for d in DG}


def in_dg(t):
    for d in DG:
        if d['enter'] - TD(minutes=2) <= t <= d['exit'] + TD(minutes=2):
            return True
    return False


for i, p in enumerate(order_list):
    uid = 'U%06d' % (100001 + i * 7 + R.randint(0, 5))
    while True:
        ph = R.choice(PHONE_PFX) + '%08d' % R.randint(0, 99999999)
        if ph not in phones:
            phones.add(ph)
            break
    s = R.choice(SURN)
    nm = s + '＊' * (1 if R.random() < 0.28 else 2)
    ch = '微信' if R.random() < 0.62 else '支付宝'
    if p in ('new_none', 'new_unverified'):
        reg = rt(DT(2026, 11, 18), NOW - TD(hours=3))
    elif p in ('dg3_failed', 'dg3_pending', 'dg3_verified'):
        reg = None  # 在降级窗口内生成
    elif p == 'old_deposit':
        reg = rt(DT(2026, 7, 2), DT(2026, 11, 10)) if R.random() < 0.6 else rt(DT(2026, 7, 2), DT(2026, 10, 14))
    else:
        reg = rt(DT(2026, 7, 2), DT(2026, 11, 12))
    if p in ('arrears_frozen',):
        reg = rt(DT(2026, 7, 2), DT(2026, 8, 25))
    u = dict(userId=uid, persona=p, personaLabel=PERSONA_LABEL[p], name=nm, phone=ph, phoneMasked=ph[:3] + '****' + ph[-4:],
             channel=ch, registeredAt=reg, w=math.exp(R.gauss(0, 0.9)), rideFrom=None, rideUntil=NOW,
             realName=None, deposit=None, account=dict(status='正常', freeze=None, history=[]), cancel=None,
             degradedSignup=None)
    USERS.append(u)
U_BY = {u['userId']: u for u in USERS}


def id_masked():
    return R.choice(['510', '511', '513', '500', '520', '530', '610', '420', '430', '360']) + '*' * 11 + '%03d' % R.randint(0, 999) + R.choice('0123456789X')


# 组装各类用户的实名、押金、可骑时段
dg3 = DG[2]
dg3_users = [u for u in USERS if u['persona'] in ('dg3_failed', 'dg3_pending', 'dg3_verified')]
for u in dg3_users:
    u['registeredAt'] = rt(dg3['enter'] + TD(minutes=1), dg3['exit'] - TD(minutes=20))
for u in USERS:
    p, reg = u['persona'], u['registeredAt']
    u['realName'] = dict(status='已核验', verifiedAt=F(reg + S(R.randint(25, 70))), idMasked=id_masked(), adult=True, method='在线实名核验')
    if p == 'new_unverified':
        u['realName'] = dict(status='未实名', verifiedAt=None, idMasked=None, adult=None, method=None)
    if p in ('dg3_failed', 'dg3_pending', 'dg3_verified'):
        u['realName'] = dict(status='待核验', verifiedAt=None, idMasked=id_masked(), adult=None, method='先骑后验（降级期间注册）',
                             deadline=F(DT(2026, 12, 4, 23, 59, 59)))
        u['degradedSignup'] = dg3['id']
    ver = ver_at(reg) if reg >= VERS[0]['eff'] else VERS[0]
    if p in ('old_deposit', 'dep_refunding', 'dep_refunded', 'dep_short') or (p == 'cancelled' and u is None):
        paid = reg + S(R.randint(60, 140))
        amt = ver_at(paid)['deposit']
        u['deposit'] = dict(mode='押金', amountPaid=amt, paidAt=paid, balance=amt, status='已缴', topups=[], deductions=[], refund=None)
        u['rideFrom'] = paid + S(30)
    elif p in ('new_none', 'new_unverified', 'dg3_failed', 'dg3_pending'):
        u['deposit'] = dict(mode='未开通')
        u['rideFrom'] = reg
    else:
        op = reg + S(R.randint(40, 120))
        u['deposit'] = dict(mode='免押', creditOpenedAt=op, provider='微信支付分' if u['channel'] == '微信' else '芝麻信用',
                            score=R.randint(560, 790))
        u['rideFrom'] = op + S(20)
# 已注销用户里放 1 个押金用户
canc = [u for u in USERS if u['persona'] == 'cancelled']
c0 = canc[0]
paid = c0['registeredAt'] + S(95)
c0['deposit'] = dict(mode='押金', amountPaid=ver_at(paid)['deposit'], paidAt=paid, balance=ver_at(paid)['deposit'], status='已缴',
                     topups=[], deductions=[], refund=None)
c0['rideFrom'] = paid + S(30)

# 各类用户的停骑时刻
for u in USERS:
    p = u['persona']
    if p == 'dep_refunding':
        u['rideUntil'] = rt(DT(2026, 11, 26), DT(2026, 11, 29, 20))
        u['refundApplyAt'] = rt(DT(2026, 11, 29, 21), NOW - TD(hours=2))
    elif p == 'dep_refunded':
        u['rideUntil'] = rt(DT(2026, 9, 25), DT(2026, 11, 15))
        u['refundApplyAt'] = u['rideUntil'] + TD(days=R.randint(1, 6), hours=R.randint(0, 10))
    elif p == 'cancelled':
        u['rideUntil'] = rt(DT(2026, 9, 20), DT(2026, 10, 25))
        u['cancelAt'] = u['rideUntil'] + TD(days=R.randint(5, 20), hours=R.randint(1, 9))
        u['refundApplyAt'] = u['cancelAt'] - TD(hours=2) if u['deposit']['mode'] == '押金' else None
    elif p in ('new_none', 'new_unverified'):
        u['rideUntil'] = u['rideFrom']  # 没有骑行
    if u['rideFrom'] < START:
        u['rideFromEff'] = START
    else:
        u['rideFromEff'] = u['rideFrom']
for u in USERS:
    if u['rideFrom'] > u['rideUntil']:
        u['rideUntil'] = u['rideFrom']

# ---------------------------------------------------------------- 月卡与优惠券（发放）
CARDS, COUPONS = [], []
cp_seq = [30010000]


def new_coupon(u, tpl, at, by='系统发放'):
    cp_seq[0] += R.randint(1, 9)
    t = COUPON_TPL[tpl]
    c = dict(couponId='CP%08d' % cp_seq[0], userId=u['userId'], templateId=tpl, name=t['name'], amount=t['amount'],
             issuedAt=at, expireAt=eod(at, t['validDays']), obtainedBy=by, usedOrderId=None, usedAt=None)
    COUPONS.append(c)
    return c


for u in USERS:
    if u['realName']['status'] == '已核验' and u['persona'] not in ('dg3_failed', 'dg3_pending', 'dg3_verified'):
        new_coupon(u, 'CPT01', DT.strptime(u['realName']['verifiedAt'], '%Y-%m-%d %H:%M:%S') + S(3))
# 周末券：每周五 17:00 开放领取
fri = DT(2026, 9, 4, 17, 0, 0)
while fri < NOW:
    for u in USERS:
        if u['realName']['status'] != '已核验' or u['registeredAt'] >= fri:
            continue
        if u['persona'] == 'cancelled' and fri >= u['cancelAt'] - TD(days=2):
            continue
        if R.random() < 0.09:
            t_ = fri + S(R.randint(10, 60 * 60 * 30))
            if t_ < NOW:
                new_coupon(u, 'CPT02', t_, '用户领取')
    fri += TD(days=7)
for u in USERS:
    if u['realName']['status'] == '已核验' and u['registeredAt'] < DT(2026, 10, 15) and u['persona'] != 'cancelled':
        new_coupon(u, 'CPT03', DT(2026, 10, 15, 0, 5, 0) + S(R.randint(0, 600)))

holders = [u for u in USERS if u['persona'] in ('old_free', 'old_deposit') and u['registeredAt'] < DT(2026, 10, 20)]
holders = R.sample(holders, 42)
mc_seq = 0
for u in holders:
    u['w'] *= 3.5
    t = max(u['rideFromEff'], START) + TD(days=R.randint(0, 40), hours=R.randint(7, 21), minutes=R.randint(0, 59))
    k = R.choice([1, 1, 2, 2, 3])
    for _ in range(k):
        if t >= NOW - TD(hours=2):
            break
        mc_seq += 1
        v = ver_at(t)
        c = dict(cardId='MC%s%04d' % (t.strftime('%Y%m%d'), 1000 + mc_seq), userId=u['userId'], pricingVersionId=v['id'],
                 price=v['card'], perOrderCap=v['cardCap'], startAt=t, endAt=t + TD(days=30), paymentId=None)
        CARDS.append(c)
        t = c['endAt'] + TD(days=R.choice([0, 0, 1, 3, 8]), hours=R.randint(0, 5), minutes=R.randint(1, 50))

# ---------------------------------------------------------------- 订单（时间与还车场景）
ORDERS = []
BUSY_V, BUSY_U = {}, {}
HOUR_W = [0.3, 0.2, 0.1, 0.1, 0.1, 0.3, 1.2, 3.5, 4.2, 2.0, 1.4, 1.6, 2.2, 1.6, 1.3, 1.5, 2.0, 3.6, 4.0, 2.4, 1.8, 1.4, 1.0, 0.6]
od_seq = {}


def free(busy, key, a, b):
    for x, y in busy.get(key, []):
        if a < y + TD(minutes=3) and x < b + TD(minutes=3):
            return False
    return True


def occupy(busy, key, a, b):
    busy.setdefault(key, []).append((a, b))


def new_oid(t):
    d = t.strftime('%Y%m%d')
    od_seq[d] = od_seq.get(d, R.randint(10200, 13800)) + R.randint(3, 61)
    return 'OD%s%05d' % (d, od_seq[d])


def pick_vehicle(b, a, z):
    pool = VH_BY_BRAND[b]
    for _ in range(40):
        v = R.choice(pool)
        if free(BUSY_V, v, a, z):
            return v
    return None


def pick_brand(t):
    bs = [b for b in BRAND_W if brand_open(b, t)]
    ws = [BRAND_W[b] for b in bs]
    return R.choices(bs, ws)[0]


def base_order(u, b, vidv, req):
    return dict(orderId=new_oid(req), userId=u['userId'], vehicleId=vidv, brandId=b, operatorId=BRANDS[b]['op'],
                channel=u['channel'], pricingVersionId=ver_at(req)['id'], status=None, requestAt=req,
                unlockResult=None, unlockFailReason=None, unlockReportAt=None, startAt=None,
                startStationId=None, startLocation=None, endAt=None, lockReportAt=None, pileInAt=None, reportReceivedAt=None,
                lateReport=False, endMethod=None, endStationId=None, endPileId=None, endLocation=None, inStation=None,
                inStationBasis=None, distanceToStationM=None, dispatchPromptShown=False, durationSec=None, billedMinutes=None,
                distanceKm=None, billPushedAt=None, degraded=None, forceEnd=None, ticketIds=[], disputeIds=[], flags=set())


SCEN = [('入桩自动结束', 40), ('站内锁车·无桩站点', 18), ('页面还车·站内', 10), ('桩满·桩旁锁车', 5), ('桩故障·车端判定', 4),
        ('入桩未锁·平台结束', 3), ('桩与车端冲突·以桩为准', 2), ('站外还车', 9)]


def plan_ride(u, req, b=None, scen=None, dur_min=None, kind='normal', vehicle=None):
    """生成一条已结束（或骑行中）订单的时间与位置，失败返回 None。"""
    if b is None:
        b = pick_brand(req)
    if scen is None:
        scen = R.choices([s for s, _ in SCEN], [w for _, w in SCEN])[0]
    s0 = R.choice(STATIONS)
    if scen in ('入桩自动结束', '入桩未锁·平台结束', '桩满·桩旁锁车', '桩与车端冲突·以桩为准'):
        cand = PILE_STS
    elif scen == '桩故障·车端判定':
        cand = FAULT_STS
    elif scen == '站内锁车·无桩站点':
        cand = NOPILE_STS
    else:
        cand = STATIONS
    e = R.choice(cand)
    straight = hav(s0['lat'], s0['lng'], e['lat'], e['lng'])
    route = straight * 1.35 + R.uniform(120, 600)
    speed = R.uniform(11, 17) * 1000 / 3600
    secs = int(route / speed + R.uniform(20, 240))
    if dur_min is not None:
        secs = int(dur_min * 60 - R.randint(5, 50))
        route = secs * speed * R.uniform(0.75, 0.95)
    if secs < 100:
        secs = R.randint(100, 260)
        route = secs * speed * 0.9
    if secs > 55 * 60 and dur_min is None:
        secs = R.randint(30 * 60, 50 * 60)
        route = secs * speed * 0.8
    unl = req + S(R.randint(1, 4)) + TD(milliseconds=0)
    end_guess = unl + S(secs + 300)
    if kind == 'ongoing':
        end_guess = NOW
    v = vehicle or pick_vehicle(b, req, end_guess)
    if v is None or not free(BUSY_U, u['userId'], req, end_guess):
        return None
    o = base_order(u, b, v, req)
    o['unlockResult'] = '成功'
    o['unlockReportAt'] = unl
    o['startAt'] = unl
    o['startStationId'] = s0['stationId'] if R.random() < 0.7 else None
    la, ln = off(s0['lat'], s0['lng'], R.uniform(-12, 12), R.uniform(-12, 12)) if o['startStationId'] else off(s0['lat'], s0['lng'], R.uniform(60, 220) * R.choice([-1, 1]), R.uniform(60, 220) * R.choice([-1, 1]))
    o['startLocation'] = [round(ln, 6), round(la, 6)]
    if kind == 'ongoing':
        o['status'] = '骑行中'
        occupy(BUSY_V, v, req, NOW + TD(hours=1))
        occupy(BUSY_U, u['userId'], req, NOW + TD(hours=1))
        return o
    stop = unl + S(secs)
    o['status'] = '已结束'
    o['endStationId'] = e['stationId']
    o['endMethod'] = '用户锁车'
    o['inStation'] = True
    elat, elng = off(e['lat'], e['lng'], R.uniform(-5, 5), R.uniform(-5, 5))
    if scen == '入桩自动结束':
        pls = [PILE_BY[x] for x in e['pileIds'] if PILE_BY[x]['status'] == '在线'] or [PILE_BY[x] for x in e['pileIds']]
        pl = R.choice(pls)
        o['pileInAt'] = stop
        o['lockReportAt'] = stop + S(R.randint(3, 25))
        o['endAt'] = stop
        o['endPileId'] = pl['pileId']
        o['endMethod'] = '入桩识别'
        o['inStationBasis'] = '桩识别'
        elng, elat = pl['location']
    elif scen == '入桩未锁·平台结束':
        pl = R.choice([PILE_BY[x] for x in e['pileIds']])
        o['pileInAt'] = stop
        o['lockReportAt'] = stop + S(R.randint(70, 180))
        o['endAt'] = stop
        o['endPileId'] = pl['pileId']
        o['endMethod'] = '入桩未锁·平台请求结束'
        o['inStationBasis'] = '桩识别'
        elng, elat = pl['location']
    elif scen == '桩与车端冲突·以桩为准':
        pl = R.choice([PILE_BY[x] for x in e['pileIds']])
        o['pileInAt'] = stop
        o['lockReportAt'] = stop + S(R.randint(4, 15))
        o['endAt'] = stop
        o['endPileId'] = pl['pileId']
        o['endMethod'] = '入桩识别'
        o['inStationBasis'] = '桩与车端冲突·以桩为准'
        o['vehicleSideInStation'] = False
        elng, elat = pl['location']
    elif scen == '桩满·桩旁锁车':
        o['lockReportAt'] = stop
        o['endAt'] = stop
        o['inStationBasis'] = '桩满·围栏内'
    elif scen == '桩故障·车端判定':
        o['lockReportAt'] = stop
        o['endAt'] = stop
        o['inStationBasis'] = '桩故障或离线·车端判定'
    elif scen == '站内锁车·无桩站点':
        o['lockReportAt'] = stop
        o['endAt'] = stop
        o['inStationBasis'] = '无桩站点·车端判定'
    elif scen == '页面还车·站内':
        o['lockReportAt'] = stop
        o['endAt'] = stop
        o['endMethod'] = '页面还车'
        o['inStationBasis'] = '桩识别' if e['type'] == '有桩' and R.random() < 0.3 else '车端判定'
        if o['inStationBasis'] == '桩识别':
            pl = R.choice([PILE_BY[x] for x in e['pileIds']])
            o['endPileId'] = pl['pileId']
            o['pileInAt'] = stop - S(R.randint(5, 20))
            o['endAt'] = o['pileInAt']
            o['endMethod'] = '入桩识别'
            elng, elat = pl['location']
    elif scen == '站外还车':
        d = R.uniform(35, 260)
        a = R.uniform(0, 2 * math.pi)
        elat, elng = off(e['lat'], e['lng'], d * math.sin(a), d * math.cos(a))
        o['lockReportAt'] = stop
        o['endAt'] = stop
        o['inStation'] = False
        o['inStationBasis'] = '站外'
        o['distanceToStationM'] = int(d)
        if R.random() < 0.45:
            o['endMethod'] = '页面还车'
            o['dispatchPromptShown'] = True
        else:
            o['endMethod'] = '用户锁车'
    o['endLocation'] = [round(elng, 6), round(elat, 6)]
    o['reportReceivedAt'] = o['endAt'] + S(R.randint(0, 2))
    o['durationSec'] = int((o['endAt'] - o['startAt']).total_seconds())
    o['billedMinutes'] = max(1, math.ceil(o['durationSec'] / 60))
    o['distanceKm'] = max(0.1, round(route / 1000.0, 1))
    o['billPushedAt'] = o['reportReceivedAt'] + S(R.randint(2, 7))
    occupy(BUSY_V, v, req, o['lockReportAt'])
    occupy(BUSY_U, u['userId'], req, o['lockReportAt'])
    return o


def plan_fail(u, req, b=None, reason=None):
    if b is None:
        b = pick_brand(req)
    v = pick_vehicle(b, req, req + S(40))
    if v is None or not free(BUSY_U, u['userId'], req, req + S(40)):
        return None
    o = base_order(u, b, v, req)
    o['status'] = '开锁失败'
    o['unlockResult'] = '失败'
    reason = reason or R.choices(['车辆无响应', '电量不足', '车辆故障', '车辆被占用'], [4, 3, 2, 1])[0]
    o['unlockFailReason'] = reason
    o['unlockReportAt'] = None if reason == '车辆无响应' else req + S(R.randint(2, 6))
    s0 = R.choice(STATIONS)
    la, ln = off(s0['lat'], s0['lng'], R.uniform(-15, 15), R.uniform(-15, 15))
    o['startLocation'] = [round(ln, 6), round(la, 6)]
    o['startStationId'] = s0['stationId']
    o['failNotifiedAt'] = req + S(30) if reason == '车辆无响应' else o['unlockReportAt'] + S(1)
    occupy(BUSY_U, u['userId'], req, req + S(40))
    return o


def eligible(u, t):
    if u['persona'] in ('new_none', 'new_unverified', 'dg3_failed', 'dg3_pending', 'dg3_verified'):
        return False
    return u['rideFromEff'] <= t < u['rideUntil'] - TD(minutes=60) and not in_dg(t)


def rand_time(a=START, b=NOW - TD(hours=1)):
    while True:
        d = a + TD(days=R.randint(0, (b - a).days))
        h = R.choices(range(24), HOUR_W)[0]
        t = d.replace(hour=h, minute=R.randint(0, 59), second=R.randint(0, 59))
        if a <= t < b and not in_dg(t):
            return t


def add(o, *flags):
    for f in flags:
        o['flags'].add(f)
    ORDERS.append(o)
    return o


def pick_user(t, pool=None):
    cands = [u for u in (pool or USERS) if eligible(u, t)]
    if not cands:
        return None
    return R.choices(cands, [u['w'] for u in cands])[0]


# --- 1) 降级单
for d in DG:
    made = 0
    tries = 0
    pool = [u for u in USERS if u['persona'] in ('old_free', 'old_deposit') and u['rideFromEff'] < d['enter'] - TD(days=3) and u['rideUntil'] > d['exit']]
    special = []
    if d is DG[1]:
        special = [u for u in USERS if u['persona'] == 'arrears'][:2]
        for u in special:
            u['rideUntil'] = d['exit'] + TD(minutes=1)
    if d is DG[2]:
        special = dg3_users
    for u in special:
        while True:
            t0 = max(d['enter'] + S(30), (u['registeredAt'] + S(R.randint(40, 90))) if u['persona'].startswith('dg3') else d['enter'] + S(30))
            req = rt(t0, d['exit'] - TD(minutes=4))
            o = plan_ride(u, req)
            if o:
                break
        o['degraded'] = dict(eventId=d['id'])
        if u['persona'] == 'arrears':
            o['flags'].add('deduct_fail')
        add(o)
        made += 1
    while made < d['n'] and tries < 500:
        tries += 1
        u = R.choice(pool)
        req = rt(d['enter'] + S(20), d['exit'] - TD(minutes=1))
        o = plan_ride(u, req)
        if not o:
            continue
        o['degraded'] = dict(eventId=d['id'])
        add(o)
        made += 1
# 先骑后验的新用户只在降级期间骑一次；核验通过的两位之后开通免押，正常骑行
for u in dg3_users:
    if u['persona'] == 'dg3_verified':
        u['realName'] = dict(status='已核验', verifiedAt=F(dg3['exit'] + TD(minutes=R.randint(20, 90))), idMasked=u['realName']['idMasked'],
                             adult=True, method='先骑后验（降级期间注册，恢复后补验通过）')
        op = DT.strptime(u['realName']['verifiedAt'], '%Y-%m-%d %H:%M:%S') + TD(hours=R.randint(10, 30))
        u['deposit'] = dict(mode='免押', creditOpenedAt=op, provider='微信支付分' if u['channel'] == '微信' else '芝麻信用', score=R.randint(600, 720))
        u['rideFrom'] = u['rideFromEff'] = op + S(30)
        u['persona'] = 'old_free'
        u['personaLabel'] = PERSONA_LABEL['old_free']
        u['wasDegradedSignup'] = True
        new_coupon(u, 'CPT01', DT.strptime(u['realName']['verifiedAt'], '%Y-%m-%d %H:%M:%S') + S(3))
    if u['persona'] == 'dg3_failed':
        u['rideUntil'] = dg3['exit']
fail_specs = [u for u in dg3_users if u['persona'] == 'dg3_failed']
fail_specs[0]['realName'] = dict(status='未通过', verifiedAt=F(DT(2026, 11, 28, 9, 12, 33)), idMasked=fail_specs[0]['realName']['idMasked'],
                                 adult=False, method='先骑后验补验', failReason='未达法定驾驶年龄（16 周岁）')
fail_specs[1]['realName'] = dict(status='未通过', verifiedAt=F(DT(2026, 11, 28, 10, 40, 2)), idMasked=fail_specs[1]['realName']['idMasked'],
                                 adult=None, method='先骑后验补验', failReason='身份信息与人像比对不一致')

# --- 2) 边界单：BR-14 跨调价时刻、BR-19 跨月
for (req, why) in [(DT(2026, 10, 14, 23, 51, 8), 'BR-14'), (DT(2026, 10, 31, 23, 49, 37), 'BR-19')]:
    while True:
        u = pick_user(req - TD(hours=2))
        o = plan_ride(u, req, b='BR01', scen='入桩自动结束', dur_min=21)
        if o:
            break
    add(o, 'boundary_' + why)

# --- 3) 晚到补报（R-30 / BR-19）
late_specs = [(DT(2026, 9, 30, 21, 8), 'BR01', DT(2026, 10, 2, 9, 16, 4)), (DT(2026, 9, 30, 21, 40), 'BR01', DT(2026, 10, 2, 9, 16, 9)),
              (DT(2026, 9, 30, 22, 5), 'BR02', DT(2026, 10, 2, 9, 16, 11)), (DT(2026, 9, 30, 22, 31), 'BR02', DT(2026, 10, 2, 9, 16, 17)),
              (DT(2026, 10, 31, 20, 12), 'BR03', DT(2026, 11, 2, 14, 3, 50)), (DT(2026, 10, 31, 21, 3), 'BR03', DT(2026, 11, 2, 14, 3, 55)),
              (DT(2026, 11, 30, 22, 55), 'BR01', DT(2026, 12, 1, 8, 41, 26)),
              (DT(2026, 10, 9, 18, 22), 'BR03', DT(2026, 10, 10, 7, 30, 12)), (DT(2026, 11, 5, 8, 2), 'BR01', DT(2026, 11, 5, 19, 48, 51)),
              (DT(2026, 9, 18, 17, 40), 'BR04', DT(2026, 9, 19, 6, 12, 40))]
for req, b, recv in late_specs:
    while True:
        u = pick_user(req)
        o = plan_ride(u, req + S(R.randint(0, 600)), b=b, scen='站内锁车·无桩站点', dur_min=R.randint(9, 22))
        if o:
            break
    o['lateReport'] = True
    o['reportReceivedAt'] = recv
    o['billPushedAt'] = recv + S(R.randint(3, 8))
    add(o, 'late')

# --- 4) BR-03 超时后才报告开锁成功
for _ in range(5):
    while True:
        req = rand_time()
        u = pick_user(req)
        if not u:
            continue
        b = pick_brand(req)
        v = pick_vehicle(b, req, req + S(120))
        if not v or not free(BUSY_U, u['userId'], req, req + S(120)):
            continue
        break
    o = base_order(u, b, v, req)
    late = R.randint(36, 58)
    o.update(status='已结束', unlockResult='超时后成功', unlockReportAt=req + S(late), startAt=req + S(late),
             failNotifiedAt=req + S(30))
    s0 = R.choice(STATIONS)
    o['startStationId'] = s0['stationId']
    o['startLocation'] = s0['center']
    o['lockReportAt'] = o['unlockReportAt'] + S(R.randint(3, 7))
    o['endAt'] = o['lockReportAt']
    o['reportReceivedAt'] = o['endAt']
    o['endMethod'] = '超时开锁·平台请求结束'
    o['endStationId'] = s0['stationId']
    o['endLocation'] = s0['center']
    o['inStation'] = True
    o['inStationBasis'] = '车端判定'
    o['durationSec'] = int((o['endAt'] - o['startAt']).total_seconds())
    o['billedMinutes'] = 0
    o['distanceKm'] = 0.0
    o['billPushedAt'] = o['endAt'] + S(3)
    occupy(BUSY_V, v, req, o['endAt'])
    occupy(BUSY_U, u['userId'], req, o['endAt'])
    add(o, 'br03')

# --- 5) 客服强制结束（历史 4 单）
for _ in range(4):
    while True:
        req = rand_time(DT(2026, 9, 10), DT(2026, 11, 25))
        u = pick_user(req)
        if not u:
            continue
        o = plan_ride(u, req, scen='站内锁车·无桩站点', dur_min=R.randint(10, 25))
        if o:
            break
    actual_end = o['endAt']
    force_at = actual_end + TD(minutes=R.randint(35, 160))
    o['lockReportAt'] = force_at + S(3)
    o['reportReceivedAt'] = force_at + S(3)
    o['endMethod'] = '客服强制结束'
    o['forceEnd'] = dict(at=force_at, billedEndAt=actual_end, by=R.choice([ACC['cs1'], ACC['cs2'], ACC['cs3']]), reason=None, ticketId=None)
    o['billPushedAt'] = force_at + S(6)
    occupy(BUSY_V, o['vehicleId'], req, force_at)
    add(o, 'force')

# --- 5b) 两单长时骑行（锁车报告延迟，后续超额退款）
for req, dm in [(DT(2026, 11, 12, 11, 38, 20), 51), (DT(2026, 11, 30, 17, 21, 45), 48)]:
    while True:
        u = pick_user(req, [x for x in USERS if x['persona'] == 'old_free' and x not in holders])
        if not u:
            continue
        o = plan_ride(u, req, b='BR01', scen='桩满·桩旁锁车', dur_min=dm)
        if o:
            break
    add(o, 'long')

# --- 6) 骑行中（现在仍在骑）与卡住待强制结束的一单
for _ in range(6):
    while True:
        req = rt(NOW - TD(minutes=38), NOW - TD(minutes=2))
        u = pick_user(NOW - TD(hours=3))
        if not u or u['persona'] not in ('old_free', 'old_deposit') or u['rideUntil'] < NOW:
            continue
        b = R.choice(['BR01', 'BR03'])
        o = plan_ride(u, req, b=b, kind='ongoing')
        if o:
            break
    add(o, 'ongoing')
while True:
    req = DT(2026, 12, 1, 7, 41, 17)
    u = pick_user(DT(2026, 12, 1, 6), [x for x in USERS if x['persona'] == 'old_free'])
    o = plan_ride(u, req, b='BR03', kind='ongoing')
    if o:
        break
o['userClaimedEndAt'] = DT(2026, 12, 1, 8, 2, 40)
o['anomalyNote'] = '用户称已在站点内手动锁车，运营商未上报锁车事件'
add(o, 'stuck')

# --- 7) 欠费类用户的最后一单（免押扣款失败）
for u in USERS:
    p = u['persona']
    if p in ('arrears', 'arrears_frozen') and not any(o['userId'] == u['userId'] and 'deduct_fail' in o['flags'] for o in ORDERS):
        a = DT(2026, 9, 3) if p == 'arrears_frozen' else DT(2026, 10, 2)
        z = DT(2026, 10, 25) if p == 'arrears_frozen' else NOW - TD(hours=6)
        a = max(a, u['rideFromEff'] + TD(days=1))
        while True:
            req = rand_time(a, z)
            o = plan_ride(u, req)
            if o:
                break
        add(o, 'deduct_fail')
        u['rideUntil'] = o['lockReportAt'] + TD(minutes=1)
    if p == 'dep_short':
        a = max(DT(2026, 10, 10), u['rideFromEff'] + TD(days=1))
        while True:
            req = rand_time(a, NOW - TD(days=2))
            o = plan_ride(u, req)
            if o:
                break
        add(o, 'never_pay')
        u['rideUntil'] = o['lockReportAt'] + TD(minutes=1)
# 曾欠费后补付的老用户：4 位经历冻结（2 位补付后自动解冻、2 位客服人工解冻），6 位未冻结即补付
olds = [u for u in USERS if u['persona'] == 'old_free' and u['rideFromEff'] < DT(2026, 9, 5) and u not in holders]
frz = R.sample(olds, 4)
for k, u in enumerate(frz):
    while True:
        req = rand_time(DT(2026, 9, 3), DT(2026, 9, 22))
        o = plan_ride(u, req)
        if o:
            break
    add(o, 'deduct_fail', 'pay_later_frozen', 'unfreeze_auto' if k < 2 else 'unfreeze_manual')
    u['frozenWindow'] = (o['billPushedAt'] + TD(days=30), DT(2026, 11, 3, 8) + TD(days=k * 2, hours=R.randint(0, 10)))
others = [u for u in olds if u not in frz]
for u in R.sample(others, 6):
    while True:
        req = rand_time(DT(2026, 9, 10), DT(2026, 11, 20))
        o = plan_ride(u, req)
        if o:
            break
    add(o, 'deduct_fail', 'pay_later')

# --- 8) 随机填充到约 800 单（含开锁失败）
TARGET = 806
guard = 0
while len(ORDERS) < TARGET and guard < 20000:
    guard += 1
    req = rand_time()
    u = pick_user(req)
    if not u:
        continue
    fw = u.get('frozenWindow')
    if fw and fw[0] - TD(hours=2) <= req <= fw[1] + TD(hours=1):
        continue
    if R.random() < 0.055:
        o = plan_fail(u, req)
        if not o:
            continue
        add(o)
        if R.random() < 0.65:
            o2 = plan_ride(u, req + S(R.randint(230, 420)))
            if o2:
                o2['afterFailOrderId'] = o['orderId']
                add(o2)
        continue
    o = plan_ride(u, req)
    if o:
        add(o)

ORDERS.sort(key=lambda o: o['requestAt'])
O_BY = {o['orderId']: o for o in ORDERS}
assert len(O_BY) == len(ORDERS), 'orderId 重复'

# 冻结窗口内若已有订单（理论上被排除），移除冻结者冻结期内的订单
for u in frz:
    a, b = u['frozenWindow']
    bad = [o for o in ORDERS if o['userId'] == u['userId'] and a - TD(hours=2) <= o['requestAt'] <= b + TD(hours=1) and not o['flags']]
    for o in bad:
        ORDERS.remove(o)
O_BY = {o['orderId']: o for o in ORDERS}

# 开锁失败关怀券（每人每天最多 1 张）
_got = set()
for o in ORDERS:
    if o['status'] == '开锁失败':
        k = (o['userId'], o['requestAt'].date())
        if k not in _got:
            _got.add(k)
            new_coupon(U_BY[o['userId']], 'CPT04', o['failNotifiedAt'] + S(2))

# ---------------------------------------------------------------- 降级单的准入分类与限额
for d in DG:
    ver = ver_at(d['enter'])
    dos = [o for o in ORDERS if o['degraded'] and o['degraded']['eventId'] == d['id']]
    dep_users = [o for o in dos if U_BY[o['userId']]['deposit']['mode'] == '押金']
    unknown = set(x['orderId'] for x in dep_users[:2])
    for o in dos:
        u = U_BY[o['userId']]
        if u['persona'].startswith('dg3') or u.get('degradedSignup'):
            adm = '先骑后验新用户'
        elif o['orderId'] in unknown:
            adm = '押金状态未知的老用户'
        else:
            adm = '老用户'
        lim = ver['limits']['tight'] if adm != '老用户' else ver['limits']['normal']
        o['degraded'].update(admission=adm, limitMinutes=lim['maxMinutes'], limitAmount=lim['maxAmount'], overLimitRemindedAt=None)
        if o['durationSec'] and o['durationSec'] > lim['maxMinutes'] * 60:
            o['degraded']['overLimitRemindedAt'] = o['startAt'] + TD(minutes=lim['maxMinutes'])
# 先骑后验的一单故意超过 45 分钟上限，演示"只提醒、不结束"
tight_o = next(o for o in ORDERS if o['degraded'] and o['degraded']['admission'] == '先骑后验新用户' and U_BY[o['userId']]['persona'] == 'dg3_pending')
# 延长：重排结束时间
ext = 47 * 60 + 18
tight_o['endAt'] = tight_o['startAt'] + S(ext)
tight_o['lockReportAt'] = tight_o['endAt'] if tight_o['pileInAt'] is None else tight_o['endAt'] + S(9)
if tight_o['pileInAt'] is not None:
    tight_o['pileInAt'] = tight_o['endAt']
tight_o['reportReceivedAt'] = tight_o['endAt'] + S(1)
tight_o['billPushedAt'] = tight_o['reportReceivedAt'] + S(4)
tight_o['durationSec'] = ext
tight_o['billedMinutes'] = math.ceil(ext / 60)
tight_o['distanceKm'] = round(ext / 3600 * 9.5, 1)
tight_o['degraded']['overLimitRemindedAt'] = tight_o['startAt'] + TD(minutes=tight_o['degraded']['limitMinutes'])

# DG3 留 2 单待人工：1 单缺锁车事实，1 单扣款结果未知
dg3_orders = [o for o in ORDERS if o['degraded'] and o['degraded']['eventId'] == dg3['id'] and U_BY[o['userId']]['persona'] in ('old_free',) and not U_BY[o['userId']].get('wasDegradedSignup')]
pend_lock, pend_pay = dg3_orders[0], dg3_orders[1]
pend_lock['flags'].add('pending_lock')
pend_pay['flags'].add('pending_pay')

# ---------------------------------------------------------------- 金额与支付
PAYMENTS = []
py_seq = [0]


def trade_no(ch, t, kind='pay'):
    if ch == '微信支付':
        return ('4200' if kind == 'pay' else '5030') + '%06d' % R.randint(0, 999999) + t.strftime('%Y%m%d') + '%010d' % R.randint(0, 9999999999)
    if ch == '支付宝':
        return t.strftime('%Y%m%d') + ('22001' if kind == 'pay' else '22003') + '%015d' % R.randint(0, 10 ** 15 - 1)
    if ch == '银行转账':
        return 'HX' + t.strftime('%Y%m%d') + '%010d' % R.randint(0, 9999999999)
    return 'ZH' + t.strftime('%Y%m%d') + '%08d' % R.randint(0, 99999999)


def pay(type_, account, direction, u, amount, ch, at, status='成功', order=None, card=None, done=None, **kw):
    py_seq[0] += 1
    p = dict(paymentId='PY%s%03d' % (at.strftime('%Y%m%d%H%M%S'), py_seq[0] % 1000), type=type_, account=account, direction=direction,
             userId=u['userId'] if u else None, orderId=order, cardId=card, amount=amount, channel=ch,
             channelTradeNo=trade_no(ch, at, 'refund' if type_ in ('退款', '退押') else 'pay') if status != '失败' or type_ in ('退款', '退押') else None,
             status=status, failReason=None, createdAt=at, completedAt=(done if done is not None else (at if status == '成功' else None)),
             channelSettledDate=None, relatedPaymentId=None, ticketId=None, operatorAccountId=None, payee=None, payeeSubmitted=None, note=None)
    p.update(kw)
    PAYMENTS.append(p)
    return p


def chan(u):
    return '微信支付' if u['channel'] == '微信' else '支付宝'


for c in CARDS:
    u = U_BY[c['userId']]
    p = pay('月卡购买', '骑行收入户', '收', u, c['price'], chan(u), c['startAt'] - S(R.randint(5, 20)), card=c['cardId'])
    c['paymentId'] = p['paymentId']

# 押金缴纳
for u in USERS:
    dpo = u['deposit']
    if dpo.get('mode') == '押金':
        p = pay('押金缴纳', '押金专户', '收', u, dpo['amountPaid'], chan(u), dpo['paidAt'])
        dpo['paymentId'] = p['paymentId']

cards_by_user = {}
for c in CARDS:
    cards_by_user.setdefault(c['userId'], []).append(c)
cp_by_user = {}
for c in COUPONS:
    cp_by_user.setdefault(c['userId'], []).append(c)

by_user = {}
for o in ORDERS:
    by_user.setdefault(o['userId'], []).append(o)


def closure_time(o):
    d = DG_BY[o['degraded']['eventId']]
    return max(o['billPushedAt'], d['exit'] + TD(minutes=R.randint(6, 25)))


for uid, os_ in by_user.items():
    u = U_BY[uid]
    os_.sort(key=lambda o: o['requestAt'])
    pending_arrears = []
    for o in os_:
        # 欠费补付（下次扫码时一键补付）
        if pending_arrears and o['status'] != '开锁失败' or (pending_arrears and o['status'] == '开锁失败'):
            for a in pending_arrears:
                t = o['requestAt'] - S(R.randint(12, 40))
                p = pay('欠费补付', '骑行收入户', '收', u, a['fee']['unpaid'], chan(u), t, order=a['orderId'])
                a['fee']['channelPaid'] += a['fee']['unpaid']
                a['fee']['unpaid'] = 0
                a['payStatus'] = '欠费已补付'
                a['arrearsPaidAt'] = t
            pending_arrears = []
        # 押金不足先补缴
        dpo = u['deposit']
        if dpo.get('mode') == '押金' and dpo['balance'] < dpo['amountPaid'] and o['status'] != '开锁失败':
            short = dpo['amountPaid'] - dpo['balance']
            t = o['requestAt'] - S(R.randint(20, 50))
            p = pay('押金补缴', '押金专户', '收', u, short, chan(u), t)
            dpo['balance'] += short
            dpo['topups'].append(p['paymentId'])
        if o['status'] != '已结束':
            o['fee'] = None
            o['payStatus'] = None if o['status'] == '骑行中' else '无需支付'
            continue
        v = VMAP[o['pricingVersionId']]
        f = dict(rideFee=0, dispatchFee=0, receivable=0, monthCardId=None, monthCardDeduct=0, couponId=None, couponDeduct=0,
                 payable=0, channelPaid=0, depositDeducted=0, refunded=0, unpaid=0, netReceived=0)
        if o['unlockResult'] == '超时后成功':
            o['fee'] = f
            o['payStatus'] = '无需支付'
            continue
        mins, rf = ride_fee(v, o['durationSec'])
        o['billedMinutes'] = mins
        f['rideFee'] = rf
        f['dispatchFee'] = v['dispatch'] if not o['inStation'] else 0
        f['receivable'] = f['rideFee'] + f['dispatchFee']
        for c in cards_by_user.get(uid, []):
            if c['startAt'] <= o['startAt'] < c['endAt']:
                f['monthCardId'] = c['cardId']
                f['monthCardDeduct'] = min(c['perOrderCap'], f['rideFee'])
                break
        rem = f['receivable'] - f['monthCardDeduct']
        if o['degraded']:
            o['billPushedAt'] = closure_time(o)
        bt = o['billPushedAt']
        if rem > 0:
            cands = [c for c in cp_by_user.get(uid, []) if c['usedOrderId'] is None and c['issuedAt'] <= bt <= c['expireAt']]
            if cands:
                cands.sort(key=lambda c: (-min(c['amount'], rem), c['expireAt']))
                c = cands[0]
                c['usedOrderId'] = o['orderId']
                c['usedAt'] = bt
                f['couponId'] = c['couponId']
                f['couponDeduct'] = min(c['amount'], rem)
        f['payable'] = f['receivable'] - f['monthCardDeduct'] - f['couponDeduct']
        o['fee'] = f
        if f['payable'] == 0:
            o['payStatus'] = '无需支付'
            continue
        mode = u['deposit'].get('mode')
        if 'pending_pay' in o['flags']:
            p = pay('订单扣款', '骑行收入户', '收', u, f['payable'], chan(u), bt + S(2), status='处理中', order=o['orderId'], done=None)
            p['note'] = '恢复后发起扣款，渠道未返回结果'
            f['unpaid'] = f['payable']
            o['payStatus'] = '待支付'
            o['payMethod'] = '免押自动扣款'
            continue
        if 'pending_lock' in o['flags']:
            o['fee'] = None
            o['payStatus'] = None
            continue
        if mode == '免押':
            t = bt + S(R.randint(1, 3))
            if 'deduct_fail' in o['flags']:
                p = pay('订单扣款', '骑行收入户', '收', u, f['payable'], chan(u), t, status='失败', order=o['orderId'])
                p['failReason'] = R.choice(['用户支付账户余额不足', '用户已关闭免密支付授权']) if o['degraded'] is None else '恢复后扣款失败：用户支付账户余额不足'
                f['unpaid'] = f['payable']
                o['payStatus'] = '欠费'
                o['payMethod'] = '免押自动扣款'
                if 'pay_later' in o['flags']:
                    pending_arrears.append(o)
                if 'pay_later_frozen' in o['flags']:
                    pending_arrears.append(o)
            else:
                pay('订单扣款', '骑行收入户', '收', u, f['payable'], chan(u), t, order=o['orderId'])
                f['channelPaid'] = f['payable']
                o['payStatus'] = '已支付'
                o['payMethod'] = '免押自动扣款'
        else:
            # 押金用户与先骑后验新用户：一键支付
            r = R.random()
            if u['persona'] == 'dg3_failed':
                f['unpaid'] = f['payable']
                o['payStatus'] = '欠费'
                o['payMethod'] = '一键支付'
                continue
            if 'never_pay' in o['flags'] or (mode == '押金' and r < 0.07 and bt + TD(hours=24, minutes=10) < NOW and u['persona'] not in ('dep_refunding', 'dep_refunded', 'cancelled')):
                t = bt + TD(hours=24, minutes=R.randint(1, 9), seconds=R.randint(0, 59))
                if t > NOW:
                    f['unpaid'] = f['payable']
                    o['payStatus'] = '待支付'
                    o['payMethod'] = '一键支付'
                    continue
                p = pay('押金抵扣', '押金专户', '转', u, f['payable'], '押金专户划转', t, order=o['orderId'])
                p['note'] = '一键支付期限（24 小时）届满未付，按 BR-22 自动从押金抵扣，已通知用户'
                f['depositDeducted'] = f['payable']
                u['deposit']['balance'] -= f['payable']
                u['deposit']['deductions'].append(p['paymentId'])
                o['payStatus'] = '押金抵扣'
                o['payMethod'] = '押金抵扣'
                continue
            if bt > NOW - TD(hours=20) and r < 0.35 and mode == '押金' and u['persona'] == 'old_deposit':
                f['unpaid'] = f['payable']
                o['payStatus'] = '待支付'
                o['payMethod'] = '一键支付'
                continue
            t = bt + (S(R.randint(8, 240)) if r < 0.85 else TD(minutes=R.randint(20, 900)))
            if t > NOW:
                t = NOW - S(R.randint(60, 600))
            pay('订单支付', '骑行收入户', '收', u, f['payable'], chan(u), t, order=o['orderId'])
            f['channelPaid'] = f['payable']
            o['payStatus'] = '已支付'
            o['payMethod'] = '一键支付'

# 冻结→解冻历史
for u in frz:
    a, b = u['frozenWindow']
    o = next(x for x in ORDERS if x['userId'] == u['userId'] and 'pay_later_frozen' in x['flags'])
    paid_at = o.get('arrearsPaidAt')
    if paid_at is None:
        # 冻结期后的首单前补付；若没有后续订单则在 b 时补付
        paid_at = b - TD(minutes=5)
        p = pay('欠费补付', '骑行收入户', '收', u, o['fee']['unpaid'], chan(u), paid_at, order=o['orderId'])
        o['fee']['channelPaid'] += o['fee']['unpaid']
        o['fee']['unpaid'] = 0
        o['payStatus'] = '欠费已补付'
        o['arrearsPaidAt'] = paid_at
    u['account']['history'].append(dict(action='冻结', at=a, reason='欠费追缴未结（超过 30 天）', by=ACC['sys']))
    if 'unfreeze_auto' in o['flags']:
        u['account']['history'].append(dict(action='自动解冻', at=paid_at + S(2), reason='已结清欠费且实名有效', by=ACC['sys']))
    else:
        t = paid_at + TD(hours=R.randint(2, 20))
        u['account']['history'].append(dict(action='人工解冻', at=t, reason='欠费已补付，实名信息过期未触发自动解冻，客服核验身份后解冻',
                                            by=ACC['cs1']))
# 欠费冻结类（当前冻结）
for u in USERS:
    if u['persona'] == 'arrears_frozen':
        o = next(x for x in ORDERS if x['userId'] == u['userId'] and 'deduct_fail' in x['flags'])
        t = o['billPushedAt'] + TD(days=30)
        u['account'] = dict(status='冻结', freeze=dict(reason='欠费追缴未结（超过 30 天）', at=t, unfreezeHow='结清欠费后自动解冻'),
                            history=[dict(action='冻结', at=t, reason='欠费追缴未结（超过 30 天）', by=ACC['sys'])])
    if u['persona'] == 'dg3_failed':
        t = DT.strptime(u['realName']['verifiedAt'], '%Y-%m-%d %H:%M:%S') + S(4)
        rsn = '先骑后验核验未通过（' + u['realName']['failReason'] + '）'
        u['account'] = dict(status='冻结', freeze=dict(reason=rsn, at=t, unfreezeHow='完成实名核验（达到法定年龄）并结清欠费后自动解冻'),
                            history=[dict(action='冻结', at=t, reason=rsn, by=ACC['sys'])])

# 退押与注销
for u in USERS:
    p = u['persona']
    dpo = u['deposit']
    if p in ('dep_refunding', 'dep_refunded') or (p == 'cancelled' and dpo.get('mode') == '押金'):
        ap = u['refundApplyAt']
        stop = ap + S(R.randint(5, 40))
        exp = add_workdays(stop, 3)
        amt = dpo['balance']
        if p == 'dep_refunding':
            pr = pay('退押', '押金专户', '付', u, amt, chan(u), stop, status='处理中', done=None)
            dpo['status'] = '退押中'
        else:
            pr = pay('退押', '押金专户', '付', u, amt, chan(u), stop, status='成功', done=stop + TD(hours=R.randint(4, 40)))
            dpo['status'] = '已退'
        dpo['refund'] = dict(appliedAt=ap, stoppedAt=stop, expectedAt=exp, paymentId=pr['paymentId'], amount=amt)
        dpo['balance'] = 0 if dpo['status'] == '已退' else dpo['balance']
# 退押原路失败 3 笔：2 笔已人工转账，1 笔待用户提供收款账户
refd = [u for u in USERS if u['persona'] == 'dep_refunded']
for k, u in enumerate(refd[:3]):
    pr = next(p for p in PAYMENTS if p['paymentId'] == u['deposit']['refund']['paymentId'])
    pr['status'] = '失败'
    pr['failReason'] = R.choice(['原支付账户已注销', '原银行卡已挂失换卡', '原支付账户已注销'])
    pr['completedAt'] = None
    if k < 2:
        t = pr['createdAt'] + TD(days=R.randint(2, 4), hours=R.randint(1, 6))
        m = pay('人工转账退押', '押金专户', '付', u, pr['amount'], '银行转账', t, relatedPaymentId=pr['paymentId'],
                operatorAccountId=ACC['fin1'], payee=dict(name=u['name'], bank=R.choice(['示范市商业银行', '示范市农商银行', '工商银行示范分行']),
                                                         account='62' + '%02d' % R.randint(10, 30) + ' **** **** ' + '%04d' % R.randint(0, 9999)))
        u['deposit']['status'] = '已退'
        u['deposit']['refund']['manualPaymentId'] = m['paymentId']
    else:
        u['deposit']['status'] = '退押中'
        u['deposit']['balance'] = pr['amount']
        u['deposit']['refund']['awaitingPayee'] = True
for u in canc:
    u['account'] = dict(status='已注销', freeze=None, history=[dict(action='注销', at=u['cancelAt'], reason='用户申请注销', by=u['userId'])])

# ---------------------------------------------------------------- 客服工单（含退款）
TICKETS = []
tk_seq = {}


def new_tk(at):
    d = at.strftime('%Y%m%d')
    tk_seq[d] = tk_seq.get(d, 0) + 1
    return 'TK%s%04d' % (d, tk_seq[d] + 30)


def ticket(u, at, cat, order=None, entry='我的订单', status='已解决'):
    t = dict(ticketId=new_tk(at), userId=u['userId'], orderId=order['orderId'] if order else None, category=cat, entry=entry,
             createdAt=at, status=status, assignee=None, closedAt=None, messages=[], refundProposal=None, forceEnd=None,
             facts=None, actions=[], awaitingPayee=False)
    if order:
        order['ticketIds'].append(t['ticketId'])
        f = order.get('fee')
        t['facts'] = dict(orderId=order['orderId'], startAt=order['startAt'], endAt=order['endAt'],
                          billedMinutes=order['billedMinutes'], distanceKm=order['distanceKm'],
                          receivable=f['receivable'] if f else None, payable=f['payable'] if f else None,
                          netReceived=None, inStation=order['inStation'], inStationBasis=order['inStationBasis'])
    TICKETS.append(t)
    return t


def msg(t, frm, at, text):
    t['messages'].append(dict(from_=frm, at=at, text=text))


def yuan(fen):
    return '¥%.1f' % (fen / 100.0)


def do_refund(t, o, amount, at, by, over=False, fail=None):
    u = U_BY[o['userId']]
    f = o['fee']
    orig = next(p for p in PAYMENTS if p['orderId'] == o['orderId'] and p['type'] in ('订单扣款', '订单支付', '欠费补付') and p['status'] == '成功')
    st = '成功' if not fail else '失败'
    p = pay('退款', '骑行收入户', '付', u, amount, orig['channel'], at, status=st, order=o['orderId'], relatedPaymentId=orig['paymentId'],
            ticketId=t['ticketId'], operatorAccountId=by, done=(at + S(R.randint(3, 40))) if st == '成功' else None)
    if fail:
        p['failReason'] = fail
    else:
        f['refunded'] += amount
    return p


refundable = [o for o in ORDERS if o['status'] == '已结束' and o['fee'] and o['fee']['channelPaid'] > 0 and o['payStatus'] == '已支付'
              and not o['degraded'] and not o['lateReport'] and 'force' not in o['flags'] and o['billPushedAt'] < NOW - TD(hours=3)]
R.shuffle(refundable)
used_orders = set()


def take(pred):
    for o in refundable:
        if o['orderId'] not in used_orders and pred(o):
            used_orders.add(o['orderId'])
            return o
    raise RuntimeError('no candidate')


# 1) 全额退款：车辆故障骑行很短
for k in range(5):
    o = take(lambda o: o['billedMinutes'] <= 8 and o['fee']['dispatchFee'] == 0)
    if k == 0:
        o2 = take(lambda x: x['endAt'] >= DT(2026, 10, 27) and x['endAt'] < DT(2026, 11, 1) and x['billedMinutes'] <= 10 and x['fee']['dispatchFee'] == 0)
        o = o2
    if k == 1:
        o = take(lambda x: x['endAt'] >= DT(2026, 11, 28) and x['endAt'] < DT(2026, 11, 30, 20) and x['billedMinutes'] <= 10 and x['fee']['dispatchFee'] == 0)
    u = U_BY[o['userId']]
    at = o['billPushedAt'] + TD(minutes=R.randint(4, 50))
    if k == 0:
        at = DT(2026, 11, 2, 10, 15, 0) + S(R.randint(0, 3000))
    if k == 1:
        at = DT(2026, 12, 1, 9, 2, 0) + S(R.randint(0, 900))
    t = ticket(u, at, '费用异议', o)
    amt = o['fee']['channelPaid']
    msg(t, '用户', at, R.choice(['车子骑了没多久就没电了，推回来的，这单能退吗？', '刹车有问题骑不了，我马上就还了，为什么还扣钱？', '车把松了骑不了，只骑了一小段就还车了。']))
    msg(t, '客服智能体', at + S(8), '已调出订单 %s：%d 分钟、%.1f 公里，已扣 %s。车辆在开锁后 %d 分钟内停在原地附近，运营商同日报告该车故障。建议全额退款 %s。' % (
        o['orderId'], o['billedMinutes'], o['distanceKm'], yuan(amt), o['billedMinutes'], yuan(amt)))
    by = R.choice([ACC['cs1'], ACC['cs2'], ACC['cs3']])
    ct = at + TD(minutes=R.randint(2, 15))
    t['refundProposal'] = dict(amount=amt, kind='全额退款', basis='车辆故障，骑行时长短且未离开起点附近', proposedAt=at + S(8), proposedBy=ACC['agCs'],
                               decision='已确认', decidedBy=by, decidedAt=ct, overLimit=False)
    p = do_refund(t, o, amt, ct, by)
    msg(t, '客服', ct, '已为你全额退款 %s，原路退回，一般几分钟到账。给你带来不便很抱歉。' % yuan(amt))
    t.update(assignee=by, closedAt=ct + TD(minutes=1))
    t['actions'].append(dict(type='退款', paymentId=p['paymentId'], at=ct, by=by))

# 2) 部分退款：调度费（桩离线时车端判为站外，实际在站内）
for k in range(4):
    o = take(lambda o: False) if False else None
    cands = [x for x in ORDERS if x['status'] == '已结束' and x['fee'] and x['fee']['dispatchFee'] > 0 and x['payStatus'] == '已支付'
             and x['orderId'] not in used_orders and x['fee']['channelPaid'] >= x['fee']['dispatchFee'] and not x['degraded']]
    o = cands[k * 3]
    used_orders.add(o['orderId'])
    u = U_BY[o['userId']]
    at = o['billPushedAt'] + TD(minutes=R.randint(10, 300))
    t = ticket(u, at, '调度费异议', o)
    amt = o['fee']['dispatchFee']
    st = ST_BY[o['endStationId']]
    msg(t, '用户', at, '我明明停在%s站点里，为什么还收了调度费？' % st['name'])
    if k < 3:
        msg(t, '客服智能体', at + S(9), '订单 %s 锁车位置距离%s站点围栏 %d 米，车端判定在站外。但该站点在你锁车前后有桩离线，车辆定位可能漂移。建议退还调度费 %s。' % (
            o['orderId'], st['name'], o['distanceToStationM'], yuan(amt)))
        by = R.choice([ACC['cs1'], ACC['cs2']])
        ct = at + TD(minutes=R.randint(3, 25))
        t['refundProposal'] = dict(amount=amt, kind='部分退款', basis='站点桩离线，定位漂移可能导致误判站外', proposedAt=at + S(9),
                                   proposedBy=ACC['agCs'], decision='已确认', decidedBy=by, decidedAt=ct, overLimit=False)
        p = do_refund(t, o, amt, ct, by)
        msg(t, '客服', ct, '核实后已退还调度费 %s，原路退回。' % yuan(amt))
        t.update(assignee=by, closedAt=ct + TD(minutes=2))
        t['actions'].append(dict(type='退款', paymentId=p['paymentId'], at=ct, by=by))
    else:
        msg(t, '客服智能体', at + S(9), '订单 %s 锁车位置距离%s站点围栏 %d 米，站点桩均在线且未识别到该车，车辆轨迹显示停在路边人行道。按规则站外还车收调度费 %s。不建议退款。' % (
            o['orderId'], st['name'], o['distanceToStationM'], yuan(amt)))
        by = ACC['cs2']
        ct = at + TD(minutes=R.randint(3, 25))
        t['refundProposal'] = dict(amount=0, kind='不退款', basis='锁车位置在站外，桩记录与车端一致', proposedAt=at + S(9), proposedBy=ACC['agCs'],
                                   decision='已驳回', decidedBy=by, decidedAt=ct, overLimit=False)
        msg(t, '客服', ct, '核实了锁车位置，车停在站点外 %d 米的人行道上，调度费按规则收取。下次还车前页面会提示最近站点，停进围栏就不收。' % o['distanceToStationM'])
        t.update(assignee=by, closedAt=ct + TD(minutes=1))

# 3) 超额退款（> 客服授权额度 20 元）：1 单已由主管确认，1 单待主管确认
CS_LIMIT = 2000
longs = sorted([x for x in ORDERS if x['status'] == '已结束' and x['fee'] and x['fee']['channelPaid'] > CS_LIMIT + 300 and x['payStatus'] == '已支付'
                and x['orderId'] not in used_orders and not x['degraded'] and not x['lateReport'] and x['fee']['dispatchFee'] == 0
                and 'force' not in x['flags']], key=lambda x: x['endAt'])
longs = sorted([x for x in ORDERS if 'long' in x['flags']], key=lambda x: x['endAt'])
if len(longs) < 2:
    # 造两单长时骑行（忘记还车）
    pass
for k in range(2):
    o = longs[k]
    used_orders.add(o['orderId'])
    u = U_BY[o['userId']]
    at = o['billPushedAt'] + TD(hours=R.randint(1, 20)) if k == 0 else o['billPushedAt'] + TD(hours=R.randint(2, 6))
    if at > NOW:
        at = NOW - TD(minutes=40)
    t = ticket(u, at, '费用异议', o)
    pm = VMAP[o['pricingVersionId']]['perMin']
    idle = (o['fee']['channelPaid'] - 300) // pm
    idle = min(idle, o['billedMinutes'] - 6)
    amt = idle * pm
    stn = ST_BY[o['endStationId']]
    how = '桩旁锁好车（桩位都满了）' if o['inStationBasis'] == '桩满·围栏内' else '站点里锁好车'
    msg(t, '用户', at, '我早就到%s%s了，结果显示骑了 %d 分钟，扣了 %s，太多了。' % (stn['name'], how, o['billedMinutes'], yuan(o['fee']['channelPaid'])))
    msg(t, '客服智能体', at + S(10), '订单 %s 计费 %d 分钟。车辆在结束前约 %d 分钟已停在%s站点内未移动，锁车报告延迟到达。建议按车辆停止移动的时刻重算，退款 %s；超过客服授权额度 ¥20.0，需客服主管确认。' % (
        o['orderId'], o['billedMinutes'], idle, stn['name'], yuan(amt)))
    t['refundProposal'] = dict(amount=amt, kind='部分退款', basis='车辆提前停在站点内，锁车报告延迟，按停止移动时刻重算', proposedAt=at + S(10), proposedBy=ACC['agCs'],
                               decision='已确认' if k == 0 else '待主管确认', decidedBy=ACC['csLead'] if k == 0 else None,
                               decidedAt=(at + TD(minutes=48)) if k == 0 else None, overLimit=True)
    t['assignee'] = ACC['cs2']
    if k == 0:
        ct = at + TD(minutes=48)
        p = do_refund(t, o, amt, ct, ACC['csLead'], over=True)
        msg(t, '客服', ct, '主管已确认，按实际骑行时长退款 %s，原路退回。' % yuan(amt))
        t['closedAt'] = ct + TD(minutes=1)
        t['actions'].append(dict(type='超额退款确认', paymentId=p['paymentId'], at=ct, by=ACC['csLead']))
    else:
        msg(t, '客服', at + TD(minutes=3), '已提交客服主管确认，确认后原路退回，会在这里通知你。')
        t['status'] = '待主管确认'

# 4) 原路退款失败 2 单：1 单已人工转账，1 单待用户提供收款账户
for k in range(2):
    o = take(lambda x: x['billedMinutes'] <= 15 and x['fee']['dispatchFee'] == 0 and x['endAt'] < DT(2026, 11, 20))
    u = U_BY[o['userId']]
    at = o['billPushedAt'] + TD(hours=R.randint(1, 30))
    if k == 1:
        at = DT(2026, 11, 29, 15, 20, 0) + S(R.randint(0, 3000))
        o = take(lambda x: x['billedMinutes'] <= 15 and x['fee']['dispatchFee'] == 0 and DT(2026, 11, 26) < x['endAt'] < DT(2026, 11, 29, 12))
        u = U_BY[o['userId']]
    t = ticket(u, at, '费用异议', o, status='已解决' if k == 0 else '处理中')
    amt = o['fee']['channelPaid']
    msg(t, '用户', at, '开锁后车没电，骑不动，这单要退。')
    msg(t, '客服智能体', at + S(7), '订单 %s：%d 分钟，车辆电量在开锁后 2 分钟降至 3%%，运营商同日报告该车电池故障。建议全额退款 %s。' % (o['orderId'], o['billedMinutes'], yuan(amt)))
    by = ACC['cs1']
    ct = at + TD(minutes=R.randint(3, 12))
    t['refundProposal'] = dict(amount=amt, kind='全额退款', basis='车辆电池故障', proposedAt=at + S(7), proposedBy=ACC['agCs'],
                               decision='已确认', decidedBy=by, decidedAt=ct, overLimit=False)
    pf = do_refund(t, o, amt, ct, by, fail='原支付账户已注销，渠道拒绝原路退回')
    msg(t, '客服', ct + S(40), '原路退回没有成功：你付款用的账户已注销。请在下方填写本人实名的银行卡或支付账户，我们会人工转账。')
    t['actions'].append(dict(type='退款', paymentId=pf['paymentId'], at=ct, by=by))
    t['assignee'] = by
    if k == 0:
        t2 = ct + TD(hours=2, minutes=R.randint(0, 50))
        acct = '62%02d **** **** %04d' % (R.randint(10, 30), R.randint(0, 9999))
        msg(t, '用户', t2, '已填写：%s，%s。' % (u['name'], acct))
        pf['payeeSubmitted'] = dict(name=u['name'], bank='示范市商业银行', account=acct, submittedAt=t2)
        mt = t2 + TD(days=1, hours=R.randint(1, 5))
        m = pay('人工转账退款', '骑行收入户', '付', u, amt, '银行转账', mt, order=o['orderId'], relatedPaymentId=pf['paymentId'],
                ticketId=t['ticketId'], operatorAccountId=ACC['fin1'], payee=dict(name=u['name'], bank='示范市商业银行', account=acct))
        o['fee']['refunded'] += amt
        msg(t, '客服', mt + TD(minutes=10), '财务已转账 %s 到你填写的账户，请查收。' % yuan(amt))
        t['closedAt'] = mt + TD(minutes=11)
        t['actions'].append(dict(type='人工转账', paymentId=m['paymentId'], at=mt, by=ACC['fin1']))
    else:
        # 用户已填写本人收款账户，等待财务人工转账
        t2 = ct + TD(hours=1, minutes=12)
        n_ = int(u['userId'][1:])
        acct = '62%02d **** **** %04d' % (10 + n_ % 20, (n_ * 37) % 10000)
        msg(t, '用户', t2, '已填写：%s，%s。' % (u['name'], acct))
        pf['payeeSubmitted'] = dict(name=u['name'], bank='示范市农商银行', account=acct, submittedAt=t2)
        t['awaitingPayee'] = False
        o['refundFailedPaymentId'] = pf['paymentId']

# 5) 出单后退款（10 月底的单，11 月初退款，计入 11 月调整项）——再加 2 单部分退款
for k in range(2):
    o = take(lambda x: DT(2026, 10, 25) <= x['endAt'] < DT(2026, 11, 1) and x['billedMinutes'] >= 8 and x['fee']['channelPaid'] >= 300)
    u = U_BY[o['userId']]
    at = DT(2026, 11, 3, 9, 0, 0) + TD(minutes=R.randint(0, 600))
    t = ticket(u, at, '费用异议', o)
    amt = min(o['fee']['channelPaid'], 300 if k == 0 else 200)
    msg(t, '用户', at, '还车的时候点了好几次还车按钮都没反应，多算了几分钟。')
    msg(t, '客服智能体', at + S(9), '订单 %s：用户在锁车前 %d 分钟内 3 次点击还车，平台记录到运营商锁车回执延迟。建议退还多计的 %d 分钟费用 %s。' % (
        o['orderId'], amt // 50 + 1, amt // 50, yuan(amt)))
    by = ACC['cs3']
    ct = at + TD(minutes=R.randint(4, 30))
    t['refundProposal'] = dict(amount=amt, kind='部分退款', basis='锁车回执延迟导致多计时', proposedAt=at + S(9), proposedBy=ACC['agCs'],
                               decision='已确认', decidedBy=by, decidedAt=ct, overLimit=False)
    p = do_refund(t, o, amt, ct, by)
    msg(t, '客服', ct, '已退还 %s，原路退回。' % yuan(amt))
    t.update(assignee=by, closedAt=ct + TD(minutes=1))
    t['actions'].append(dict(type='退款', paymentId=p['paymentId'], at=ct, by=by))

# 6) 强制结束工单（历史 4 单 + 当前 1 单待处理）
for o in [x for x in ORDERS if 'force' in x['flags']]:
    u = U_BY[o['userId']]
    fe = o['forceEnd']
    at = fe['at'] - TD(minutes=R.randint(6, 25))
    t = ticket(u, at, '订单无法结束', o, entry='骑行中页')
    st = ST_BY[o['endStationId']]
    msg(t, '用户', at, '我已经在%s把车锁好了，页面还在计时。' % st['name'])
    msg(t, '客服智能体', at + S(6), '订单 %s 未收到锁车报告。车辆定位自 %s 起停在%s站点内未移动，已向运营商请求结束骑行两次无回执。建议强制结束并按 %s 计费。' % (
        o['orderId'], F(fe['billedEndAt'])[11:16], st['name'], F(fe['billedEndAt'])[11:16]))
    reason = '用户已在站点内锁车，运营商未上报锁车，车辆定位自 %s 起静止，按实际还车时刻结束' % F(fe['billedEndAt'])[11:16]
    fe['reason'] = reason
    fe['ticketId'] = t['ticketId']
    t['forceEnd'] = dict(at=fe['at'], by=fe['by'], reason=reason, billedEndAt=fe['billedEndAt'])
    msg(t, '客服', fe['at'], '已为你结束订单并锁车，按 %s 还车计费，共 %d 分钟。' % (F(fe['billedEndAt'])[11:16], o['billedMinutes']))
    t.update(assignee=fe['by'], closedAt=fe['at'] + TD(minutes=2))
    t['actions'].append(dict(type='强制结束', at=fe['at'], by=fe['by']))
stuck = next(o for o in ORDERS if 'stuck' in o['flags'])
u = U_BY[stuck['userId']]
t = ticket(u, DT(2026, 12, 1, 11, 32, 8), '订单无法结束', None, entry='骑行中页', status='待处理')
t['orderId'] = stuck['orderId']
stuck['ticketIds'].append(t['ticketId'])
t['facts'] = dict(orderId=stuck['orderId'], startAt=stuck['startAt'], endAt=None, billedMinutes=None, distanceKm=None, receivable=None,
                  payable=None, netReceived=None, inStation=None, inStationBasis=None)
st_near = min(STATIONS, key=lambda s: hav(s['lat'], s['lng'], stuck['startLocation'][1], stuck['startLocation'][0]) if s['stationId'] != stuck['startStationId'] else 1e9)
msg(t, '用户', DT(2026, 12, 1, 11, 32, 8), '早上 8 点就在%s把车锁了，现在页面还在计时，已经三个多小时了。' % st_near['name'])
msg(t, '客服智能体', DT(2026, 12, 1, 11, 32, 15), '订单 %s 未收到锁车报告。车辆定位自 08:02 起停在%s站点内未移动。建议强制结束并按 08:02 计费，约 21 分钟。待客服确认。' % (stuck['orderId'], st_near['name']))
stuck['claimedStationId'] = st_near['stationId']

# 7) 其他咨询类工单
def misc_ticket(cat, user_pred, lines, status='已解决', with_order=None, entry='我的', at=None):
    us = [x for x in USERS if user_pred(x)]
    u = R.choice(us)
    os_ = [o for o in ORDERS if o['userId'] == u['userId'] and o['status'] in ('已结束', '开锁失败')]
    o = R.choice(os_) if (with_order and os_) else None
    if o and at is not None:
        o = max(os_, key=lambda x: x['requestAt'])
    base = (o['billPushedAt'] if o and o.get('billPushedAt') else (o['requestAt'] if o else None))
    if at is None:
        at = (base + TD(minutes=R.randint(3, 120))) if base else rt(max(u['registeredAt'], DT(2026, 9, 5)), NOW - TD(hours=1))
    if at > NOW:
        at = NOW - TD(minutes=R.randint(10, 80))
    t = ticket(u, at, cat, o, entry=entry, status=status)
    k = 0
    for who, text in lines(u, o):
        msg(t, who, at + S(k * R.randint(6, 40)), text)
        k += 1
    by = R.choice([ACC['cs1'], ACC['cs2'], ACC['cs3']])
    t['assignee'] = by if status != '待处理' else None
    if status == '已解决':
        t['closedAt'] = at + S(k * 40 + 60)
    return t


for _ in range(6):
    misc_ticket('旧押金咨询', lambda x: x['realName']['status'] == '已核验' and x['persona'] in ('old_free', 'new_none'),
                lambda u, o: [('用户', R.choice(['我以前在别的单车App交过押金，能转到这里吗？', '原来那个App的押金怎么退？', '之前在简途交了99押金，现在还要再交吗？'])),
                              ('客服智能体', '原品牌的押金由原品牌退还，不转到这里。' + ('你已开通免押，不用再交押金。' if u['deposit'].get('mode') == '免押' else '推荐开通信用免押，就不用再交押金。')),
                              ('用户', '好的，知道了。')])
for _ in range(5):
    misc_ticket('开锁失败', lambda x: any(o['userId'] == x['userId'] and o['status'] == '开锁失败' for o in ORDERS),
                lambda u, o: [('用户', '刚才扫码开不了锁，会扣钱吗？'),
                              ('客服智能体', '开锁失败的订单不计费，你这次没有扣款。附近还有可用车，可以在扫码页查看。'),
                              ('用户', '换了一辆已经骑上了。')], with_order=False, entry='失败提示页')
for _ in range(4):
    misc_ticket('月卡与优惠券', lambda x: x['userId'] in [c['userId'] for c in CARDS],
                lambda u, o: [('用户', R.choice(['月卡是每单都能抵吗？', '为什么这单没有用我的优惠券？', '月卡和优惠券能一起用吗？'])),
                              ('客服智能体', '月卡有效期内每单最多抵扣 ¥3.0；结账时还会自动用一张抵扣最多的优惠券，金额相同时先用快到期的。两者可以一起用。'),
                              ('用户', '明白了。')])
for _ in range(3):
    misc_ticket('稍后扣费咨询', lambda x: any(o['userId'] == x['userId'] and o['degraded'] for o in ORDERS),
                lambda u, o: [('用户', '那天骑车页面写着费用稍后收取，后来扣了，是正常的吗？'),
                              ('客服智能体', '那段时间支付服务临时故障，为了不耽误你骑车，费用在恢复后才收取，金额按开锁时的价格计算，没有多扣。'),
                              ('用户', '好的。')])
# 冻结咨询
for u in [x for x in USERS if x['account']['status'] == '冻结'][:4]:
    at = rt(u['account']['freeze']['at'], NOW - TD(hours=1))
    t = ticket(u, at, '账户冻结', None, entry='失败提示页', status='已解决')
    msg(t, '用户', at, '为什么我的账户被冻结了？')
    if '欠费' in u['account']['freeze']['reason']:
        arrears = sum(o['fee']['unpaid'] for o in ORDERS if o['userId'] == u['userId'] and o['fee'] and o['payStatus'] == '欠费')
        msg(t, '客服智能体', at + S(6), '你有一笔 %s 的欠费超过 30 天未结清，账户已冻结。在扫码页一键补付后会自动解冻。' % yuan(arrears))
    else:
        msg(t, '客服智能体', at + S(6), '降级期间注册时的实名核验在恢复后补做未通过（%s），账户已冻结。完成本人实名核验并结清本次费用后自动解冻。' % u['realName']['failReason'])
    t.update(assignee=ACC['cs1'], closedAt=at + TD(minutes=4))
# 人工解冻的两位用户各一张工单
for u in frz[2:]:
    h = next(x for x in u['account']['history'] if x['action'] == '人工解冻')
    at = h['at'] - TD(minutes=R.randint(15, 60))
    t = ticket(u, at, '账户冻结', None, entry='失败提示页')
    msg(t, '用户', at, '欠费我已经补了，为什么还是显示账户冻结？')
    msg(t, '客服智能体', at + S(7), '欠费已结清。你的实名信息已过期，未能触发自动解冻。建议客服核验本人身份后人工解冻。')
    msg(t, '客服', h['at'], '已核验本人身份，账户已解冻，可以正常骑行。')
    t.update(assignee=h['by'], closedAt=h['at'] + TD(minutes=1))
    t['actions'].append(dict(type='人工解冻', at=h['at'], by=h['by']))
# 退押协助
for u in [x for x in USERS if x['persona'] in ('dep_refunding', 'dep_refunded')][:6]:
    rf = u['deposit']['refund']
    at = rf['appliedAt'] + TD(hours=R.randint(1, 30))
    if at > NOW:
        at = NOW - TD(minutes=R.randint(10, 60))
    awaiting = rf.get('awaitingPayee')
    t = ticket(u, at, '退押', None, entry='押金', status='处理中' if (awaiting or u['deposit']['status'] == '退押中') else '已解决')
    msg(t, '用户', at, '押金什么时候到账？')
    if awaiting:
        msg(t, '客服智能体', at + S(6), '押金 %s 原路退回失败（%s）。请填写本人实名的银行卡或支付账户，由平台公司人工转账。' % (yuan(rf['amount']), next(p for p in PAYMENTS if p['paymentId'] == rf['paymentId'])['failReason']))
    else:
        msg(t, '客服智能体', at + S(6), '押金 %s 已申请退回，预计 %s 前原路到账。' % (yuan(rf['amount']), F(rf['expectedAt'])[:10]))
    t['assignee'] = ACC['cs2']
    if t['status'] == '已解决':
        t['closedAt'] = at + TD(minutes=3)
# 注销
for k in range(4):
    if k < 3:
        u = canc[k]
        at = u['cancelAt'] - TD(hours=3)
        t = ticket(u, at, '注销', None, entry='账户')
        msg(t, '用户', at, '我要注销账户。')
        msg(t, '客服智能体', at + S(5), '你没有欠费和骑行中订单' + ('，押金已申请退回' if u['deposit'].get('mode') == '押金' else '') + '，可以直接在"账户"页注销。注销后订单记录按规定保存。')
        t.update(assignee=ACC['cs3'], closedAt=at + TD(minutes=5))
    else:
        u = next(x for x in USERS if x['persona'] == 'arrears')
        at = NOW - TD(hours=5, minutes=12)
        t = ticket(u, at, '注销', None, entry='账户', status='处理中')
        arrears = sum(o['fee']['unpaid'] for o in ORDERS if o['userId'] == u['userId'] and o['fee'] and o['payStatus'] == '欠费')
        msg(t, '用户', at, '帮我注销账户。')
        msg(t, '客服智能体', at + S(5), '你还有 %s 欠费未结清，结清后才能注销。可以在"账户"页一键补付。' % yuan(arrears))
        t['assignee'] = ACC['cs3']
# 待处理的新工单若干
for _ in range(3):
    misc_ticket('开具发票', lambda x: x['persona'] in ('old_free', 'old_deposit'),
                lambda u, o: [('用户', '这单需要开发票，怎么开？'),
                              ('客服智能体', '骑行发票由客服人工开具。请提供发票抬头和税号，开好后发送到你留的邮箱。'),
                              ('用户', '抬头是个人，邮箱已在资料里。'),
                              ('客服', '已开具电子发票并发送到你的邮箱，请查收。')], with_order=True, entry='我的订单')
for _ in range(2):
    misc_ticket('旧押金咨询', lambda x: x['persona'] == 'old_deposit',
                lambda u, o: [('用户', '我在原来品牌的骑行卡还有 20 多天，能转过来吗？'),
                              ('客服智能体', '原品牌的骑行卡、余额和押金由原品牌处理，不转到这里。这里可以购买月卡，每单最多抵扣 ¥3.0。'),
                              ('用户', '好吧。')])
for _ in range(2):
    misc_ticket('费用咨询', lambda x: x['persona'] == 'old_deposit',
                lambda u, o: [('用户', '这单为什么要我自己点支付，不能自动扣吗？'),
                              ('客服智能体', '你是缴押金用户，骑完在账单页一键支付即可；24 小时内未支付会自动从押金抵扣并通知你。也可以改为开通信用免押，骑完自动扣款。')],
                status='待处理', with_order=True, at=NOW - TD(minutes=R.randint(15, 70)))

TICKETS.sort(key=lambda t: t['createdAt'])

# 欠费、押金不足状态写回用户
for u in USERS:
    arrears = sum(o['fee']['unpaid'] for o in ORDERS if o['userId'] == u['userId'] and o['fee'] and o['payStatus'] == '欠费')
    u['arrears'] = arrears
    dpo = u['deposit']
    if dpo.get('mode') == '押金' and dpo['status'] == '已缴' and dpo['balance'] < dpo['amountPaid']:
        dpo['status'] = '押金不足'

# ---------------------------------------------------------------- 订单实收与结算月
for o in ORDERS:
    f = o.get('fee')
    if f:
        f['netReceived'] = f['channelPaid'] + f['depositDeducted'] - f['refunded']
        if f['refunded'] == 0:
            o['refundStatus'] = '无'
        elif f['refunded'] >= f['channelPaid'] + f['depositDeducted']:
            o['refundStatus'] = '全额退款'
        else:
            o['refundStatus'] = '部分退款'
        if o.get('refundFailedPaymentId'):
            o['refundStatus'] = '原路失败·待人工转账'
    else:
        o['refundStatus'] = None
    o['settleMonth'] = o['endAt'].strftime('%Y-%m') if (o['status'] == '已结束' and 'pending_lock' not in o['flags']) else None
# 待人工的缺锁车事实单：没有结束事实
pend_lock['status'] = '待人工处理'
pend_lock['settleMonth'] = None
for k in ('endAt', 'lockReportAt', 'pileInAt', 'reportReceivedAt', 'endStationId', 'endPileId', 'endLocation', 'inStation', 'inStationBasis',
          'distanceToStationM', 'durationSec', 'billedMinutes', 'distanceKm', 'billPushedAt'):
    pend_lock[k] = None
pend_lock['endMethod'] = None
pend_lock['userClaimedEndAt'] = dg3['enter'] + TD(minutes=24)
pend_lock['anomalyNote'] = '降级期间开锁成功，运营商补报窗口已过仍未收到锁车报告'
# 释放该车占用后，若车辆已被他人使用也无妨（缺锁车事实本身就是异常）

# 降级闭合结果
for o in ORDERS:
    if not o['degraded']:
        continue
    d = DG_BY[o['degraded']['eventId']]
    if 'pending_lock' in o['flags']:
        o['degraded']['closure'] = dict(unlockResult=True, lockFact=False, payment='未收款', status='待人工', missing=['锁车事实', '收款结果'], closedAt=None,
                                        note='运营商补报窗口（72 小时）已过仍未收到锁车报告，用户称 21:30 前后已在站点锁车')
    elif 'pending_pay' in o['flags']:
        o['degraded']['closure'] = dict(unlockResult=True, lockFact=True, payment='结果未知', status='待人工', missing=['收款结果'], closedAt=None,
                                        note='恢复后发起免押扣款，渠道未返回结果，需人工向渠道查询')
    else:
        pay_res = '已收款' if o['payStatus'] in ('已支付', '押金抵扣', '无需支付', '欠费已补付') else ('已转欠费' if o['payStatus'] == '欠费' else '已推送一键支付')
        o['degraded']['closure'] = dict(unlockResult=True, lockFact=True, payment=pay_res, status='已闭合', missing=[],
                                        closedAt=max(o['billPushedAt'], d['exit']) + TD(minutes=R.randint(1, 8)), note=None)

# ---------------------------------------------------------------- 异议
DISPUTES = []
SETTLEMENTS = []
GEN = {m: DT(int(m[:4]), int(m[5:]) + 1 if int(m[5:]) < 12 else 1, 1, 0, 30, 0) for m in ['2026-09', '2026-10', '2026-11']}
GEN['2026-12'] = DT(2027, 1, 1, 0, 30, 0)
MONTHS = ['2026-09', '2026-10', '2026-11']


def corrections_by(o, T):
    return sum(c['delta'] for c in o.get('baseCorrections', []) if c['at'] <= T)


def refunded_by(o, T):
    s = 0
    for p in PAYMENTS:
        if p['orderId'] == o['orderId'] and p['type'] in ('退款', '人工转账退款') and p['status'] == '成功' and p['completedAt'] <= T:
            s += p['amount']
    return s


def base_at(o, T):
    f = o['fee']
    if f is None or f['receivable'] == 0:
        return 0
    rf = refunded_by(o, T)
    paid = f['channelPaid'] + f['depositDeducted']
    if rf > 0 and rf >= paid:
        return 0
    return f['receivable'] - rf + corrections_by(o, T)


def ratio_of(o):
    return VMAP[o['pricingVersionId']]['ratios'][o['brandId']]


def fee_of(base, ratio):
    return half_up(base * ratio)


ds_seq = [0]


def dispute(op, sid, orders, field, claim, fact, compare, status, submitted, decided=None, by=None, delta=None):
    ds_seq[0] += 1
    d = dict(disputeId='DS%s%03d' % (submitted.strftime('%Y%m'), ds_seq[0]), operatorId=op, settlementId=sid, orderIds=[o['orderId'] for o in orders],
             field=field, operatorClaim=claim, platformFact=fact, agentComparison=compare, status=status, submittedAt=submitted,
             submittedBy={'OP01': ACC['op1biz'], 'OP02': ACC['op2biz'], 'OP03': ACC['op3biz']}[op], decidedAt=decided, decidedBy=by,
             adjustmentId=None)
    for o in orders:
        o['disputeIds'].append(d['disputeId'])
        if delta is not None and status == '已采纳':
            o.setdefault('baseCorrections', []).append(dict(at=decided, delta=delta, disputeId=d['disputeId']))
    DISPUTES.append(d)
    return d


def pick_orders(op, month, n, pred=lambda o: True):
    c = [o for o in ORDERS if o['operatorId'] == op and o['settleMonth'] == month and o['fee'] and o['fee']['receivable'] > 0 and not o['lateReport']
         and o['refundStatus'] == '无' and not o['disputeIds'] and pred(o)]
    return R.sample(c, n)


o1 = pick_orders('OP01', '2026-09', 2, lambda o: o['pileInAt'] is not None)
dispute('OP01', 'ST-M-202609-OP01', o1, '时长', '我方车端锁车时刻比平台记录晚 1～2 分钟，应按锁车时刻计时长',
        '两单均有桩在位记录，桩在位时刻早于锁车报告；按 BR-01 以较早时刻结束',
        '对账智能体比对：%s 桩在位 %s、锁车报告 %s；%s 桩在位 %s、锁车报告 %s。平台计时与规则一致，建议驳回。' % (
            o1[0]['orderId'], F(o1[0]['pileInAt'])[11:], F(o1[0]['lockReportAt'])[11:], o1[1]['orderId'], F(o1[1]['pileInAt'])[11:], F(o1[1]['lockReportAt'])[11:]),
        '已驳回', DT(2026, 10, 3, 10, 12, 0), DT(2026, 10, 8, 15, 40, 0), ACC['fin1'])
o2 = pick_orders('OP01', '2026-10', 1)
dispute('OP01', 'ST-M-202610-OP01', o2, '里程', '我方车端里程为 %.1f 公里，比平台多 0.6 公里' % (o2[0]['distanceKm'] + 0.6),
        '里程不参与计价，订单应收不变', '对账智能体比对：计价规则只按时长计费，里程差异不影响服务费基数，建议无需调整。',
        '无需调整', DT(2026, 11, 2, 16, 5, 0), DT(2026, 11, 4, 11, 20, 0), ACC['fin2'])
o3 = pick_orders('OP02', '2026-10', 1, lambda o: o['pileInAt'] is None and o['inStation'] and o['pricingVersionId'] == 'PR0002' and o['endMethod'] == '用户锁车')
o3o = o3[0]
v3 = VMAP[o3o['pricingVersionId']]
new_min = o3o['billedMinutes'] + 6
delta3 = (v3['startFee'] + max(0, new_min - 3) * v3['perMin']) - o3o['fee']['rideFee']
dispute('OP02', 'ST-M-202610-OP02', o3, '时长', '车端时钟偏差导致锁车时刻上报偏早，实际骑行 %d 分钟' % new_min,
        '平台记录 %d 分钟；该站点无桩，结束时刻以运营商锁车报告为准' % o3o['billedMinutes'],
        '对账智能体比对：车辆定位在平台记录的结束时刻后仍移动约 6 分钟，%s 后才静止于站点围栏内；运营商主张与轨迹一致。建议采纳，服务费基数按 %d 分钟计，用户侧不补收。' % (
            F(o3o['endAt'] + TD(minutes=6))[11:16], new_min),
        '已采纳', DT(2026, 11, 3, 9, 30, 0), DT(2026, 11, 10, 14, 2, 0), ACC['fin1'], delta=delta3)
o4 = pick_orders('OP03', '2026-10', 3)
dispute('OP03', 'ST-M-202610-OP03', o4, '时长', '三单锁车报告丢失后补报，平台按首次锁车报告计时，少计 3～5 分钟',
        '三单均有一次锁车报告，未收到补报', '对账智能体比对：三单车辆定位在平台结束时刻后静止，未见继续骑行；运营商未提供补报记录。建议驳回，待财务确认。',
        '待核实', DT(2026, 11, 25, 17, 20, 0))
o5 = pick_orders('OP01', '2026-10', 1, lambda o: o['brandId'] == 'BR02')
dispute('OP01', 'ST-M-202610-OP01', o5, '时长', '该单车辆在骑行中途断电重启，我方记录骑行 %d 分钟' % (o5[0]['billedMinutes'] + 3),
        '平台记录 %d 分钟' % o5[0]['billedMinutes'], '对账智能体比对：车辆在骑行中途有 3 分钟定位缺失，缺失前后位置相距 0.9 公里，运营商主张有一定依据。建议采纳 +3 分钟，待财务确认。',
        '待核实', DT(2026, 11, 28, 10, 45, 0))

# ---------------------------------------------------------------- 月结算单
card_income = {}
for c in CARDS:
    p = next(x for x in PAYMENTS if x['paymentId'] == c['paymentId'])
    m = p['completedAt'].strftime('%Y-%m')
    card_income[m] = card_income.get(m, 0) + c['price']
adj_seq = {}


def settled_orders_for(op, m):
    return [o for o in ORDERS if o['operatorId'] == op and o['settleMonth'] == m and o['reportReceivedAt'] <= GEN[m]]


SETTLE_STATE = {}  # orderId -> (base, fee) 已结算到的累计值
for mi, m in enumerate(MONTHS):
    gen = GEN[m]
    prev = GEN[MONTHS[mi - 1]] if mi > 0 else None
    # 月卡收入按单数分到各品牌（全平台）
    card_orders = [o for o in ORDERS if o['settleMonth'] == m and o['reportReceivedAt'] <= gen and o['fee'] and o['fee']['monthCardDeduct'] > 0]
    cnt = {}
    for o in card_orders:
        cnt[o['brandId']] = cnt.get(o['brandId'], 0) + 1
    tot = sum(cnt.values())
    inc = card_income.get(m, 0)
    share = {}
    if tot:
        raw = {b: inc * n / tot for b, n in cnt.items()}
        share = {b: int(math.floor(x)) for b, x in raw.items()}
        left = inc - sum(share.values())
        for b in sorted(raw, key=lambda b: -(raw[b] - share[b]))[:left]:
            share[b] += 1
    for op in ['OP01', 'OP02', 'OP03']:
        sid = 'ST-M-%s-%s' % (m.replace('-', ''), op)
        dets = []
        for o in sorted(settled_orders_for(op, m), key=lambda o: o['endAt']):
            b = base_at(o, gen)
            ratio = ratio_of(o)
            fee = fee_of(b, ratio)
            remark = None
            if o['fee']['receivable'] == 0:
                remark = '超时后才报告开锁成功，0 元'
            elif b == 0:
                remark = '已全额退款，不计'
            elif refunded_by(o, gen) > 0:
                remark = '已部分退款，按退款后金额计'
            if o['degraded']:
                remark = (remark + '；' if remark else '') + '降级单'
            if o['payStatus'] in ('欠费', '待支付'):
                remark = (remark + '；' if remark else '') + '用户未付（按应收计）'
            dets.append(dict(orderId=o['orderId'], brandId=o['brandId'], startAt=o['startAt'], endAt=o['endAt'], billedMinutes=o['billedMinutes'],
                             distanceKm=o['distanceKm'], degraded=bool(o['degraded']), pricingVersionId=o['pricingVersionId'],
                             receivable=o['fee']['receivable'], refundedAtGeneration=refunded_by(o, gen), base=b, ratio=ratio, fee=fee, remark=remark))
            SETTLE_STATE[o['orderId']] = (b, fee)
            o['settlement'] = dict(settlementId=sid, kind='明细', adjustmentId=None)
        adjs = []
        if prev:
            # 晚到补报：上一结算月及更早、在上次出单后才收到报告
            late = [o for o in ORDERS if o['operatorId'] == op and o['settleMonth'] and o['settleMonth'] < m and o['orderId'] not in SETTLE_STATE
                    and prev < o['reportReceivedAt'] <= gen]
            # 出单后退款或异议更正
            changed = []
            for o in ORDERS:
                if o['operatorId'] != op or o['orderId'] not in SETTLE_STATE or o['orderId'] in [x['orderId'] for x in late]:
                    continue
                nb = base_at(o, gen)
                ob, of = SETTLE_STATE[o['orderId']]
                if nb != ob:
                    changed.append((o, ob, of, nb))
            groups = {}
            for o in late:
                groups.setdefault(('晚到补报', o['brandId']), []).append((o, 0, 0, base_at(o, gen)))
            for (o, ob, of, nb) in changed:
                typ = '异议核实' if o.get('baseCorrections') and any(prev < c['at'] <= gen for c in o['baseCorrections']) else '出单后退款'
                groups.setdefault((typ, o['brandId']), []).append((o, ob, of, nb))
            for (typ, b), items in sorted(groups.items()):
                adj_seq[sid] = adj_seq.get(sid, 0) + 1
                aid = 'ADJ-%s-%s-%02d' % (m.replace('-', ''), op, adj_seq[sid])
                lines = []
                for (o, ob, of, nb) in items:
                    ratio = ratio_of(o)
                    nf = fee_of(nb, ratio)
                    lines.append(dict(orderId=o['orderId'], endAt=o['endAt'], previousBase=ob, newBase=nb, baseDelta=nb - ob, ratio=ratio,
                                      feeDelta=nf - of))
                    SETTLE_STATE[o['orderId']] = (nb, nf)
                    if typ == '晚到补报':
                        o['settlement'] = dict(settlementId=sid, kind='调整项', adjustmentId=aid)
                n = len(items)
                bname = BRANDS[b]['name']
                if typ == '晚到补报':
                    ends = sorted(set(F(x[0]['endAt'])[5:10].replace('-', ' 月 ', 1) + ' 日' for x in items))
                    reason = '%s %s结束、出单后才收到运营商补报的 %d 单，按原结算月计价后计入本期' % (bname, '、'.join(e.lstrip('0') for e in ends), n)
                elif typ == '异议核实':
                    dsid = items[0][0]['baseCorrections'][0]['disputeId']
                    reason = '%s 异议 %s 经对账智能体比对、财务核实采纳，%d 单服务费基数更正' % (bname, dsid, n)
                    for d in DISPUTES:
                        if d['disputeId'] == dsid:
                            d['adjustmentId'] = aid
                else:
                    reason = '%s 上期已结算的 %d 单在出单后发生退款，按退款后金额更正' % (bname, n)
                creator = ACC['agRecon']
                adjs.append(dict(adjustmentId=aid, type=typ, brandId=b, items=lines, baseDelta=sum(x['baseDelta'] for x in lines),
                                 feeDelta=sum(x['feeDelta'] for x in lines), reason=reason, createdBy=creator, createdAt=gen + TD(minutes=2),
                                 confirmedBy=None, confirmedAt=None))
        # 品牌汇总
        brands = []
        for b in [x for x in BRANDS if BRANDS[x]['op'] == op]:
            bd = [d for d in dets if d['brandId'] == b]
            if not bd and b not in ('BR01', 'BR02', 'BR03', 'BR04'):
                continue
            ratios = {}
            for d in bd:
                k = (d['ratio'], d['pricingVersionId'])
                r_ = ratios.setdefault(d['ratio'], dict(ratio=d['ratio'], pricingVersionIds=set(), orderCount=0, baseSum=0, fee=0))
                r_['pricingVersionIds'].add(d['pricingVersionId'])
                r_['orderCount'] += 1
                r_['baseSum'] += d['base']
                r_['fee'] += d['fee']
            brands.append(dict(brandId=b, orderCount=len(bd), receivableSum=sum(d['receivable'] for d in bd), baseSum=sum(d['base'] for d in bd),
                               detailFee=sum(d['fee'] for d in bd),
                               ratioBreakdown=[dict(ratio=r_['ratio'], pricingVersionIds=sorted(r_['pricingVersionIds']), orderCount=r_['orderCount'],
                                                    baseSum=r_['baseSum'], fee=r_['fee']) for r_ in sorted(ratios.values(), key=lambda x: x['ratio'])],
                               adjustmentFee=sum(a['feeDelta'] for a in adjs if a['brandId'] == b),
                               monthCardOrderCount=cnt.get(b, 0), monthCardShare=share.get(b, 0)))
        detail_fee = sum(d['fee'] for d in dets)
        adj_fee = sum(a['feeDelta'] for a in adjs)
        st = dict(settlementId=sid, operatorId=op, month=m, generatedAt=gen, dueDate=(gen + TD(days=5)).strftime('%Y-%m-%d'),
                  status=None, reviewedBy=None, reviewedAt=None, sentAt=None, paidAt=None, bankTradeNo=None, invoiceStatus=None,
                  totals=dict(orderCount=len(dets), receivableSum=sum(d['receivable'] for d in dets), baseSum=sum(d['base'] for d in dets),
                              detailFee=detail_fee, adjustmentFee=adj_fee, payableFee=detail_fee + adj_fee),
                  monthCard=dict(platformIncome=inc, allocationRule='按骑行单数', platformCardOrders=tot,
                                 note='服务费基数取订单应收，月卡收入分摊仅作收入归属展示，不进入服务费计算'),
                  brands=brands, adjustments=adjs, details=dets, note=None)
        if m != '2026-11':
            rv = gen.replace(hour=9, minute=0) + TD(days=1, hours=R.randint(0, 5), minutes=R.randint(0, 59))
            snt = rv + TD(hours=R.randint(1, 4))
            pd_ = gen + TD(days=4, hours=R.randint(9, 16), minutes=R.randint(0, 59)) if not (m == '2026-10' and op == 'OP03') else gen + TD(days=5, hours=10, minutes=12)
            st.update(status='已到账', reviewedBy=ACC['fin2'], reviewedAt=rv, sentAt=snt, paidAt=pd_, bankTradeNo=trade_no('银行转账', pd_),
                      invoiceStatus='已收到发票')
            for a in adjs:
                a['confirmedBy'] = ACC['fin2']
                a['confirmedAt'] = rv - TD(minutes=20)
        else:
            st.update(status='待审核')
            if op == 'OP03':
                st['note'] = '运营商已于 2026-11-20 退出，本期为最后一期结算'
        SETTLEMENTS.append(st)

# 退出前的 OP03 车辆清单、11 月底的未结算单（12 月）
for o in ORDERS:
    if 'settlement' not in o:
        o['settlement'] = None
        if o['settleMonth'] and o['settleMonth'] < '2026-12' and o['status'] == '已结束':
            o['settlementNote'] = '出单后才收到补报，计入 2026-12 结算单调整项'
    if o['settleMonth'] == '2026-11' and o['settlement'] is None and o['status'] == '已结束':
        o['settlementNote'] = '出单后才收到补报，计入 2026-12 结算单调整项'
    if o['settleMonth'] and o['settleMonth'] >= '2026-12':
        o['settlementNote'] = '归属 2026-12 结算月，2027-01-01 出单'

# 出单后（11 月结算单出单后）发生的退款，在订单上标注将计入 12 月调整项
for o in ORDERS:
    if o.get('settlement') and o['settlement']['settlementId'].startswith('ST-M-202611'):
        if base_at(o, NOW) != SETTLE_STATE[o['orderId']][0]:
            o['settlementNote'] = '出单后发生退款，差额计入 2026-12 结算单调整项'

# ---------------------------------------------------------------- 收单对账（按资金事件日）
# 两笔跨日到账：渠道到账日为次日；一笔渠道未到账待核实
chan_events = [p for p in PAYMENTS if p['account'] == '骑行收入户' and p['channel'] in ('微信支付', '支付宝') and p['status'] == '成功']
for p in chan_events:
    p['channelSettledDate'] = p['completedAt'].strftime('%Y-%m-%d')
lag_targets = []
for day in [DT(2026, 10, 9), DT(2026, 11, 21)]:
    c = [p for p in chan_events if p['type'] in ('订单扣款', '订单支付') and p['completedAt'].date() == day.date() and p['completedAt'].hour >= 20]
    if not c:
        c = [p for p in chan_events if p['type'] in ('订单扣款', '订单支付') and p['completedAt'].date() == day.date()]
    p = max(c, key=lambda x: x['completedAt'])
    p['completedAt'] = p['completedAt'].replace(hour=23, minute=59, second=R.randint(51, 58))
    p['createdAt'] = p['completedAt']
    p['channelSettledDate'] = (day + TD(days=1)).strftime('%Y-%m-%d')
    lag_targets.append(p)
miss = [p for p in chan_events if p['type'] == '订单扣款' and p['channel'] == '支付宝' and p['completedAt'].date() == DT(2026, 11, 30).date()]
missing_p = miss[len(miss) // 2] if miss else None
if missing_p:
    missing_p['channelSettledDate'] = None
    missing_p['note'] = '支付宝对账单未包含该笔，待向渠道核实'
RECON = []
day = START
while day < DT(2026, 12, 1):
    ds = day.strftime('%Y-%m-%d')
    actual = {'微信支付': 0, '支付宝': 0}
    for p in chan_events:
        if p['channelSettledDate'] == ds:
            sign = 1 if p['direction'] == '收' else -1
            actual[p['channel']] += sign * p['amount']
    ev = [p for p in PAYMENTS if p['account'] == '骑行收入户' and p['status'] == '成功' and p['completedAt'] and p['completedAt'].strftime('%Y-%m-%d') == ds]
    ord_pay = sum(p['amount'] for p in ev if p['type'] in ('订单扣款', '订单支付', '欠费补付'))
    card = sum(p['amount'] for p in ev if p['type'] == '月卡购买')
    ref_o = sum(p['amount'] for p in ev if p['type'] == '退款')
    ref_m = sum(p['amount'] for p in ev if p['type'] == '人工转账退款')
    dep_ev = [p for p in PAYMENTS if p['type'] == '押金抵扣' and p['status'] == '成功' and p['completedAt'].strftime('%Y-%m-%d') == ds]
    dep = sum(p['amount'] for p in dep_ev)
    net = ord_pay + dep - ref_o - ref_m
    expected = net + card - dep
    total = actual['微信支付'] + actual['支付宝']
    items = []
    for p in PAYMENTS:
        if p['type'] == '人工转账退款' and p['status'] == '成功' and p['completedAt'].strftime('%Y-%m-%d') == ds:
            items.append(dict(type='人工转账退款不经收单渠道', amount=p['amount'], paymentId=p['paymentId'], orderId=p['orderId'], status='已处理',
                              handledBy=ACC['fin1'], note='原路退款失败后由银行转账退回，渠道无对应流水'))
    for p in lag_targets:
        if p['completedAt'].strftime('%Y-%m-%d') == ds:
            items.append(dict(type='跨日到账', amount=-p['amount'], paymentId=p['paymentId'], orderId=p['orderId'], status='已处理', handledBy=ACC['fin1'],
                              note='%s 支付，渠道次日入账' % F(p['completedAt'])))
        if p['channelSettledDate'] == ds:
            items.append(dict(type='跨日到账', amount=p['amount'], paymentId=p['paymentId'], orderId=p['orderId'], status='已处理', handledBy=ACC['fin1'],
                              note='前一日 %s 的支付在本日入账' % F(p['completedAt'])[11:]))
    if missing_p and missing_p['completedAt'].strftime('%Y-%m-%d') == ds:
        items.append(dict(type='渠道未到账', amount=-missing_p['amount'], paymentId=missing_p['paymentId'], orderId=missing_p['orderId'], status='待处理',
                          handledBy=None, note='平台记录扣款成功，支付宝对账单无此笔，待向渠道核实'))
    diff = total - expected
    assert diff == sum(i['amount'] for i in items), (ds, diff, items)
    status = '平' if not items else ('差异待处理' if any(i['status'] == '待处理' for i in items) else '差异已处理')
    RECON.append(dict(reconId='RC' + day.strftime('%Y%m%d'), date=ds, generatedAt=day + TD(days=1, hours=2, minutes=10),
                      channelActual=dict(wechat=actual['微信支付'], alipay=actual['支付宝'], total=total),
                      orderPayments=ord_pay, depositDeducted=dep, refundsOriginal=ref_o, refundsManual=ref_m, orderNetReceived=net,
                      monthCardIncome=card, expected=expected, diff=diff, diffItems=items, status=status))
    day += TD(days=1)

# ---------------------------------------------------------------- 车辆最终状态
last_end = {}
for o in ORDERS:
    if o['status'] == '已结束' and o['endLocation']:
        if o['vehicleId'] not in last_end or o['lockReportAt'] > last_end[o['vehicleId']]['lockReportAt']:
            last_end[o['vehicleId']] = o
riding = {o['vehicleId']: o for o in ORDERS if o['status'] == '骑行中'}
pile_station_vehicles = {}
for v in VEHICLES:
    vid_ = v['vehicleId']
    v['battery'] = R.randint(18, 100) if R.random() > 0.07 else R.randint(3, 14)
    if vid_ in riding:
        o = riding[vid_]
        v['status'] = '骑行中'
        v['location'] = o['startLocation']
        v['lastReportAt'] = F(NOW - S(R.randint(2, 30)))
        continue
    if vid_ in last_end and (last_end[vid_]['lockReportAt'] > NOW - TD(hours=36) or R.random() < 0.3):
        o = last_end[vid_]
        v['location'] = o['endLocation']
        v['stationId'] = o['endStationId'] if o['inStation'] else None
        v['lastReportAt'] = F(max(o['lockReportAt'], NOW - TD(minutes=R.randint(1, 20))))
    else:
        # 运营商日常调度后的位置
        if R.random() < 0.22:
            s = R.choice(STATIONS)
            la, ln = off(s['lat'], s['lng'], R.uniform(-6, 6), R.uniform(-6, 6))
            v['stationId'] = s['stationId']
        else:
            s = R.choice(STATIONS)
            la, ln = off(s['lat'], s['lng'], R.uniform(80, 420) * R.choice([-1, 1]), R.uniform(80, 420) * R.choice([-1, 1]))
        v['location'] = [round(ln, 6), round(la, 6)]
        v['lastReportAt'] = F(NOW - TD(minutes=R.randint(1, 40)))
    r = R.random()
    if v['codeStatus'] == '待启用':
        v['status'] = '码未启用'
    elif v['codeStatus'] == '已失效':
        v['status'] = '码已失效'
        v['stationId'] = None
        v['lastReportAt'] = F(DT(2026, 11, 20, 9, 0, 0) + TD(minutes=R.randint(0, 50)))
    elif v['battery'] < 15:
        v['status'] = '低电量'
    elif r < 0.02:
        v['status'] = '故障'
    elif r < 0.03:
        v['status'] = '离线'
        v['lastReportAt'] = F(NOW - TD(hours=R.randint(3, 40)))
    else:
        v['status'] = '可用'
    if v['stationId']:
        pile_station_vehicles.setdefault(v['stationId'], []).append(v)
for sid, vs in pile_station_vehicles.items():
    s = ST_BY[sid]
    online = [PILE_BY[p] for p in s['pileIds'] if PILE_BY[p]['status'] == '在线']
    for v, p in zip(vs, online):
        p['occupiedVehicleId'] = v['vehicleId']
        v['pileId'] = p['pileId']
        v['location'] = p['location']
# 让 2 个站点桩满（演示 S-06）
full_sts = [s for s in PILE_STS if all(PILE_BY[p]['status'] == '在线' for p in s['pileIds'])][:2]
spare = [v for v in VEHICLES if v['status'] == '可用' and v['stationId'] is None and v['brandId'] in ('BR01', 'BR03')]
for s in full_sts:
    for p in s['pileIds']:
        pl = PILE_BY[p]
        if pl['occupiedVehicleId'] is None:
            v = spare.pop()
            v['stationId'] = s['stationId']
            v['pileId'] = p
            v['location'] = pl['location']
            pl['occupiedVehicleId'] = v['vehicleId']
for s in STATIONS:
    s['fullNow'] = bool(s['pileIds']) and all(PILE_BY[p]['occupiedVehicleId'] or PILE_BY[p]['status'] != '在线' for p in s['pileIds'])

# ---------------------------------------------------------------- 运行监控
OPS_DAILY, OPS_HOURLY, PLATFORM_DAILY = [], [], []
day = START
while day < DT(2026, 12, 1):
    for b in ['BR01', 'BR02', 'BR03', 'BR04']:
        if b == 'BR04' and day >= DT(2026, 11, 20):
            continue
        base_u = {'BR01': 0.985, 'BR02': 0.975, 'BR03': 0.982, 'BR04': 0.962}[b]
        base_a = {'BR01': 0.93, 'BR02': 0.91, 'BR03': 0.925, 'BR04': 0.88}[b]
        u_ = base_u + R.uniform(-0.008, 0.008)
        a_ = base_a + R.uniform(-0.02, 0.015)
        if b == 'BR02' and day.date() == DT(2026, 10, 22).date():
            u_, a_ = 0.902, 0.85
        if b == 'BR02' and day >= DT(2026, 11, 29):
            u_, a_ = (0.921, 0.86) if day.date() == DT(2026, 11, 29).date() else (0.874, 0.81)
        if b == 'BR04' and day >= DT(2026, 11, 1):
            u_ -= 0.03 * (day - DT(2026, 11, 1)).days / 19
            a_ -= 0.06 * (day - DT(2026, 11, 1)).days / 19
        OPS_DAILY.append(dict(date=day.strftime('%Y-%m-%d'), brandId=b, unlockSuccessRate=round(u_, 4),
                              availability=round(a_, 4), rentableVehicles=int(BRANDS[b]['n'] * a_)))
    PLATFORM_DAILY.append(dict(date=day.strftime('%Y-%m-%d'), entryAvailability=round(R.uniform(0.9985, 1.0), 4) if not any(d['enter'].date() == day.date() for d in DG) else round(R.uniform(0.993, 0.997), 4),
                               unlockP90Sec=round(R.uniform(2.4, 3.6), 1), billPushP90Sec=round(R.uniform(3.1, 6.2), 1)))
    day += TD(days=1)
h = NOW - TD(hours=72)
while h < NOW:
    for b in ['BR01', 'BR02', 'BR03']:
        u_ = {'BR01': 0.985, 'BR02': 0.975, 'BR03': 0.982}[b] + R.uniform(-0.01, 0.008)
        a_ = {'BR01': 0.93, 'BR02': 0.91, 'BR03': 0.925}[b] + R.uniform(-0.02, 0.015)
        if b == 'BR02' and h >= DT(2026, 11, 30, 4):
            u_ = R.uniform(0.84, 0.885)
            a_ = R.uniform(0.78, 0.83)
        if b == 'BR02' and h >= DT(2026, 12, 1, 6):
            u_ = R.uniform(0.885, 0.905)
            a_ = R.uniform(0.82, 0.86)
        paused = b == 'BR02' and h >= DT(2026, 11, 30, 8)
        OPS_HOURLY.append(dict(at=F(h), brandId=b, paused=paused, unlockSuccessRate=round(u_, 4), availability=round(a_, 4),
                               rentableVehicles=0 if paused else int(BRANDS[b]['n'] * a_)))
    h += TD(hours=1)

PAUSES = [
    dict(id='OPS-PS-20261022-01', brandId='BR02', pausedAt=DT(2026, 10, 22, 9, 5, 0), resumedAt=DT(2026, 10, 22, 12, 40, 0),
         metric='开锁成功率', value=0.874, threshold=0.90, sustainMinutes=125, by=ACC['agOps'], note='运营商车联网平台升级导致开锁结果回报延迟；恢复后自动回到已接入'),
    dict(id='OPS-PS-20261108-01', brandId='BR04', pausedAt=DT(2026, 11, 8, 14, 10, 0), resumedAt=DT(2026, 11, 9, 10, 20, 0),
         metric='可用率', value=0.762, threshold=0.80, sustainMinutes=130, by=ACC['agOps'], note='大批车辆电量不足未及时换电；恢复后自动回到已接入'),
    dict(id='OPS-PS-20261130-01', brandId='BR02', pausedAt=DT(2026, 11, 30, 8, 0, 0), resumedAt=None,
         metric='开锁成功率', value=0.861, threshold=0.90, sustainMinutes=240, by=ACC['agOps'], note='自 11-30 04:00 起开锁成功率持续低于 90%，运营商反馈车载通信模块固件问题，正在分批升级'),
]
ALERTS = []
al_seq = [0]


def alert(at, level, typ, text, brand=None, ev=None, ack=None, ackAt=None):
    al_seq[0] += 1
    ALERTS.append(dict(alertId='OPS-AL-%s-%02d' % (at.strftime('%Y%m%d'), al_seq[0]), at=at, level=level, type=typ, text=text, brandId=brand,
                       degradeEventId=ev, notified=[ACC['duty1'], ACC['duty2']], ackBy=ack, ackAt=ackAt))


DG_OUT = []
for d in DG:
    dos = [o for o in ORDERS if o['degraded'] and o['degraded']['eventId'] == d['id']]
    ver = ver_at(d['enter'])
    by_hour = {}
    for o in dos:
        k = o['requestAt'].strftime('%Y-%m-%d %H:00')
        by_hour[k] = by_hour.get(k, 0) + 1
    adm = {}
    for o in dos:
        adm[o['degraded']['admission']] = adm.get(o['degraded']['admission'], 0) + 1
    amt = sum((o['fee']['receivable'] if o['fee'] else 0) for o in dos)
    timeline = [dict(at=d['enter'] - TD(minutes=3), by=ACC['agOps'], action='检测到故障', text='检测到%s，判断仍可请求运营商开锁' % d['cause']),
                dict(at=d['enter'], by=ACC['agOps'], action='降级进入', text='自动进入降级开单并通知值班人'),
                dict(at=d['enter'] + TD(minutes=2), by=ACC['duty1'] if d is not DG[1] else ACC['duty2'], action='值班人确认', text='已知悉，保持降级开单，持续关注限额')]
    if d['tighten']:
        timeline.append(dict(at=d['tighten'], by=ACC['duty2'], action='降级收紧', text='降级已持续超过 30 分钟，暂停押金状态未知用户与先骑后验新用户的降级开单'))
    timeline.append(dict(at=d['exit'], by=ACC['agOps'], action='降级退出', text='故障恢复，退出降级开单，开始恢复闭合'))
    pend = [o['orderId'] for o in dos if o['degraded']['closure']['status'] == '待人工']
    closed_at = max(o['degraded']['closure']['closedAt'] for o in dos if o['degraded']['closure']['closedAt'])
    if not pend:
        timeline.append(dict(at=closed_at, by=ACC['agRecon'], action='全部闭合', text='降级单 %d 笔全部匹配运营商报告并计价、收款结果已落定' % len(dos)))
    else:
        timeline.append(dict(at=closed_at, by=ACC['agRecon'], action='部分闭合', text='降级单 %d 笔，%d 笔闭合，%d 笔缺项待人工处理' % (len(dos), len(dos) - len(pend), len(pend))))
    pays = {}
    for o in dos:
        k = o['degraded']['closure']['payment']
        pays[k] = pays.get(k, 0) + 1
    newv = {}
    for o in dos:
        uu = U_BY[o['userId']]
        if o['degraded']['admission'] == '先骑后验新用户':
            s_ = '待核验' if uu['realName']['status'] == '待核验' else ('通过' if uu['realName']['status'] == '已核验' else '未通过')
            newv[s_] = newv.get(s_, 0) + 1
    DG_OUT.append(dict(eventId=d['id'], cause=d['cause'], components=d['components'], enteredAt=d['enter'], exitedAt=d['exit'],
                       durationMinutes=int((d['exit'] - d['enter']).total_seconds() // 60), enteredBy=ACC['agOps'], status='已退出',
                       orderIds=[o['orderId'] for o in dos], orderCount=len(dos), receivableSum=amt, admissionCounts=adm,
                       limitsVersionId=ver['id'], limits=ver['limits'], maxHourlyOrders=max(by_hour.values()),
                       maxUserOpenOrders=1, overLimitReminders=sum(1 for o in dos if o['degraded']['overLimitRemindedAt']),
                       closure=dict(status='部分待人工' if pend else '已全部闭合', closed=len(dos) - len(pend), pendingOrderIds=pend, paymentResults=pays,
                                    newUserVerification=newv), timeline=timeline))
    alert(d['enter'], '严重', '降级进入', '已自动进入降级开单：%s' % d['cause'], ev=d['id'], ack=timeline[2]['by'], ackAt=timeline[2]['at'])
    if (d['exit'] - d['enter']).total_seconds() > 30 * 60:
        alert(d['enter'] + TD(minutes=30), '警告', '降级持续超阈值', '降级已持续 30 分钟，请决定是否收紧或停止', ev=d['id'], ack=ACC['duty2'] if d['tighten'] else ACC['duty1'],
              ackAt=d['enter'] + TD(minutes=31))
    for o in dos:
        if o['degraded']['overLimitRemindedAt']:
            alert(o['degraded']['overLimitRemindedAt'], '提示', '降级单超时长上限', '降级单 %s 已骑行超过 %d 分钟上限，已提醒用户尽快还车，骑行不结束' % (
                o['orderId'], o['degraded']['limitMinutes']), ev=d['id'], ack=ACC['duty1'], ackAt=o['degraded']['overLimitRemindedAt'] + TD(minutes=2))
    alert(d['exit'], '提示', '降级退出', '故障恢复，已退出降级开单', ev=d['id'])
for p_ in PAUSES:
    alert(p_['pausedAt'], '警告', '暂停可租', '%s %s持续低于门槛（%.1f%% < %.0f%%），已标为暂停可租' % (BRANDS[p_['brandId']]['name'], p_['metric'], p_['value'] * 100, p_['threshold'] * 100),
          brand=p_['brandId'], ack=ACC['duty1'], ackAt=p_['pausedAt'] + TD(minutes=6))
    if p_['resumedAt']:
        alert(p_['resumedAt'], '提示', '恢复可租', '%s 指标恢复，已自动回到已接入' % BRANDS[p_['brandId']]['name'], brand=p_['brandId'])
ALERTS.sort(key=lambda a: a['at'])

# ---------------------------------------------------------------- 接入
CASES = [('M01-01', 'M-01 开锁', '正常开锁请求在时限内回报结果'), ('M01-02', 'M-01 开锁', '同一订单重复开锁请求不重复开锁'),
         ('M01-03', 'M-01 开锁', '带降级标记的开锁请求按普通开锁执行'), ('M01-04', 'M-01 开锁', '延迟到达的开锁请求不重复执行'),
         ('M02-01', 'M-02 结束骑行', '用户点还车后锁车并回报'), ('M02-02', 'M-02 结束骑行', '入桩未锁时平台请求结束并锁车'),
         ('M02-03', 'M-02 结束骑行', '超时后才报告开锁成功时平台请求结束'), ('M02-04', 'M-02 结束骑行', '客服强制结束带事由'),
         ('M03-01', 'M-03 骑行事件', '开锁结果在时限内上报（含"已受理"后补报结果）'), ('M03-02', 'M-03 骑行事件', '锁车事件字段完整（时间、位置、车端在点、时长、里程）'),
         ('M03-03', 'M-03 骑行事件', '平台未确认时在补报窗口内补报'), ('M03-04', 'M-03 骑行事件', '重复上报的事件平台只记一次'),
         ('M03-05', 'M-03 骑行事件', '乱序到达的事件按事件时间处理'), ('M04-01', 'M-04 车辆清单', '清单字段完整（品牌、车号、车型、车辆标识）'),
         ('M04-02', 'M-04 车辆清单', '桩识别用车辆标识全局唯一'), ('M04-03', 'M-04 车辆清单', '车辆增减时增量更新'),
         ('SEC-01', '接入安全', '未登记来源的请求被拒绝'), ('SEC-02', '接入安全', '被篡改或重放的请求被拒绝')]
FAILS = {
    'M03-01': ('开锁结果在请求后 41 秒才到达，规范要求 30 秒内', '规范允许先回"已受理"，车辆确认后再报结果；见接入规范第 3.2 节参考报文。'),
    'M01-02': ('同一订单号第二次开锁请求触发了第二次开锁指令', '以平台订单号做幂等键，收到重复请求时直接返回首次结果；见规范第 3.1 节。'),
    'M03-03': ('模拟断网 10 分钟后未补报锁车事件', '本地保留未确认事件，网络恢复后在 72 小时窗口内按事件时间顺序补报；见规范第 3.4 节。'),
    'M04-02': ('清单中 3 辆车的车辆标识重复（E28011…A3F2）', '车辆标识须与车身电子标签一致且全局唯一，请核对后重新提交。'),
    'SEC-02': ('重放 5 分钟前的请求未被拒绝', '请在验签时校验请求时间戳与随机串，超过 5 分钟或重复随机串的请求一律拒绝；见规范第 5 节。'),
    'M02-02': ('入桩未锁的结束请求返回成功但车辆未上锁', '收到结束请求后须确认车辆锁定再回报锁车事件。'),
}
RUNS = []
run_seq = {}


def run(brand, at, failed, env='联调环境'):
    run_seq[brand] = run_seq.get(brand, 0) + 1
    rid = 'IN-%s-R%03d' % (brand, run_seq[brand])
    cases = []
    for cid, grp, name in CASES:
        ok = cid not in failed
        c = dict(caseId=cid, group=grp, name=name, result='通过' if ok else '失败', durationMs=R.randint(180, 4200))
        if not ok:
            c['failReason'], c['suggestion'] = FAILS[cid]
            c['suggestedBy'] = ACC['agAcc']
        cases.append(c)
    RUNS.append(dict(runId=rid, integrationId='IN-' + brand, brandId=brand, at=at, env=env,
                     summary=dict(total=len(cases), passed=sum(1 for c in cases if c['result'] == '通过'), failed=len(failed)), cases=cases))
    return rid


hist = {}
INTEG = []
plan = {
    'BR01': [(DT(2026, 7, 9, 15, 0, 0), ['M03-01', 'M01-02', 'M03-03']), (DT(2026, 7, 13, 10, 20, 0), ['M03-03']), (DT(2026, 7, 16, 16, 40, 0), [])],
    'BR02': [(DT(2026, 7, 9, 15, 30, 0), ['M03-01', 'M01-02', 'M03-03']), (DT(2026, 7, 14, 11, 0, 0), []), ],
    'BR03': [(DT(2026, 7, 30, 10, 0, 0), ['M04-02', 'SEC-02', 'M03-03']), (DT(2026, 8, 4, 14, 30, 0), ['SEC-02']), (DT(2026, 8, 7, 9, 45, 0), [])],
    'BR04': [(DT(2026, 7, 4, 10, 0, 0), ['M01-02', 'M02-02']), (DT(2026, 7, 10, 15, 10, 0), [])],
    'BR05': [(DT(2026, 11, 12, 10, 30, 0), ['M03-01', 'M01-02', 'M03-03', 'M04-02', 'SEC-02', 'M02-02']), (DT(2026, 11, 20, 15, 40, 0), ['M03-01', 'M03-03', 'SEC-02']),
             (DT(2026, 11, 28, 16, 12, 0), ['M03-01'])],
}
ACCEPT = {'BR01': (DT(2026, 7, 20, 16, 30, 0), 20, 60), 'BR02': (DT(2026, 7, 20, 16, 35, 0), 20, 60), 'BR03': (DT(2026, 8, 12, 11, 0, 0), 20, 62),
          'BR04': (DT(2026, 7, 15, 10, 30, 0), 20, 60)}
for b, info in BRANDS.items():
    rids = [run(b, at, fl) for at, fl in plan.get(b, [])]
    sh = []
    op = info['op']
    if b in ACCEPT:
        at, nv, no = ACCEPT[b]
        first = plan[b][0][0] - TD(days=2)
        sh = [dict(status='联调中', at=first, by=ACC['accLead'], note='发放联调环境凭证')]
        sh.append(dict(status='已接入', at=at, by=ACC['accLead'], note='验收通过，上线可租'))
        for p_ in PAUSES:
            if p_['brandId'] == b:
                sh.append(dict(status='暂停可租', at=p_['pausedAt'], by=ACC['agOps'], note='%s持续低于门槛' % p_['metric']))
                if p_['resumedAt']:
                    sh.append(dict(status='已接入', at=p_['resumedAt'], by=ACC['agOps'], note='指标恢复，自动回到已接入'))
        if b == 'BR04':
            sh.append(dict(status='已退出', at=DT(2026, 11, 20, 10, 0, 0), by=ACC['gov1'], note='按主管部门线下决定设置退出，车辆码同时失效'))
        acc = dict(reportId='IN-%s-AR01' % b, realVehiclesRequired=20, realVehiclesRidden=nv, realOrders=no, matchedOrders=no,
                   lockReportsMatched=no, timeLimitOk=True, conformanceRunId=rids[-1], submittedAt=at - TD(hours=3), submittedBy=ACC['agAcc'],
                   result='通过', confirmedBy=ACC['accLead'], confirmedAt=at,
                   summary='符合性测试 %d 项全部通过；真车 %d 辆实骑 %d 单，订单与锁车报告全部匹配；开锁结果 P90 %.1f 秒。' % (len(CASES), nv, no, R.uniform(2.1, 3.4)))
    elif b == 'BR05':
        sh = [dict(status='未接入', at=DT(2026, 11, 3, 10, 0, 0), by=ACC['admin'], note='登记政府自有车队'),
              dict(status='联调中', at=info['since'], by=ACC['accLead'], note='发放联调环境凭证')]
        acc = dict(reportId=None, realVehiclesRequired=20, realVehiclesRidden=0, realOrders=0, matchedOrders=0, lockReportsMatched=0,
                   timeLimitOk=False, conformanceRunId=rids[-1], submittedAt=None, submittedBy=None, result='未开始', confirmedBy=None,
                   confirmedAt=None, summary='符合性测试 18 项中 17 项通过，剩余"开锁结果时限"1 项未通过；通过后进入真车联调。')
    else:
        sh = [dict(status='未接入', at=DT(2026, 10, 30, 14, 0, 0), by=ACC['admin'], note='主管部门登记，尚未开始联调')]
        acc = None
    cred = None
    if b != 'BR06':
        cred = dict(sandboxAppId='sbx_%s_%06d' % (b.lower(), R.randint(100000, 999999)), sandboxKeyMasked='sk_****' + ''.join(R.choice('abcdef0123456789') for _ in range(4)),
                    issuedAt=sh[0]['at'] if b != 'BR05' else info['since'], prodAppId=('app_%s_%06d' % (b.lower(), R.randint(100000, 999999))) if b in ACCEPT else None,
                    status='停用' if b == 'BR04' else '有效', disabledAt=DT(2026, 11, 20, 10, 0, 0) if b == 'BR04' else None)
    vl = None
    if info['n']:
        vl = dict(submittedAt=VH_BY[VH_BY_BRAND[b][0]]['listedAt'], vehicleCount=info['n'], codesIssued=info['n'],
                  codeStatus={'BR04': '已失效', 'BR05': '待启用（计划 2026-12-08 09:00 随上线启用）'}.get(b, '已启用'), submittedBy={'OP01': ACC['op1tech'], 'OP02': ACC['sp1tech'], 'OP03': ACC['op3biz'], 'OP04': ACC['op4tech']}[op])
    INTEG.append(dict(integrationId='IN-' + b, brandId=b, operatorId=op, accessVia=next(x for x in OPERATORS if x['operatorId'] == op)['accessVia'],
                      providerId=next(x for x in OPERATORS if x['operatorId'] == op)['providerId'], status=info['status'],
                      statusSince=info['since'], statusHistory=sh, credentials=cred, vehicleList=vl, runIds=rids, acceptance=acc,
                      targetDays=21, startedAt=sh[0]['at'] if b != 'BR06' else None))
SANDBOX = dict(
    note='联调环境使用模拟车辆、模拟用户、模拟支付，不产生真实收费',
    baseUrl='https://sandbox.ride-sfs.example/open/v1',
    simVehicles=[dict(simVehicleId='SBX-V%03d' % i, plateNo='SBX%05d' % (10000 + i), pileTag='E28011SBX' + '%015d' % i, behavior=b_)
                  for i, b_ in enumerate(['正常', '正常', '正常', '开锁慢（35 秒回报）', '电量不足拒绝开锁', '断网 10 分钟后恢复', '锁车报告重复发送 3 次', '入桩不上锁'], 1)],
    simUsers=[dict(simUserId='SBX-U%03d' % i, kind=k) for i, k in enumerate(['免押老用户', '押金老用户', '新用户', '有欠费用户', '先骑后验新用户'], 1)],
    simPayment='模拟支付：全部扣款返回成功，可在联调页切换为"余额不足"以测试欠费路径')
SPEC = dict(name='示范市共享电单车接入规范', version='1.2', publishedAt=DT(2026, 10, 20, 10, 0, 0), sections=[
    dict(no='1', title='接入方式与凭证', summary='运营商自行接入或由车联网服务商代接，经公网；每家一套凭证，可随时停用。'),
    dict(no='2', title='车辆清单（M-04）', summary='字段：品牌、车号、车型、桩识别用车辆标识；系统提交或页面上传；增减车辆时增量更新。',
         example='{"brand":"BR03","plateNo":"CL384920","model":"驰路 C5 电单车","pileTag":"E28011A93F07C21B5D4E6A1C"}'),
    dict(no='3.1', title='开锁（M-01）', summary='平台带订单号与降级标记请求开锁；同一订单号重复请求不得重复开锁。',
         example='{"orderId":"OD2026113012345","plateNo":"QH318204","degraded":false}'),
    dict(no='3.2', title='开锁结果（M-03）', summary='30 秒内回报开锁成功或失败及原因；车辆确认较慢时可先回"已受理"，确认后再报结果。',
         example='{"orderId":"OD2026113012345","event":"unlock","result":"success","at":"2026-11-30 08:12:03"}'),
    dict(no='3.3', title='结束骑行（M-02）与锁车（M-03）', summary='用户点还车、入桩未锁、超时后才报告开锁成功、客服强制结束时平台请求结束；锁车事件含时间、位置、车端在点判定、时长、里程。',
         example='{"orderId":"OD2026113012345","event":"lock","at":"2026-11-30 08:30:41","lng":104.0712,"lat":30.6013,"inStation":true,"durationSec":1118,"distanceM":3420}'),
    dict(no='3.4', title='补报', summary='平台未确认的事件本地保留，在 72 小时补报窗口内按事件时间补报。'),
    dict(no='4', title='异常与重复', summary='重复或延迟到达的请求不得重复执行；每种异常的含义与处理方式。'),
    dict(no='5', title='安全', summary='只接受已登记来源；请求签名、时间戳与随机串防篡改防重放；单家凭证泄露不影响他家。'),
    dict(no='6', title='验收', summary='符合性测试全部通过、时限达标、真车 20 辆联调订单与锁车报告全部匹配，接入负责人确认后上线。'),
])

# ---------------------------------------------------------------- 执法取证
EVIDENCE, EXPORTS = [], []
officers = [ACC['law1'], ACC['law2'], ACC['law3'], ACC['law4']]
badges = {ACC['law1']: '031207', ACC['law2']: '031215', ACC['law3']: '031322', ACC['law4']: '031408'}
ev_seq = {}
kinds = ['无政府码'] * 20 + ['码已失效'] * 7 + ['码未启用'] * 3
R.shuffle(kinds)
br04v = VH_BY_BRAND['BR04']
br05v = VH_BY_BRAND['BR05']
for k in kinds:
    if k == '码已失效':
        at = rt(DT(2026, 11, 20, 14), NOW - TD(hours=2))
    elif k == '码未启用':
        at = rt(DT(2026, 11, 22, 9), NOW - TD(hours=2))
    else:
        at = rt(DT(2026, 9, 20), NOW - TD(hours=2))
    at = at.replace(hour=R.randint(8, 18))
    if at > NOW:
        at = NOW - TD(hours=3)
    d = at.strftime('%Y%m%d')
    ev_seq[d] = ev_seq.get(d, 0) + 1
    eid = 'EV%s%03d' % (d, ev_seq[d])
    s = R.choice(STATIONS)
    la, ln = off(s['lat'], s['lng'], R.uniform(-150, 150), R.uniform(-150, 150))
    road = R.choice(ROADS)
    addr = '示范市%s%s%d号附近' % (s['district'], road, R.randint(2, 380))
    off_ = R.choice(officers)
    e = dict(evidenceId=eid, officerId=off_, badgeNo=badges[off_], capturedAt=at, location=[round(ln, 6), round(la, 6)], address=addr,
             accuracyM=R.randint(4, 18), scanResult=k, vehicleId=None, brandId=None, operatorId=None, codeStatus=None,
             vehicleDescription=None, photos=[], note=None)
    if k == '无政府码':
        if R.random() < 0.7:
            e['brandId'] = 'BR06'
            e['operatorId'] = 'OP05'
            e['vehicleDescription'] = '车身"途迈"字样，车尾编号 TM%06d，无政府二维码' % R.randint(100000, 999999)
        else:
            e['vehicleDescription'] = R.choice(['车身无品牌标识，二维码被撕除', '车身贴有非政府码的二维码（指向品牌原 App）', '车把二维码被涂抹无法识别，车身无编号'])
        e['note'] = R.choice(['停放在人行道盲道上', '停放在非机动车道', '停放在公交站台旁', '停放在小区出入口', None])
    elif k == '码已失效':
        v = VH_BY[R.choice(br04v)]
        e.update(vehicleId=v['vehicleId'], brandId='BR04', operatorId='OP03', codeStatus='已失效', vehicleDescription='简途车辆，政府码 %s 已于 2026-11-20 失效（运营商已退出）' % v['govCode'])
    else:
        v = VH_BY[R.choice(br05v)]
        e.update(vehicleId=v['vehicleId'], brandId='BR05', operatorId='OP04', codeStatus='待启用', vehicleDescription='市民骑行车辆，政府码 %s 尚未启用（联调中，计划 2026-12-08 启用）' % v['govCode'])
    nph = R.randint(1, 3)
    e['photos'] = ['photos/evidence-%d.svg' % ((R.randint(1, 4) + j) % 4 + 1) for j in range(nph)]
    canon = '|'.join([eid, e['officerId'], F(at), '%.6f,%.6f' % tuple(e['location']), k] + e['photos'])
    e['sha256'] = hashlib.sha256(canon.encode('utf-8')).hexdigest()
    e['submittedAt'] = at + S(R.randint(20, 90))
    e['locked'] = True
    EVIDENCE.append(e)
EVIDENCE.sort(key=lambda e: e['capturedAt'])
for i, (at, flt) in enumerate([(DT(2026, 10, 12, 10, 5, 0), dict(scanResult='无政府码', from_='2026-09-01', to='2026-10-11')),
                               (DT(2026, 11, 10, 15, 30, 0), dict(brandId='BR06', from_='2026-10-01', to='2026-11-09')),
                               (DT(2026, 11, 26, 9, 40, 0), dict(operatorId='OP03', from_='2026-11-20', to='2026-11-25')),
                               (DT(2026, 11, 30, 16, 20, 0), dict(from_='2026-11-01', to='2026-11-30'))], 1):
    sel = []
    for e in EVIDENCE:
        dd = e['capturedAt'].strftime('%Y-%m-%d')
        if not (flt['from_'] <= dd <= flt['to']) or e['submittedAt'] > at:
            continue
        if 'scanResult' in flt and e['scanResult'] != flt['scanResult']:
            continue
        if 'brandId' in flt and e['brandId'] != flt['brandId']:
            continue
        if 'operatorId' in flt and e['operatorId'] != flt['operatorId']:
            continue
        sel.append(e['evidenceId'])
    EXPORTS.append(dict(exportId='EV-EXP-%s-%02d' % (at.strftime('%Y%m%d'), i), at=at, by=ACC['gov1'] if i % 2 else ACC['gov2'],
                        filter={('from' if k_ == 'from_' else k_): v_ for k_, v_ in flt.items()}, evidenceIds=sel, count=len(sel),
                        fileName='执法证据_%s.zip' % at.strftime('%Y%m%d%H%M')))

# ---------------------------------------------------------------- 账号
PWD = 'Shifan@2026'
PERM = {
    '主管部门管理员': ['pricing.view', 'pricing.edit', 'station.edit', 'pile.edit', 'access.view', 'metrics.unlockRate.view', 'access.setExit', 'evidence.view', 'evidence.export', 'audit.view'],
    '运营商业务': ['vehicle.view.own', 'vehicle.edit.own', 'order.view.own', 'order.export.own', 'settlement.view.own', 'settlement.export.own', 'dispute.submit.own', 'access.view.own'],
    '运营商技术对接': ['vehicle.view.own', 'vehicle.edit.own', 'integration.doc', 'integration.sandbox.own', 'integration.conformance.own', 'acceptance.view.own', 'access.view.own'],
    '财务': ['settlement.manage', 'settlement.adjust', 'dispute.decide', 'recon.manage', 'deposit.view', 'arrears.view', 'refund.manualTransfer', 'degrade.closure.view', 'audit.view'],
    '客服': ['user.view', 'order.view', 'refund.confirm', 'order.forceEnd', 'account.unfreeze', 'deposit.assist', 'account.cancel.assist'],
    '客服主管': ['user.view', 'order.view', 'refund.confirm', 'refund.confirm.overLimit', 'order.forceEnd', 'account.unfreeze', 'deposit.assist', 'account.cancel.assist', 'audit.view'],
    '运维值班': ['ops.monitor', 'degrade.control', 'degrade.closure.handle', 'access.pause', 'audit.view'],
    '运维接入负责人': ['ops.monitor', 'acceptance.confirm', 'access.pause', 'code.issue', 'audit.view'],
    '运维账号管理员': ['ops.monitor', 'account.manage', 'audit.view'],
    '执法人员': ['enforce.scan', 'enforce.capture', 'enforce.mine'],
    '智能体': [],
    '系统': [],
}
MENU = {
    '主管部门管理员': ['价格与规则配置', '站点与围栏', '桩管理', '接入状态总览', '证据查询导出', '操作留痕'],
    '运营商业务': ['车辆清单', '订单明细', '结算单', '验收与上线'],
    '运营商技术对接': ['车辆清单', '接入文档', '联调与测试', '验收与上线'],
    '财务': ['结算单管理', '异议处理', '收单对账', '押金专户', '欠费与退款失败', '恢复闭合', '操作留痕'],
    '客服': ['用户与订单查询', '退款处理', '强制结束订单', '账户处理'],
    '客服主管': ['用户与订单查询', '退款处理', '强制结束订单', '账户处理', '操作留痕'],
    '运维值班': ['运行监控', '降级与限额', '恢复闭合', '接入验收', '操作留痕'],
    '运维接入负责人': ['运行监控', '接入验收', '操作留痕'],
    '运维账号管理员': ['运行监控', '登录与账号', '操作留痕'],
    '执法人员': ['扫码核查', '取证', '我的取证记录'],
    '智能体': [], '系统': [],
}
POST = {'主管部门管理员': '主管部门', '运营商业务': '运营商', '运营商技术对接': '运营商', '财务': '平台公司财务', '客服': '平台公司客服', '客服主管': '平台公司客服',
        '运维值班': '平台公司运维', '运维接入负责人': '平台公司运维', '运维账号管理员': '平台公司运维', '执法人员': '执法', '智能体': '智能体', '系统': '系统'}
ACCOUNTS = []


def account(aid, role, name, username, org, scope=None, created=DT(2026, 6, 15, 10, 0, 0), status='正常', agent=None):
    a = dict(accountId=aid, role=role, post=POST[role], name=name, username=username, password=PWD if role not in ('智能体', '系统') else None,
             org=org, operatorScope=scope, permissions=PERM[role], menus=MENU[role], status=status, createdAt=created,
             createdBy=ACC['admin'] if aid != ACC['admin'] else None, lastLoginAt=None, agentRole=agent)
    ACCOUNTS.append(a)
    return a


account(ACC['gov1'], '主管部门管理员', '周＊＊', 'zgbm.zhou', '示范市交通运输局 共享单车管理科')
account(ACC['gov2'], '主管部门管理员', '叶＊', 'zgbm.ye', '示范市交通运输局 共享单车管理科')
account(ACC['op1biz'], '运营商业务', '何＊＊', 'qhcx.he', '青禾出行科技有限公司', 'OP01')
account(ACC['op2biz'], '运营商业务', '沈＊＊', 'clzx.shen', '驰路智行科技有限公司', 'OP02')
account(ACC['op3biz'], '运营商业务', '韦＊＊', 'jtgx.wei', '简途共享出行有限公司', 'OP03', status='只读（运营商已退出）')
account(ACC['op4biz'], '运营商业务', '龚＊＊', 'gjcd.gong', '示范市公共交通集团有限公司', 'OP04', created=DT(2026, 11, 3, 11, 0, 0))
account(ACC['op1tech'], '运营商技术对接', '邓＊', 'qhcx.deng', '青禾出行科技有限公司', 'OP01')
account(ACC['sp1tech'], '运营商技术对接', '闫＊＊', 'lt.yan', '联泰车联网科技有限公司（代接驰路智行）', 'OP02')
account(ACC['op4tech'], '运营商技术对接', '黎＊', 'gjcd.li', '示范市公共交通集团有限公司', 'OP04', created=DT(2026, 11, 3, 11, 5, 0))
account(ACC['fin1'], '财务', '吕＊＊', 'cw.lv', '示范市城市出行服务有限公司 财务部')
account(ACC['fin2'], '财务', '姜＊', 'cw.jiang', '示范市城市出行服务有限公司 财务部')
account(ACC['cs1'], '客服', '苏＊＊', 'kf.su', '示范市城市出行服务有限公司 客服中心')
account(ACC['cs2'], '客服', '钟＊', 'kf.zhong', '示范市城市出行服务有限公司 客服中心')
account(ACC['cs3'], '客服', '陆＊＊', 'kf.lu', '示范市城市出行服务有限公司 客服中心')
account(ACC['csLead'], '客服主管', '谭＊＊', 'kf.tan', '示范市城市出行服务有限公司 客服中心')
account(ACC['duty1'], '运维值班', '贺＊＊', 'yw.he', '示范市城市出行服务有限公司 运行保障部')
account(ACC['duty2'], '运维值班', '顾＊', 'yw.gu', '示范市城市出行服务有限公司 运行保障部')
account(ACC['accLead'], '运维接入负责人', '毛＊＊', 'yw.mao', '示范市城市出行服务有限公司 运行保障部')
account(ACC['admin'], '运维账号管理员', '郝＊', 'yw.hao', '示范市城市出行服务有限公司 运行保障部')
account(ACC['law1'], '执法人员', '邵＊＊', 'zf031207', '示范市城市管理综合执法支队 一大队')
account(ACC['law2'], '执法人员', '万＊', 'zf031215', '示范市城市管理综合执法支队 一大队')
account(ACC['law3'], '执法人员', '严＊＊', 'zf031322', '示范市城市管理综合执法支队 二大队')
account(ACC['law4'], '执法人员', '覃＊', 'zf031408', '示范市城市管理综合执法支队 三大队')
account(ACC['agOps'], '智能体', '运行智能体', None, '平台公司', agent='判断降级进入与暂停可租，通知值班人')
account(ACC['agRecon'], '智能体', '对账智能体', None, '平台公司', agent='月结、收单对账、降级单闭合、异议比对')
account(ACC['agAcc'], '智能体', '接入智能体', None, '平台公司', agent='解释联调失败、生成验收报告')
account(ACC['agCs'], '智能体', '客服智能体', None, '平台公司', agent='解释费用、办理退押、提议退款')
account(ACC['sys'], '系统', '系统定时任务', None, '平台公司', agent=None)
for a in ACCOUNTS:
    if a['password']:
        a['lastLoginAt'] = NOW - TD(hours=R.randint(1, 40), minutes=R.randint(0, 59))

# ---------------------------------------------------------------- 欠费超过 30 天未结清：冻结（平台参数 arrearsFreezeDays）
for u in USERS:
    if u['account']['status'] != '正常':
        continue
    due = sorted(o['billPushedAt'] for o in ORDERS if o['userId'] == u['userId'] and o['fee'] and o['payStatus'] == '欠费')
    if due and due[0] + TD(days=30) <= NOW:
        t = due[0] + TD(days=30)
        rsn = '欠费追缴未结（超过 30 天）'
        u['account'] = dict(status='冻结', freeze=dict(reason=rsn, at=t, unfreezeHow='结清欠费后自动解冻'),
                            history=u['account']['history'] + [dict(action='冻结', at=t, reason=rsn, by=ACC['sys'])])
        u['persona'] = 'arrears_frozen'
        u['personaLabel'] = PERSONA_LABEL['arrears_frozen']

# ---------------------------------------------------------------- 留痕
for v in VERS:
    audit(v['created'], v['by'], '配置录入', 'pricing', v['id'], '录入计价规则版本 %s「%s」，%s生效' % (v['id'], v['name'], F(v['eff'])[:16]), reason=v['note'])
for s in STATIONS:
    audit(DT.strptime(s['createdAt'], '%Y-%m-%d %H:%M:%S'), ACC['gov1'], '站点调整', 'station', s['stationId'], '新建站点 %s「%s」（%s）' % (s['stationId'], s['name'], s['type']))
# 部分站点围栏调整（近期）
for s in R.sample(STATIONS, 14):
    at = rt(DT(2026, 9, 5), NOW - TD(hours=6)).replace(hour=R.randint(9, 17))
    s['updatedAt'] = F(at)
    s['updatedBy'] = R.choice([ACC['gov1'], ACC['gov2']])
    audit(at, s['updatedBy'], '站点调整', 'station', s['stationId'], '调整站点「%s」电子围栏' % s['name'],
          reason=R.choice(['围栏覆盖到机动车道，向人行道内收', '应街道办要求避开消防通道', '站点扩容，延长围栏', '与新建公交站台冲突，平移围栏']))
for p in R.sample(PILES, 28):
    at = rt(DT(2026, 9, 5), NOW - TD(hours=6)).replace(hour=R.randint(9, 17))
    audit(at, R.choice([ACC['gov1'], ACC['gov2']]), '桩登记', 'pile', p['pileId'], '登记桩 %s（%s %d 号桩位）位置更正' % (p['pileId'], ST_BY[p['stationId']]['name'], p['no']),
          reason='现场复核坐标偏差')
for b in ['BR01', 'BR02', 'BR03', 'BR04', 'BR05']:
    v0 = VH_BY[VH_BY_BRAND[b][0]]
    lt = DT.strptime(v0['listedAt'], '%Y-%m-%d %H:%M:%S')
    op = BRANDS[b]['op']
    audit(lt, {'OP01': ACC['op1tech'], 'OP02': ACC['sp1tech'], 'OP03': ACC['op3biz'], 'OP04': ACC['op4tech']}[op], '车辆清单提交', 'brand', b,
          '%s 提交车辆清单 %d 辆' % (BRANDS[b]['name'], BRANDS[b]['n']))
    audit(DT.strptime(v0['codeIssuedAt'], '%Y-%m-%d %H:%M:%S'), ACC['accLead'], '政府码发放', 'brand', b, '按车辆清单为 %s 发放政府码 %d 个' % (BRANDS[b]['name'], BRANDS[b]['n']))
audit(DT(2026, 11, 20, 10, 0, 0), ACC['gov1'], '运营商退出设置', 'operator', 'OP03', '按线下结果将简途共享出行有限公司设为已退出，品牌简途 90 辆车政府码失效',
      reason='主管部门 2026-11-18 线下决定', before='已接入', after='已退出')
for i_ in INTEG:
    for h_ in i_['statusHistory']:
        if h_['status'] == '已接入' and h_['by'] == ACC['accLead']:
            audit(h_['at'], ACC['accLead'], '验收确认', 'integration', i_['integrationId'], '%s 验收通过，上线可租' % BRANDS[i_['brandId']]['name'],
                  reason=i_['acceptance']['summary'])
for p_ in PAUSES:
    audit(p_['pausedAt'], ACC['agOps'], '暂停可租', 'brand', p_['brandId'], '%s 标为暂停可租' % BRANDS[p_['brandId']]['name'],
          reason='%s %.1f%% 持续 %d 分钟低于门槛 %.0f%%' % (p_['metric'], p_['value'] * 100, p_['sustainMinutes'], p_['threshold'] * 100), before='已接入', after='暂停可租')
    if p_['resumedAt']:
        audit(p_['resumedAt'], ACC['agOps'], '恢复可租', 'brand', p_['brandId'], '%s 指标恢复，自动回到已接入' % BRANDS[p_['brandId']]['name'], before='暂停可租', after='已接入')
for d in DG_OUT:
    for tl in d['timeline']:
        m_ = {'降级进入': '降级进入', '降级收紧': '降级收紧', '降级退出': '降级退出'}.get(tl['action'])
        if m_:
            audit(tl['at'], tl['by'], m_, 'degrade', d['eventId'], tl['text'], reason=d['cause'] if m_ == '降级进入' else None)
for p in PAYMENTS:
    if p['type'] == '押金抵扣':
        audit(p['completedAt'], ACC['agRecon'], '押金抵扣', 'order', p['orderId'], '订单 %s 一键支付期限届满未付，自动从押金抵扣 %s 并通知用户' % (p['orderId'], yuan(p['amount'])),
              reason='BR-22')
    if p['type'] == '人工转账退款':
        audit(p['completedAt'], p['operatorAccountId'], '人工转账', 'payment', p['paymentId'], '订单 %s 原路退款失败，人工转账 %s 至用户本人账户' % (p['orderId'], yuan(p['amount'])),
              reason='原路退回失败（R-11）')
    if p['type'] == '人工转账退押':
        audit(p['completedAt'], p['operatorAccountId'], '人工转账', 'payment', p['paymentId'], '用户 %s 退押原路失败，人工转账押金 %s' % (p['userId'], yuan(p['amount'])),
              reason='原路退回失败（R-11）')
    if p['type'] == '退押':
        audit(p['createdAt'], ACC['agCs'], '退押处理', 'user', p['userId'], '用户 %s 申请退押，已停止开新单并原路退回押金 %s' % (p['userId'], yuan(p['amount'])))
for t in TICKETS:
    rp = t['refundProposal']
    if rp and rp['decision'] in ('已确认',):
        audit(rp['decidedAt'], rp['decidedBy'], '超额退款确认' if rp['overLimit'] else '退款确认', 'order', t['orderId'],
              '确认订单 %s 退款 %s（%s）' % (t['orderId'], yuan(rp['amount']), rp['kind']), reason=rp['basis'])
    if rp and rp['decision'] == '已驳回':
        audit(rp['decidedAt'], rp['decidedBy'], '退款驳回', 'order', t['orderId'], '订单 %s 不予退款' % t['orderId'], reason=rp['basis'])
    if t['forceEnd']:
        audit(t['forceEnd']['at'], t['forceEnd']['by'], '强制结束', 'order', t['orderId'], '强制结束订单 %s 并锁车，按 %s 计费' % (t['orderId'], F(t['forceEnd']['billedEndAt'])[11:16]),
              reason=t['forceEnd']['reason'])
for u in USERS:
    for h_ in u['account']['history']:
        if h_['action'] in ('冻结', '人工解冻', '自动解冻'):
            audit(h_['at'], h_['by'], {'冻结': '账户冻结', '人工解冻': '人工解冻', '自动解冻': '自动解冻'}[h_['action']], 'user', u['userId'],
                  '用户 %s %s' % (u['userId'], h_['action']), reason=h_['reason'])
for st in SETTLEMENTS:
    audit(st['generatedAt'], ACC['agRecon'], '结算单生成', 'settlement', st['settlementId'], '生成 %s %s 结算单，应付服务费 %s' % (
        next(o for o in OPERATORS if o['operatorId'] == st['operatorId'])['shortName'], st['month'], '¥%.2f' % (st['totals']['payableFee'] / 100)))
    for a in st['adjustments']:
        if a['confirmedBy']:
            audit(a['confirmedAt'], a['confirmedBy'], '结算调整', 'settlement', st['settlementId'], '确认调整项 %s：%s，服务费 %+.2f 元' % (a['adjustmentId'], a['type'], a['feeDelta'] / 100), reason=a['reason'])
    if st['reviewedAt']:
        audit(st['reviewedAt'], st['reviewedBy'], '结算单审核', 'settlement', st['settlementId'], '审核通过 %s' % st['settlementId'])
        audit(st['sentAt'], st['reviewedBy'], '结算单发送', 'settlement', st['settlementId'], '结算单已发送运营商')
        audit(st['paidAt'], ACC['fin1'], '到账登记', 'settlement', st['settlementId'], '服务费 %s 已转账，银行流水 %s' % ('¥%.2f' % (st['totals']['payableFee'] / 100), st['bankTradeNo']))
for d in DISPUTES:
    if d['decidedAt']:
        audit(d['decidedAt'], d['decidedBy'], '异议处理', 'dispute', d['disputeId'], '异议 %s %s' % (d['disputeId'], d['status']), reason=d['agentComparison'])
for a in ACCOUNTS:
    if a['createdBy']:
        audit(a['createdAt'], a['createdBy'], '账号开通', 'account', a['accountId'], '开通账号 %s（%s，%s）' % (a['username'] or a['name'], a['role'], a['org']))
for e in EXPORTS:
    audit(e['at'], e['by'], '证据导出', 'evidence', e['exportId'], '导出执法证据 %d 条（%s）' % (e['count'], e['fileName']))
# 权限调整与桩状态变更若干
for i in range(9):
    a = R.choice([x for x in ACCOUNTS if x['password'] and x['accountId'] != ACC['admin']])
    at = rt(DT(2026, 9, 10), NOW - TD(hours=4)).replace(hour=R.randint(9, 18))
    audit(at, ACC['admin'], '权限调整', 'account', a['accountId'], '调整账号 %s 的菜单权限' % a['username'], reason=R.choice(['岗位职责调整', '新增查看操作留痕权限', '按岗位核对后收回多余权限']))
bad_piles = [p for p in PILES if p['status'] != '在线']
for p in bad_piles:
    hb = DT.strptime(p['lastHeartbeatAt'], '%Y-%m-%d %H:%M:%S')
    audit(hb + TD(minutes=5), ACC['sys'], '桩状态变更', 'pile', p['pileId'], '桩 %s（%s %d 号）状态变为%s' % (p['pileId'], ST_BY[p['stationId']]['name'], p['no'], p['status']), reason=p['faultNote'])
AUDIT.sort(key=lambda a: a['at'])
AU = []
au_seq = {}
for a in AUDIT:
    d = a['at'].strftime('%Y%m%d')
    au_seq[d] = au_seq.get(d, 0) + 1
    a2 = dict(auditId='AU%s%04d' % (d, au_seq[d]))
    a2.update(a)
    AU.append(a2)

# ---------------------------------------------------------------- 输出


def conv(x):
    if isinstance(x, DT):
        return F(x)
    if isinstance(x, set):
        return sorted(x)
    if isinstance(x, dict):
        return {k: conv(v) for k, v in x.items()}
    if isinstance(x, (list, tuple)):
        return [conv(v) for v in x]
    return x


def money_fields(d, keys):
    for k in keys:
        if k in d and d[k] is not None:
            d[k] = Y(d[k])


def dump(name, obj):
    with open(os.path.join(OUT, name), 'w', encoding='utf-8', newline='\n') as f:
        json.dump(conv(obj), f, ensure_ascii=False, indent=1)


# operators.json
cur = ver_at(NOW)
brands_out = []
for b, info in BRANDS.items():
    brands_out.append(dict(brandId=b, name=info['name'], operatorId=info['op'], vehicleType='电单车', color=info['color'], accessStatus=info['status'],
                           statusSince=info['since'], vehicleCount=info['n'], currentServiceFeeRatio=cur['ratios'].get(b),
                           displayName='【%s】电单车' % info['name']))
ops_out = []
for o in OPERATORS:
    x = dict(o)
    x['brandIds'] = [b for b, i in BRANDS.items() if i['op'] == o['operatorId']]
    ops_out.append(x)
dump('operators.json', dict(operators=ops_out, brands=brands_out, providers=PROVIDERS))

# vehicles.json
dump('vehicles.json', VEHICLES)

# stations.json
st_out = []
for s in STATIONS:
    x = {k: v for k, v in s.items() if k not in ('hl', 'ang', 'lat', 'lng')}
    st_out.append(x)
dump('stations.json', dict(region=dict(city='示范市', center=[CENTER[1], CENTER[0]], bounds=[[104.030, 30.570], [104.102, 30.622]], coordSystem='GCJ-02',
                                       note='坐标顺序 [经度, 纬度]'), stations=st_out, piles=PILES))

# users.json
us_out = []
for u in USERS:
    dpo = dict(u['deposit'])
    if dpo.get('mode') == '押金':
        dpo = dict(mode='押金', amountPaid=Y(dpo['amountPaid']), balance=Y(dpo['balance']), status=dpo['status'], paidAt=dpo['paidAt'],
                   paymentId=dpo['paymentId'], topupPaymentIds=dpo['topups'], deductionPaymentIds=dpo['deductions'],
                   refund=(dict(appliedAt=dpo['refund']['appliedAt'], stoppedAt=dpo['refund']['stoppedAt'], expectedAt=dpo['refund']['expectedAt'],
                                amount=Y(dpo['refund']['amount']), paymentId=dpo['refund']['paymentId'], manualPaymentId=dpo['refund'].get('manualPaymentId'),
                                awaitingPayee=dpo['refund'].get('awaitingPayee', False)) if dpo['refund'] else None))
    elif dpo.get('mode') == '免押':
        dpo = dict(mode='免押', creditOpenedAt=dpo['creditOpenedAt'], provider=dpo['provider'], score=dpo['score'])
    else:
        dpo = dict(mode='未开通')
    acct = u['account']
    us_out.append(dict(userId=u['userId'], name=u['name'], phone=u['phone'], phoneMasked=u['phoneMasked'], channel=u['channel'],
                       registeredAt=u['registeredAt'], persona=u['personaLabel'], realName=u['realName'], deposit=dpo,
                       arrears=Y(u['arrears']), account=dict(status=acct['status'], freeze=acct['freeze'], history=acct['history']),
                       cancelledAt=u.get('cancelAt') if acct['status'] == '已注销' else None, degradedSignupEventId=u.get('degradedSignup') or (dg3['id'] if u.get('wasDegradedSignup') else None),
                       lastActiveAt=max([o['requestAt'] for o in ORDERS if o['userId'] == u['userId']] or [u['registeredAt']])))
cards_out = []
for c in CARDS:
    x = dict(c)
    x['price'] = Y(c['price'])
    x['perOrderCap'] = Y(c['perOrderCap'])
    x['status'] = '生效中' if c['startAt'] <= NOW < c['endAt'] else ('已过期' if c['endAt'] <= NOW else '未生效')
    x['usedOrderIds'] = [o['orderId'] for o in ORDERS if o.get('fee') and o['fee']['monthCardId'] == c['cardId']]
    cards_out.append(x)
cps_out = []
for c in COUPONS:
    x = dict(c)
    x['amount'] = Y(c['amount'])
    x['status'] = '已使用' if c['usedOrderId'] else ('已过期' if c['expireAt'] < NOW else '未使用')
    cps_out.append(x)
dump('users.json', dict(users=us_out, monthCards=cards_out, coupons=cps_out))

# orders.json
ORDER_KEYS = ['orderId', 'userId', 'vehicleId', 'brandId', 'operatorId', 'channel', 'pricingVersionId', 'status', 'requestAt', 'unlockResult',
              'unlockFailReason', 'unlockReportAt', 'failNotifiedAt', 'afterFailOrderId', 'startAt', 'startStationId', 'startLocation', 'endAt',
              'lockReportAt', 'pileInAt', 'reportReceivedAt', 'lateReport', 'endMethod', 'endStationId', 'endPileId', 'endLocation', 'inStation',
              'inStationBasis', 'vehicleSideInStation', 'distanceToStationM', 'dispatchPromptShown', 'durationSec', 'billedMinutes', 'distanceKm',
              'billPushedAt', 'fee', 'payStatus', 'payMethod', 'arrearsPaidAt', 'refundStatus', 'refundFailedPaymentId', 'degraded', 'forceEnd',
              'userClaimedEndAt', 'claimedStationId', 'anomalyNote', 'settleMonth', 'settlement', 'settlementNote', 'ticketIds', 'disputeIds']
for o in ORDERS:
    extra = set(o) - set(ORDER_KEYS) - {'flags', 'baseCorrections'}
    assert not extra, extra
    for k in ORDER_KEYS:
        o.setdefault(k, None)
od_out = []
for o in ORDERS:
    x = {k: o[k] for k in ORDER_KEYS}
    if o.get('fee'):
        x['fee'] = {k: (Y(v) if k not in ('monthCardId', 'couponId') else v) for k, v in o['fee'].items()}
    if o['degraded']:
        x['degraded'] = dict(o['degraded'])
        x['degraded']['limitAmount'] = Y(o['degraded']['limitAmount'])
    x['payStatus'] = o.get('payStatus')
    x['payMethod'] = o.get('payMethod')
    od_out.append(x)
dump('orders.json', od_out)

# payments.json
pm_out = []
for p in sorted(PAYMENTS, key=lambda p: p['createdAt']):
    x = dict(p)
    x['amount'] = Y(p['amount'])
    pm_out.append(x)
rc_out = []
for r_ in RECON:
    x = dict(r_)
    x['channelActual'] = {k: Y(v) for k, v in r_['channelActual'].items()}
    for k in ('orderPayments', 'depositDeducted', 'refundsOriginal', 'refundsManual', 'orderNetReceived', 'monthCardIncome', 'expected', 'diff'):
        x[k] = Y(r_[k])
    x['diffItems'] = [dict(i, amount=Y(i['amount'])) for i in r_['diffItems']]
    rc_out.append(x)
dump('payments.json', dict(payments=pm_out, reconciliation=rc_out))

# pricing.json
pv_out = []
for v in VERS:
    status = '待生效' if v['eff'] > NOW else ('生效中' if v is cur else '已失效')
    nxt = next((w['eff'] for w in VERS if w['eff'] > v['eff']), None)
    pv_out.append(dict(versionId=v['id'], name=v['name'], status=status, effectiveAt=v['eff'], effectiveUntil=nxt, createdAt=v['created'], createdBy=v['by'], note=v['note'],
                       ride=dict(vehicleType='电单车', startFee=Y(v['startFee']), startMinutes=v['startMin'], perMinute=Y(v['perMin']),
                                 billingUnit='按分钟计，不足 1 分钟按 1 分钟'),
                       dispatchFee=Y(v['dispatch']),
                       monthCard=dict(price=Y(v['card']), days=30, perOrderCap=Y(v['cardCap']), appliesTo='骑行费（不含调度费）', allocation='按骑行单数'),
                       coupons=[dict(templateId=t, **{k: (Y(vv) if k == 'amount' else vv) for k, vv in COUPON_TPL[t].items()}) for t in v['coupons']],
                       deposit=dict(amount=Y(v['deposit'])), creditFree=dict(threshold=v['threshold'], providers=['微信支付分', '芝麻信用']),
                       serviceFee=dict(basis='订单应收', ratios=v['ratios']),
                       riskLimits=dict(perUserOpenDegradedOrders=v['limits']['perUserOpenDegradedOrders'],
                                       normal=dict(maxMinutes=v['limits']['normal']['maxMinutes'], maxAmount=Y(v['limits']['normal']['maxAmount'])),
                                       tight=dict(maxMinutes=v['limits']['tight']['maxMinutes'], maxAmount=Y(v['limits']['tight']['maxAmount'])),
                                       hourlyDegradedOrders=v['limits']['hourlyDegradedOrders'], degradeDurationAlertMinutes=v['limits']['degradeDurationAlertMinutes'])))
PARAMS = dict(updatedAt=DT(2026, 10, 10, 11, 20, 0), updatedBy=ACC['admin'], owner='平台公司',
              items=dict(payWindowHours=24, csRefundLimit=20.0, backfillWindowHours=72, unlockReportTimeoutSec=30, nearbyRadiusM=500,
                         arrearsFreezeDays=30, depositRefundWorkdays=3, billPushTargetSec=10,
                         pauseThreshold=dict(unlockSuccessRate=0.90, availability=0.80, sustainMinutes=120), autoPauseByAgent=False,
                         acceptance=dict(realVehicles=20, realOrders=60, targetDays=21), availabilityTarget=0.995, unlockP90TargetSec=5,
                         retentionYears=dict(orders=5, payments=10, audit=5, evidence=5), trackRetentionDays=180))
dump('pricing.json', dict(versions=pv_out, platformParams=PARAMS))

# settlements.json
se_out = []
for st in SETTLEMENTS:
    x = dict(st)
    x['totals'] = {k: (v if k == 'orderCount' else Y(v)) for k, v in st['totals'].items()}
    x['monthCard'] = dict(st['monthCard'], platformIncome=Y(st['monthCard']['platformIncome']))
    x['brands'] = [dict(b, receivableSum=Y(b['receivableSum']), baseSum=Y(b['baseSum']), detailFee=Y(b['detailFee']), adjustmentFee=Y(b['adjustmentFee']),
                        monthCardShare=Y(b['monthCardShare']), ratioBreakdown=[dict(r_, baseSum=Y(r_['baseSum']), fee=Y(r_['fee'])) for r_ in b['ratioBreakdown']])
                   for b in st['brands']]
    x['adjustments'] = [dict(a, baseDelta=Y(a['baseDelta']), feeDelta=Y(a['feeDelta']),
                             items=[dict(i, previousBase=Y(i['previousBase']), newBase=Y(i['newBase']), baseDelta=Y(i['baseDelta']), feeDelta=Y(i['feeDelta'])) for i in a['items']])
                        for a in st['adjustments']]
    x['details'] = [dict(d, receivable=Y(d['receivable']), refundedAtGeneration=Y(d['refundedAtGeneration']), base=Y(d['base']), fee=Y(d['fee'])) for d in st['details']]
    se_out.append(x)
dump('settlements.json', dict(settlements=se_out, disputes=DISPUTES))

# tickets.json
tk_out = []
for t in TICKETS:
    x = dict(t)
    x['messages'] = [dict(('from' if k == 'from_' else k, v) for k, v in m.items()) for m in t['messages']]
    if t['refundProposal']:
        x['refundProposal'] = dict(t['refundProposal'], amount=Y(t['refundProposal']['amount']))
    if t['facts']:
        fx = dict(t['facts'])
        o = O_BY.get(t['orderId'])
        if o and o.get('fee'):
            fx['receivable'] = Y(o['fee']['receivable'])
            fx['payable'] = Y(o['fee']['payable'])
            fx['netReceived'] = Y(o['fee']['netReceived'])
        x['facts'] = fx
    tk_out.append(x)
dump('tickets.json', tk_out)

# evidence.json
dump('evidence.json', dict(evidence=EVIDENCE, exports=EXPORTS))

# integration.json
dump('integration.json', dict(integrations=INTEG, conformanceRuns=RUNS, sandbox=SANDBOX, spec=SPEC))

# ops.json
dg_out = []
for d in DG_OUT:
    x = dict(d)
    x['receivableSum'] = Y(d['receivableSum'])
    x['limits'] = dict(perUserOpenDegradedOrders=d['limits']['perUserOpenDegradedOrders'],
                       normal=dict(maxMinutes=d['limits']['normal']['maxMinutes'], maxAmount=Y(d['limits']['normal']['maxAmount'])),
                       tight=dict(maxMinutes=d['limits']['tight']['maxMinutes'], maxAmount=Y(d['limits']['tight']['maxAmount'])),
                       hourlyDegradedOrders=d['limits']['hourlyDegradedOrders'], degradeDurationAlertMinutes=d['limits']['degradeDurationAlertMinutes'])
    dg_out.append(x)
dump('ops.json', dict(current=dict(degraded=False, lastDegradeEventId=DG[-1]['id'], pausedBrandIds=['BR02'], asOf=NOW),
                      metricsDaily=OPS_DAILY, metricsHourly=OPS_HOURLY, platformDaily=PLATFORM_DAILY, degradeEvents=dg_out, pauses=PAUSES, suspendSuggestions=[], alerts=ALERTS))

# audit.json / accounts.json
dump('audit.json', AU)
dump('accounts.json', dict(accounts=ACCOUNTS, loginInfo=dict(password=PWD, note='所有可登录账号同一密码；智能体与系统账号不可登录')))

# 占位照片
ph_dir = os.path.join(OUT, 'photos')
os.makedirs(ph_dir, exist_ok=True)
for i, (bg, label) in enumerate([('#D9D4CC', '现场照片'), ('#CFC9BF', '车身特写'), ('#D5D0C7', '车把与码位'), ('#CCC6BB', '停放位置')], 1):
    svg = ('<svg xmlns="http://www.w3.org/2000/svg" width="640" height="480" viewBox="0 0 640 480">'
           '<rect width="640" height="480" fill="%s"/>'
           '<g fill="none" stroke="#8A8378" stroke-width="10" stroke-linecap="round">'
           '<circle cx="210" cy="300" r="70"/><circle cx="440" cy="300" r="70"/><path d="M210 300 L290 200 L400 200 L440 300 M290 200 L330 300 L210 300 M400 200 L420 160 L450 160"/></g>'
           '<text x="320" y="430" font-size="26" text-anchor="middle" fill="#6B655C" font-family="sans-serif">%s</text></svg>') % (bg, label)
    with open(os.path.join(ph_dir, 'evidence-%d.svg' % i), 'w', encoding='utf-8') as f:
        f.write(svg)

print('orders', len(ORDERS), 'users', len(USERS), 'vehicles', len(VEHICLES), 'stations', len(STATIONS), 'piles', len(PILES),
      'payments', len(PAYMENTS), 'tickets', len(TICKETS), 'evidence', len(EVIDENCE), 'audit', len(AU), 'cards', len(CARDS), 'coupons', len(COUPONS))
