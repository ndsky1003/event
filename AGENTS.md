# AGENTS.md

基于观察者模式的事件总线库：**p2p 模式**——通过 TCP/UDS 在多个进程间做事件订阅/发布。没有中心路由节点，靠一个轻量服务端发现彼此，节点之间订阅广播 + 事件点对点路由。消息框架（`Msg`/`MsgVerifyReq`/`MsgVerifyRes`）统一走 `coder.Msgp`；事件参数 body 走 `coder/` 子包（由 `Msg.Coder` 字段声明，默认 `coder.Msgp`）。

## 构建与验证

```bash
go build ./...
go vet ./...
```

- 模块路径是 `github.com/ndsky1003/event/v3`（带 `/v3`，import 时必须带上）。
- `go test ./...` 的 `client_test.go` 里 `TestMain` 会真实监听 `127.0.0.1:8080`（服务端）并让一个节点监听 `127.0.0.1:18081`，端口被占用会 panic，跑测试前先确认端口空闲。
- 真测试在 `integration_test.go`（端到端 p2p：收发/错误回传/多监听者聚合/EmitSomeOne/EmitAsync/EmitBatch/心跳/重连/secret/UDS/服务端重启/节点下线通知/本地 On 后 Emit 等）、`conn_test.go`（连接层：写后关闭/超长帧）、`coder/`（各 coder 往返 + benchmark）。
- 快速验证：`go build ./... && go vet ./... && go test -race ./...`。

## 架构

- **服务端** `server.go`：`NewServer()`，`Listen`/`Shutdown`。只做三件事：节点注册（`Register`/`RegisterRes`）、列表下发、下线通知（`NodeDown`）。**不参与事件路由**。`nodes map[name]*serverNode` 由 `l` 保护。
- **节点** `client.go`：`Dial(dirAddr, opts...)` 返回 `*Client`。节点既是发布者也是订阅者，内部三种连接：
  - `serverConn`：连服务端（`keepAliveServer` 重连），注册拿列表。
  - `peers`：与其他节点的对等连接。新节点主动连老节点（`keepAlivePeer` 重连），老节点 accept（`handlePeerIn`，不主动重连，对方会重连）。
  - `ln`：节点自己的监听器（`SetListenAddr` 指定），接受其他节点连接。
- **订阅广播**：`On` 本地记录 + `broadcastSubscribe` 给所有 peer；新 peer 连接建立时 `broadcastAllSubscribe` 补发自己的全部订阅。节点各自维护订阅表 `subs map[eventname.T]map[peerName]struct{}`。peer 断线 `disconnectPeer` 从 `subs` 里清掉它。
- **事件路由**：`Emit` 查 `subs[en]` 得到订阅者 peer 集合，本地 `execLocal` 执行 + 点对点发 `Req` 给各 peer，等全部回包后聚合。`nodeCall`（`client.go`）做跨 peer 聚合（`peerCount` 递减、`settled` 去重、`errs` 聚合）。`EmitSomeOne`（`ReqRandOne`）本地有订阅者则本地最快直接返回，否则等第一个 `ResRandOne`。
- **节点收 Req**：`func_call` 本地匹配执行回调并回 `Res`/`ResRandOne` 给来源 peer。回调 panic 会被 recover 兜底回错误包。
- **断线收尾**：`disconnectPeer` 清 peers/subs 后**扫全部 pending**，找出该 peer 欠的请求并立即结算（`settleLocked`），不让发布者干等。别改成 per-peer inflight 表——断线是罕见路径。
- 锁顺序固定为 `rwl -> l`：`rwl` 保护 `topics`，`l` 保护 `serverConn/peers/wantPeers/subs/pending/ln`。持 `l` 的路径绝不再拿 `rwl`。
- 已删除：`server.go`、`service.go`（原 C/S 中心路由）。`service.go` 的握手逻辑提炼成 `server.go` 里的 `serverSideVerify`/`clientSideVerify` 共享函数。

## 关键语义（容易踩坑）

- `On(t, Func)` 用 `panic` 校验 `Func`：必须是 func、必须有且仅有一个 `error` 返回值。**panic 是设计行为**。
- **p2p 不再支持正则 topic**：正则无法用订阅表精确路由，`topic.Topic` 只做精确匹配，`On` 里正则相关校验（`[]string` 第一个参数）已删。
- **订阅同步是异步的**：`On` 广播 `Subscribe` 有网络窗口。同一节点 `On` 后紧跟 `Emit` 不丢（本地 `execLocal` 直接执行）；跨节点 `On → Emit` 要等订阅同步（测试 `waitRoute` 等 `subCount` 到位）。这是与旧 C/S「跨连接无顺序保证」一致的语义。
- `Dial` 是异步的：`keepAliveServer` 后台拨号。`SetListenAddr` 默认 `127.0.0.1:0`（随机端口，`startListen` 同步监听后回填实际地址注册到服务端），可变参数支持多地址（`unix://` 或 `ip:端口`，UDS 保留 `unix://` 前缀、TCP 才回填）；`SetName` 全网唯一（服务端重名拒绝）。
- `Conn.Write`/`TryWrite` 返回值**不能返回 nil 表示失败**：上层靠 `err != nil` 判断。关闭原因统一走 `closedErr()`。
- 客户端 `seq` 单调递增、**不要归零**：归零后旧连接迟到的 `Res(seq=N)` 可能结算到新连接的 call 上。
- 回调入参类型必须与 `Emit` 传参一一对应（msgpack 解码按 `method.argsType` 已存类型解）。**`interface{}` 参数会丢失具体类型**，别用接口参数。
- msgpack 整数解码**不校验溢出**：位宽不一致会静默截断，务必类型严格一致。
- `Emit` 等所有监听者返回；`EmitSomeOne` 等最快；`EmitAsync` 返回 `*Call`。
- 投递失败（`writeToPeer` 报错）会立即结算该 peer（复用 `resolve`），发布者不会干等。监听者**收包之后**才死/卡死，只能等连接断开（`disconnectPeer` 结算）——没有超时兜底（节点 `Emit` 本身无 Timeout，靠 peer 回包或断线）。
- 单个事件参数个数受 `Msg.BodyCount`（`int8`）限制，最多 127 个。
- `msg.Msg` 的 `msgpack:"..."` tag 就是线上协议，两端必须同源。改字段要重新 `go generate ./...`（见下）。
- msgp 快路径：跑 `msgp` 生成 `*_gen.go` 后，值类型参数走 msgp，指针类型参数（解码目标是 `**T`）仍走 msgpack 兜底。msgp 的 `Decodable` 内部走 fwd.Reader 会预读丢字节，所以 `coder/msgpack_coder.go` 只用 `UnmarshalMsg`。
- **`Conn.r`（`bufio.Reader`）不能去**：去掉每帧多 2 次 syscall，吞吐 -10%~-16%。读侧 copy 也别抠，收益 <4%。
- **压测方法学**：单进程跑会把吞吐压低约 2.5 倍，必须分进程（`cmd/bench`）；插桩全局原子计数器、回调里的锁都会显著改变结果；本机背景负载波动大，要交替跑取中位数。

## msgp codegen

`*_gen.go` 是 msgp codegen 产物，库自己的类型已全部 codegen（`msg`/`msgtype`/`eventname`/`coder`）。改了类型字段后重新生成：`go generate ./...`，指令 `//go:generate msgp -tests=false`，本机 PATH 必须有 `msgp`（`go install github.com/tinylib/msgp@v1.6.5`）。

- 工具与库版本已对齐：`github.com/tinylib/msgp` v1.6.5，要求 `go >= 1.24`，本模块 `go` 指令是 1.27。
- `msgtype.T` 的枚举追加了新值（`Register`/`RegisterRes`/`NodeDown`/`Subscribe`/`Unsubscribe`），不影响已有取值，编解码是 uint8，无需重新生成 t_gen.go。
