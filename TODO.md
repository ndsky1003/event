# TODO

> 高并发压测（`go run -race` + pprof）暴露的问题与优化建议。行号以当前 `v2` 分支为准。

## 一、需要修复的 Bug（数据竞争）

`go run -race` 实测抓到 3 处，全部在 `server.go` 的 `req`/`res`。

- [ ] **1. `s_call.serverReqCount` 锁外读** — `server.go:266`
  - `res()` 在 `this.l.Unlock()` 之后才判断 `s_call.serverReqCount == 0`，与另一 goroutine 在锁内 `serverReqCount--`（`server.go:243`）竞争。
  - 后果：多监听者并发响应时，可能多个 `res` 同时满足条件，重复 `write(origin_sid, msg)`，甚至把未聚合完整的错误先返回。
  - 修复方向：锁内用局部变量 `leftCount` 决定是否写回，锁外不再读共享字段。

- [ ] **2. `s_call.tn` 锁外初始化** — `server.go:223`
  - `req()` 在 `this.l.Unlock()`（`server.go:218`）之后才 `s_call.tn = this.tm.AfterFunc(...)`，而 `res()` 在锁内读 `s_call.tn`（`server.go:259`）并 `Stop`（`server.go:260`）。
  - 后果：`res` 可能读到 `nil` 跳过 `Stop`，导致响应已返回仍被超时定时器误报 timeout。

- [ ] **3. timer 内部状态竞争** — `server.go:260` vs `server.go:223`
  - 第 2 点的延伸：`tn.Stop()` 与 `AfterFunc()` 对同一 timer 节点并发写。
  - 修复方向：把 `s_call.tn` 的初始化移进 `this.l` 锁内（与 `res` 的访问互斥）。

- [ ] **4. `client.go:154` `stop()` 遍历 `topics` 未加锁**（代码自带 FIXME 注释，承认"放开也死锁"）
  - `stop()` 无锁遍历 `this.topics` 并修改 `tp.IsRegistSuccess`，与 `On`/`regist_topic` 的 `rwl` 加锁并发。属已知隐患，需重构锁顺序。

## 二、性能优化建议

pprof 结论：瓶颈是系统调用（`syscall.rawsyscalln` 占 75% CPU），非 CPU 计算。

- [ ] **1. 去掉每条消息的 `Flush`** — `codec/gobcodec.go:39`
  - `gobCodec.Write` 每次 `enc.Encode` 后立即 `encbuf.Flush()`，一次 `Emit` 链路触发约 10 次独立 `syscall.write`（广播 N 个监听者时线性增长）。
  - 建议：攒批/合并写，或由调用方控制 Flush 时机。

- [ ] **2. 复用 gob encoder/decoder，避免每次重写类型定义**
  - `client.go:347` `emit_async` 每次 `gob.NewEncoder`；`client.go:208` `parse` 每次 `gob.NewDecoder`，导致每次都要序列化/反序列化 gob 类型定义，浪费字节与 CPU。
  - 建议：复用 encoder/decoder，或换 protobuf/msgpack 等更高效的编码。

- [ ] **3. 降低服务端全局锁粒度** — `server.go:205`（`this.l`）
  - `req()`/`res()` 都持有同一把全局锁，`req` 在锁内遍历所有 `monitor×sid`（代码自注"性能损耗及其严重"）。
  - 建议：锁分片、缩小临界区，或去掉锁内遍历。

- [ ] **4. 扩展性验证**（数据参考，本机 loopback）
  - 监听者 1→8，QPS 45K→16K；发布者 8→16，QPS 24K→30K（不线性）。修完 1~3 后应复测 `go run -race` 与 `pprof`，并做真实跨机器 RTT 压测再评估生产吞吐。

## 验证方式

```bash
# 压测程序（临时目录）
go run . -listeners 4 -pubbers 8 -reqs 20000      # 吞吐/QPS
go run . -verify                                    # 广播正确性 + 错误聚合
go run -race . -listeners 4 -pubbers 8 -reqs 5000  # 数据竞争（修完应 0 race）
go run . -cpuprofile cpu.prof && go tool pprof -top eventbench cpu.prof
```
