# Bug 清单

> 来源：`correctness_test.go` 正确性测试，`go test -race` 暴露。
> 状态：均未修复，测试作为回归记录保留（对应测试当前 FAIL 或 Skip）。

## 一、功能正确性（会 panic）

### 1. 非法正则导致 panic
- **位置**：`msg/eventType.go:30`（`regexp.MustCompile(str[1 : length-1])`）
- **现象**：`On("/[/", ...)` 传入非法正则时 `regexp.MustCompile` 直接 panic，拖垮整个进程。
- **期望**：返回错误而非 panic。
- **复现测试**：`TestOnInvalidRegexpShouldNotPanic`

### 2. 空正则 slice 越界 panic
- **位置**：`msg/eventType.go:30`（`str[1:0]`）
- **现象**：`On("/", ...)`（单斜杠）被判定为正则，`str[1:length-1]` 即 `str[1:0]` 越界 panic。
- **期望**：返回错误而非 panic。
- **复现测试**：`TestOnEmptyRegexpShouldNotPanic`

### 3. nil 指针参数导致 gob panic
- **位置**：`client.go:436`（`paramEncoder.Encode(arg)`）
- **现象**：`Emit("ev", (*T)(nil))` 时 `gob.Encode` 对 nil 指针直接 panic（非返回 error）。
- **期望**：捕获并返回错误。
- **复现测试**：`TestNilPointerArg`

## 二、并发正确性（数据竞争 / 崩溃）

### 4. `Seq()` 无锁读导致 DATA RACE
- **位置**：`client.go:493-495`
- **现象**：并发 `Emit` + `Seq()` 时，`Seq()` 无锁读 `seq` 与 `send()` 持锁写 `seq` 竞争。
- **期望**：`Seq()` 加锁或用原子操作。
- **复现测试**：`TestConcurrentEmitAndSeq`

### 5. `PrintCall()` 无锁遍历 pending 导致 DATA RACE + 崩溃
- **位置**：`client.go:208-212`
- **现象**：并发 `EmitAsync` + `PrintCall()` 时，无锁遍历 `pending` map 与 `input()`/`stop()` 的 delete 竞争，出现 DATA RACE 及 nil 指针崩溃。
- **期望**：`PrintCall()` 加锁保护。
- **复现测试**：`TestConcurrentEmitAndPrintCall`

### 6. service 重复 close(done) 导致 server 崩溃
- **位置**：`server.go:368`（`close(this.done)`）
- **现象**：同一 service 多次写失败会重复 `close(this.done)`，触发 `panic: close of closed channel`，**整个 server 进程崩溃**（最严重）。
- **期望**：`done` 用 `sync.Once` 关闭，或加标志位。
- **复现测试**：`TestServiceWriteCloseTwice`（`TestConcurrentEmitAndClose` 因会崩溃进程已 Skip）

### 7. `send()` 锁外写 `call.Msg.ClientSeq` 导致 DATA RACE
- **位置**：`client.go:476`（`call.Msg.ClientSeq = seq`）
- **现象**：`send()` 在 `mutex.Unlock()` 后写 `call.Msg.ClientSeq`，与 `stop()` 持锁读 `call.Msg` 竞争。
- **期望**：将 `ClientSeq` 赋值移入锁内。
- **复现测试**：`TestConcurrentEmitAndClose`（Skip，依赖 server 端崩溃场景）

## 验证方式

```bash
# 全部 bug 回归测试
go test -race -run 'TestOnInvalidRegexpShouldNotPanic|TestOnEmptyRegexpShouldNotPanic|TestNilPointerArg|TestConcurrentEmitAndSeq|TestConcurrentEmitAndPrintCall|TestServiceWriteCloseTwice' -v ./

# 完整正确性测试套件
go test -race -run 'TestOn|TestNilPointer|TestConcurrentEmitAndSeq|TestConcurrentEmitAndPrintCall|TestServiceWriteCloseTwice|TestHandler|TestRegex|TestBroadcast|TestMultipleHandlers|TestPartialHandler|TestReconnect|TestArgCount|TestUnencodable|TestEmitZero|TestMixed|TestServerError|TestEmitAsync|TestEventType' -v ./
```
