# AGENTS.md

基于观察者模式的事件总线库：通过 TCP 在多个进程间做事件订阅/发布（README 场景是"多服务器配置文件检查"）。消息框架和事件参数 body 都走 `coder/` 子包，默认 msgpack（`coder.Msgp`）。原来的 `codec/` 包（gob）已删除。

## 构建与验证

```bash
go build ./...
go vet ./...
```

- 模块路径是 `github.com/ndsky1003/event/v2`（带 `/v2`，import 时必须带上）。
- 当前工作分支是 `v2`；仓库另有 `main`、`v3` 分支，注意别改错分支。
- `go test ./...` 的 `client_test.go` 里 `TestMain` 会真实监听 `127.0.0.1:8080`，端口被占用会 panic，跑测试前先确认端口空闲。
- 现在有真测试了：`integration_test.go`（端到端：收发/错误回传/正则 topic/心跳/重连/secret/多监听者聚合/断线结算等）、`envelope_test.go`（消息框架编解码 + benchmark）、`conn_test.go`（连接层：写后关闭/超长帧）、`coder/`（各 coder 往返 + benchmark）。`client_test.go` 里只剩 `TestMain` 和 `BenchmarkEmit`。
- 快速验证：`go build ./... && go vet ./... && go test -race ./...`。

## 架构

- `server.go`：服务端，`NewServer()`（`server` 类型未导出），`Listen`/`Listens` 监听（**阻塞**），`Shutdown()` 关闭（幂等，`closed` 用 CAS）。topic 索引拆成 `exact map[eventname.T]*topicSids`（普通 topic 查表命中）和 `regs`（正则只能线性扫），**不要在锁内遍历全部 topic**——旧写法实测 10000 个 topic 时 QPS 掉到 3060，查表写法 5467。`this.l` 保护 `exact`/`regs`/`services`/`pending`/`service.tps`/`listeners`。
- 服务端生命周期：listener 必须登记进 `server.listeners`，否则关不掉，`listen()` 里的 Accept 错误分支就成了永久忙循环。`acceptWg` / `connWg` 要**分开**：`Shutdown` 的顺序是"关 listener → 等 accept 循环退出 → 断连接 → 等 handle_conn 退出 → `tm.Stop()`"，合成一个 WaitGroup 就是自己等自己（连接没断，handle_conn 不会返回）。
- `client.go`：客户端 `Dial(url, opts...)`，内含自动重连（`keepAlive`）、注册 topic（`regist_topic`）。`Stop(err)` 只断当前连接、会自动重连；**`Close()` 才是终态**（置 `stopped`、让 `keepAlive` 退出、幂等），不用 `Close` 就会漏 goroutine 和连接。
- `conn.go`：传输层核心。`Conn` 内嵌 `net.Conn`，帧格式 `[flag 1B][body 长度 4B BigEndian][body]`，单帧 body 上限 `maxFrameSize` 64KB；`flag_msg`/`flag_ping`/`flag_pong`。只有 `writePump` 一个 goroutine 写 socket，别人通过 `sendChan`（容量 30）投递，`writePump` 攒批后用 `net.Buffers` 一次 writev 发出。心跳与断连检测在这一层（`heartInterval` ticker 看门狗 + `pingTimer` 空闲才 PING，判死时限 `2.2 * heartInterval`）。`Serve()` 起两个泵并阻塞到连接结束；`Close()` 幂等，`cancel` 叫醒 writePump、`Conn.Close` 叫醒 readPump。`writeSync`/`readSync` 只允许在 `Serve()` 之前用（握手）。
- `api.go`：对外 `On` / `Emit` / `EmitAsync` / `EmitSomeOne` / `EmitSomeOneAsnyc`。
- `service.go`：服务端每条连接的封装，只做消息框架的编解码与分发；原来的 `sendChan`（容量 30）和 `codec` 字段已删，发送队列在 `Conn` 里。另外记一笔 `tps`（这条连接注册过哪些 topic，断线时只摘这几条），由 `server.l` 保护。
- 断线收尾：`server_call.targets` 记下这次请求投递给了谁；连接死掉时 `closeLocked` **扫全部 pending** 找出它欠的响应并立即结算。别改成"每条连接维护一个 inflight 表"——那是 per-request 的 map 写，实测吃掉约 7% 吞吐，而断线是罕见路径。
- `envelope.go`：消息框架（`Msg`/`MsgVerifyReq`/`MsgVerifyRes`）的序列化。帧 body 布局是 `[1 字节 coder 标记][coder 编码出来的结构体]`，标记字节让服务端不用预先协商 coder，握手第一帧定下后整条连接回包沿用。`coder.Raw` 只能透传 `[]byte`，框架层一律退回 `Msgp`（见 `envelopeCoder`）。
- 事件参数 body 走 `coder/` 子包（`coder.Encode`/`coder.Unmarshal`）：优先 msgp（`msgp.Marshaler`/`Unmarshaler`，业务方 codegen 后命中），否则 msgpack 反射兜底。参数用哪种 coder 由 `Msg.Coder` 字段声明，与消息框架的 coder 互不影响。
- `msg/`：线协议结构 `Msg`（含 `MsgVerifyReq/Res` 握手），字段带 `msgpack` 短 tag；`msgtype/`：消息类型枚举；`topic/`：topic 匹配（支持正则）；`eventname/`：事件名类型。
- `*_gen.go` 是 msgp codegen 产物，**库自己的类型也已全部 codegen**（`msg`/`msgtype`/`eventname`/`coder` 四个包）。改了这些类型的字段后要重新生成：`go generate ./...`，指令是 `//go:generate msgp -tests=false`，所以**本机 PATH 里必须有 `msgp`**（`go install github.com/tinylib/msgp@v1.6.5`）。`-tests=false` 表示不生成 `*_gen_test.go`，`msg` 包因此没有自己的测试，它的往返正确性由 `envelope_test.go` 覆盖（走真实 coder 路径，比生成的测试更贴近实际）。实测收益：消息框架编码 999→71 ns/op、解码 444→95 ns/op、分配 20→2 次。
  - 工具与库版本已对齐：`go.mod` 里 `github.com/tinylib/msgp` 升到 **v1.6.5**，和本机 `msgp` 一致。代价是 msgp v1.6.5 要求 `go >= 1.24`，所以本模块的 `go` 指令从 1.20 抬到了 **1.24**——**下游使用者必须用 Go 1.24+**。想保住低版本的话：msgp v1.3.0 只要 go 1.20，v1.4.0/v1.5.0 要 go 1.22。

## 关键语义（容易踩坑）

- `On(t, Func)` 会用 `panic` 校验 `Func`：必须是 func、必须有且仅有一个 `error` 返回值。**panic 是设计行为，不是 bug**。
- **单条连接上的消息必须按序处理**：`On` 里注册 topic 是**同步**调 `regist_topic()`，服务端 `service.HandleMsg` 也是**同步**调 `server.handle()`。任何一处改回 `go ...`，`On` 之后紧跟的 `Emit` 就可能先被服务端处理，那次 Emit 变成"无人监听"，**不报错、不送达、事件静默消失**（`TestOnThenEmitIsNotLost` 会红）。服务端 `handle` 只做索引改动和转发、不跑用户代码，所以同步不耗时；业务回调的耗时在客户端 `func_call` 里异步消化。
- `Dial` 是异步的：连接还没起来时 `On` 只会把 topic 记在本地，要等 `connect()` 成功才上报。测试里要测顺序，得先 `isConnected()` 再 `On`。
- `Conn.write`/`tryWrite` 的返回值**不能返回 nil 表示失败**：上层靠 `err != nil` 判断，返回 nil 就是"消息没进发送队列却被当成发送成功"。关闭原因统一走 `closedErr()`（保证非 nil）。
- `server_call.settled` 是给 `serverReqCount--` 做幂等的，别删：同一个 sid 被结算两次会把 uint64 计数减成负数，pending 再也等不到 0。
- 客户端 `seq` 单调递增、**不要归零**：归零后旧连接迟到的 `Res(seq=N)` 可能结算到新连接的 call 上。
- 正则 topic 写法是 `/db_[a-z]+/`（前后带 `/`，见 `eventname.IsReg`）。注册正则 topic 时，回调的**第一个参数必须是 `[]string`**（对应 `regexp.FindStringSubmatch` 的结果）。
- 回调的入参类型必须与 `Emit` 传入的参数一一对应（msgpack 解码按 `method.argsType` 已存类型解，类型不匹配会报错；参数不够会补零值）。具体类型无需注册；**`interface{}` 参数会丢失具体类型**（整数可能解成 `int8`/`int64`），别用接口参数。
- msgp 快路径（可选，针对**业务方参数类型**）：跑 `msgp` 生成 `*_gen.go` 后，**值类型参数**（`func(o Outer)`）走 msgp（`MarshalMsg`/`UnmarshalMsg`，基于 []byte）；**指针类型参数**（`func(o *Outer)`）因解码目标是 `**T`（为还原 nil），仍走 msgpack 兜底。两者 struct 默认都是 map 格式，互通。注意：msgp 的 `Decodable`（`DecodeMsg`）内部走 fwd.Reader 缓冲、连续解码会预读丢字节，所以 `coder/msgpack_coder.go` 里只用 `UnmarshalMsg`。
- msgpack 整数解码**不校验溢出**：`Emit` 传的整数与回调入参位宽不一致会静默截断（如 `int64(300)` 解成 `int8` 得 `44`，不报错），务必保证类型严格一致。
- `Emit`（`msgtype.Req`）等所有监听者都返回才完成；`EmitSomeOne` 只等最快的响应。`EmitAsync` 返回 `*Call`（`call.Err` / `<-call.Done`）。
- 服务端 `req` 投递失败会**立即**释放 pending（`dispatch_failed` 复用 `res` 的计数与聚合），发布者不会干等 `Timeout`（默认 10s）。但监听者**收包之后**才死/卡死，仍然只能等 Timeout——服务端不替它回包，避免"事件可能已被业务处理"时的重复消费语义。
- 客户端 `Emit` 自己**没有超时**，全靠服务端回包、或连接断开时 `disconnect`/`stopLocked` 把 pending 全部 `call.done()` 掉。所以给服务端设 `SetTimeout` 时要清楚：它同时是"监听者卡死时发布者的最长阻塞时间"。
- 单个事件参数个数受 `Msg.BodyCount`（`int8`）限制，别传超过 127 个参数。
- `client.go` 的 `init()` 调用了 `logrus.SetReportCaller(true)`，是全局副作用，注意。
- 错误会默认被包装（`SetIsWrapError`，默认 `true`），用 `errors.Is` 判断时注意包装链上的 `ErrClient`/`ErrServer`/`ErrConnClosed` 等哨兵错误（定义在 `err.go`）。
- `msg.Msg` 上的 `msgpack:"..."` tag **就是线上协议**：msgpack map 格式每条消息都带字段名，短 tag 一条省 50 字节上下。字段名对不上时解码端**不报错、只静默丢字段**，所以两端必须同源。实测 tinylib/msgp codegen 只认 `msgpack:"..."`（忽略 `msgp:"..."` 和 `json:"..."`），所以给 `msg.Msg` 跑 codegen 不会改变线上格式。
- `Client` 的锁顺序固定为 `rwl -> l`：持 `l` 的路径绝不再去拿 `rwl`，否则与 `On`/`regist_topic_lock`/`Stop` 交叉死锁。
- **吞吐瓶颈是 syscall，不是序列化**：饱和点 profile 里 `writev`+`read` 占 86%，编码/锁/reflect 加起来不到 3%。同步 `Emit` 是 stop-and-wait，`QPS = 在途并发 / 往返延迟`，`net.Buffers` 平均只攒到 1.18 帧（无货可攒）。要提吞吐靠 `EmitAsync` 流水线（batch 升到 5.86、QPS ×2~3）或加并发，**别去抠编码和拷贝**。实测数字见 `TODO.md` 性能第 5 条。
- **`Conn.r`（`bufio.Reader`）不能去，读侧的 copy 也别抠**：去掉 bufio 每帧多 2 次 syscall，实测吞吐 **-10%~-16%**；反过来把 bufio 的 copy + `make([]byte, size)` 全干掉（复用缓冲，忽略正确性）端到端只有 **-0.5%~+1.8%**，在 4% 噪声底里。真要做零拷贝读得先解决 `clientConn.HandleMsg` 里 `go func_call` 留存 `body` 的问题，收益 <4%，不划算。
- **压测方法学**：单进程跑（服务端 + 所有客户端同进程）会把吞吐压低约 2.5 倍，必须分进程；插桩用的全局原子计数器、回调里的锁都会显著改变结果；本机背景负载约 1.2 核且波动，同一配置能测出 27K 和 77K。要交替跑、取中位数。
- `go.mod` 是 `go 1.24`（被 msgp v1.6.5 抬上去的），循环变量**每轮独立**，闭包捕获循环变量不再需要 `idx := i`（`integration_test.go` 里那处显式捕获现在只是冗余，留着无害）。注意这是从 1.20 升上来的语义变化，老代码里如果有依赖"共享循环变量"的写法，行为会变。
