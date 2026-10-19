# event

基于观察者模式的事件总线库：通过 TCP 在多个进程间做事件订阅/发布（消息框架用 gob 编码、事件参数用 msgpack 编码），典型场景是"多服务器配置文件检查"。

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
- `Listen(url string)` — 监听单个地址，如 `":8080"`。
- `Listens(addrs []string)` — 监听多个地址。

服务端选项 `event.ServerOptions()`：

| 方法 | 说明 | 默认 |
| --- | --- | --- |
| `SetTimeout(time.Duration)` | 等待所有监听者响应的超时 | `10s` |
| `SetSecret(string)` | 连接握手密钥，客户端需一致 | 空 |
| `SetIsWrapError(bool)` | 是否包装错误来源 | `true` |

### 客户端

- `event.Dial(url string, opts ...*ClientOption) *Client` — 创建客户端，内置自动重连与心跳。
- `On(t eventname.T, Func any)` — 注册监听（无返回值）。
- `Emit(en eventname.T, args ...any) error` — 发布，等所有监听者返回。
- `EmitAsync(en eventname.T, args ...any) *Call` — 异步发布。
- `EmitSomeOne(en eventname.T, args ...any) error` — 发布，只等最快的响应。
- `EmitSomeOneAsnyc(en eventname.T, args ...any) *Call` — 异步版。

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
- 单个事件的参数个数受 `Msg.BodyCount`（`int8`）限制，最多 127 个。
- 导入本库会触发 `logrus.SetReportCaller(true)`（`client.go` 的 `init()`），有全局副作用。
