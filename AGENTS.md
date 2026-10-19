# AGENTS.md

基于观察者模式的事件总线库：通过 TCP 在多个进程间做事件订阅/发布（README 场景是"多服务器配置文件检查"）。消息框架用 gob 编码、事件参数 body 用 msgpack 编码。

## 构建与验证

```bash
go build ./...
go vet ./...
```

- 模块路径是 `github.com/ndsky1003/event/v2`（带 `/v2`，import 时必须带上）。
- 当前工作分支是 `v2`；仓库另有 `main`、`v3` 分支，注意别改错分支。
- `go test ./...` 的 `client_test.go` 里 `TestMain` 会真实监听 `127.0.0.1:8080`，端口被占用会 panic。仓库里没有真正的单测，只有 `TestMain` 和一个 `BenchmarkEmit`，日常验证靠 `go build`/`go vet` 即可。

## 架构

- `server.go`：服务端，`NewServer()`（`server` 类型未导出），`Listen`/`Listens` 监听。
- `client.go`：客户端 `Dial(url, opts...)`，内含自动重连、心跳（`keepAlive`）、注册 topic（`regist_topic`）。
- `service.go`：服务端每个连接的封装，通过 `sendChan`（容量 30）异步写。
- `api.go`：对外 `On` / `Emit` / `EmitAsync` / `EmitSomeOne` / `EmitSomeOneAsnyc`。
- `codec/`：gob 编码消息框架 `Msg`（`bufio.Writer` 缓冲，`NewGobCodec` 只支持网络连接）。
- `client.go` 内层参数 body 走 `coder/` 子包（`coder.Coder.Encode/Decode`）：优先 msgp（`msgp.Marshaler/Unmarshaler`，业务方 codegen 后命中），否则 msgpack 反射兜底。
- `msg/`：线协议结构 `Msg`（含 `MsgVerifyReq/Res` 握手）；`msgtype/`：消息类型枚举；`topic/`：topic 匹配（支持正则）；`eventname/`：事件名类型。

## 关键语义（容易踩坑）

- `On(t, Func)` 会用 `panic` 校验 `Func`：必须是 func、必须有且仅有一个 `error` 返回值。**panic 是设计行为，不是 bug**。
- 正则 topic 写法是 `/db_[a-z]+/`（前后带 `/`，见 `eventname.IsReg`）。注册正则 topic 时，回调的**第一个参数必须是 `[]string`**（对应 `regexp.FindStringSubmatch` 的结果）。
- 回调的入参类型必须与 `Emit` 传入的参数一一对应（msgpack 解码按 `method.argsType` 已存类型解，类型不匹配会报错；参数不够会补零值）。具体类型无需注册；**`interface{}` 参数会丢失具体类型**（整数可能解成 `int8`/`int64`），别用接口参数。
- msgp 快路径（可选）：业务方对参数类型跑 `msgp` 生成 `*_gen.go` 后，**值类型参数**（`func(o Outer)`）走 msgp（`MarshalMsg`/`UnmarshalMsg`，基于 []byte）；**指针类型参数**（`func(o *Outer)`）因解码目标是 `**T`（为还原 nil），仍走 msgpack 兜底。两者 struct 默认都是 map 格式，互通。注意：msgp 的 `Decodable`（`DecodeMsg`）内部走 fwd.Reader 缓冲、连续解码会预读丢字节，所以 `coder/coder.go` 里只用 `UnmarshalMsg`。
- msgpack 整数解码**不校验溢出**：`Emit` 传的整数与回调入参位宽不一致会静默截断（如 `int64(300)` 解成 `int8` 得 `44`，不报错），务必保证类型严格一致。
- `Emit`（`msgtype.Req`）等所有监听者都返回才完成；`EmitSomeOne` 只等最快的响应。`EmitAsync` 返回 `*Call`（`call.Err` / `<-call.Done`）。
- 单个事件参数个数受 `Msg.BodyCount`（`int8`）限制，别传超过 127 个参数。
- `client.go` 的 `init()` 调用了 `logrus.SetReportCaller(true)`，是全局副作用，注意。
- 错误会默认被包装（`SetIsWrapError`，默认 `true`），用 `errors.Is` 判断时注意包装链上的 `ErrClient`/`ErrServer` 等哨兵错误（定义在 `err.go`）。
