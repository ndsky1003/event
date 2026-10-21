package event

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	flag_msg byte = 1 << iota
	flag_ping
	flag_pong
)

const (
	// 帧格式：[flag 1 字节][body 长度 4 字节 BigEndian][body]
	// 帧与帧背靠背相连，一帧最多占 2 个 iovec（hdr + body），由 net.Buffers 一次 writev 发出
	frameHeaderSize = 5
	maxFrameSize    = 64 << 10
	// IOV_MAX = 1024，每帧最多 2 个 iovec，所以一批最多 512 帧
	maxBatchFrames = 512
	sendChanSize   = 30
)

// Handler 处理收到的业务帧（flag_msg）的 body
type Handler interface {
	HandleMsg(body []byte) error
}

// connOption Conn 这一层的参数，与 ClientOption/ServerOption 独立
type connOption struct {
	HeartInterval      *time.Duration // 看门狗周期；空闲超过它才发 PING
	ReadTimeoutFactor  *float64       // 判死时限 = HeartInterval * 该系数，默认 2.2
	WriteTimeoutFactor *float64       // 单次 writev 的上限 = HeartInterval * 该系数，默认 2.2
	HandshakeTimeout   *time.Duration // pump 启动前握手读写的超时
}

func ConnOptions() *connOption {
	return new(connOption)
}

func (this *connOption) SetHeartInterval(d time.Duration) *connOption {
	if this == nil {
		return this
	}
	this.HeartInterval = &d
	return this
}

func (this *connOption) SetReadTimeoutFactor(f float64) *connOption {
	if this == nil {
		return this
	}
	this.ReadTimeoutFactor = &f
	return this
}

func (this *connOption) SetWriteTimeoutFactor(f float64) *connOption {
	if this == nil {
		return this
	}
	this.WriteTimeoutFactor = &f
	return this
}

func (this *connOption) SetHandshakeTimeout(d time.Duration) *connOption {
	if this == nil {
		return this
	}
	this.HandshakeTimeout = &d
	return this
}

func (this *connOption) merges(opts ...*connOption) *connOption {
	for _, opt := range opts {
		this.merge(opt)
	}
	return this
}

func (this *connOption) merge(opt *connOption) {
	if opt == nil {
		return
	}
	if opt.HeartInterval != nil {
		this.HeartInterval = opt.HeartInterval
	}
	if opt.ReadTimeoutFactor != nil {
		this.ReadTimeoutFactor = opt.ReadTimeoutFactor
	}
	if opt.WriteTimeoutFactor != nil {
		this.WriteTimeoutFactor = opt.WriteTimeoutFactor
	}
	if opt.HandshakeTimeout != nil {
		this.HandshakeTimeout = opt.HandshakeTimeout
	}
}

// frame 一帧待发数据。hdr 内联（每帧一份，不能复用 scratch：帧会在 chan 和
// net.Buffers 里排队，共享缓冲区会被后一帧覆盖）；body 直接引用调用方的字节，不复制。
type frame struct {
	hdr  [frameHeaderSize]byte
	body []byte
}

var framePool = sync.Pool{
	New: func() any { return new(frame) },
}

func (this *frame) Release() {
	this.hdr = [frameHeaderSize]byte{}
	this.body = nil
	framePool.Put(this)
}

type Conn struct {
	net.Conn
	r    *bufio.Reader
	hbuf [frameHeaderSize]byte // 读侧帧头，只有 readPump 一个 goroutine 用

	handler Handler

	heartInterval    time.Duration
	readTimeout      time.Duration
	writeTimeout     time.Duration
	handshakeTimeout time.Duration

	// 只有 writePump 这一个 goroutine 从 socket 写；其他 goroutine 一律通过 sendChan 投递。
	// sendChan 永远不 close：close 会和生产者竞态，靠 ctx + closed 收尾。
	sendChan chan *frame
	bufs     net.Buffers // writePump 专用，攒一批的 iovec 列表

	// nextDeadline / nextWriteDeadline 分别是 readPump、writePump 自己的检查点，
	// 各自只有一个 goroutine 碰，不需要原子。
	nextDeadline      time.Time
	nextWriteDeadline time.Time

	closed atomic.Bool
	ctx    context.Context
	cancel context.CancelFunc

	l        sync.Mutex // protect closeErr
	closeErr error
}

func New(conn net.Conn, handler Handler, opts ...*connOption) (*Conn, error) {
	// 先铺默认值，再让调用方的 opts 覆盖（merge 只认非 nil 字段），
	// 与 client.go / server.go 的写法保持一致；这样下面解引用不用再判 nil
	opt := ConnOptions().
		SetHeartInterval(10 * time.Second).
		SetReadTimeoutFactor(2.2). // 允许丢 1 个心跳，并允许第 2 个晚到 20%
		SetWriteTimeoutFactor(2.2).
		SetHandshakeTimeout(5 * time.Second).
		merges(opts...)

	c := &Conn{
		Conn:             conn,
		r:                bufio.NewReaderSize(conn, 16<<10),
		handler:          handler,
		heartInterval:    *opt.HeartInterval,
		readTimeout:      time.Duration(float64(*opt.HeartInterval) * *opt.ReadTimeoutFactor),
		writeTimeout:     time.Duration(float64(*opt.HeartInterval) * *opt.WriteTimeoutFactor),
		handshakeTimeout: *opt.HandshakeTimeout,
		sendChan:         make(chan *frame, sendChanSize),
		bufs:             make(net.Buffers, 0, maxBatchFrames*2),
	}
	c.closed.Store(false)
	c.ctx, c.cancel = context.WithCancel(context.Background())

	c.nextDeadline = time.Now().Add(c.readTimeout)
	return c, nil
}

// Close 主动关闭，幂等。要停连接请调它，不要只调 cancel——cancel 叫不醒阻塞中的 read。
func (this *Conn) Close() error {
	return this.CloseWithError(ErrConnClosed)
}

// CloseWithError 关闭并记录原因，幂等；第一个非 nil 原因生效。
// pump 异常退出时调它：cancel 叫醒 writePump，Conn.Close 叫醒阻塞中的 readPump。
// 只有第一个调用者真正执行关闭并返回底层错误；后续调用是空操作，返回 nil，
// 想知道连接为什么结束用 Err()。
func (this *Conn) CloseWithError(err error) error {
	if err != nil {
		this.l.Lock()
		if this.closeErr == nil {
			this.closeErr = err
		}
		this.l.Unlock()
	}
	if !this.closed.CompareAndSwap(false, true) {
		return nil
	}
	this.cancel()
	return this.Conn.Close()
}

// Err 返回连接关闭的原因；未关闭返回 nil，主动 Close 返回 ErrConnClosed
func (this *Conn) Err() error {
	this.l.Lock()
	defer this.l.Unlock()
	return this.closeErr
}

func (this *Conn) IsClosed() bool {
	return this.closed.Load()
}

// Serve 启动读写泵并阻塞到两者都退出，返回连接终止的原因。
// 主动 Close 返回 ErrConnClosed；对端断连或超时返回包装后的底层错误。
func (this *Conn) Serve() error {
	// 初始 read deadline：对端连上一个字节都不发，也能在 readTimeout 内被踢掉。
	// 之后由 readPump 按检查点续期、writePump 的看门狗判死。
	dl := time.Now().Add(this.readTimeout)
	this.nextDeadline = dl
	if err := this.SetReadDeadline(dl); err != nil {
		this.CloseWithError(fmt.Errorf("set read deadline: %w", err))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := this.writePump(); err != nil {
			this.CloseWithError(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := this.readPump(); err != nil {
			this.CloseWithError(err)
		}
	}()
	wg.Wait()

	this.Close()
	return this.Err()
}

// SetHandler 设置业务处理器。必须在 Serve() 之前调用。
func (this *Conn) SetHandler(h Handler) {
	this.handler = h
}

// writeSync 直接写一帧并落盘，绕过 sendChan。
// 只允许在 Serve() 之前用（握手）：那时 writePump 还没起，不存在并发写，
// 也就不会和 this.bufs / sendChan 抢。
func (this *Conn) writeSync(flag byte, data []byte) error {
	f, err := newFrame(flag, data)
	if err != nil {
		return err
	}
	defer f.Release()

	if err = this.SetWriteDeadline(time.Now().Add(this.handshakeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	bufs := net.Buffers{f.hdr[:]}
	if len(f.body) > 0 {
		bufs = append(bufs, f.body)
	}
	if _, err = bufs.WriteTo(this.Conn); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// readSync 直接读一帧。同样只允许在 Serve() 之前调用。
func (this *Conn) readSync() (flag byte, body []byte, err error) {
	if err = this.SetReadDeadline(time.Now().Add(this.handshakeTimeout)); err != nil {
		return 0, nil, fmt.Errorf("set read deadline: %w", err)
	}
	return this.read()
}

// newFrame 组帧：填 flag + 4 字节长度，body 只引用不复制
func newFrame(flag byte, data []byte) (*frame, error) {
	if len(data) > maxFrameSize {
		return nil, fmt.Errorf("frame size %d exceeds maximum %d", len(data), maxFrameSize)
	}
	f := framePool.Get().(*frame)
	f.hdr[0] = flag
	binary.BigEndian.PutUint32(f.hdr[1:], uint32(len(data)))
	f.body = data
	return f, nil
}

// closedErr 返回连接关闭的原因，保证非 nil。
// write 这类路径一定要拿到真错误：返回 nil 会让上层以为"已经发出去了"，
// 而帧其实没进发送队列，属于静默丢数据。
func (this *Conn) closedErr() error {
	if err := this.Err(); err != nil {
		return err
	}
	return ErrConnClosed
}

// write 组帧并投递到 sendChan，供外部 goroutine（Emit 等）调用。
// 通道满时阻塞，直到写出去、ctx 结束或连接关闭。
// 注意：返回只代表"已入队"，data 的所有权要等 writev 完成才真正交出去，
// 调用方在此之前不能复用/归还 data。
func (this *Conn) write(flag byte, data []byte) error {
	if this.closed.Load() {
		return this.closedErr()
	}
	f, err := newFrame(flag, data)
	if err != nil {
		return err
	}
	select {
	case this.sendChan <- f:
		// 入队和 Close 之间没有原子性：可能刚 check 完 closed=false，
		// 另一边就 CAS 成功并 cancel 了，select 两个 case 同时就绪时是随机选，
		// 于是帧进了一个已经没有消费者的队列。这里补一次检查，
		// 把"静默丢一条"变成"如实报错"。
		if this.closed.Load() {
			return this.closedErr()
		}
		return nil
	case <-this.ctx.Done():
		f.Release()
		return this.closedErr()
	}
}

// tryWrite 非阻塞投递。通道满就返回 false，由调用方决定丢弃还是报错。
// pong 和 writePump 的心跳必须走这里：它们本身就在 writePump/readPump 里，
// 用阻塞的 write 会自己等自己，直接死锁。
func (this *Conn) tryWrite(flag byte, data []byte) bool {
	if this.closed.Load() {
		return false
	}
	f, err := newFrame(flag, data)
	if err != nil {
		return false
	}
	select {
	case this.sendChan <- f:
		return true
	default:
		f.Release()
		return false
	}
}

// writeBatch 把一批帧拼成 net.Buffers，一次 writev 发出。
// 只能由 writePump 调用（this.bufs 无锁）。
func (this *Conn) writeBatch(frames []*frame) error {
	if len(frames) == 0 {
		return nil
	}
	this.bufs = this.bufs[:0]
	for _, f := range frames {
		this.bufs = append(this.bufs, f.hdr[:])
		if len(f.body) > 0 {
			this.bufs = append(this.bufs, f.body)
		}
	}

	// 写 deadline 是兜底：对端已死时 writev 会卡在 TCP 重传里，
	// 没有它 writePump 能挂几分钟，readPump 也一起挂死。
	// 但和读侧一样不能每批都设：拿 fd 锁 + 起停 timer 在热路径上是白扔的开销，
	// 实测 8 发布者 × 4 监听者场景下每秒约 20 万次 SetWriteDeadline。
	// 预算快用尽才续期，这样任何一次 writev 至少还剩 writeTimeout/4 的额度。
	now := time.Now()
	if this.nextWriteDeadline.IsZero() || now.After(this.nextWriteDeadline.Add(-this.writeTimeout/4)) {
		this.nextWriteDeadline = now.Add(this.writeTimeout)
		if err := this.SetWriteDeadline(this.nextWriteDeadline); err != nil {
			return fmt.Errorf("set write deadline: %w", err)
		}
	}

	// 必须传 this.Conn，不能传 this：
	// *net.TCPConn 实现了 buffersWriter 才会走 writev，*Conn 没实现会退化成逐个 Write。
	// 另外 WriteTo 会消费它拿到的那个 slice header，所以转一个临时值出去，保住 this.bufs。
	bufs := net.Buffers(this.bufs)
	if _, err := bufs.WriteTo(this.Conn); err != nil {
		// writev 部分写后出错，字节流已经错位，这条连接只能关掉
		return fmt.Errorf("writev %d frames: %w", len(frames), err)
	}
	return nil
}

// read 读一帧。固定 5 字节头，一次 ReadFull 就能拿到，不用 varint 逐字节读。
func (this *Conn) read() (flag byte, body []byte, err error) {
	if _, err = io.ReadFull(this.r, this.hbuf[:]); err != nil {
		return
	}
	flag = this.hbuf[0]
	size := binary.BigEndian.Uint32(this.hbuf[1:])
	if size > maxFrameSize {
		err = fmt.Errorf("frame size %d exceeds maximum %d", size, maxFrameSize)
		return
	}
	if size == 0 {
		// PING / PONG 帧只有 5 字节头
		return
	}
	body = make([]byte, size)
	// 别想着省这次分配：bufio 那一跳 copy + 这次 make 合计约 30ns/帧，
	// 实测换成每连接复用缓冲（连正确性问题一起忽略）端到端 QPS 变化 -0.5%~+1.8%，
	// 全在 4% 的噪声底里。而 bufio 本身不能去：去掉它每帧多 2 次 syscall，吞吐掉 10%~16%。
	_, err = io.ReadFull(this.r, body)
	return
}

// ping 由 writePump 的 pingTimer 调用（只有空闲时才会触发），非阻塞投递
func (this *Conn) ping() {
	if !this.tryWrite(flag_ping, nil) {
		// 通道里还排着数据，说明连接本来就活跃，这个周期跳过 PING 是安全的
		slog.Warn("ping", "err", "send buffer full, skip ping")
	}
}

// pong 由 readPump 调用。丢弃 PONG 是安全的：对方下个周期会重试 PING，
// 或者对方发业务数据时也会刷新活跃状态，所以不应该因此关连接。
func (this *Conn) pong() {
	if !this.tryWrite(flag_pong, nil) {
		slog.Warn("pong", "err", "send pong buffer full")
	}
}

// writePump 负责将 sendChan 中的数据写入连接，并维护心跳。
// 判死不在这里做：readPump 的 read deadline 是唯一判死手段（对端静默时 read 超时）。
// pingTimer 每次成功写出后重置，只有真正空闲才发 PING。它是相对计时，
// 不受 ticker 抖动影响——用"now-lastWrite >= heartInterval"这种时长比较，
// 实测会因为 149.9998ms 这种抖动少 0.2 微秒而跳过 PING，把活连接判死。
func (this *Conn) writePump() (err error) {
	pingTimer := time.NewTimer(this.heartInterval)
	defer pingTimer.Stop()

	batch := make([]*frame, 0, maxBatchFrames)
	defer func() {
		for _, f := range batch {
			f.Release()
		}
	}()

	for {
		var f *frame
		select {
		case <-this.ctx.Done():
			return nil
		case f = <-this.sendChan:
		case <-pingTimer.C:
			// ping 走 tryWrite：writePump 自己是 sendChan 唯一的消费者，
			// 用阻塞的 write 会自己等自己。通道满就跳过这个周期。
			this.ping()
			pingTimer.Reset(this.heartInterval)
			continue
		}

		// 拿到一帧后非阻塞排空，攒成一批；通道空了就立刻发，低负载下不会为了攒包憋延迟
		batch = append(batch, f)
	drain:
		for len(batch) < maxBatchFrames {
			select {
			case f = <-this.sendChan:
				batch = append(batch, f)
			default:
				break drain
			}
		}

		if err = this.writeBatch(batch); err != nil {
			return err
		}
		for _, x := range batch {
			x.Release()
		}
		batch = batch[:0]

		// 刚写过数据，连接活跃，把 PING 推后一个周期
		if !pingTimer.Stop() {
			select {
			case <-pingTimer.C:
			default:
			}
		}
		pingTimer.Reset(this.heartInterval)
	}
}

// readPump 负责从连接读取数据。
// deadline 不在每帧刷新：那在热路径上是白扔的开销（拿 fd 锁 + 起停 timer）。
// 改成只在预算快用尽时续期，调用次数从"每帧一次"降到"每个 heartInterval 一次"。
func (this *Conn) readPump() error {
	for {
		now := time.Now()

		if now.After(this.nextDeadline.Add(-this.heartInterval)) {
			this.nextDeadline = now.Add(this.readTimeout)
			if err := this.SetReadDeadline(this.nextDeadline); err != nil {
				return fmt.Errorf("set read deadline: %w", err)
			}
		}

		flag, body, err := this.read()
		if err != nil {
			// 对端静默超时（read deadline 到期）单独包装成"判死"文案，
			// 便于与对端正常断开（EOF）/ 异常断开（reset）区分；底层错误用 %w 保留。
			if os.IsTimeout(err) {
				return fmt.Errorf("peer silent: %w", err)
			}
			return fmt.Errorf("read error: %w", err)
		}

		switch flag {
		case flag_msg:
			if this.handler != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("handler panic", "err", r)
						}
					}()
					if err := this.handler.HandleMsg(body); err != nil {
						slog.Error("handler msg", "err", err)
					}
				}()
			}
		case flag_ping:
			// 收到 PING，回复 PONG
			this.pong()
		case flag_pong:
			// 收到 PONG，仅表示对方活着，无需操作
		default:
			return fmt.Errorf("unknown flag: %d", flag)
		}
	}
}
