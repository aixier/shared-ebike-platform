#!/usr/bin/env bash
# 本机联调灌种子：只用 curl 调网关（http://127.0.0.1:18300）与服务的内部入口（127.0.0.1），可重复执行（已有的跳过）。
# 灌入：运营商与品牌登记（fleet 登记接口）→ 价格规则 → 站点与桩 → 品牌上线 → 运营商密钥（写给 mock 运营商）→ 车辆清单（发政府码）→ 启动 mock 运营商。
# 前提：PostgreSQL、NATS、15 个服务与网关已在运行（fleet 须是带登记接口的新版）；opgw 已带 OPGW_BASE_URLS 启动（写在 extra.tsv 里，首次需要 bash deploy/local/svc-restart.sh opgw）。
#       运营商与品牌走 fleet 的登记接口，已登记的（409 ALREADY_DONE）跳过；fleet 的 FLEET_SEED_FILE 仅作兜底，不再是必需。
# 用法：bash deploy/local/seed.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
set +e

G="${BIKE_GATEWAY:-http://127.0.0.1:18300}"
SEED="$LOCAL_DIR/seed"
CREDS="$SECRETS_DIR/extra/mock-operator-creds.json"
PWD_ALL="Shifan@2026"
ok() { printf '  ok   %s\n' "$*"; }
skip() { printf '  跳过 %s\n' "$*"; }
fail() { printf '  失败 %s\n' "$*" >&2; exit 1; }
field() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" | head -1; }

# 登录管理端账号，输出令牌。JSON 走标准输入，避免命令行里的中文在 Windows 上被转码
login() {
  curl -s -m10 -X POST "$G/api/iam/login" -H 'content-type: application/json' --data-binary @- <<EOF | field token
{"username":"$1","password":"$PWD_ALL","client":"管理端"}
EOF
}

echo "== 0 检查前提"
curl -fsS -m3 "$G/api/rule/pricing/current" -o /dev/null -w '' 2>/dev/null; code=$(curl -s -m3 -o /dev/null -w '%{http_code}' "$G/api/site/map")
[[ "$code" == 200 ]] || fail "网关或 site 不通（$G/api/site/map 返回 $code），先确认服务与网关在运行"
ZG="$(login zgbm.zhou)"; [[ -n "$ZG" ]] || fail "主管部门账号登录失败（iam 没有种子账号？）"
ok "网关、iam 就绪"

echo "== 0b 运营商与品牌登记（fleet 登记接口；中文 JSON 用文件 --data-binary 传）"
OBK="$(cat "$SECRETS_DIR/service/onboard.key")"
register() { # 路径 文件 幂等键 名称
  local out code body
  out="$(curl -s -m10 -w '\n%{http_code}' -X POST "$G$1" -H "Authorization: Bearer $ZG" -H 'content-type: application/json' -H "Idempotency-Key: $3" --data-binary @"$2")"
  code="${out##*$'\n'}"; body="${out%$'\n'*}"
  case "$code" in
    200) ok "已登记 $4" ;;
    409) grep -q ALREADY_DONE <<<"$body" && skip "$4 已登记" || fail "$4 登记冲突：$body" ;;
    404|403) fail "登记 $4 失败（$code）：$body；fleet 是否已用新代码重启（bash deploy/local/svc-restart.sh fleet）？" ;;
    *) fail "登记 $4 失败（$code）：$body" ;;
  esac
}
for f in "$SEED"/register/operator-*.json; do n="$(basename "$f" .json)"; register /api/fleet/operators "$f" "seed-reg-$n" "运营商 ${n#operator-}"; done
for f in "$SEED"/register/brand-*.json; do n="$(basename "$f" .json)"; register /api/fleet/brands "$f" "seed-reg-$n" "品牌 ${n#brand-}"; done
# 新登记的品牌是未接入，开始联调后才能提交车辆清单
for f in "$SEED"/register/brand-*.json; do
  b="$(basename "$f" .json)"; b="${b#brand-}"
  st="$(curl -s -m5 "$G/api/fleet/brands/$b" -H "Authorization: Bearer $ZG" | field accessStatus)"
  if [[ "$st" == "未接入" ]]; then
    r="$(curl -s -m10 -X POST "http://127.0.0.1:18305/internal/fleet/brands/$b/start-integration" -H "Authorization: Bearer $OBK" -H 'X-Caller: onboard' -H 'content-type: application/json' -H "Idempotency-Key: seed-integ-$b" --data-binary @- <<'EOF'
{"actor":{"kind":"系统","id":"AC0900"}}
EOF
)"
    grep -q '"accessStatus":"联调中"' <<<"$r" && ok "$b 联调中" || fail "$b 开始联调失败：$r"
  fi
done

echo "== 1 价格规则"
if curl -s -m5 "$G/api/rule/pricing/current" -H "Authorization: Bearer $ZG" | grep -q '"versionId"'; then skip "已有现行价格版本"
else
  r="$(curl -s -m10 -X POST "$G/api/rule/pricing/versions" -H "Authorization: Bearer $ZG" -H 'content-type: application/json' -H 'Idempotency-Key: seed-price-1' --data-binary @- <<'EOF'
{"name":"本机联调首版价格","reason":"本机联调初始价格","ride":{"startFee":150,"startMinutes":3,"perMinute":50},"dispatchFee":500,
"monthCard":{"price":2500,"days":30,"perOrderCap":300,"allocation":"按骑行单数"},
"coupons":[{"templateId":"CPT01","name":"新用户体验券","amount":200,"audience":"新注册并实名用户","validDays":7,"issue":"注册后自动发放","perUser":1},{"templateId":"CPT04","name":"开锁失败关怀券","amount":100,"audience":"当天首次开锁失败的用户","validDays":7,"issue":"开锁失败后自动发放","perUser":1}],
"deposit":{"amount":19900},"creditFree":{"threshold":550},
"serviceFee":{"basis":"订单应收","ratios":{"BR01":0.62,"BR02":0.6,"BR03":0.61}},
"riskLimits":{"perUserOpenDegradedOrders":1,"normal":{"maxMinutes":120,"maxAmount":6000},"tight":{"maxMinutes":30,"maxAmount":1500},"hourlyDegradedOrders":200,"degradeDurationAlertMinutes":30}}
EOF
)"
  grep -q '"versionId"' <<<"$r" && ok "已录入价格版本 $(field versionId <<<"$r")" || fail "价格录入失败：$r"
fi

echo "== 2 站点与桩"
stations="$(curl -s -m5 "$G/api/site/admin/stations/all" -H "Authorization: Bearer $ZG")"
mkstation() { # 名称 区 类型 围栏 幂等键
  curl -s -m10 -X POST "$G/api/site/admin/stations" -H "Authorization: Bearer $ZG" -H 'content-type: application/json' -H "Idempotency-Key: $5" --data-binary @- <<EOF
{"name":"$1","district":"$2","type":"$3","fence":$4,"capacity":10,"reason":"本机联调初始站点"}
EOF
}
if grep -q '联调1号站' <<<"$stations"; then skip "站点 联调1号站（有桩）"; else
  r="$(mkstation '联调1号站（有桩）' 东城区 有桩 '[[104.0655,30.5955],[104.0665,30.5955],[104.0665,30.5965],[104.0655,30.5965]]' seed-st-1)"
  grep -q '"stationId"' <<<"$r" && ok "站点 $(field stationId <<<"$r") 联调1号站（有桩）" || fail "建站失败：$r"
fi
if grep -q '联调2号站' <<<"$stations"; then skip "站点 联调2号站（无桩）"; else
  r="$(mkstation '联调2号站（无桩）' 西城区 无桩 '[[104.0555,30.5855],[104.0565,30.5855],[104.0565,30.5865],[104.0555,30.5865]]' seed-st-2)"
  grep -q '"stationId"' <<<"$r" && ok "站点 $(field stationId <<<"$r") 联调2号站（无桩）" || fail "建站失败：$r"
fi
st1="$(curl -s -m5 "$G/api/site/admin/stations/all" -H "Authorization: Bearer $ZG" | grep -o '"stationId":"ST[0-9]*","name":"联调1号站' | head -1 | field stationId)"
[[ -n "$st1" ]] || st1=ST0001
piles="$(curl -s -m5 "$G/api/site/admin/piles?stationId=$st1" -H "Authorization: Bearer $ZG")"
if grep -q '"pileId"' <<<"$piles"; then skip "$st1 已有桩"; else
  i=0
  for lng in 104.0657 104.0659 104.0661 104.0663; do
    i=$((i + 1))
    r="$(curl -s -m10 -X POST "$G/api/site/admin/piles" -H "Authorization: Bearer $ZG" -H 'content-type: application/json' -H "Idempotency-Key: seed-pile-$i" --data-binary @- <<EOF
{"stationId":"$st1","location":[$lng,30.5960],"reason":"本机联调初始桩"}
EOF
)"
    grep -q '"pileId"' <<<"$r" && ok "桩 $(field pileId <<<"$r")（$st1）" || fail "登记桩失败：$r"
  done
fi

echo "== 3 品牌上线（联调中 -> 已接入，触发 opgw 生成生产凭证）"
for b in BR01 BR02 BR03; do
  st="$(curl -s -m5 "$G/api/fleet/brands/$b" -H "Authorization: Bearer $ZG" | field accessStatus)"
  if [[ "$st" == "联调中" ]]; then
    r="$(curl -s -m10 -X POST "http://127.0.0.1:18305/internal/fleet/brands/$b/go-live" -H "Authorization: Bearer $OBK" -H 'X-Caller: onboard' -H 'content-type: application/json' -H "Idempotency-Key: seed-golive-$b" --data-binary @- <<'EOF'
{"actor":{"kind":"系统","id":"AC0900"},"reason":"本机联调上线"}
EOF
)"
    grep -q '"accessStatus":"已接入"' <<<"$r" && ok "$b 已接入" || fail "$b 上线失败：$r"
  else skip "$b 已是 ${st:-未知}"; fi
done

echo "== 4 运营商密钥（只在本机写给 mock 运营商，不入库）"
MAO="$(login yw.mao)"; [[ -n "$MAO" ]] || fail "运维接入负责人登录失败"
mkdir -p "$(dirname "$CREDS")"; [[ -s "$CREDS" ]] || echo '{}' > "$CREDS"
sleep 1
list="$(curl -s -m5 "$G/api/opgw/credentials" -H "Authorization: Bearer $MAO")"
pairs="$(grep -o '"credentialId":"CRD[0-9]*","appId":"app_[0-9a-f]*"' <<<"$list")"
[[ -n "$pairs" ]] || fail "opgw 还没有生产凭证（品牌是否已接入？）"
new="{"
first=1
while IFS= read -r p; do
  cid="$(field credentialId <<<"{$p}")"; app="$(field appId <<<"{$p}")"
  sec="$(grep -o "\"$app\": *\"sk_[0-9a-f]*\"" "$CREDS" | sed 's/.*: *"\(sk_[0-9a-f]*\)"/\1/')"
  if [[ -z "$sec" ]]; then
    r="$(curl -s -m10 -X POST "$G/api/opgw/credentials/$cid/rotate" -H "Authorization: Bearer $MAO" -H 'content-type: application/json' -H "Idempotency-Key: seed-rot-$cid-$(date +%s)" --data-binary @- <<'EOF'
{"reason":"本机联调取得密钥"}
EOF
)"
    sec="$(field secret <<<"$r")"
    [[ -n "$sec" ]] || fail "取密钥失败 $cid：$r"
    ok "凭证 $cid（$app）已轮换并保存密钥"
  else skip "凭证 $cid（$app）密钥已在本机"; fi
  [[ $first == 1 ]] || new+=","; first=0
  new+="\"$app\":\"$sec\""
done <<<"$pairs"
new+="}"
printf '%s' "$new" > "$CREDS"

echo "== 5 车辆清单（发政府码；已存在的车更新车型）"
upload() { # 账号 文件 幂等键
  tok="$(login "$1")"; [[ -n "$tok" ]] || fail "$1 登录失败"
  r="$(curl -s -m20 -X POST "$G/api/fleet/operator/vehicles/upload" -H "Authorization: Bearer $tok" -H 'content-type: application/json' -H "Idempotency-Key: $3" --data-binary @"$2")"
  grep -q '"added"' <<<"$r" && ok "$1 上传 $(sed -n 's/.*"added":\([0-9]*\),"updated":\([0-9]*\).*/新增 \1 更新 \2/p' <<<"$r")" || fail "$1 上传失败：$r"
}
upload qhcx.he "$SEED/vehicles-OP01.json" seed-veh-OP01-$(date +%s)
upload clzx.shen "$SEED/vehicles-OP02.json" seed-veh-OP02-$(date +%s)

echo "== 6 mock 运营商"
bash "$LOCAL_DIR/mock-operator.sh" start

echo "== 7 政府码（扫码用）"
for t in qhcx.he clzx.shen; do
  tok="$(login $t)"
  curl -s -m5 "$G/api/fleet/operator/vehicles?pageSize=20" -H "Authorization: Bearer $tok" | grep -o '"govCode":"SF[0-9]*"[^}]*"plateNo":"[A-Z0-9]*"\|"plateNo":"[A-Z0-9]*"[^}]*"govCode":"SF[0-9]*"' | sed 's/"//g' | head -12
done
echo
echo "完成。打开 $G/app/index.html?backend=1 （骑行用户）、$G/admin/index.html?backend=1 （管理端，账号 zgbm.zhou / qhcx.he / kf.su 等，密码 $PWD_ALL）。"
