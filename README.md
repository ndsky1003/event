# event

基于观察者模式的事件总线库：通过 TCP 在多个进程间做事件订阅/发布，**p2p 模式**——没有中心路由节点，每个进程都是对等节点，靠一个轻量服务端发现彼此，事件按订阅表点对点路由。

- 消息框架（`Msg`）统一走 `coder.Msgp`（msgpack）。
- 事件参数由 `Msg.Coder` 声明，默认 `coder.Msgp`。
- 只支持精确 topic（正则无法用订阅表精确路由，已移除）。

## 安装

```bash
go get github.com/ndsky1003/event/v3
```

> 注意：模块路径带 `/v3`，import 时必须带上。

```go
import "github.com/ndsky1003/event/v3"
```

## 架构

```
            ┌─────────────┐
            │  服务端    │  只做注册 / 列表下发 / 下线通知
            └──────┬──────┘
       注册/拿列表  │   注册/拿列表
     ┌────────────┴────────────┐
     │                         │
  ┌──┴──┐   订阅广播 + 事件    ┌──┴──┐
  │节点A│◄───────────────────►│节点B│   对等连接（TCP/UDS）
  └─────┘                     └─────┘
```

- **服务端**（`NewServer`）：已知地址的轻量服务。节点注册上来，服务端维护节点列表并下发给新节点；节点下线时广播通知其他节点。**服务端不参与事件路由**。
- **节点**（`Dial`）：连服务端拿列表，主动连接老节点、接受新节点连接，形成对等连接。`On` 时把「我订阅了 X」广播给所有 peer，peer 各自维护订阅表；`Emit` 查订阅表点对点路由给订阅者。
- **连接方向**：新节点主动连老节点（服务端按注册顺序下发列表），每对节点恰好一条 TCP/UDS 连接，双向通信。

## 快速开始

### 1. 启动服务端

```go
dir := event.NewServer()
go dir.Listen(":8080")
```

### 2. 订阅者

```go
client := event.Dial(":8080", event.ClientOptions().SetName("client-1").SetListenAddr(":9001"))

client.On("db_op", func(a int, b string) error {
    // do something
    return nil
})

select {}
```

### 3. 发布者

```go
client := event.Dial(":8080", event.ClientOptions().SetName("client-2").SetListenAddr(":9002"))

if err := client.Emit("db_op", 1, "hello", &Person{Name: "飞毛腿1号", Age: 10}); err != nil {
    // handle error
}
```

> `Dial` 的第一个参数是**服务端地址**；`SetListenAddr` 是**节点自己**的监听地址（供其他节点连接），默认 `127.0.0.1:0`（随机端口），必须全网唯一（`SetName` 同理）。可以同时监听多个地址——UDS 给同机、TCP 给跨机：

```go
client := event.Dial(":8080", event.ClientOptions().
    SetName("client-1").
    SetListenAddr(":9001", "unix:///tmp/client-1.sock"))
```

## API

### 服务端

- `event.NewServer(opts ...*ServerOption) *Server` — 创建服务端。
- `Listen(url string)` — 监听并阻塞，直到 `Shutdown`。
- `Shutdown() error` — 关闭服务端：停止监听、断开所有节点连接。幂等。
- `IsShutdown() bool` — 是否已关闭。

服务端选项 `event.ServerOptions()`：`SetSecret`（握手密钥）、`SetHeartInterval`（心跳间隔）、`SetIsWrapError`。

### 节点

- `event.Dial(dirAddr string, opts ...*ClientOption) *Client` — 创建节点，连服务端、拿列表、连 peer。异步，返回时可能还没连上。
- `On(t eventname.T, Func any) error` — 注册监听（本地记录 + 广播订阅给所有 peer）。
- `Emit(en eventname.T, args ...any) error` — 发布，等所有监听者（本地 + 远程）返回。
- `EmitAsync(en eventname.T, args ...any) *Call` — 异步发布。
- `EmitSomeOne(en eventname.T, args ...any) error` — 发布，只等最快的响应。
- `EmitSomeOneAsnyc(en eventname.T, args ...any) *Call` — 异步版。
- `EmitBatch(en eventname.T, argsList ...[]any) []error` — 批量发布。
- `Builder() *builder` — 链式构建一次发送。
- `Close() error` — 彻底关闭：断开服务端连接、所有 peer 连接并停止重连，幂等。

节点选项 `event.ClientOptions()`：

| 方法 | 说明 | 默认 |
| --- | --- | --- |
| `SetName(string)` | 节点名（全网唯一，服务端用它区分节点） | 空 |
| `SetListenAddr(...string)` | 节点监听地址（可变，可多个），默认 `127.0.0.1:0`；支持 `unix://` 和多个 `ip:端口` | `127.0.0.1:0` |
| `SetSecret(string)` | 握手密钥（与服务端、其他节点一致） | 空 |
| `SetCheckInterval(time.Duration)` | 重连间隔（指数退避起点） | `2s` |
| `SetHeartInterval(time.Duration)` | 心跳间隔 | `10s` |
| `SetIsWrapError(bool)` | 是否包装错误来源 | `true` |
| `SetCoderType(coder.T)` | 事件参数编码方式 | `Msgp` |

### 异步调用

```go
call := client.EmitAsync("db_op", 1, "hello")
<-call.Done
if err := call.Err; err != nil {
    // handle error
}
```

## 事件匹配

只支持精确 topic，如 `"db_op"`。事件名类型是 `eventname.T`（`type T string`），字符串字面量可直接传入。

## 监听回调约定

`On` 注册的回调若不满足以下条件会直接 `panic`（设计行为）：

- 必须是一个 func。
- 必须有且仅有一个 `error` 返回值。
- 入参类型与 `Emit` 传参一一对应（类型不匹配会导致解码报错）。

回调参数个数可与 `Emit` 传参个数不同：多了补零值，少了被忽略。

## 错误处理

- 错误默认被包装以标明来源，可用 `errors.Is` 配合哨兵错误判断：`ErrClient`、`ErrServer`、`ErrClientEncode`、`ErrClientDecode`、`ErrConnClosed`。
- 通过 `SetIsWrapError(false)` 可关闭包装。

## 注意事项

- **订阅同步是异步的**：`On` 广播订阅给 peer 有网络窗口。同一节点 `On` 之后立刻 `Emit` 不丢（本地订阅者直接执行）；**跨节点**的 `On → Emit` 需要等订阅同步（测试用 `waitRoute` 等）。这与此前 C/S 模式的「跨连接无顺序保证」语义一致。
- **投递语义是 at-most-once**：`Emit` 返回 `nil` 只代表所有监听者都回了包。监听者收下请求后断线，发布者会拿到 `connection lost before response` 错误，事件是否已被处理无法确定，库不会自动重投。
- **断线收尾**：peer 断线时，它欠着的请求会被立即结算（错误带 `connection lost`），不会让发布者干等。
- **重连**：节点连服务端、连 peer 都内置自动重连（指数退避）；服务端重启后节点会重新注册、重新拿列表并恢复路由。
- **节点下线**：节点 `Close` 后，服务端广播下线通知，其他节点清掉与它的连接和订阅。
- 事件参数默认经 msgpack 反射编码；可选 msgp 快路径（对参数类型跑 `msgp` 生成 `*_gen.go`，值类型参数走 `MarshalMsg`/`UnmarshalMsg`）。
- 单个事件的参数个数受 `Msg.BodyCount`（`int8`）限制，最多 127 个。

## 吞吐

p2p 下单次 `Emit` 是 **2 跳往返**（发布者 → 订阅者 → 发布者），比 C/S 模式的 4 跳（客户端 → 服务器 → 订阅者 → 服务器 → 客户端）少一半中转。

分进程实测（服务端 / 监听者 / 发布者三个独立进程，`cmd/bench`，本机 loopback）：

| 监听者 | 发布者 | `Emit` 同步 QPS | 单次往返 |
| --- | --- | --- | --- |
| 1 | 1 | ~46K | ~19µs |
| 4 | 1 | ~23K | ~42µs |
| 8 | 1 | ~12K | ~78µs |
| 4 | 4 | ~37K | ~105µs |
| 4 | 16 | ~79K | ~199µs |
| 4 | 16（`async-16`） | ~199K | — |

压测方法（分进程，避免单进程自干扰）：

```bash
go build -o bench ./cmd/bench
./bench -mode server -addr :18080                          # 终端 1：服务端
./bench -mode listener  -addr :18080 -topic bench -count 4    # 终端 2：监听者
./bench -mode publisher -addr :18080 -topic bench -count 16 -subs 4 -reqs 20000 [-async 16]  # 终端 3：发布者
```

几个结论：

- **单次往返 ~19µs（1 监听者），每加一个监听者 +~10µs**：p2p 点对点扇出，每个订阅者一次写、一次回。
- **同步 `Emit` 是 stop-and-wait**：QPS = 在途并发数 / 往返延迟，多发布者并发能显著提总 QPS（4 发布者 → 16 发布者，37K → 79K）。
- **`EmitAsync` 攒批提速约 2.5 倍**：同样 16 发布者 × 4 监听者，同步 79K、`async-16` 199K——连接上有了积压，`net.Buffers` 才真正攒到包。
- 机器背景负载波动大，数字仅供参考，建议交替跑取中位数。
