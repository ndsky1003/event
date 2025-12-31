# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**请使用中文与用户交流。**

## Overview

This is a distributed event system based on the observer pattern, written in Go (module `github.com/ndsky1003/event/v2`). It enables event-driven communication between multiple services/processes over TCP, with support for regular expression event matching.

**底层网络层使用 `github.com/ndsky1003/net/v2` 库。**

## Project Structure

```
event/
├── api.go           # Public API (EmitOne, EmitAll, EmitFirst, On, etc.)
├── server.go        # Server implementation (wraps net.Server)
├── server_mgr.go    # server_manager implementation
├── client.go        # Client implementation (wraps net.Client)
├── client_handler.go # conn.Handler implementation for client
├── opt.go           # Options pattern for Client/Server
├── codec.go         # gob encoding/decoding helpers
├── call.go          # Call objects for async operations
├── method.go        # Method reflection helpers
├── err.go           # Error definitions
├── msg/             # Message types
├── msgtype/         # Message type constants (ReqAll, ReqOne, ReqFirst, Res, On, etc.)
├── eventname/       # Event name type (supports regex via "/pattern/")
└── topic/           # Topic with regex matching support
```

## Architecture

### Core Components

```
                    ┌─────────────────────────────────┐
                    │         event.Server            │
                    │  (包装 net.Server + eventMgr)    │
                    └─────────────┬───────────────────┘
                                  │ implements
                    ┌─────────────▼───────────────────┐
                    │       eventServer               │
                    │  (server_manager 实现)          │
                    │  - OnConnect: 验证逻辑           │
                    │  - OnMessage: 消息路由           │
                    │  - OnDisconnect: 清理           │
                    └─────────────┬───────────────────┘
                                  │ uses
                    ┌─────────────▼───────────────────┐
                    │         net.Server              │
                    │  (读写分离 + 心跳 + Session管理)  │
                    └─────────────────────────────────┘
```

```
                    ┌─────────────────────────────────┐
                    │         event.Client            │
                    │  (包装 net.Client + handler)     │
                    └─────────────┬───────────────────┘
                                  │ uses
                    ┌─────────────▼───────────────────┐
                    │      eventHandler               │
                    │  (conn.Handler 实现)            │
                    │  - HandleMsg: 处理收到的消息      │
                    └─────────────┬───────────────────┘
                                  │ uses
                    ┌─────────────▼───────────────────┐
                    │         net.Client              │
                    │  (自动重连 + 心跳)               │
                    └─────────────────────────────────┘
```

### Message Flow

```
Client1 (Emitter)           net/v2 Server            Client2 (Listener)
    |                           |                          |
    |--Emit(event, args)------->|                          |
    |   (gob encode)            |                          |
    |                           |--Req(event, args)------->|
    |                           |   (gob decode)           |--execute handler
    |                           |                          |
    |                           |<--Res(result)------------|
    |<--result------------------|                          |
```

### Event Name Patterns

- **Exact match**: `"db_op"` - matches only "db_op"
- **Regex match**: `"/db_[a-z]+/"` - matches db_op, db_insert, etc. (wrapped in `/`)

When using regex, the first parameter of the handler must be `[]string` (regex groups).

### Message Types (`msgtype`)

- `On`: Register interest in an event
- `ReqAll`: Emit to all listeners, wait for all responses, collect all errors
- `ReqOne`: Emit to a random listener (one of all matching)
- `ReqFirst`: Emit to all listeners, only accept first response
- `Res`: Response message
- `ResFirst`: First response message

### Emit 模式

`event.Client` 提供了三种 Emit 模式来处理多个监听者的情况：

| 模式 | 方法 | 说明 |
|-----|------|-----|
| 随机发送给一个 | `EmitOne()` / `EmitOneAsync()` | 随机选择一个监听者处理，适用于负载均衡场景 |
| 发送给所有，收集全部错误 | `EmitAll()` / `EmitAllAsync()` | 等待所有监听者响应，收集所有错误，适用于需要所有结果的场景 |
| 发送给所有，只接受第一个返回 | `EmitFirst()` / `EmitFirstAsync()` | 收到第一个响应就返回，其余响应被忽略，适用于竞争场景 |

示例：
```go
// 随机发送给一个监听者
err := client.EmitOne("event_name", arg1, arg2)

// 发送给所有，等待全部响应并收集错误
err := client.EmitAll("event_name", arg1, arg2)

// 发送给所有，只接受第一个返回值
err := client.EmitFirst("event_name", arg1, arg2)
```

### Options Pattern

Both `Client` and `Server` use a functional options pattern:

```go
// Client options
event.Dial(addr, options.Client().
    SetName("my-service").
    SetSecret("shared-secret").
    SetIsWrapError(true))

// Server options
event.NewServer(options.Server().
    SetSecret("shared-secret").
    SetTimeout(10 * time.Second).
    SetIsWrapError(true))
```

**注意**: 心跳和重连由 `net/v2` 自动处理，无需配置。

## Key Design Decisions

1. **底层网络层**: 使用 `github.com/ndsky1003/net/v2` 库，获得：
   - 自动重连（指数退避）
   - 内置心跳机制
   - 读写分离（readPump/writePump）
   - Session 管理（UUID）

2. **Regex-based subscriptions**: No need for predefined event groups; regex patterns provide flexible matching

3. **Gob encoding**: Messages are gob-encoded for transmission; must be gob-serializable types

4. **Time wheel for timeouts**: Server uses `github.com/antlabs/timer` for request timeout management

## Common Commands

```bash
# Run tests
go test ./...

# Run benchmarks
go test -bench=. ./...

# Build the module
go build ./...
```

## Dependencies

- `github.com/ndsky1003/net/v2` - 底层网络通信库
- `github.com/antlabs/timer` - 时间轮，用于超时管理
- `github.com/ndsky1003/buffer` - Buffer 池化
- `github.com/google/uuid` - UUID 类型（Session ID）
- `github.com/sirupsen/logrus` - 日志

## Lock Ordering

- Server uses `l` (Mutex) protecting monitor, sessions, and pending maps
- Client uses `rwl` (RWMutex) for topics and `l` (Mutex) for connection state
- Be careful when introducing new locks to avoid deadlocks
