# conn.go 待解决问题

## 1. msgChan 满仍阻塞 readPump（高）

- 位置：`conn.go` `readPump` 的 `flag_msg` 入队 select
- 现象：`msgChan`（容量 `msgChanSize = 256`）满时，`select { case this.msgChan <- body: }` 阻塞 `readPump`，停止读 socket。`ping/pong` 也在 `readPump` 里处理，因此心跳一起停摆，对端会在 `2.2 * heartInterval` 后判死。
- 本质：只是把阻塞阈值从 0 抬到 256，并未解耦"读泵"与"业务处理"。
- 方向（择一）：
  - 有界队列 + 非阻塞入队，超限直接 `CloseWithError`（显式失败，不静默丢、不 OOM）。
  - 应用层信用窗口（类似 HTTP/2 `WINDOW_UPDATE`）：接收端按消费进度回 credit，发送端窗口为 0 时在发布方阻塞。接收队列恒有界、读泵永不阻塞、心跳独立，但需改协议。
