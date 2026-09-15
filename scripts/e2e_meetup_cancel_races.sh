#!/bin/bash
# 取消相关竞态回归：取消 vs 创建、取消 vs 交接
# 修复后语义：
#   取消 vs 创建 —— 取消总是赢：订单 cancelled 且不再保留有效预约
#     （创建先提交则预约被同事务作废；取消先提交则创建 409）
#   取消 vs 交接 —— 恰好一条路径成功：
#     取消赢 → cancelled + 商品 on_sale + 无有效预约；交接赢 → completed + 商品 sold
# 需在干净种子库上运行。
BASE=http://localhost:29514/api/v1
PASS=0; FAIL=0
FUTURE=$(date '+%Y-%m-%d %H:%M' -d '+2 days')

req() { # method path token json -> sets CODE / BODY
  local args=(-s -o /tmp/cr_resp.json -w "%{http_code}" -X "$1" -H 'Content-Type: application/json')
  [ -n "$3" ] && args+=(-H "Authorization: Bearer $3")
  [ -n "$4" ] && args+=(-d "$4")
  CODE=$(curl "${args[@]}" "$BASE$2"); BODY=$(cat /tmp/cr_resp.json)
}

expect() { # name actual expected
  if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'PASS  %-40s %s\n' "$1" "$2"
  else FAIL=$((FAIL+1)); printf 'FAIL  %-40s expect %s got %s\n' "$1" "$3" "$2"; fi
}

req POST /users/login '' '{"phone":"13700000002","password":"123456"}'; TB=$(jq -r .data.token /tmp/cr_resp.json)
req POST /users/login '' '{"phone":"13700000001","password":"123456"}'; TS=$(jq -r .data.token /tmp/cr_resp.json)
[ -n "$TB" ] && [ -n "$TS" ] && echo "tokens ok" || { echo "login failed"; exit 1; }

mkproduct() {
  req POST /products "$TS" "{\"title\":\"取消竞态商品$1\",\"price\":9.9,\"category\":\"daily\",\"condition\":\"全新\",\"campus\":\"东校区\",\"trade_location\":\"东门\"}"
  jq -r .data.id /tmp/cr_resp.json
}

echo "== 竞态：取消 vs 创建（6轮，取消必须总是赢且无有效预约残留）=="
BAD1=0
for i in $(seq 1 6); do
  P=$(mkproduct "A$i")
  req POST /trade-orders "$TB" "{\"product_id\":$P}"; O=$(jq -r .data.id /tmp/cr_resp.json)
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O/cancel" \
    -H "Authorization: Bearer $TB" > /tmp/cr_cancel.code) &
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O/appointments" \
    -H "Authorization: Bearer $TS" -H 'Content-Type: application/json' \
    -d "{\"meet_at\":\"$FUTURE\",\"location\":\"取消竞态点\"}" > /tmp/cr_create.code) &
  wait
  CC=$(cat /tmp/cr_cancel.code); AC=$(cat /tmp/cr_create.code)
  req GET "/trade-orders/$O/appointments" "$TB"
  ACT=$(jq -r '[.data[] | select(.status=="pending" or .status=="accepted")] | length' /tmp/cr_resp.json)
  AST=$(jq -r '.data[0].status // "none"' /tmp/cr_resp.json)
  req GET /trade-orders/me "$TB"
  OST=$(jq -r ".data.items[] | select(.id==$O) | .status" /tmp/cr_resp.json)
  # 取消必须成功；订单必须 cancelled；不得有有效预约
  if [ "$CC" = "200" ] && [ "$OST" = "cancelled" ] && [ "$ACT" = "0" ]; then
    if [ "$AC" = "409" ]; then V="取消先提交，创建409(一致)"
    elif [ "$AC" = "200" ] && [ "$AST" = "cancelled" ]; then V="创建先提交，预约被作废(一致)"
    else V="不一致!!"; BAD1=$((BAD1+1)); fi
  else
    V="不一致!!"; BAD1=$((BAD1+1))
  fi
  echo "  轮$i: cancel=$CC create=$AC → 订单=$OST 有效预约=$ACT 预约状态=$AST [$V]"
done
expect "取消vs创建 6轮全部一致" "$BAD1" "0"

echo "== 竞态：取消 vs 交接（6轮，恰好一条路径成功）=="
BAD2=0
for i in $(seq 1 6); do
  P=$(mkproduct "B$i")
  req POST /trade-orders "$TB" "{\"product_id\":$P}"; O=$(jq -r .data.id /tmp/cr_resp.json)
  req POST "/trade-orders/$O/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"交接竞态点\"}"
  A=$(jq -r .data.id /tmp/cr_resp.json)
  req POST "/appointments/$A/accept" "$TS" >/dev/null
  req POST "/appointments/$A/handover-confirm" "$TB" >/dev/null  # 买家先确认，卖家确认即完成
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/trade-orders/$O/cancel" \
    -H "Authorization: Bearer $TB" > /tmp/cr_cancel.code) &
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/appointments/$A/handover-confirm" \
    -H "Authorization: Bearer $TS" > /tmp/cr_hand.code) &
  wait
  CC=$(cat /tmp/cr_cancel.code); HC=$(cat /tmp/cr_hand.code)
  req GET /trade-orders/me "$TB"
  OST=$(jq -r ".data.items[] | select(.id==$O) | .status" /tmp/cr_resp.json)
  req GET /products/$P ''
  PST=$(jq -r .data.status /tmp/cr_resp.json)
  req GET "/trade-orders/$O/appointments" "$TB"
  AST=$(jq -r '.data[0].status // "none"' /tmp/cr_resp.json)
  if [ "$CC" = "200" ] && [ "$HC" = "409" ] && [ "$OST" = "cancelled" ] && [ "$PST" = "on_sale" ] && [ "$AST" = "cancelled" ]; then
    V="取消胜出(一致)"
  elif [ "$CC" = "409" ] && [ "$HC" = "200" ] && [ "$OST" = "completed" ] && [ "$PST" = "sold" ] && [ "$AST" = "completed" ]; then
    V="交接胜出(一致)"
  else
    V="不一致!!"; BAD2=$((BAD2+1))
  fi
  echo "  轮$i: cancel=$CC handover=$HC → 订单=$OST 商品=$PST 预约=$AST [$V]"
done
expect "取消vs交接 6轮全部一致" "$BAD2" "0"

echo "== 取消后操作全部失败 =="
# 取上一轮 B 中一个已取消的订单做操作面验证（重新造一个更直观）
P=$(mkproduct "C1")
req POST /trade-orders "$TB" "{\"product_id\":$P}"; O=$(jq -r .data.id /tmp/cr_resp.json)
req POST "/trade-orders/$O/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"取消前地点\"}"
A=$(jq -r .data.id /tmp/cr_resp.json)
req POST "/appointments/$A/accept" "$TS" >/dev/null
req POST "/trade-orders/$O/cancel" "$TS"
expect "取消订单(接受中的预约被作废)" "$CODE" "200"
req GET "/trade-orders/$O/appointments" "$TB"
AST=$(jq -r '.data[0].status' /tmp/cr_resp.json)
expect "已接受预约被置为cancelled" "$AST" "cancelled"
req POST "/trade-orders/$O/appointments" "$TB" "{\"meet_at\":\"$FUTURE\",\"location\":\"图书馆门口\"}"
expect "取消后创建失败" "$CODE" "409"
req POST "/appointments/$A/accept" "$TS"
expect "取消后接受失败" "$CODE" "409"
req POST "/appointments/$A/handover-confirm" "$TB"
expect "取消后交接失败" "$CODE" "409"

echo
echo "==================================="
echo "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = "0" ]
