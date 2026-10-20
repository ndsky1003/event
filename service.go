package event

import (
	"fmt"

	"github.com/ndsky1003/event/v2/coder"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/sirupsen/logrus"
)

// service 服务端一条客户端连接的封装。
// 传输（帧、攒批 writev、心跳、看门狗、关闭）全部交给 Conn，
// 这里只负责 msg.Msg 的编解码与分发。
type service struct {
	id     uint32
	name   string
	server *server
	conn   *Conn
	// envCoder 这条连接的消息框架编码方式，由握手第一帧的标记字节决定，回包沿用。
	// 只在 handshake 里写一次，之后只读——service 是握手成功后才发布到
	// server.services 的，发布走的是 server.l，读侧天然看得到。
	envCoder coder.T

	// tps 这条连接注册过的 topic 条目。连接断开时只摘这几条，
	// 不必遍历服务端全部的 topic 索引。由 server.l 保护。
	tps []*topicSids
}

// addTopicLocked 记录注册关系，去重。必须在持 server.l 时调用。
func (this *service) addTopicLocked(ts *topicSids) {
	for _, x := range this.tps {
		if x == ts {
			return
		}
	}
	this.tps = append(this.tps, ts)
}

func newService(server *server, id uint32, conn *Conn) *service {
	return &service{
		id:       id,
		name:     fmt.Sprintf("sid-%d", id),
		server:   server,
		conn:     conn,
		envCoder: coder.Msgp,
	}
}

// handshake 鉴权握手。必须在 conn.Serve() 之前调用：
// 那时 readPump/writePump 都还没起，只有当前 goroutine 碰 socket，
// 所以可以用 writeSync/readSync 这种同步读写。
func (this *service) handshake() error {
	_, body, err := this.conn.readSync()
	if err != nil {
		return fmt.Errorf("%w,read verify req:%w", ErrServer, err)
	}
	var req msg.MsgVerifyReq
	if this.envCoder, err = decodeEnvelope(body, &req); err != nil {
		return fmt.Errorf("%w,decode verify req:%w", ErrServer, err)
	}

	res := &msg.MsgVerifyRes{}
	if *this.server.opt.secret != "" && req.Secret != *this.server.opt.secret {
		res.Err = fmt.Errorf("%w,invalid secret", ErrServer).Error()
		if e := this.writeHandshakeRes(res); e != nil {
			return fmt.Errorf("%w,write verify res:%w", ErrServer, e)
		}
		return fmt.Errorf("%w,invalid secret,name:%s", ErrServer, req.Name)
	}
	this.name = req.Name
	return this.writeHandshakeRes(res)
}

func (this *service) writeHandshakeRes(res *msg.MsgVerifyRes) error {
	body, err := encodeEnvelope(this.envCoder, res)
	if err != nil {
		return fmt.Errorf("%w,encode verify res:%w", ErrServer, err)
	}
	if err = this.conn.writeSync(flag_msg, body); err != nil {
		return fmt.Errorf("%w,write verify res:%w", ErrServer, err)
	}
	return nil
}

// HandleMsg 实现 Handler，由 readPump 同步调用。
// PING/PONG 已经在传输层消化掉了，不会走到这里。
//
// 这里**必须同步**调用 server.handle，不能丢给 goroutine：
// 服务端 handle 只做索引改动和转发，不执行任何用户代码，本身不耗时；
// 一旦 go 出去，同一条连接上的消息顺序就没了——`On` 的注册帧可能排在
// 紧随其后的 `Emit` 之后才被处理，那次 Emit 就变成"无人监听"，事件静默丢失。
// 这正是之前端到端偶发失败的成因之一。
// 真正的耗时（业务回调）在客户端侧由 func_call 异步执行，不在这条路径上。
func (this *service) HandleMsg(body []byte) error {
	var frame msg.Msg
	if _, err := decodeEnvelope(body, &frame); err != nil {
		return fmt.Errorf("%w,decode body err:%w", ErrServer, err)
	}
	switch frame.T {
	case msgtype.On, msgtype.Req, msgtype.ReqRandOne, msgtype.Res, msgtype.ResRandOne:
		this.server.handle(this.id, this.name, &frame)
	default:
		logrus.Infof("%v,invalid msg:%+v", ErrServer, frame)
	}
	return nil
}

// Write 编码后投递给 Conn 的发送队列。
// 不再自己维护 sendChan：Conn 里已经有了，而且会攒批 writev。
func (this *service) Write(m *msg.Msg) error {
	body, err := encodeEnvelope(this.envCoder, m)
	if err != nil {
		return fmt.Errorf("%w,encode msg err:%w", ErrServer, err)
	}
	if err = this.conn.write(flag_msg, body); err != nil {
		return fmt.Errorf("%w,conn write err:%w", ErrServer, err)
	}
	return nil
}

// Close 关闭底层连接。幂等（Conn.Close 自身幂等）。
func (this *service) Close() error {
	if err := this.conn.Close(); err != nil {
		return fmt.Errorf("%w,close conn err:%w", ErrServer, err)
	}
	return nil
}
