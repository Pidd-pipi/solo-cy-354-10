#!/bin/bash
# 面交交接模块回归：并发唯一预约 + 双完成路径 + 旧确认入口阻断 + 原行为保留
# 需在干净种子库上运行（4 商品全部 on_sale，无订单）。
BASE=http://localhost:29514/api/v1
PASS=0; FAIL=0

req() { # method path token json -> sets CODE / BODY
  local args=(-s -o /tmp/e2e_resp.json -w "%{http_code}" -X "$1" -H 'Content-Type: application/json')
  [ -n "$3" ] && args+=(-H "Authorization: Bearer $3")
  [ -n "$4" ] && args+=(-d "$4")
  CODE=$(curl "${args[@]}" "$BASE$2"); BODY=$(cat /tmp/e2e_resp.json)
}

check() { # name expected_code
  local got_msg; got_msg=$(echo "$BODY" | jq -r '.message')
  if [ "$CODE" = "$2" ]; then
    PASS=$((PASS+1)); printf 'PASS  %-36s HTTP %s  %s\n' "$1" "$CODE" "$got_msg"
  else
    FAIL=$((FAIL+1)); printf 'FAIL  %-36s expect HTTP %s got %s  %s\n' "$1" "$2" "$CODE" "$got_msg"
  fi
}

expect() { # name actual expected
  if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'PASS  %-36s %s\n' "$1" "$2"
  else FAIL=$((FAIL+1)); printf 'FAIL  %-36s expect %s got %s\n' "$1" "$3" "$2"; fi
}

FUTURE=$(date '+%Y-%m-%d %H:%M' -d '+2 days')
FUTURE2=$(date '+%Y-%m-%d %H:%M' -d '+3 days')
PAST=$(date '+%Y-%m-%d %H:%M' -d '-2 hours')

echo "== 登录 =="
req POST /users/login '' '{"phone":"13700000002","password":"123456"}'; TB=$(jq -r .data.token /tmp/e2e_resp.json)
req POST /users/login '' '{"phone":"13700000001","password":"123456"}'; TS=$(jq -r .data.token /tmp/e2e_resp.json)
req POST /users/login '' '{"phone":"13700000003","password":"123456"}'; TX=$(jq -r .data.token /tmp/e2e_resp.json)
[ -n "$TB" ] && [ -n "$TS" ] && [ -n "$TX" ] && echo "tokens ok" || { echo "login failed"; exit 1; }

echo "== 基础拒绝场景 =="
req POST /trade-orders "$TB" '{"product_id":1}'; O1=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/trade-orders/$O1/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"  \"}"
check "空地点拒绝" 400
req POST "/trade-orders/$O1/appointments" "$TB" "{\"meet_at\":\"$PAST\",\"location\":\"图书馆门口\"}"
check "过期时间拒绝" 400
req POST "/trade-orders/$O1/appointments" "$TX" "{\"meet_at\":\"$FUTURE\",\"location\":\"图书馆门口\"}"
check "越权(非订单双方)拒绝" 403

echo "== 并发：同一订单双方同时发起，仅一份生效 =="
for i in 1 2 3; do
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O1/appointments" \
    -H "Authorization: Bearer $TB" -H 'Content-Type: application/json' \
    -d "{\"meet_at\":\"$FUTURE\",\"location\":\"并发点B$i\"}" > /tmp/conc_b$i.txt) &
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O1/appointments" \
    -H "Authorization: Bearer $TS" -H 'Content-Type: application/json' \
    -d "{\"meet_at\":\"$FUTURE\",\"location\":\"并发点S$i\"}" > /tmp/conc_s$i.txt) &
done
wait
CODES=$(cat /tmp/conc_b*.txt /tmp/conc_s*.txt)
N200=$(echo "$CODES" | grep -cx 200); N409=$(echo "$CODES" | grep -cx 409)
echo "并发响应: $(echo $CODES | tr '\n' ' ')"
expect "并发仅1个成功" "$N200" "1"
expect "并发其余全409" "$N409" "5"
req GET "/trade-orders/$O1/appointments" "$TB"
ACOUNT=$(jq -r '.data | length' /tmp/e2e_resp.json)
ASTATUS=$(jq -r '.data[0].status' /tmp/e2e_resp.json)
expect "库中仅1份预约" "$ACOUNT" "1"
expect "该预约为待响应" "$ASTATUS" "pending"
APPT=$(jq -r '.data[0].id' /tmp/e2e_resp.json)
CPART=$(jq -r '.data[0].counterpart_id' /tmp/e2e_resp.json)
# 胜者的对方才能拒绝该预约（counterpart_id: 1=卖家 2=买家）
CT=$TS; [ "$CPART" = "2" ] && CT=$TB

echo "== 有效预约阻断旧确认入口 =="
req POST "/trade-orders/$O1/buyer-confirm" "$TB"
check "待响应预约下买家确认被阻断" 409
req GET /products/1 ''
expect "商品仍在售" "$(jq -r .data.status /tmp/e2e_resp.json)" "on_sale"

req POST "/appointments/$APPT/reject" "$CT"
check "拒绝预约(原行为保留)" 200
req POST "/trade-orders/$O1/buyer-confirm" "$TB"
check "无有效预约后买家确认恢复" 200
req POST "/trade-orders/$O1/appointments" "$TS" "{\"meet_at\":\"$FUTURE\",\"location\":\"三食堂\"}"
check "卖家再发起预约" 200
APPT=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/appointments/$APPT/accept" "$TB"
check "买家接受预约" 200
req POST "/trade-orders/$O1/seller-confirm" "$TS"
check "已接受预约下卖家确认被阻断" 409
req GET "/trade-orders/me" "$TS"
OST=$(jq -r ".data.items[] | select(.id==$O1) | .status" /tmp/e2e_resp.json)
expect "订单未被旧入口完成" "$OST" "confirmed"
req GET /products/1 ''
expect "商品未被旧入口下架" "$(jq -r .data.status /tmp/e2e_resp.json)" "on_sale"

echo "== 完成路径B：预约双方交接确认 =="
req POST "/appointments/$APPT/handover-confirm" "$TB"
check "买家确认交接" 200
req POST "/appointments/$APPT/handover-confirm" "$TS"
check "卖家确认交接(双方完成)" 200
req GET "/trade-orders/me" "$TS"
ROW=$(jq ".data.items[] | select(.id==$O1)" /tmp/e2e_resp.json)
expect "订单经交接完成" "$(echo "$ROW" | jq -r .status)" "completed"
expect "双方确认状态在列表中" "$(echo "$ROW" | jq -r '[.appointment.buyer_confirmed_at, .appointment.seller_confirmed_at] | map(. != null) | join(",")')" "true,true"
req GET /products/1 ''
expect "商品售出下架" "$(jq -r .data.status /tmp/e2e_resp.json)" "sold"
req POST "/trade-orders/$O1/seller-confirm" "$TS"
check "完成后旧入口仍拒绝" 409

echo "== 完成路径A：无预约时旧流程保留（商品#4 卖家=user1）=="
req POST /trade-orders "$TB" '{"product_id":4}'; O2=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/trade-orders/$O2/buyer-confirm" "$TB"
check "旧流程买家确认" 200
req POST "/trade-orders/$O2/seller-confirm" "$TS"
check "旧流程卖家确认完成" 200
req GET /products/4 ''
expect "旧流程商品售出" "$(jq -r .data.status /tmp/e2e_resp.json)" "sold"

echo "== 改约/取消行为保留（商品#3 卖家=user3）=="
req POST /trade-orders "$TB" '{"product_id":3}'; O3=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/trade-orders/$O3/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"图书馆门口\"}"
check "发起预约" 200
APPT3=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/appointments/$APPT3/reschedule" "$TX" "{\"meet_at\":\"$FUTURE2\",\"location\":\"东门快递点\"}"
check "改约(原行为保留)" 200
APPT3N=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/appointments/$APPT3N/reject" "$TB"
check "拒绝改约后的预约" 200
req POST "/trade-orders/$O3/cancel" "$TB"
check "取消订单(原行为保留)" 200
req POST "/trade-orders/$O3/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"图书馆门口\"}"
check "已取消订单预约拒绝" 409

echo "== 并发：改约与发起竞争下仍只有一份有效预约 =="
req POST /trade-orders "$TB" '{"product_id":3}'; O4=$(jq -r .data.id /tmp/e2e_resp.json)
req POST "/trade-orders/$O4/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"图书馆门口\"}" >/dev/null
APPT4=$(jq -r .data.id /tmp/e2e_resp.json)
# 卖家(user3)改约 + 买家重复发起 同时发生；无论时序如何，最终有效预约 ≤ 1
(curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/appointments/$APPT4/reschedule" \
  -H "Authorization: Bearer $TX" -H 'Content-Type: application/json' \
  -d "{\"meet_at\":\"$FUTURE2\",\"location\":\"改约点\"}" > /tmp/conc_r.txt) &
for i in 1 2; do
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O4/appointments" \
    -H "Authorization: Bearer $TB" -H 'Content-Type: application/json' \
    -d "{\"meet_at\":\"$FUTURE\",\"location\":\"并发点X$i\"}" > /tmp/conc_x$i.txt) &
done
wait
echo "竞争响应: reschedule=$(cat /tmp/conc_r.txt) creates=$(cat /tmp/conc_x*.txt | tr '\n' ' ')"
req GET "/trade-orders/$O4/appointments" "$TB"
ACTIVE=$(jq -r '[.data[] | select(.status=="pending" or .status=="accepted")] | length' /tmp/e2e_resp.json)
expect "竞争后有效预约数≤1" "$([ "$ACTIVE" -le 1 ] && echo ok)" "ok"
TOTAL=$(jq -r '.data | length' /tmp/e2e_resp.json)
echo "订单$O4 预约总数=$TOTAL 有效=$ACTIVE"

echo "== 并发：预约创建 vs 卖家确认收款 互斥（8轮）=="
PIDS=()
for i in $(seq 1 8); do
  req POST /products "$TS" "{\"title\":\"竞态测试商品$i\",\"price\":9.9,\"category\":\"daily\",\"condition\":\"全新\",\"campus\":\"东校区\",\"trade_location\":\"东门\"}"
  PIDS+=($(jq -r .data.id /tmp/e2e_resp.json))
done
INCONSISTENT=0
for i in $(seq 0 7); do
  P=${PIDS[$i]}
  req POST /trade-orders "$TB" "{\"product_id\":$P}"; O=$(jq -r .data.id /tmp/e2e_resp.json)
  req POST "/trade-orders/$O/buyer-confirm" "$TB" >/dev/null  # 订单进入 confirmed
  (curl -s -o /tmp/race_sc.json -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O/seller-confirm" \
    -H "Authorization: Bearer $TS" > /tmp/race_sc.code) &
  (curl -s -o /tmp/race_ac.json -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O/appointments" \
    -H "Authorization: Bearer $TB" -H 'Content-Type: application/json' \
    -d "{\"meet_at\":\"$FUTURE\",\"location\":\"竞态点\"}" > /tmp/race_ac.code) &
  wait
  SC=$(cat /tmp/race_sc.code); AC=$(cat /tmp/race_ac.code)
  req GET "/trade-orders/$O/appointments" "$TB"
  ACT=$(jq -r '[.data[] | select(.status=="pending" or .status=="accepted")] | length' /tmp/e2e_resp.json)
  req GET "/trade-orders/me" "$TS"
  OST=$(jq -r ".data.items[] | select(.id==$O) | .status" /tmp/e2e_resp.json)
  req GET /products/$P ''
  PST=$(jq -r .data.status /tmp/e2e_resp.json)
  if [ "$AC" = "200" ] && [ "$SC" = "409" ] && [ "$OST" = "confirmed" ] && [ "$ACT" = "1" ] && [ "$PST" = "on_sale" ]; then
    V="预约胜出(一致)"
  elif [ "$AC" = "409" ] && [ "$SC" = "200" ] && [ "$OST" = "completed" ] && [ "$ACT" = "0" ] && [ "$PST" = "sold" ]; then
    V="旧确认胜出(一致)"
  else
    V="不一致!!"; INCONSISTENT=$((INCONSISTENT+1))
  fi
  echo "  轮$((i+1)): seller-confirm=$SC create=$AC → 订单=$OST 商品=$PST 有效预约=$ACT [$V]"
done
expect "8轮竞态最终状态全部一致" "$INCONSISTENT" "0"

echo
echo "==================================="
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = "0" ]
