# TODO

> 高并发压测（`go run -race` + pprof）暴露的问题与优化建议。行号以当前 `v2` 分支为准。

## 一、需要修复的 Bug

1~4 是 `go run -race` 实测抓到的数据竞争（集中在 `server.go` 的 `req`/`res` 与 `client.go`）；5 是端到端复现出来的逻辑缺陷，不是竞争。

- [x] **1. `s_call.serverReqCount` 锁外读** — `server.go:266`（已修：`res()` 在锁内用局部 `needWrite` 决定是否写回，锁外不再读共享字段）
  - `res()` 在 `this.l.Unlock()` 之后才判断 `s_call.serverReqCount == 0`，与另一 goroutine 在锁内 `serverReqCount--`（`server.go:243`）竞争。
  - 后果：多监听者并发响应时，可能多个 `res` 同时满足条件，重复 `write(origin_sid, msg)`，甚至把未聚合完整的错误先返回。
  - 修复方向：锁内用局部变量 `leftCount` 决定是否写回，锁外不再读共享字段。

- [x] **2. `s_call.tn` 锁外初始化** — `server.go:223`（已修：定时器改在 `this.l` 锁内创建）
  - `req()` 在 `this.l.Unlock()`（`server.go:218`）之后才 `s_call.tn = this.tm.AfterFunc(...)`，而 `res()` 在锁内读 `s_call.tn`（`server.go:259`）并 `Stop`（`server.go:260`）。
  - 后果：`res` 可能读到 `nil` 跳过 `Stop`，导致响应已返回仍被超时定时器误报 timeout。

- [x] **3. timer 内部状态竞争** — `server.go:260` vs `server.go:223`（随第 2 条一并解决）
  - 第 2 点的延伸：`tn.Stop()` 与 `AfterFunc()` 对同一 timer 节点并发写。
  - 修复方向：把 `s_call.tn` 的初始化移进 `this.l` 锁内（与 `res` 的访问互斥）。

- [x] **4. `client.go:154` `stop()` 遍历 `topics` 未加锁**（已修：锁顺序统一为 `rwl -> l`，`Stop`/`disconnect` 先拿 `rwl` 写锁再拿 `l`；`On`/`regist_topic_lock` 本来就是 `rwl -> l`，任何持 `l` 的路径都不再回头拿 `rwl`，交叉死锁的成因消除）

- [x] **5. 投递失败不释放 pending，发布者白等 Timeout** — `server.go` `req()`（已修：新增 `dispatch_failed()`，投递失败直接复用 `res()` 递减计数并聚合错误）（非数据竞争，逻辑缺陷）
  - `req()` 先按 `monitor` 快照给每个监听者 `serverReqCount++`，再逐个 `write`；而 `write` 把投递错误**只记日志、不返回**（`server.go:290`）。
  - 后果：快照里若有已死的连接，这一计数永远不会被 `res` 减掉，`pending` 只能等 `Timeout`（默认 10s）才释放。实测：监听者 `Stop` 后立刻 `Emit`，耗时 **10.009s** 才拿到 `ErrServer,timeout`，而事件其实根本没投递出去。
  - 修复：`write` 返回 error；`req` 投递失败时调 `dispatch_failed(sid, server_seq, err)`，走 `res` 的递减/聚合路径，减到 0 就把错误回给发布者。修复后同一场景 **0~1ms** 返回，错误也更准（`conn write err:read error: EOF` 而不是笼统的 `timeout`）。
  - 回归测试：`TestDispatchFailureReleasesPending`（往 `monitor` 注入一个 `services` 里不存在的幽灵 sid，让投递必然失败，测试因此是确定的、不依赖 EOF 与 Emit 的赛跑）。去掉修复后该测试稳定失败在 5.01s（= Timeout）。
  - **仍未处理**：监听者**收包之后**才死掉/卡死，服务端依旧只能等 Timeout。这是有意的取舍——服务端无法判断事件是否已被业务处理，替它回包可能造成重复消费语义。

- [x] **6. `On` 之后立刻 `Emit` 会丢事件**（已修：`On` 改同步注册 + `service.HandleMsg` 改同步分发）
  - `On` 里是 `go this.regist_topic()`，服务端 `HandleMsg` 里是 `go this.server.handle(...)`。注册帧（`On`）和业务帧（`Req`）在同一条 TCP 连接上本来是有序的，两端各 `go` 一次就把顺序彻底打乱了。
  - 后果：`Emit` 可能先于注册帧被服务端处理，此时 topic 还没进索引 → 走"无人监听"分支 → **不报错、不送达，事件静默消失**。实测还原修复后，20 轮 `On`+`Emit` 的第 2 轮就丢。
  - 修复：两端都改成同步。服务端 `handle` 只做索引改动和转发，不执行用户代码，同步不耗时；真正的耗时（业务回调）在客户端 `func_call` 里异步跑，不在这条路径上。
  - 回归测试：`TestOnThenEmitIsNotLost`（20 轮，`On` 之后不做任何等待直接 `Emit`）。

- [x] **7. 监听者半路断线，它欠的响应没人收尾**（已修：`service.inflight` 记账 + `closeLocked` 结算）
  - `req` 给每个监听者 `serverReqCount++` 之后，如果这条连接在回包前死掉，计数永远不会减，发布者只能等满 `Timeout`（默认 10s）。
  - 修复：`server_call.targets` 记下这次请求投递给了哪些监听者；`closeLocked` 摘连接时**扫全部 pending**，凡 targets 里有这个 sid 的就走 `settle`，错误文案 `service id:%d connection lost before response`。
  - 为什么不给每条连接维护一张 `inflight` 表（首版实现就是这么写的）：那是 per-request 的 map 写，每个监听者两次，实测吃掉约 7% 吞吐（31.8K→29.4K QPS）。断线是罕见路径，扫 pending 只在断线时付 `O(pending × targets)`——实测 500 条在途请求 11ms 全部结算。改回去之前先看 `TestListenerDeathReleasesManyPending`。
  - 回归测试：`TestListenerDeathReleasesPending`（监听者进回调后不返回，期间断线；有修复是毫秒级，没修复要等满 Timeout）。

- [x] **8. `serverReqCount--` 没有幂等保护**（已修：`server_call.settled` 去重）
  - 同一个 sid 被结算两次（先收到 `Res`、之后连接断了又触发第 7 条的收尾）就会把 uint64 计数减成负数（下溢成天文数字），`leftCount == 0` 永远不成立，pending 只能靠 Timeout 收尾；更糟的是第二次会提前判定"完成"，把没聚合完的结果发出去。
  - 回归测试：`TestSettleIsIdempotent`（两个监听者、同一个结算三次，断言计数还剩 1 且 pending 未被提前删）。

- [x] **9. `Conn.write` 在连接已关闭时可能返回 nil**（已修：`closedErr()` 兜底 + 入队后复查）
  - `write` 直接 `return this.Err()`，而 `Err()` 在某些路径下是 nil；上层 `writeLocked` 判 `err != nil` 就放行，于是"消息没进发送队列"被当成"发送成功"——静默丢数据。
  - 另一个窗口：`closed.Load()` 与 `CloseWithError` 的 CAS 之间没有原子性，`select` 两个 case 同时就绪时随机选，帧可能进到一个已经没有消费者的队列。入队后补一次 `closed` 检查，把静默丢变成如实报错。
  - 回归测试：`conn_test.go` 的 `TestConnWriteAfterCloseIsNotSilentlyOK`。

- [x] **10. 参数超过 127 个时 `BodyCount`（int8）溢出**（已修：`emit_async` 显式报错）
  - `int8(len(args))` 超过 127 会溢出成负数，接收端 `parse()` 的 `i < req_args_count` 恒假，所有参数被补成零值，**不报错**。
  - 现在返回 `too many args:N,limit 127`。

- [x] **11. `bodyPool` 被 nil 污染**（已修）
  - `coder.Encode` 失败时返回 `nil` 切片，原代码无条件 `bodyPool.Put(body[:0])` 会把 nil 存进池子，后续 `Get` 拿到 nil、容量预置全部失效。

- [x] **12. `stopLocked` 把客户端 `seq` 归零**（已修：不再重置）
  - 归零后新 call 复用旧序号，而服务端回包带的是客户端序号：旧连接上迟到的 `Res(seq=N)` 可能被结算到新连接的 call 上。
  - 正常情况下旧连接的 readPump 已退出不会投递消息，但这是靠时序赌的。uint64 单调递增没有耗尽风险，没必要复用。

- [x] **13. `Client` 无法彻底终止**（已修：新增 `Close()`）
  - `keepAlive` 是 `for { connect(); sleep }` 无限循环，`Stop` 只断当前连接。`Dial` 出来的 goroutine 和连接永远停不掉，测试里尤其明显（一个测试攒几十个还在重连的客户端）。
  - 新增 `Client.Close()`：置 `stopped`、关当前连接、让 `keepAlive` 退出，幂等。`connect()` 在"拨号完成"和"发布 cc"之间也要检查 `stopped`，否则 Close 正好落在这个窗口时会漏一条连接和一个卡在读上的 goroutine。
  - 回归测试：`TestClientCloseStopsReconnect`。

- [x] **14. 服务端没有关闭语义，`Accept` 错误分支是永久忙循环**（已修：新增 `Shutdown()`）
  - `listen()` 里 `Accept` 出错就 `logrus.Error` + 裸 `continue`。listener 从没被存下来，所以没有任何地方能关它：这个循环永远退不出去，`Listen`/`Listens` 永久阻塞，accept 循环和 `timer.Run` 的 goroutine 一直存在。fd 耗尽这类持续错误还会瞬间刷出几十万行日志。
  - 修复：listener 登记进 `server.listeners`；新增 `Shutdown()`（`closed` CAS 幂等）—— 顺序是"关 listener → `acceptWg.Wait()` → 逐个 `Close(sid, true)` 断连接 → `connWg.Wait()` → `tm.Stop()`"。`acceptWg` 和 `connWg` 必须分开，合成一个就是自己等自己（连接没断，`handle_conn` 不会返回）。
  - 配套：`listen()` 的 Accept 错误若非 Shutdown 引起，退避 100ms 再试；`handle_conn` 在登记 service 前检查 `closed`；`write` 在 `closed` 后直接返回错误（顺带挡住断连接时结算回包的日志噪音）；`Listen` 在已关闭时记日志返回，不再 panic。
  - 回归测试：`TestServerShutdown`（`Listen` 返回、services/pending/listeners 清零、端口可重新绑定、幂等、关闭后 `Emit` 报错、已关闭时 `Listen` 不 panic）。另外 `startServer` 现在给每个用例挂 `t.Cleanup(Shutdown)`。
  - 实测：独立程序里 `Shutdown` 耗时 1ms，goroutine 从 16 降到 2，`Listens` 返回，`main` 正常退出。

## 二、性能优化建议

pprof 结论：瓶颈是系统调用（`syscall.rawsyscalln` 占 75% CPU），非 CPU 计算。

- [x] **1. 去掉每条消息的 `Flush`** — `codec/gobcodec.go:39`（已修：传输层换成 `conn.go` 的帧 + `net.Buffers` 攒批 writev，`codec.Codec` 这条路径已不在链路上）
  - `gobCodec.Write` 每次 `enc.Encode` 后立即 `encbuf.Flush()`，一次 `Emit` 链路触发约 10 次独立 `syscall.write`（广播 N 个监听者时线性增长）。
  - 建议：攒批/合并写，或由调用方控制 Flush 时机。

- [x] **2. 复用 gob encoder/decoder，避免每次重写类型定义**（已修：`codec.GobStream` 复用 Encoder/Decoder 状态，实测首条 93 字节、后续 30 字节，见 `codec/gobstream_test.go`）
  - `client.go:347` `emit_async` 每次 `gob.NewEncoder`；`client.go:208` `parse` 每次 `gob.NewDecoder`，导致每次都要序列化/反序列化 gob 类型定义，浪费字节与 CPU。
  - 建议：复用 encoder/decoder，或换 protobuf/msgpack 等更高效的编码。

- [x] **3. 降低服务端全局锁粒度** — `server.go:205`（`this.l`）**已做（去掉锁内遍历 + 缩小临界区）**
  - 原 `req()` 在锁内遍历**所有** topic 做 `Match`，再遍历每个 topic 的 sid 集合（代码自注"性能损耗极其严重"）。
  - 改法：索引拆成 `exact map[eventname.T]*topicSids`（普通 topic 事件名字符串相等，直接查表命中）和 `regs map[eventname.T]*topicSids`（正则只能线性扫，数量天然少）。`req` 锁内工作量从"全部 topic × sid 集合"降到"一次查表 + 正则条数"。
  - 顺带：`close(sid)` 原本也遍历全部 topic 删 sid，现在靠 `service.tps` 只摘自己注册过的几条。
  - 实测（单 publisher，Emit 到其中一个 topic）：

    | 已注册 topic 数 | 旧（锁内全遍历） | 新（查表+只扫正则） |
    | --- | --- | --- |
    | 200 | 15,361 QPS / 65µs | 15,801 QPS / 63µs |
    | 2,000 | 8,175 QPS / 122µs | 11,372 QPS / 88µs |
    | 10,000 | 3,060 QPS / 327µs | 5,467 QPS / 183µs |

    topic 越多差距越大：10,000 个 topic 时 QPS +79%、延迟 -44%。
  - **仍未做**：`this.l` 还是一把全局互斥锁（没有分片）。`req`/`settle`/`write` 都要拿它，锁本身的争用没变，只是临界区里的活儿变少了。要再进一步就得拆锁或改成 per-topic 分片。

- [x] **4. 扩展性验证**（已复测，本机 loopback，修完 1~14 之后）

  | 场景 | 结果 |
  | --- | --- |
  | `-verify`：4 监听者 × 5000 广播 | 全部收到、`emit失败=0`；错误聚合 5000/5000 |
  | `-listeners 4 -pubbers 8 -reqs 20000` | 160,000 次 Emit，**0 失败**，QPS 29.6K~31.9K，平均 245~266µs |
  | `-race` 版 `-listeners 8 -pubbers 16 -reqs 5000` | 80,000 次 Emit，**0 失败、0 数据竞争** |
  | 10,000 个 topic 下单 publisher | 3,060 → 5,467 QPS（第 3 条的收益） |

  QPS 有约 8% 的 run-to-run 抖动（29.6K~31.9K），多次运行取中位数才准。
  **注意**：上面这些是 `eventbench` 的数字，服务端和所有客户端**在同一进程里**，互相抢 8 个核，吞吐被压低约 2.5 倍。分进程拓扑的真实数字见第 5 条。
  **仍未做**：真实跨机器 RTT 压测（本机 loopback 掩盖了网络往返成本）。

- [x] **5. "QPS 上不去"的根因分析**（已定位，结论：不是传输层的问题）

  背景：换了自定义帧 + `net.Buffers` 攒批 writev 之后，`eventbench` 的 QPS 仍是 31K 上下，和改造前同档。

  **结论：同步 `Emit` 是 stop-and-wait，吞吐 = 在途并发 / 往返延迟（Little 定律），`net.Buffers` 在这种模式下根本没东西可攒。**

  实测链条：

  1. **攒批确实没生效**。给 `writeBatch` 插桩统计：同步模式平均 **1.18 帧/writev**（多帧批只占 12.6%），流水线模式 **5.86 帧/writev**。原因是 `writePump` 拿到一帧后非阻塞排空 `sendChan`，而同步模式下每条连接同时只有 1 条在途，通道是空的。
  2. **吞吐随并发线性上升**（单进程，4 监听者）：发布者 1/2/4/8/16/32/64 → QPS 9.3K/13.7K/20.8K/31.2K/42.2K/52.4K/59.9K。31K 只是"8 个发布者 ÷ 250µs"的结果，不是库的天花板。
  3. **延迟由 4 跳内核往返决定**：监听者 0 个时单条往返 31µs（纯 client→server→client），1/2/4/8/16 个监听者 → 61/78/102/136/202µs，边际约 +8~11µs/监听者。
  4. **CPU 全在 syscall**：饱和点 profile 里 `syscall.rawsyscalln` 占 85.9%，其中 `poll.FD.Writev` 55%、`poll.FD.Read` 31%；编码、锁、`reflect.Value.call`、分发加起来不到 3%。所以再抠序列化和拷贝都不会提吞吐。
  5. **已经贴着 syscall 地板**：裸 loopback（4 连接、100B 帧）每帧一次 writev 的地板是 **422K 帧/s**；攒到 64 帧可到 1.45M 帧/s。同步模式实测 310K 帧/s，即地板的 73%。
  6. **制造积压就有收益**：`EmitAsync` + 窗口 256，batch 升到 5.86、writev 少 5 倍，QPS 提升 2~3 倍。

  分进程实测（服务端独立进程，4 监听者，各 3 次取中位数，抖动 ±1%）：

  | 监听者 | 发布者 | `Emit` 同步 | `EmitAsync` 窗口 256 |
  | --- | --- | --- | --- |
  | 1 | 16 | 58,305 | 186,105 |
  | 4 | 16 | 36,197 | 75,392 |
  | 8 | 16 | 22,855 | 42,191 |
  | 4 | 4 / 16 / 32 / 64 | 16,807 / 35,326 / 43,224 / 49,957 | — |

  顺带修掉一处白扔的开销（**没有吞吐收益，但少干活**）：`writeBatch` 原来每批无条件 `SetWriteDeadline`，实测 8 发布者 × 4 监听者场景下 **135 万次**调用。改成和读侧一样"预算快用尽才续期"（`nextWriteDeadline`，余量 `writeTimeout/4`），调用次数降到 **24 次**。QPS 不变——说明 `SetWriteDeadline` 本身不贵，之前一次 A/B 测出 +14.6% 是假信号（那次跑的帧数少 26 万、还有 12 个失败，是连接提前死掉少干活）。

  踩到的测量坑，下次别再犯：
  - **单进程压测自干扰**：服务端 + 68 条客户端连接挤在 8 核里，吞吐被压低约 2.5 倍。要量真实吞吐必须分进程。
  - **插桩本身会改变结果**：`writeBatch` 里加 4 个全局原子计数器，400K ops/s 下直接把 QPS 拖慢；监听者回调里放一把全局 mutex，吞吐掉 3 倍。
  - **本机背景负载约 1.2 核且波动**（Ghostty/SkyLight/opencode/Chrome），同一配置能测出 27K 和 77K 两个数。必须交替跑、取中位数。

  **还能做但没做**：
  - 减少每次 `Emit` 的帧数（现在 4 监听者 = 10 帧）。这是唯一能突破 syscall 地板的方向，但要改语义。
  - `this.l` 仍是一把全局锁；饱和点锁只占 2.6%，优先级低。
  - 跨机器 RTT 压测。

- [x] **6. `Conn.r`（`bufio.Reader`）是否多余**（已实测：不多余，且读侧的 copy 不值得抠）

  疑问：`Conn` 内嵌 `net.Conn` 又套一层 `bufio.Reader`，`io.ReadFull(this.r, body)` 等于多一次 `[]byte` 拷贝。

  实测（分进程、4 监听者 × 16 发布者、各 3 次取中位数）：

  | 变体 | 同步 QPS | 流水线 QPS |
  | --- | --- | --- |
  | 带 bufio（现状） | 35,630 | 72,090 |
  | 去掉 bufio，直接 `io.ReadFull(this.Conn, ...)` | 31,987（**-10.2%**） | 60,757（**-15.7%**） |
  | 保留 bufio，但 `make([]byte, size)` 换成每连接复用缓冲（忽略正确性，测上限） | 35,002（-0.5%） | 76,455（+1.8%） |

  读路径微基准（5000 帧 × 105B，`net.Pipe`）：bufio+make 77ns/帧、bufio+pool 74.8ns/帧、**无 bufio 1,256ns/帧**、自己缓冲就地解析零拷贝 47ns/帧。

  结论：
  - bufio 的价值是把"每帧 2 次 syscall"摊成约 0.2 次，这个价值（微基准里 16 倍）远大于它引入的一次 copy。
  - 那次 copy + 一次分配合计约 **30ns/帧**。400K 帧/s × 30ns ≈ 12ms/s ≈ 1% 个核，而进程有 3.5 个核在 syscall 里，所以端到端测不出差别（-0.5%~+1.8%，噪声底 4%）。
  - 真要零拷贝读，正确性障碍在 `clientConn.HandleMsg` 的 `go this.client.func_call(&got)`：`got.Bytes` 别名 `body`，read() 返回后另一个 goroutine 还在读。服务端侧安全（`HandleMsg` 同步、`service.Write` 走 `encodeEnvelope` 重新编码）。收益 <4%，不划算。
  - 顺带否掉同类想法：`Msg.Bytes` 改 `msgpack:"-"`、args 裸接在 envelope 后面省 6 字节 + 一次拷贝——同一量级的收益（几字节 + 一次百字节 memcpy），同样在噪声底里。

## 验证方式

```bash
# 压测程序（临时目录）
go run . -listeners 4 -pubbers 8 -reqs 20000      # 吞吐/QPS
go run . -verify                                    # 广播正确性 + 错误聚合
go run -race . -listeners 4 -pubbers 8 -reqs 5000  # 数据竞争（修完应 0 race）
go run . -cpuprofile cpu.prof && go tool pprof -top eventbench cpu.prof
```
