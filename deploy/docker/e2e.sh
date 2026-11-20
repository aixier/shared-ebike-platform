#!/usr/bin/env bash
# 主链路冒烟（对着单镜像容器，先跑过 seed.sh）：授权实名免押 -> 扫码 -> 开锁 -> 骑行 -> 桩旁还车 -> 账单 -> 一键支付 -> 订单列表；
# 再用管理端 admin/Shifan@2026（需 IAM_DEV_SEED_ADMIN=1）登录查订单。每次用新手机号注册新用户（BIKE_PHONE 可指定）。
# 骑两单：第一单的账单会被新用户体验券全额抵扣（应付 0，无需支付）；第二单无券，走一键支付。
# 业务调用只用 curl 打网关；桩在位通过容器内 bike-sim 向网关 /device/v1/ 发 occupancy（等价真实桩设备）。
# 说明：平台在桩识别在位后等 PILE_NOLOCK_WAIT_SEC（默认 15 秒）仍无锁车报告，才请求运营商结束骑行，所以还车要等十几秒。
set -uo pipefail
G="${BIKE_GATEWAY:-http://127.0.0.1:28300}"; C="${BIKE_CONTAINER:-bike-single}"
PHONE="${BIKE_PHONE:-139$(date +%s | tail -c 9)}"
H='content-type: application/json'
fld() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" | head -1; }
num() { sed -n "s/.*\"$1\":\\([0-9-]*\\).*/\\1/p" | head -1; }
step() { printf '\n== %s\n' "$*"; }
need() { [[ -n "$2" ]] || { echo "失败：$1" >&2; exit 1; }; }
dxb() { MSYS_NO_PATHCONV=1 docker exec -i -u bike "$C" "$@"; }
call() { # 方法 路径 令牌 幂等键 [JSON 走标准输入]
  local m=$1 p=$2 t=$3 k=$4; shift 4
  local a=(-s -m30 -X "$m" "$G$p" -H "$H"); [[ -n "$t" ]] && a+=(-H "Authorization: Bearer $t"); [[ -n "$k" ]] && a+=(-H "Idempotency-Key: $k")
  if [[ "$m" == POST ]]; then a+=(--data-binary @-); curl "${a[@]}"; else curl "${a[@]}" </dev/null; fi
}
RUN="$(date +%s)"

step "1 授权登录（手机号 $PHONE）"
r="$(call POST /api/acct/authorize "" e2e-auth-$RUN <<EOF
{"channel":"微信","phone":"$PHONE"}
EOF
)"; echo "$r" | cut -c1-120
TOK="$(fld token <<<"$r")"; need "授权登录没拿到令牌" "$TOK"

step "2 实名核验（免押前置）"
body17="11010519900307123"; w=(7 9 10 5 8 4 2 1 6 3 7 9 10 5 8 4 2); s=0   # 18 位身份证号，校验位按 GB 11643 现算
for i in $(seq 0 16); do s=$(( s + ${body17:$i:1} * ${w[$i]} )); done
chk=(1 0 X 9 8 7 6 5 4 3 2); ID="${body17}${chk[$((s % 11))]}"
call POST /api/acct/real-name "$TOK" e2e-rn-$RUN <<EOF | cut -c1-120
{"name":"张三丰","idNo":"$ID"}
EOF
echo

step "3 开通信用免押"
call POST /api/deposit/me/credit-free "$TOK" e2e-cf-$RUN <<<'{}' | cut -c1-300; echo
sleep 4   # 押金状态经事件同步到 ride（最终一致），马上扫码可能还看不到

ZG="$(call POST /api/iam/login "" "" <<EOF | fld token
{"username":"qhcx.he","password":"Shifan@2026","client":"管理端"}
EOF
)"; need "运营商账号登录失败" "$ZG"
veh="$(call GET "/api/fleet/operator/vehicles?pageSize=50" "$ZG" "")"

ride_once() { # 车号 桩位号
  local PLATE=$1 SLOT=$2 GOV TAG r ORDER st
  GOV="$(grep -o "\"govCode\":\"SF[0-9]*\"[^}]*\"plateNo\":\"$PLATE\"\|\"plateNo\":\"$PLATE\"[^}]*\"govCode\":\"SF[0-9]*\"" <<<"$veh" | grep -o 'SF[0-9]*' | head -1)"
  TAG="E2000000000000000000000${PLATE: -1}"
  need "找不到 $PLATE 的政府码（先跑 seed.sh）" "$GOV"
  step "[$PLATE] 4 扫码（政府码 $GOV）"
  call POST /api/ride/scan "$TOK" "" <<EOF | grep -o '"check":{[^}]*}' ; echo
{"code":"$GOV"}
EOF
  step "[$PLATE] 5 开锁（运营商模拟器受理后经网关 /open/v1/ride-events 报告开锁成功）"
  r="$(call POST /api/ride/unlock "$TOK" e2e-unlock-$PLATE-$RUN <<EOF
{"code":"$GOV"}
EOF
)"
  ORDER="$(fld orderId <<<"$r")"; need "开锁没拿到订单号：$r" "$ORDER"
  echo "订单 $ORDER 状态 $(fld status <<<"$r")"
  step "[$PLATE] 6 骑行中订单"
  call GET /api/ride/current "$TOK" "" | grep -o '"\(orderId\|status\|plateNo\|elapsedSec\)":[^,]*' | tr '\n' ' '; echo
  step "[$PLATE] 7 桩旁还车：桩 $SLOT 号位识别到该车标签（桩设备 occupancy）"
  dxb sh -c "set -a; . /data/env/sim.env; set +a; /opt/bike/bin/bike-sim pile-sim occupy $SLOT $TAG" 2>&1 | tail -1 | cut -c1-120
  echo "等待平台在桩识别 15 秒仍无锁车报告后请求运营商结束骑行（PILE_NOLOCK_WAIT_SEC，BR-09）..."
  for i in $(seq 1 40); do
    r="$(call GET "/api/ride/orders/$ORDER" "$TOK" "")"; st="$(fld status <<<"$r")"
    [[ "$st" != "骑行中" ]] && break; sleep 3
  done
  echo "订单状态：$st"; echo "$r" | grep -o '"\(endMethod\|inStationBasis\|durationSec\|endPileId\|endAt\)":[^,]*' | tr '\n' ' '; echo
  [[ "$st" == "已结束" ]] || { echo "失败：订单没有结束（$st）" >&2; exit 1; }
  dxb sh -c "set -a; . /data/env/sim.env; set +a; /opt/bike/bin/bike-sim pile-sim leave $SLOT" >/dev/null 2>&1   # 桩位还原
  step "[$PLATE] 8 账单（订单结束后由 bill 订阅事件生成）"
  local b payable
  for i in $(seq 1 20); do b="$(call GET "/api/bill/me/orders/$ORDER" "$TOK" "")"; grep -q '"payable"' <<<"$b" && break; sleep 2; done
  echo "$b" | grep -o '"\(rideFee\|receivable\|couponDeduct\|monthCardDeduct\|payable\|channelPaid\|payStatus\)":[^,]*' | tr '\n' ' '; echo
  payable="$(num payable <<<"$b")"
  step "[$PLATE] 9 一键支付（应付 ${payable:-?} 分；模拟渠道）"
  r="$(call POST "/api/bill/me/orders/$ORDER/pay-intent" "$TOK" e2e-pay-$PLATE-$RUN <<<'{}')"; echo "$r" | cut -c1-300
  if [[ "${payable:-0}" -gt 0 ]]; then
    for i in $(seq 1 10); do b="$(call GET "/api/bill/me/orders/$ORDER" "$TOK" "")"; grep -q '"payStatus":"已付清"\|"payStatus":"已支付"' <<<"$b" && break; sleep 2; done
    echo "支付后：$(echo "$b" | grep -o '"\(payable\|channelPaid\|payStatus\)":[^,]*' | tr '\n' ' ')"
  fi
  LAST_ORDER="$ORDER"
}

ride_once QH000001 1
ride_once QH000002 2

step "10 订单列表（用户端）"
call GET "/api/ride/orders?page=1&pageSize=5" "$TOK" "" | grep -o '"\(orderId\|status\|plateNo\)":"[^"]*"' | tr '\n' ' '; echo
call GET "/api/bill/me/orders?orderIds=$LAST_ORDER" "$TOK" "" | cut -c1-500; echo

step "11 管理端 admin 登录并查订单"
AD="$(call POST /api/iam/login "" "" <<EOF | fld token
{"username":"admin","password":"Shifan@2026","client":"管理端"}
EOF
)"; need "admin 登录失败（容器要带 IAM_DEV_SEED_ADMIN=1）" "$AD"
echo "admin 令牌 ${AD:0:16}..."
call GET "/api/ride/admin/orders?page=1&pageSize=5" "$AD" "" | grep -o '"\(orderId\|status\|plateNo\)":"[^"]*"' | tr '\n' ' '; echo
call GET "/api/ride/admin/orders/$LAST_ORDER" "$AD" "" | grep -o '"\(orderId\|status\|plateNo\|endMethod\)":"[^"]*"' | tr '\n' ' '; echo
echo "主链路完成，最后一单 $LAST_ORDER"
