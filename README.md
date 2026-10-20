# event

基于观察者模式的事件总线库：通过 TCP 在多个进程间做事件订阅/发布（消息框架与事件参数都走 msgpack 编码，默认 `coder.Msgp`），典型场景是"多服务器配置文件检查"。

## 安装

```bash
go get github.com/ndsky1003/event/v2
```

> 注意：模块路径带 `/v2`，import 时必须带上。

```go
import "github.com/ndsky1003/event/v2"
```

## 快速开始

### 1. 启动服务端

```go
event.NewServer().Listen(":8080")
```

### 2. 监听者

```go
client := event.Dial(":8080", event.ClientOptions().SetName("client-1"))

// 普通模式：回调入参与 Emit 传参一一对应，且返回一个 error
client.On("db_op", func(a int, b string) error {
    // do something
    return nil
})

// 正则模式：topic 前后带 /，回调第一个参数固定为 []string（正则子匹配结果）
client.On("/db_[a-z]+/", func(match []string, a int, b string, c *Person) error {
    // do something
    return nil
})

select {}
```

### 3. 发布者

```go
client := event.Dial(":8080")

if err := client.Emit("db_op", 1, "hello", &Person{Name: "飞毛腿1号", Age: 10}); err != nil {
    // handle error
}
```

## API

### 服务端

- `event.NewServer(opts ...*ServerOption)` — 创建服务端（`server` 类型未导出）。
- `Listen(url string)` — 监听单个地址，如 `":8080"`。**阻塞**，直到 `Shutdown`。
- `Listens(addrs []string)` — 监听多个地址（最后一个地址阻塞，其余各起一个 goroutine）。
- `Shutdown() error` — 关闭服务端：停止监听 → 断开所有客户端连接 → 停掉时间轮，并等已接进来的连接处理干净。**幂等**；会让阻塞在 `Listen`/`Listens` 里的调用返回。不调它的话 accept 循环与时间轮 goroutine 没有退出的一天。
- `IsShutdown() bool` — 是否已关闭。

服务端选项 `event.ServerOptions()`：

| 方法 | 说明 | 默认 |
| --- | --- | --- |
| `SetTimeout(time.Duration)` | 等待所有监听者响应的超时 | `10s` |
| `SetSecret(string)` | 连接握手密钥，客户端需一致 | 空 |
| `SetIsWrapError(bool)` | 是否包装错误来源 | `true` |

### 关闭服务端

`Listen` / `Listens` 是阻塞的，`Shutdown` 要从另一个 goroutine 调，用它来返回：

```go
srv := event.NewServer()
go srv.Listen(":8080")

// ... 收到退出信号时
if err := srv.Shutdown(); err != nil {
    // handle error
}
```

`Shutdown` 关掉监听、断开所有客户端连接、停掉时间轮，并等已接进来的连接处理干净（幂等）。客户端会感知到断连，本地未完成的 `Call` 被结算成错误；客户端自己也要调 `Close()` 才会停止重连。

### 客户端

- `event.Dial(url string, opts ...*ClientOption) *Client` — 创建客户端，内置自动重连与心跳。`Dial` 是异步的，返回时连接可能还没建立。
- `On(t eventname.T, Func any)` — 注册监听（无返回值）。连接已就绪时**同步**完成注册，所以 `On` 之后可以立刻 `Emit`。
- `Emit(en eventname.T, args ...any) error` — 发布，等所有监听者返回。
- `EmitAsync(en eventname.T, args ...any) *Call` — 异步发布。
- `EmitSomeOne(en eventname.T, args ...any) error` — 发布，只等最快的响应。
- `EmitSomeOneAsnyc(en eventname.T, args ...any) *Call` — 异步版。
- `Stop(err error)` — 断开当前连接，**会自动重连**；`err` 会被结算到所有未完成的 `Call` 上。
- `Close() error` — 彻底关闭：断开当前连接并停止重连，幂等。不用 `Close` 的话，客户端的重连 goroutine 与连接会一直存在。

客户端选项 `event.ClientOptions()`：

| 方法 | 说明 | 默认 |
| --- | --- | --- |
| `SetName(string)` | 客户端名称 | 空 |
| `SetSecret(string)` | 握手密钥 | 空 |
| `SetCheckInterval(time.Duration)` | 断线重连间隔 | `2s` |
| `SetHeartInterval(time.Duration)` | 心跳间隔 | `20s` |
| `SetIsWrapError(bool)` | 是否包装错误来源 | `true` |

### 异步调用

```go
call := client.EmitAsync("db_op", 1, "hello")
<-call.Done // 等待完成
if err := call.Err; err != nil {
    // handle error
}
```

## 吞吐从哪来

`Emit` 是**同步**的：发一条、等所有监听者回包、再发下一条。这种 stop-and-wait 模式下吞吐只由一件事决定：

```
QPS = 在途并发数 / 单条往返延迟
```

想提吞吐只有两条路：**加并发**，或**把往返摊薄（流水线）**。调传输层参数基本没用。

分进程实测（本机 loopback，服务端独立进程，M1 8 核，每格 3 次取中位数，抖动 ±1%）：

| 监听者 | 发布者 | `Emit` 同步 | `EmitAsync` 窗口 256 |
| --- | --- | --- | --- |
| 1 | 16 | 58,305 | 186,105 |
| 4 | 16 | 36,197 | 75,392 |
| 8 | 16 | 22,855 | 42,191 |
| 4 | 4 | 16,807 | — |
| 4 | 32 | 43,224 | — |
| 4 | 64 | 49,957 | — |

几个能直接拿去用的结论：

- **流水线比同步快 2~3 倍**。同样 16 条连接、4 个监听者：同步 36K，`EmitAsync` 攒 256 条在途 75K。原因不是"少等了"，而是连接上有了积压，`net.Buffers` 才真正攒到包：同步模式平均 **1.18 帧/writev**，流水线模式 **5.86 帧/writev**，writev 次数少 5 倍。
- **攒批在同步模式下无货可攒**。每条连接同时只有一条消息在途，`writePump` 非阻塞排空 `sendChan` 时通道是空的，`net.Buffers` 只能一帧一发。它不是没用，是需要积压才有用。
- **扇出是主要成本**。监听者 1→4→8，吞吐 58K→36K→23K。一次 `Emit` 有 4 个监听者就是 10 帧（1 进 + 4 散 + 4 回 + 1 回），每帧一次 writev 一次 read。
- **单条往返的地板约 31µs**（无监听者，纯 client→server→client）。每加一个监听者约 +8~11µs。
- **传输层已经贴着 syscall 地板**。CPU profile 里 86% 是 `writev` + `read`，编码/锁/分发加起来不到 3%。裸 loopback 每帧一次 writev 的地板是 422K 帧/s，同步模式实测跑到 310K 帧/s（73%）——所以再抠编码、抠拷贝都不会提吞吐，要提就得减少帧数或制造积压。

## 事件匹配

- 普通 topic：精确匹配，如 `"db_op"`。
- 正则 topic：用 `/.../` 包裹正则，如 `/db_[a-z]+/`。回调第一个参数固定为 `[]string`（即 `regexp.FindStringSubmatch` 的结果）。

事件名类型是 `eventname.T`（`type T string`），字符串字面量可直接传入，`string` 变量需显式转换。

## 监听回调约定

`On` 注册的回调若不满足以下条件会直接 `panic`（这是设计行为）：

- 必须是一个 func。
- 必须有且仅有一个 `error` 返回值。
- 普通 topic：入参类型与 `Emit` 传参一一对应。
- 正则 topic：第一个入参必须是 `[]string`，其后参数与 `Emit` 传参对应。

回调参数个数可与 `Emit` 传参个数不同：多了补零值，少了被忽略。类型不匹配会导致 msgpack 解码报错。

## 错误处理

- 错误默认被包装以标明来源，可用 `errors.Is` 配合哨兵错误判断，如 `ErrClient`、`ErrServer`、`ErrNoConnect`、`ErrClientEncode`、`ErrClientDecode`（定义在 `err.go`）。
- 通过 `SetIsWrapError(false)` 可关闭包装。

## 注意事项

- 事件参数默认经 msgpack 反射编码，具体类型（结构体/切片/基本类型等）开箱即用，无需注册。但**接口（`interface{}`）参数会丢失具体类型**（如整数 `42` 可能解成 `int8`），请使用具体类型参数。
- 可选 msgp 快路径：对参数类型跑 `msgp` 生成 `*_gen.go` 后，**值类型参数**（`func(o Outer) error`）自动走 msgp（无反射，更快）；**指针类型参数**（`func(o *Outer) error`）仍走 msgpack（为支持 `nil`）。两者 struct 默认都编码成 map 格式，互通。
- 整数解码**不校验溢出**：`Emit` 传的整数与回调入参位宽不一致会静默截断（如 `int64(300)` 解成 `int8` 得 `44`，不报错），请保证 `Emit` 传参与回调签名类型严格一致。
- `Emit` 传 `nil` 指针会正确还原为 `nil`。
- 单个事件的参数个数受 `Msg.BodyCount`（`int8`）限制，最多 127 个；超过会直接返回 `too many args` 错误，不会静默丢参数。
- **顺序**：同一条连接上的消息按到达顺序处理，所以 `On` 之后立刻 `Emit` 不会丢事件。但**跨连接之间没有顺序保证**——发布者与监听者是两条连接时，先发的 `On` 注册帧与后发的 `Emit` 之间仍有一个网络往返的窗口，稳妥做法是等注册完成（`On` 在连接已就绪时是同步的）或重试。
- **投递语义是 at-most-once**：`Emit` 返回 `nil` 只代表所有监听者都回了包。监听者收下请求后断线，发布者会拿到 `connection lost before response` 错误，但事件是否已被处理无法确定，库不会自动重投。
- 监听者断线时，它欠着的请求会被**立即**结算（错误里带 `connection lost before response`），不会让发布者干等 `SetTimeout`。
- 导入本库会触发 `logrus.SetReportCaller(true)`（`client.go` 的 `init()`），有全局副作用。
