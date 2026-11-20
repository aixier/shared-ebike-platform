# -*- coding: utf-8 -*-
"""mockdata 一致性自检。只读 JSON，独立复算，不调用生成器。

用法：python validate.py   退出码 0 = 全部通过
"""
import json, os, math, sys
from datetime import datetime as DT, timedelta as TD
from collections import Counter, defaultdict

D = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), '..'))
NOW = DT(2026, 12, 1, 12, 0, 0)


def L(n):
    with open(os.path.join(D, n), encoding='utf-8') as f:
        return json.load(f)


def T(s):
    return DT.strptime(s, '%Y-%m-%d %H:%M:%S') if s else None


def C(y):
    """元 -> 分（整数）"""
    return None if y is None else int(round(y * 100))


ops = L('operators.json'); veh = L('vehicles.json'); sts = L('stations.json'); usr = L('users.json'); ords = L('orders.json')
pay = L('payments.json'); pri = L('pricing.json'); sett = L('settlements.json'); tks = L('tickets.json'); evd = L('evidence.json')
itg = L('integration.json'); opsd = L('ops.json'); aud = L('audit.json'); acc = L('accounts.json')

RESULTS = []


def check(name, errs):
    RESULTS.append((name, len(errs), errs[:5]))


OP = {o['operatorId']: o for o in ops['operators']}
BR = {b['brandId']: b for b in ops['brands']}
VH = {v['vehicleId']: v for v in veh}
ST = {s['stationId']: s for s in sts['stations']}
PL = {p['pileId']: p for p in sts['piles']}
U = {u['userId']: u for u in usr['users']}
MC = {c['cardId']: c for c in usr['monthCards']}
CP = {c['couponId']: c for c in usr['coupons']}
O = {o['orderId']: o for o in ords}
P = {p['paymentId']: p for p in pay['payments']}
V = {v['versionId']: v for v in pri['versions']}
AC = {a['accountId']: a for a in acc['accounts']}
SE = {s['settlementId']: s for s in sett['settlements']}
TK = {t['ticketId']: t for t in tks}

# ---------- 1 编号唯一与引用完整
e = []
for name, arr, key in [('vehicles', veh, 'vehicleId'), ('stations', sts['stations'], 'stationId'), ('piles', sts['piles'], 'pileId'), ('users', usr['users'], 'userId'),
                       ('orders', ords, 'orderId'), ('payments', pay['payments'], 'paymentId'), ('tickets', tks, 'ticketId'), ('audit', aud, 'auditId'),
                       ('evidence', evd['evidence'], 'evidenceId'), ('coupons', usr['coupons'], 'couponId'), ('cards', usr['monthCards'], 'cardId')]:
    c = Counter(x[key] for x in arr)
    e += ['%s 重复 %s' % (name, k) for k, n in c.items() if n > 1]
for k in ('govCode', 'pileTag', 'plateNo'):
    c = Counter(v[k] for v in veh)
    e += ['车辆 %s 重复 %s' % (k, x) for x, n in c.items() if n > 1]
for v in veh:
    if v['brandId'] not in BR or BR[v['brandId']]['operatorId'] != v['operatorId']:
        e.append('车辆品牌/运营商不符 %s' % v['vehicleId'])
    if v['stationId'] and v['stationId'] not in ST: e.append('车辆站点缺失 %s' % v['vehicleId'])
    if v['pileId'] and (v['pileId'] not in PL or PL[v['pileId']]['occupiedVehicleId'] != v['vehicleId']): e.append('车辆-桩互指不一致 %s' % v['vehicleId'])
for p in sts['piles']:
    if p['stationId'] not in ST or p['pileId'] not in ST[p['stationId']]['pileIds']: e.append('桩-站点不一致 %s' % p['pileId'])
    if p['occupiedVehicleId'] and VH[p['occupiedVehicleId']]['pileId'] != p['pileId']: e.append('桩占用不一致 %s' % p['pileId'])
for o in ords:
    if o['userId'] not in U: e.append('订单用户缺失 %s' % o['orderId'])
    v = VH.get(o['vehicleId'])
    if not v or v['brandId'] != o['brandId'] or v['operatorId'] != o['operatorId']: e.append('订单车辆/品牌不符 %s' % o['orderId'])
    for k in ('startStationId', 'endStationId'):
        if o.get(k) and o[k] not in ST: e.append('订单站点缺失 %s' % o['orderId'])
    if o.get('endPileId') and (o['endPileId'] not in PL or PL[o['endPileId']]['stationId'] != o['endStationId']): e.append('订单桩不在还车站点 %s' % o['orderId'])
    if o['pricingVersionId'] not in V: e.append('订单价格版本缺失 %s' % o['orderId'])
    for t in o['ticketIds']:
        if t not in TK or TK[t]['orderId'] != o['orderId']: e.append('订单-工单互指不一致 %s' % o['orderId'])
for p in pay['payments']:
    if p['userId'] and p['userId'] not in U: e.append('流水用户缺失 %s' % p['paymentId'])
    if p['orderId'] and p['orderId'] not in O: e.append('流水订单缺失 %s' % p['paymentId'])
    if p['cardId'] and p['cardId'] not in MC: e.append('流水月卡缺失 %s' % p['paymentId'])
    if p['relatedPaymentId'] and p['relatedPaymentId'] not in P: e.append('关联流水缺失 %s' % p['paymentId'])
    if p['ticketId'] and p['ticketId'] not in TK: e.append('流水工单缺失 %s' % p['paymentId'])
for a in aud:
    if a['accountId'] not in AC and a['accountId'] not in U: e.append('留痕操作人缺失 %s' % a['auditId'])
for t in tks:
    if t['userId'] not in U: e.append('工单用户缺失 %s' % t['ticketId'])
    if t['orderId'] and t['orderId'] not in O: e.append('工单订单缺失 %s' % t['ticketId'])
check('编号唯一与跨文件引用', e)

# ---------- 2 同一车辆、同一用户同一时刻只有一单
e = []
iv = defaultdict(list)
iu = defaultdict(list)
for o in ords:
    if o['status'] == '开锁失败' or o['status'] == '待人工处理':
        continue
    a = T(o['requestAt'])
    z = T(o['lockReportAt']) if o['lockReportAt'] else NOW
    iv[o['vehicleId']].append((a, z, o['orderId']))
    iu[o['userId']].append((a, z, o['orderId']))
for dct, nm in ((iv, '车辆'), (iu, '用户')):
    for k, lst in dct.items():
        lst.sort()
        for (a1, z1, i1), (a2, z2, i2) in zip(lst, lst[1:]):
            if a2 < z1:
                e.append('%s %s 订单重叠 %s / %s' % (nm, k, i1, i2))
riding = [o for o in ords if o['status'] == '骑行中']
for o in riding:
    if VH[o['vehicleId']]['status'] != '骑行中': e.append('骑行中订单车辆状态不符 %s' % o['orderId'])
check('一车一单、一人一单（BR-04、NFR-08）', e)

# ---------- 3 计价复算（BR-01、BR-03、BR-11、BR-14、BR-07）
e = []
vers = sorted(pri['versions'], key=lambda v: v['effectiveAt'])


def ver_at(t):
    cur = None
    for v in vers:
        if T(v['effectiveAt']) <= t:
            cur = v
    return cur


used_cp = Counter()
for o in ords:
    if o['status'] in ('开锁失败',):
        if o['fee'] is not None or o['startAt'] is not None: e.append('开锁失败订单不应计费 %s' % o['orderId'])
        continue
    if o['status'] != '已结束':
        continue
    v = V[o['pricingVersionId']]
    if ver_at(T(o['requestAt']))['versionId'] != v['versionId']: e.append('价格版本不是开锁时刻有效版本 %s' % o['orderId'])
    ends = [T(x) for x in (o['lockReportAt'], o['pileInAt']) if x]
    if o['endMethod'] == '客服强制结束':
        if T(o['forceEnd']['billedEndAt']) != T(o['endAt']): e.append('强制结束计费终点不符 %s' % o['orderId'])
    elif T(o['endAt']) != min(ends):
        e.append('结束时刻不是锁车与桩在位较早者 %s' % o['orderId'])
    secs = int((T(o['endAt']) - T(o['startAt'])).total_seconds())
    if secs != o['durationSec']: e.append('时长不符 %s' % o['orderId'])
    f = {k: (C(x) if not k.endswith('Id') else x) for k, x in o['fee'].items()}
    if o['unlockResult'] == '超时后成功':
        if f['receivable'] != 0 or f['payable'] != 0: e.append('BR-03 订单应为 0 元 %s' % o['orderId'])
        if not (T(o['unlockReportAt']) - T(o['requestAt'])).total_seconds() > pri['platformParams']['items']['unlockReportTimeoutSec']:
            e.append('BR-03 订单开锁回报未超时 %s' % o['orderId'])
        continue
    mins = max(1, math.ceil(secs / 60))
    if mins != o['billedMinutes']: e.append('计费分钟不符 %s' % o['orderId'])
    rf = C(v['ride']['startFee']) + max(0, mins - v['ride']['startMinutes']) * C(v['ride']['perMinute'])
    if rf != f['rideFee']: e.append('骑行费不符 %s %s!=%s' % (o['orderId'], rf, f['rideFee']))
    df = 0 if o['inStation'] else C(v['dispatchFee'])
    if df != f['dispatchFee']: e.append('调度费不符 %s' % o['orderId'])
    if f['receivable'] != rf + df: e.append('应收不符 %s' % o['orderId'])
    if o['inStation'] is False and o['inStationBasis'] != '站外': e.append('站外依据不符 %s' % o['orderId'])
    if o['inStationBasis'] == '桩与车端冲突·以桩为准' and (not o['inStation'] or not o['endPileId']): e.append('BR-13 应以桩为准 %s' % o['orderId'])
    # 月卡
    cd = 0
    if f['monthCardId']:
        c = MC[f['monthCardId']]
        if c['userId'] != o['userId'] or not (T(c['startAt']) <= T(o['startAt']) < T(c['endAt'])): e.append('月卡不在有效期 %s' % o['orderId'])
        cd = min(C(c['perOrderCap']), f['rideFee'])
    else:
        for c in usr['monthCards']:
            if c['userId'] == o['userId'] and T(c['startAt']) <= T(o['startAt']) < T(c['endAt']):
                e.append('有有效月卡却未抵扣 %s' % o['orderId'])
    if cd != f['monthCardDeduct']: e.append('月卡抵扣不符 %s' % o['orderId'])
    rem = f['receivable'] - cd
    # 优惠券（BR-07：每单一张，抵扣最大，同额先到期）
    bt = T(o['billPushedAt'])
    cands = [c for c in usr['coupons'] if c['userId'] == o['userId'] and T(c['issuedAt']) <= bt <= T(c['expireAt'])
             and (c['usedOrderId'] is None or c['usedOrderId'] == o['orderId'] or T(c['usedAt']) > bt)]
    if rem > 0 and cands:
        best = sorted(cands, key=lambda c: (-min(C(c['amount']), rem), c['expireAt']))[0]
        if f['couponId'] is None:
            e.append('有可用券未使用 %s' % o['orderId'])
        elif min(C(CP[f['couponId']]['amount']), rem) != min(C(best['amount']), rem) or CP[f['couponId']]['expireAt'] > best['expireAt'] and min(C(CP[f['couponId']]['amount']), rem) == min(C(best['amount']), rem) and CP[f['couponId']]['expireAt'] != best['expireAt']:
            e.append('未按 BR-07 选券 %s' % o['orderId'])
    cpd = 0
    if f['couponId']:
        c = CP[f['couponId']]
        used_cp[c['couponId']] += 1
        if c['usedOrderId'] != o['orderId']: e.append('券-订单互指不一致 %s' % o['orderId'])
        cpd = min(C(c['amount']), rem)
    if cpd != f['couponDeduct']: e.append('券抵扣不符 %s' % o['orderId'])
    if f['payable'] != f['receivable'] - cd - cpd: e.append('应付不符 %s' % o['orderId'])
    # 支付构成
    if f['channelPaid'] + f['depositDeducted'] + f['unpaid'] != f['payable']: e.append('应付 != 渠道付+押金抵扣+未付 %s' % o['orderId'])
    if f['netReceived'] != f['channelPaid'] + f['depositDeducted'] - f['refunded']: e.append('实收公式不符 %s' % o['orderId'])
    if f['refunded'] > f['channelPaid'] + f['depositDeducted']: e.append('退款超过已付 %s' % o['orderId'])
    st = o['payStatus']
    exp = {'无需支付': f['payable'] == 0, '已支付': f['unpaid'] == 0 and f['channelPaid'] == f['payable'], '欠费已补付': f['unpaid'] == 0,
           '押金抵扣': f['depositDeducted'] == f['payable'], '欠费': f['unpaid'] > 0, '待支付': f['unpaid'] > 0}
    if not exp.get(st, False): e.append('支付状态与金额不符 %s %s' % (o['orderId'], st))
for c in usr['coupons']:
    if c['usedOrderId'] and used_cp[c['couponId']] != 1: e.append('券被使用次数不为 1 %s' % c['couponId'])
check('订单计价复算（应收、月卡、优惠券、调度费、实收）', e)

# ---------- 4 订单 <-> 支付流水
e = []
agg = defaultdict(lambda: Counter())
for p in pay['payments']:
    if not p['orderId']:
        continue
    a = agg[p['orderId']]
    amt = C(p['amount'])
    if p['status'] == '成功':
        if p['type'] in ('订单扣款', '订单支付', '欠费补付'): a['paid'] += amt
        if p['type'] == '押金抵扣': a['dep'] += amt
        if p['type'] in ('退款', '人工转账退款'): a['ref'] += amt
for o in ords:
    if not o.get('fee'):
        if o['orderId'] in agg and any(agg[o['orderId']].values()): e.append('无金额订单却有成功流水 %s' % o['orderId'])
        continue
    f = o['fee']
    a = agg[o['orderId']]
    if a['paid'] != C(f['channelPaid']): e.append('渠道收款与流水不符 %s' % o['orderId'])
    if a['dep'] != C(f['depositDeducted']): e.append('押金抵扣与流水不符 %s' % o['orderId'])
    if a['ref'] != C(f['refunded']): e.append('退款与流水不符 %s' % o['orderId'])
    if o['payStatus'] == '押金抵扣':
        dp = [p for p in pay['payments'] if p['orderId'] == o['orderId'] and p['type'] == '押金抵扣'][0]
        if (T(dp['completedAt']) - T(o['billPushedAt'])) < TD(hours=pri['platformParams']['items']['payWindowHours']):
            e.append('押金抵扣早于一键支付期限 %s' % o['orderId'])
        if U[o['userId']]['deposit']['mode'] != '押金': e.append('非押金用户被押金抵扣 %s' % o['orderId'])
    for p in pay['payments']:
        if p['orderId'] == o['orderId'] and p['type'] in ('订单扣款', '订单支付') and p['status'] == '成功' and T(p['completedAt']) < T(o['billPushedAt']):
            e.append('扣款早于账单 %s' % o['orderId'])
for c in usr['monthCards']:
    p = P[c['paymentId']]
    if p['type'] != '月卡购买' or C(p['amount']) != C(c['price']) or p['cardId'] != c['cardId']: e.append('月卡与流水不符 %s' % c['cardId'])
    if C(c['price']) != C(V[c['pricingVersionId']]['monthCard']['price']): e.append('月卡价格不是购买时规则 %s' % c['cardId'])
    used = [o['orderId'] for o in ords if o.get('fee') and o['fee']['monthCardId'] == c['cardId']]
    if sorted(used) != sorted(c['usedOrderIds']): e.append('月卡使用记录不符 %s' % c['cardId'])
check('订单与支付流水金额一致', e)

# ---------- 5 用户：押金余额、欠费、状态
e = []
for u in usr['users']:
    arrears = sum(C(o['fee']['unpaid']) for o in ords if o['userId'] == u['userId'] and o.get('fee') and o['payStatus'] == '欠费')
    if arrears != C(u['arrears']): e.append('欠费合计不符 %s' % u['userId'])
    d = u['deposit']
    if d['mode'] == '押金':
        ins = sum(C(p['amount']) for p in pay['payments'] if p['userId'] == u['userId'] and p['type'] in ('押金缴纳', '押金补缴') and p['status'] == '成功')
        ded = sum(C(p['amount']) for p in pay['payments'] if p['userId'] == u['userId'] and p['type'] == '押金抵扣' and p['status'] == '成功')
        out = sum(C(p['amount']) for p in pay['payments'] if p['userId'] == u['userId'] and p['type'] in ('退押', '人工转账退押') and p['status'] == '成功')
        if ins - ded - out != C(d['balance']) and not (d['status'] == '退押中'):
            e.append('押金余额不符 %s' % u['userId'])
        if d['status'] == '退押中' and ins - ded - out != C(d['balance']): e.append('退押中余额不符 %s' % u['userId'])
        if C(d['amountPaid']) != C(V[ver_at(T(d['paidAt']))['versionId']]['deposit']['amount']): e.append('押金金额不是缴纳时规则（BR-25）%s' % u['userId'])
        if d['refund']:
            # BR-24：申请退押后不开新单；BR-23：有骑行中或欠费不能退
            for o in ords:
                if o['userId'] == u['userId'] and T(o['requestAt']) > T(d['refund']['appliedAt']): e.append('退押申请后仍开新单 %s' % u['userId'])
            if arrears: e.append('有欠费却已退押 %s' % u['userId'])
    if u['account']['status'] == '冻结':
        for o in ords:
            if o['userId'] == u['userId'] and T(o['requestAt']) > T(u['account']['freeze']['at']): e.append('冻结后仍开新单 %s' % u['userId'])
    if u['account']['status'] == '已注销':
        if arrears or (d['mode'] == '押金' and d['status'] != '已退'): e.append('注销用户仍有欠费或押金 %s' % u['userId'])
    for o in ords:
        if o['userId'] == u['userId'] and T(o['requestAt']) < T(u['registeredAt']): e.append('注册前下单 %s' % u['userId'])
# BR-05：欠费未补付前不能开新单
for u in usr['users']:
    os_ = sorted([o for o in ords if o['userId'] == u['userId']], key=lambda o: o['requestAt'])
    for i, o in enumerate(os_):
        if o.get('fee') and o['payStatus'] == '欠费':
            if any(x['requestAt'] > o['requestAt'] for x in os_[i + 1:]): e.append('欠费后仍开新单 %s' % o['orderId'])
        if o.get('fee') and o['payStatus'] == '欠费已补付':
            ap = [p for p in pay['payments'] if p['orderId'] == o['orderId'] and p['type'] == '欠费补付'][0]
            for x in os_[i + 1:]:
                if T(x['requestAt']) < T(ap['completedAt']): e.append('补付前开新单 %s' % x['orderId'])
# 欠费超过冻结期限（平台参数 arrearsFreezeDays）仍未结清的账户须已冻结
FREEZE_DAYS = pri['platformParams']['items']['arrearsFreezeDays']
for u in usr['users']:
    due = sorted(T(o['billPushedAt']) for o in ords if o['userId'] == u['userId'] and o.get('fee') and o['payStatus'] == '欠费')
    if due and due[0] + TD(days=FREEZE_DAYS) <= NOW and u['account']['status'] == '正常': e.append('欠费超期未冻结 %s' % u['userId'])
# 退款原路失败的流水须有用户提交的收款账户或已人工转账
for p in pay['payments']:
    if p['type'] == '退款' and p['status'] == '失败' and not p.get('payeeSubmitted'): e.append('退款失败缺收款账户 %s' % p['paymentId'])
check('用户押金、欠费与准入（BR-05、BR-22～25）', e)

# ---------- 6 结算单：合计、逐单复算、覆盖
e = []
GEN = {s['month']: T(s['generatedAt']) for s in sett['settlements']}


def refunded_by(oid, t):
    return sum(C(p['amount']) for p in pay['payments'] if p['orderId'] == oid and p['type'] in ('退款', '人工转账退款') and p['status'] == '成功' and T(p['completedAt']) <= t)


corr = defaultdict(list)
for d in sett['disputes']:
    if d['status'] == '已采纳':
        adj = None
        for s in sett['settlements']:
            for a in s['adjustments']:
                if a['adjustmentId'] == d['adjustmentId']:
                    adj = a
        if not adj: e.append('采纳的异议没有调整项 %s' % d['disputeId'])
        else:
            for it in adj['items']:
                corr[it['orderId']].append((T(d['decidedAt']), C(it['baseDelta'])))


def base_at(o, t):
    f = o['fee']
    if C(f['receivable']) == 0:
        return 0
    rf = refunded_by(o['orderId'], t)
    if rf > 0 and rf >= C(f['channelPaid']) + C(f['depositDeducted']):
        return 0
    return C(f['receivable']) - rf + sum(dl for (at, dl) in corr[o['orderId']] if at <= t)


def ratio(o):
    return V[o['pricingVersionId']]['serviceFee']['ratios'][o['brandId']]


def fee(b, r):
    return int(math.floor(b * r + 0.5 + 1e-9))


seen = Counter()
cum = {}
for s in sorted(sett['settlements'], key=lambda s: s['generatedAt']):
    g = T(s['generatedAt'])
    if s['generatedAt'][8:10] != '01': e.append('结算单不是 1 日生成 %s' % s['settlementId'])
    if (T(s['dueDate'] + ' 00:00:00') - g.replace(hour=0, minute=0)).days != 5: e.append('到账期限不是 T+5 %s' % s['settlementId'])
    if s['paidAt'] and T(s['paidAt']).date() > T(s['dueDate'] + ' 00:00:00').date(): e.append('超期到账 %s' % s['settlementId'])
    dsum = 0
    for d in s['details']:
        o = O[d['orderId']]
        seen[d['orderId']] += 1
        if o['operatorId'] != s['operatorId']: e.append('明细跨运营商 %s' % d['orderId'])
        if o['endAt'][:7] != s['month'] or o['settleMonth'] != s['month']: e.append('明细结算月不符（BR-19）%s' % d['orderId'])
        if T(o['reportReceivedAt']) > g: e.append('出单后才到达的报告进了明细 %s' % d['orderId'])
        for k in ('startAt', 'endAt', 'billedMinutes', 'distanceKm', 'pricingVersionId', 'brandId'):
            if d[k] != o[k]: e.append('明细 %s 与订单不一致 %s' % (k, d['orderId']))
        if C(d['receivable']) != C(o['fee']['receivable']): e.append('明细应收与订单不一致 %s' % d['orderId'])
        b = base_at(o, g)
        if b != C(d['base']): e.append('明细基数复算不符 %s %s!=%s' % (d['orderId'], b, C(d['base'])))
        if d['ratio'] != ratio(o): e.append('明细比例不是开锁时刻比例（BR-14）%s' % d['orderId'])
        if fee(b, d['ratio']) != C(d['fee']): e.append('明细服务费复算不符 %s' % d['orderId'])
        if o['settlement'] is None or o['settlement']['settlementId'] != s['settlementId'] or o['settlement']['kind'] != '明细':
            e.append('订单未指回结算单 %s' % d['orderId'])
        dsum += C(d['fee'])
        cum[d['orderId']] = (b, C(d['fee']))
    asum = 0
    for a in s['adjustments']:
        isum = 0
        for it in a['items']:
            o = O[it['orderId']]
            prev_b, prev_f = cum.get(it['orderId'], (0, 0))
            if prev_b != C(it['previousBase']): e.append('调整项前值不符 %s' % it['orderId'])
            nb = base_at(o, g)
            if nb != C(it['newBase']): e.append('调整项新基数复算不符 %s' % it['orderId'])
            if C(it['feeDelta']) != fee(nb, it['ratio']) - prev_f: e.append('调整项服务费差额不符 %s' % it['orderId'])
            if a['type'] == '晚到补报':
                seen[it['orderId']] += 1
                if not (o['settleMonth'] < s['month'] and T(o['reportReceivedAt']) > GEN.get(o['settleMonth'], DT(2000, 1, 1)) and T(o['reportReceivedAt']) <= g):
                    e.append('晚到补报归期不符 %s' % it['orderId'])
            cum[it['orderId']] = (nb, fee(nb, it['ratio']))
            isum += C(it['feeDelta'])
        if isum != C(a['feeDelta']): e.append('调整项合计不符 %s' % a['adjustmentId'])
        if not a['reason']: e.append('调整项缺事由 %s' % a['adjustmentId'])
        asum += isum
    t = s['totals']
    if dsum != C(t['detailFee']) or asum != C(t['adjustmentFee']) or C(t['payableFee']) != dsum + asum: e.append('结算单合计不符 %s' % s['settlementId'])
    if t['orderCount'] != len(s['details']) or C(t['baseSum']) != sum(C(d['base']) for d in s['details']) or C(t['receivableSum']) != sum(C(d['receivable']) for d in s['details']):
        e.append('结算单汇总不符 %s' % s['settlementId'])
    bs = 0
    for b in s['brands']:
        bd = [d for d in s['details'] if d['brandId'] == b['brandId']]
        if BR[b['brandId']]['operatorId'] != s['operatorId']: e.append('品牌不属于该运营商 %s' % s['settlementId'])
        if C(b['detailFee']) != sum(C(d['fee']) for d in bd) or C(b['baseSum']) != sum(C(d['base']) for d in bd): e.append('品牌汇总不符 %s %s' % (s['settlementId'], b['brandId']))
        if sum(C(r['fee']) for r in b['ratioBreakdown']) != C(b['detailFee']): e.append('比例分组合计不符 %s' % s['settlementId'])
        bs += C(b['detailFee']) + C(b['adjustmentFee'])
    if bs != C(t['payableFee']): e.append('各品牌合计 != 结算单应付 %s' % s['settlementId'])
# 每个已结束且在出单前收到报告的订单恰好进一次结算（明细或晚到补报）
for o in ords:
    if o['status'] == '已结束' and o['settleMonth'] in GEN and o['operatorId'] in ('OP01', 'OP02', 'OP03'):
        rr = T(o['reportReceivedAt'])
        nxt = sorted(m for m in GEN if m >= o['settleMonth'])
        should = any(rr <= GEN[m] for m in nxt)
        if should and seen[o['orderId']] != 1: e.append('订单结算次数不为 1：%s（%d 次）' % (o['orderId'], seen[o['orderId']]))
        if not should and seen[o['orderId']] != 0: e.append('订单不应已结算 %s' % o['orderId'])
# 月卡收入分摊合计
for m in set(s['month'] for s in sett['settlements']):
    ss = [s for s in sett['settlements'] if s['month'] == m]
    inc = C(ss[0]['monthCard']['platformIncome'])
    real = sum(C(c['price']) for c in usr['monthCards'] if P[c['paymentId']]['completedAt'][:7] == m)
    if inc != real: e.append('月卡收入不符 %s' % m)
    shares = sum(C(b['monthCardShare']) for s in ss for b in s['brands'])
    nc = sum(b['monthCardOrderCount'] for s in ss for b in s['brands'])
    if nc == ss[0]['monthCard']['platformCardOrders'] and shares != inc and nc > 0: e.append('月卡分摊合计不等于月卡收入 %s' % m)
check('结算单：合计=逐单汇总、服务费=逐单复算、调整项归期（BR-17～20）', e)

# ---------- 7 收单对账（G3 第①条）
e = []
for r in pay['reconciliation']:
    ds = r['date']
    actual = Counter()
    for p in pay['payments']:
        if p['account'] == '骑行收入户' and p['channel'] in ('微信支付', '支付宝') and p['status'] == '成功' and p['channelSettledDate'] == ds:
            actual[p['channel']] += C(p['amount']) * (1 if p['direction'] == '收' else -1)
    ev = [p for p in pay['payments'] if p['status'] == '成功' and p['completedAt'] and p['completedAt'][:10] == ds]
    op_ = sum(C(p['amount']) for p in ev if p['account'] == '骑行收入户' and p['type'] in ('订单扣款', '订单支付', '欠费补付'))
    card = sum(C(p['amount']) for p in ev if p['type'] == '月卡购买')
    dep = sum(C(p['amount']) for p in ev if p['type'] == '押金抵扣')
    ref = sum(C(p['amount']) for p in ev if p['type'] in ('退款', '人工转账退款'))
    net = op_ + dep - ref
    exp = net + card - dep
    if C(r['orderNetReceived']) != net or C(r['monthCardIncome']) != card or C(r['depositDeducted']) != dep or C(r['expected']) != exp:
        e.append('对账口径复算不符 %s' % ds)
    if C(r['channelActual']['wechat']) != actual['微信支付'] or C(r['channelActual']['alipay']) != actual['支付宝']: e.append('渠道到账复算不符 %s' % ds)
    tot = actual['微信支付'] + actual['支付宝']
    if tot - exp != C(r['diff']) or C(r['diff']) != sum(C(i['amount']) for i in r['diffItems']): e.append('差异未被差异项完全解释 %s' % ds)
# 全期：订单实收合计 + 月卡款 − 押金抵扣 = 渠道到账 + 人工转账退款 + 未到账
net_all = sum(C(o['fee']['netReceived']) for o in ords if o.get('fee'))
card_all = sum(C(c['price']) for c in usr['monthCards'])
dep_all = sum(C(o['fee']['depositDeducted']) for o in ords if o.get('fee'))
chan_all = sum(C(p['amount']) * (1 if p['direction'] == '收' else -1) for p in pay['payments'] if p['account'] == '骑行收入户' and p['channel'] in ('微信支付', '支付宝') and p['status'] == '成功' and p['channelSettledDate'])
manual = sum(C(p['amount']) for p in pay['payments'] if p['type'] == '人工转账退款' and p['status'] == '成功')
unarr = sum(C(p['amount']) for p in pay['payments'] if p['account'] == '骑行收入户' and p['status'] == '成功' and p['channel'] in ('微信支付', '支付宝') and not p['channelSettledDate'])
if chan_all != net_all + card_all - dep_all + manual - unarr:
    e.append('全期对账不平：渠道 %d，实收+月卡-押金抵扣 %d，人工转账 %d，未到账 %d' % (chan_all, net_all + card_all - dep_all, manual, unarr))
check('收单对账：渠道到账 = 订单实收 + 月卡款 − 押金抵扣（差异逐项列明）', e)
RECON_TOTAL = (chan_all, net_all, card_all, dep_all, manual, unarr)

# ---------- 8 工单与订单、流水一致
e = []
for t in tks:
    o = O.get(t['orderId']) if t['orderId'] else None
    if t['facts'] and o:
        fx = t['facts']
        for k in ('startAt', 'endAt', 'billedMinutes', 'distanceKm', 'inStation', 'inStationBasis'):
            if fx[k] != o[k]: e.append('工单事实 %s 与订单不一致 %s' % (k, t['ticketId']))
        if o.get('fee'):
            for k in ('receivable', 'payable', 'netReceived'):
                if C(fx[k]) != C(o['fee'][k]): e.append('工单金额 %s 与订单不一致 %s' % (k, t['ticketId']))
    rp = t['refundProposal']
    if rp and rp['decision'] == '已确认':
        rs = [p for p in pay['payments'] if p['ticketId'] == t['ticketId'] and p['type'] == '退款']
        if not rs or C(rs[0]['amount']) != C(rp['amount']): e.append('工单退款与流水不符 %s' % t['ticketId'])
        if rp['overLimit'] != (C(rp['amount']) > C(pri['platformParams']['items']['csRefundLimit'])): e.append('超额标记不符 %s' % t['ticketId'])
        if rp['overLimit'] and AC[rp['decidedBy']]['role'] != '客服主管': e.append('超额退款未由客服主管确认（BR-06）%s' % t['ticketId'])
    if t['forceEnd']:
        if not o or o['forceEnd'] is None or o['forceEnd']['at'] != t['forceEnd']['at'] or o['endMethod'] != '客服强制结束' or not t['forceEnd']['reason']:
            e.append('强制结束记录不一致 %s' % t['ticketId'])
    for m in t['messages']:
        if T(m['at']) > NOW: e.append('工单消息时间晚于现在 %s' % t['ticketId'])
check('客服工单与订单、退款流水一致', e)

# ---------- 9 降级单
e = []
for d in opsd['degradeEvents']:
    dos = [o for o in ords if o['degraded'] and o['degraded']['eventId'] == d['eventId']]
    if sorted(o['orderId'] for o in dos) != sorted(d['orderIds']) or len(dos) != d['orderCount']: e.append('降级单清单不符 %s' % d['eventId'])
    for o in dos:
        if not (T(d['enteredAt']) <= T(o['requestAt']) <= T(d['exitedAt'])): e.append('降级单不在降级时段 %s' % o['orderId'])
        c = o['degraded']['closure']
        missing = [k for k, ok in (('锁车事实', c['lockFact']), ('收款结果', c['payment'] not in ('结果未知', '未收款')), ('开锁结果', c['unlockResult'])) if not ok]
        if sorted(missing) != sorted(c['missing']): e.append('闭合缺项不符 %s' % o['orderId'])
        if (c['status'] == '已闭合') != (not missing): e.append('闭合状态不符 %s' % o['orderId'])
        lim = o['degraded']
        if o['durationSec'] and o['durationSec'] > lim['limitMinutes'] * 60 and not lim['overLimitRemindedAt']: e.append('超时长未提醒 %s' % o['orderId'])
    pend = sorted(o['orderId'] for o in dos if o['degraded']['closure']['status'] == '待人工')
    if pend != sorted(d['closure']['pendingOrderIds']): e.append('待人工清单不符 %s' % d['eventId'])
    if d['receivableSum'] is not None and C(d['receivableSum']) != sum(C(o['fee']['receivable']) for o in dos if o.get('fee')): e.append('降级单金额合计不符 %s' % d['eventId'])
    hrs = Counter(o['requestAt'][:13] for o in dos)
    if max(hrs.values()) != d['maxHourlyOrders'] or d['maxHourlyOrders'] > d['limits']['hourlyDegradedOrders']: e.append('每小时降级单统计不符 %s' % d['eventId'])
    uc = Counter(o['userId'] for o in dos)
    if max(uc.values()) > d['limits']['perUserOpenDegradedOrders']: e.append('同一用户多张降级单 %s' % d['eventId'])
for o in ords:
    if o['degraded'] and U[o['userId']]['account']['status'] == '冻结' and '先骑后验' not in U[o['userId']]['account']['freeze']['reason'] and T(o['requestAt']) > T(U[o['userId']]['account']['freeze']['at']):
        e.append('冻结用户开了降级单 %s' % o['orderId'])
check('降级单：时段、三项闭合、限额（BR-28～34）', e)

# ---------- 10 价格版本、接入状态、执法证据、时间
e = []
st = Counter(v['status'] for v in pri['versions'])
if st['生效中'] != 1 or st['待生效'] < 1: e.append('价格版本状态不对：%s' % dict(st))
for a, b in zip(vers, vers[1:]):
    if a['effectiveUntil'] != b['effectiveAt']: e.append('版本衔接不连续 %s' % a['versionId'])
for b in ops['brands']:
    i = [x for x in itg['integrations'] if x['brandId'] == b['brandId']][0]
    if i['status'] != b['accessStatus'] or i['statusHistory'][-1]['status'] != b['accessStatus']: e.append('接入状态不一致 %s' % b['brandId'])
    n = sum(1 for v in veh if v['brandId'] == b['brandId'])
    if n != b['vehicleCount']: e.append('品牌车辆数不符 %s' % b['brandId'])
    cur = [v for v in pri['versions'] if v['status'] == '生效中'][0]
    if b['currentServiceFeeRatio'] != cur['serviceFee']['ratios'].get(b['brandId']): e.append('品牌当前比例与生效版本不符 %s' % b['brandId'])
if set(b['accessStatus'] for b in ops['brands']) != {'未接入', '联调中', '已接入', '暂停可租', '已退出'}: e.append('接入状态未覆盖五种')
for v in veh:
    if BR[v['brandId']]['accessStatus'] == '已退出' and v['codeStatus'] != '已失效': e.append('退出品牌车辆码未失效 %s' % v['vehicleId'])
# 品牌不可租期间不得开新单（BR-36）
for o in ords:
    b = o['brandId']
    t = T(o['requestAt'])
    for p in opsd['pauses']:
        if p['brandId'] == b and T(p['pausedAt']) <= t < (T(p['resumedAt']) if p['resumedAt'] else NOW + TD(days=1)): e.append('暂停可租期间开单 %s' % o['orderId'])
    if b == 'BR04' and t >= DT(2026, 11, 20, 10): e.append('退出后开单 %s' % o['orderId'])
    if b in ('BR05', 'BR06'): e.append('未上线品牌有订单 %s' % o['orderId'])
import hashlib
for ev in evd['evidence']:
    canon = '|'.join([ev['evidenceId'], ev['officerId'], ev['capturedAt'], '%.6f,%.6f' % tuple(ev['location']), ev['scanResult']] + ev['photos'])
    if hashlib.sha256(canon.encode('utf-8')).hexdigest() != ev['sha256']: e.append('证据摘要不符 %s' % ev['evidenceId'])
    if AC[ev['officerId']]['role'] != '执法人员': e.append('取证人不是执法人员 %s' % ev['evidenceId'])
    for ph in ev['photos']:
        if not os.path.exists(os.path.join(D, ph)): e.append('照片占位文件缺失 %s' % ph)
for ex in evd['exports']:
    for i in ex['evidenceIds']:
        if i not in [x['evidenceId'] for x in evd['evidence']]: e.append('导出引用缺失 %s' % ex['exportId'])
# 时间不晚于"现在"（待生效版本、到期时间、预计到账除外）
FUT_OK = {'effectiveAt', 'effectiveUntil', 'expireAt', 'endAt', 'expectedAt', 'codeEnableAt', 'deadline', 'dueDate'}


def walk(x, path, fname):
    if isinstance(x, dict):
        for k, v in x.items():
            walk(v, path + [k], fname)
    elif isinstance(x, list):
        for v in x:
            walk(v, path, fname)
    elif isinstance(x, str) and len(x) == 19 and x[4] == '-' and x[10] == ' ' and x[13] == ':':
        if T(x) > NOW and path and path[-1] not in FUT_OK:
            e.append('%s 字段 %s 晚于现在：%s' % (fname, '.'.join(path), x))


for fn, obj in [('orders', ords), ('payments', pay), ('settlements', sett), ('tickets', tks), ('audit', aud), ('evidence', evd), ('ops', opsd), ('users', usr), ('integration', itg)]:
    walk(obj, [], fn)
check('价格版本、接入状态、执法证据、时间锚点', e)

# ---------- 11 文案卫生
e = []
bad = ['test', 'Test', 'TEST', 'xxx', 'XXX', 'lorem', 'Lorem', '张三', '李四', '示例', '演示', 'demo', 'Demo', '美团', '哈啰', '青桔', 'mock']
for fn in ['operators.json', 'vehicles.json', 'stations.json', 'users.json', 'orders.json', 'payments.json', 'pricing.json', 'settlements.json',
           'tickets.json', 'evidence.json', 'integration.json', 'ops.json', 'audit.json', 'accounts.json']:
    s = open(os.path.join(D, fn), encoding='utf-8').read()
    for w in bad:
        if w in s:
            e.append('%s 含有"%s"' % (fn, w))
for u in usr['users']:
    if '＊' not in u['name'] or '****' not in u['phoneMasked']: e.append('用户未脱敏 %s' % u['userId'])
check('文案卫生（无 test/xxx/lorem/真实品牌，姓名手机号脱敏）', e)

# ---------- 输出
ok = True
print('mockdata 自检  锚点 %s' % NOW)
print('规模：车辆 %d，站点 %d，桩 %d，用户 %d，订单 %d，流水 %d，结算单 %d，工单 %d，取证 %d，留痕 %d，账号 %d' % (
    len(veh), len(sts['stations']), len(sts['piles']), len(usr['users']), len(ords), len(pay['payments']), len(sett['settlements']), len(tks),
    len(evd['evidence']), len(aud), len(acc['accounts'])))
for name, n, ex in RESULTS:
    print('[%s] %s%s' % ('通过' if n == 0 else '失败', name, '' if n == 0 else '（%d 处）' % n))
    for x in ex:
        print('      ', x)
    ok = ok and n == 0
c, n_, cd, dd, mm, ua = RECON_TOTAL
print('全期对账：渠道到账 %.2f = 订单实收 %.2f + 月卡款 %.2f − 押金抵扣 %.2f + 人工转账退款 %.2f − 渠道未到账 %.2f' % (c / 100, n_ / 100, cd / 100, dd / 100, mm / 100, ua / 100))
sys.exit(0 if ok else 1)
