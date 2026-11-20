#!/bin/bash
# 分进程压测：服务端、监听者、发布者各一个进程。
# 用法: ./bench.sh [监听者数] [发布者数] [每发布者请求数] [地址]
set -e
cd "$(dirname "$0")/../.."
go build -o /tmp/event-bench ./cmd/bench

LISTENERS=${1:-4}
PUBLISHERS=${2:-16}
REQS=${3:-20000}
ADDR=${4:-:18080}

/tmp/event-bench -mode server -addr "$ADDR" >/tmp/bench-server.log 2>&1 &
SERVER=$!
sleep 1
/tmp/event-bench -mode listener -addr "$ADDR" -count "$LISTENERS" >/tmp/bench-listener.log 2>&1 &
LISTENER=$!
sleep 2

echo "== 同步 Emit（$LISTENERS 监听者 × $PUBLISHERS 发布者 × $REQS）=="
/tmp/event-bench -mode publisher -addr "$ADDR" -count "$PUBLISHERS" -reqs "$REQS"

echo "== EmitAsync 窗口 256 =="
/tmp/event-bench -mode publisher -addr "$ADDR" -count "$PUBLISHERS" -reqs "$REQS" -async 16

kill "$SERVER" "$LISTENER" 2>/dev/null || true
