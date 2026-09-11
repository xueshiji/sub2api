#!/usr/bin/env bash
# 重置所有用户订阅的用量窗口：把每条订阅的日/周/月已用额度清零。
# 不会修改限额配置，只清零当前窗口内的用量。
#
# 依赖：curl、jq
#
# 用法：
#   BASE_URL=http://127.0.0.1:8080 ADMIN_API_KEY=sk-xxx ./tools/reset_all_subscription_quotas.sh
#
# 环境变量：
#   BASE_URL       必填，面板地址（不带 /api/v1 后缀）
#   ADMIN_API_KEY  必填（与 ADMIN_JWT 二选一），面板「系统设置 → Admin API Key」生成
#   ADMIN_JWT      可选，改用管理员登录 JWT 认证（Authorization: Bearer）
#   STATUS         可选，订阅状态过滤，如 active；默认不过滤（重置全部订阅）
#   DRY_RUN=1      只列出将被重置的订阅，不实际执行
#
# 对应接口：
#   GET  /api/v1/admin/subscriptions
#   POST /api/v1/admin/subscriptions/:id/reset-quota  {"daily":true,"weekly":true,"monthly":true}
set -euo pipefail

BASE_URL="${BASE_URL:-}"
ADMIN_API_KEY="${ADMIN_API_KEY:-}"
ADMIN_JWT="${ADMIN_JWT:-}"
STATUS="${STATUS:-}"
DRY_RUN="${DRY_RUN:-0}"

if [[ -z "$BASE_URL" ]]; then
  echo "错误：请设置 BASE_URL，例如 BASE_URL=http://127.0.0.1:8080" >&2
  exit 1
fi
if [[ -z "$ADMIN_API_KEY" && -z "$ADMIN_JWT" ]]; then
  echo "错误：请设置 ADMIN_API_KEY（面板「系统设置 → Admin API Key」生成）或 ADMIN_JWT" >&2
  exit 1
fi
for cmd in curl jq; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "错误：缺少 $cmd，请先安装" >&2; exit 1; }
done

if [[ -n "$ADMIN_API_KEY" ]]; then
  AUTH_HEADER=("x-api-key: $ADMIN_API_KEY")
else
  AUTH_HEADER=("Authorization: Bearer $ADMIN_JWT")
fi

PAGE=1
IDS=()
TOTAL=0
while :; do
  RESP=$(curl -sS -w '\n%{http_code}' \
    -G "$BASE_URL/api/v1/admin/subscriptions" \
    -H "${AUTH_HEADER[0]}" \
    --data-urlencode "page=$PAGE" \
    --data-urlencode "page_size=1000" \
    ${STATUS:+--data-urlencode "status=$STATUS"} \
  ) || { echo "错误：请求订阅列表失败（page=$PAGE）" >&2; exit 1; }
  HTTP_CODE=$(tail -n1 <<<"$RESP")
  BODY=$(sed '$d' <<<"$RESP")
  [[ "$HTTP_CODE" == "200" ]] || { echo "错误：订阅列表返回 HTTP $HTTP_CODE：$BODY" >&2; exit 1; }
  PAGE_IDS=$(jq -r 'if .code == 0 then .data.items[].id else empty end' <<<"$BODY" 2>/dev/null) \
    || { echo "错误：解析订阅列表响应失败：$BODY" >&2; exit 1; }
  while IFS= read -r id; do
    [[ -n "$id" ]] && IDS+=("$id")
  done <<<"$PAGE_IDS"
  TOTAL=$(jq -r '.data.total // 0' <<<"$BODY")
  PAGES=$(jq -r '.data.pages // 1' <<<"$BODY")
  (( PAGE >= PAGES )) && break
  PAGE=$((PAGE + 1))
done

echo "共获取 $TOTAL 条订阅（${#IDS[@]} 个 ID）"
if (( ${#IDS[@]} == 0 )); then
  echo "没有需要重置的订阅"
  exit 0
fi

if [[ "$DRY_RUN" == "1" ]]; then
  printf 'DRY_RUN：将重置以下订阅\n%s\n' "${IDS[*]}"
  exit 0
fi

OK=0
FAIL=0
for id in "${IDS[@]}"; do
  RESP=$(curl -sS -w '\n%{http_code}' -X POST \
    "$BASE_URL/api/v1/admin/subscriptions/$id/reset-quota" \
    -H "${AUTH_HEADER[0]}" \
    -H 'Content-Type: application/json' \
    -d '{"daily":true,"weekly":true,"monthly":true}' \
  ) || { echo "[$id] 请求失败" >&2; FAIL=$((FAIL + 1)); continue; }
  HTTP_CODE=$(tail -n1 <<<"$RESP")
  BODY=$(sed '$d' <<<"$RESP")
  if [[ "$HTTP_CODE" == "200" ]] && [[ $(jq -r '.code // -1' <<<"$BODY") == "0" ]]; then
    OK=$((OK + 1))
    echo "[$id] 重置成功"
  else
    FAIL=$((FAIL + 1))
    echo "[$id] 重置失败 HTTP $HTTP_CODE：$BODY" >&2
  fi
done

echo "完成：成功 $OK，失败 $FAIL"
(( FAIL == 0 )) || exit 1
