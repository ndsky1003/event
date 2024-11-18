package event

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antlabs/timer"
	"github.com/ndsky1003/event/v2/codec"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/ndsky1003/event/v2/topic"
	"github.com/sirupsen/logrus"
)

type server struct {
	codecFunc  codec.CreateCodecFunc
	sid        uint32
	opt        *ServerOption
	seq        uint64
	tm         timer.Timer //时间轮
	sync.Mutex             //protect under
	monitor    map[*topic.Topic]map[uint32]struct{}
	services   map[uint32]*service
	pending    map[uint64]*server_call //server to client 调用的call
}

type server_call struct {
	tn         timer.TimeNoder //超时检测,如期返回就stop
	origin_sid uint32          //原始的sid
	origin_seq uint64          // frame.需要改成服务器的seq

	serverReqCount uint64
	errs           []error
}

func NewServer(opts ...*ServerOption) *server {
	c := &server{
		codecFunc: func(conn io.ReadWriteCloser) (codec.Codec, error) { return codec.NewGobCodec(conn), nil },
		tm:        timer.NewTimer(),
		services:  map[uint32]*service{},
		monitor:   map[*topic.Topic]map[uint32]struct{}{},
		pending:   map[uint64]*server_call{},
	}
	c.opt = ServerOptions().
		SetSecret("").SetTimeout(10).SetIsWrapError(true).
		merges(opts...)
	go c.tm.Run()
	return c
}

// addrs ["192.168.0.1:8080","192.168.0.2:8080"]
func (this *server) Listens(addrs []string) {
	for i := len(addrs) - 1; i >= 0; i-- {
		addr := addrs[i]
		if i != 0 {
			go this.listen(addr)
		} else {
			this.listen(addr)
		}
	}
}

//url:port
func (this *server) Listen(url string) {
	this.listen(url)
}

func (this *server) listen(url string) {
	if this == nil {
		panic("server is nil")
	}
	listen, err := net.Listen("tcp", url)
	if err != nil {
		panic(err)
	}
	for {
		conn, err := listen.Accept()
		if err != nil {
			err = fmt.Errorf("%w,err:%w", ErrServer, err)
			logrus.Error(err)
			continue
		}
		go this.handle_conn(conn)
	}
}

func (this *server) handle_conn(conn net.Conn) {
	codec, err := this.codecFunc(conn)
	if err != nil {
		if e := conn.Close(); e != nil {
			err = fmt.Errorf("%w,%w", ErrServer, e)
		}
		logrus.Error(err)
		return
	}
	sid := atomic.AddUint32(&this.sid, 1)
	service := newService(this, sid, codec)
	var firstFrame msg.MsgVerifyReq
	err = codec.Read(&firstFrame)
	if err != nil {
		err = fmt.Errorf("%w,%w", ErrServer, err)
	}

	if err == nil && *this.opt.secret != "" && firstFrame.Secret != *this.opt.secret {
		err = fmt.Errorf("%w,%v", ErrServer, "invalid secret")
	}
	if err != nil {
		if err := codec.Write(&msg.MsgVerifyRes{Err: err.Error()}); err != nil {
			err = fmt.Errorf("%w,%w", ErrServer, err)
			logrus.Error(err)
		}
		if err := codec.Close(); err != nil {
			err = fmt.Errorf("%w,%w", ErrServer, err)
			logrus.Error(err)
		}
		return
	}
	service.name = firstFrame.Name
	if err := codec.Write(&msg.MsgVerifyRes{}); err != nil {
		err = fmt.Errorf("%w,%w", ErrServer, err)
		logrus.Error(err)
		if err := codec.Close(); err != nil {
			err = fmt.Errorf("%w,%w", ErrServer, err)
			logrus.Error(err)
		}
		return
	}
	this.Lock()
	this.services[this.sid] = service
	this.Unlock()
	logrus.Infof("service:%s[%d] is ready", firstFrame.Name, this.sid)
	go service.serve()

}

func (this *server) Close(sid uint32, isCloseSon bool) error {
	this.Lock()
	defer this.Unlock()
	return this.close(sid, isCloseSon)
}

func (this *server) close(sid uint32, isCloseSon bool) (err error) {
	for _, m := range this.monitor {
		delete(m, sid)
	}
	if service, ok := this.services[sid]; ok {
		if isCloseSon {
			err = service.Close(false)
		}
		delete(this.services, sid)
	}
	return
}

func (this *server) handle(sid uint32, sname string, frame *msg.Msg) {
	switch frame.T {
	case msgtype.On:
		this.on(sid, frame)
	case msgtype.Req, msgtype.ReqSomeOne:
		this.req(sid, frame)
	case msgtype.Res, msgtype.ResSomeOne:
		this.res(sid, sname, frame)
	default:
		logrus.Infof("丢弃：%d,name:%v,msg:%+v\n", sid, sname, frame)
	}

}

func (this *server) on(sid uint32, frame *msg.Msg) {
	en := frame.EventName
	this.Lock()
	defer this.Unlock()
	var is_exist bool
	for tp, sids := range this.monitor {
		if tp.Equal(en) {
			sids[sid] = struct{}{}
			is_exist = true
		}
	}
	if !is_exist {
		v := map[uint32]struct{}{sid: {}}
		this.monitor[topic.New(en)] = v
	}
	this.write(sid, frame)
}

func (this *server) req(sid uint32, frame *msg.Msg) {
	et := frame.EventName
	server_seq := atomic.AddUint64(&this.seq, 1)
	origin_seq := frame.Seq
	frame.Seq = server_seq
	s_call := &server_call{
		origin_sid:     sid,
		origin_seq:     origin_seq,
		serverReqCount: 0,
	}
	var isDone bool
	var hasSendServiceID = map[uint32]struct{}{}
	this.Lock()
	defer this.Unlock()
	this.pending[server_seq] = s_call
	for tp, v := range this.monitor {
		if tp.Match(et) {
			for sid := range v {
				if _, ok := hasSendServiceID[sid]; !ok {
					this.write(sid, frame)
					hasSendServiceID[sid] = struct{}{}
					isDone = true
					s_call.serverReqCount++ //这里与下面的res在同一个锁里,没问题,但是性能损耗及其严重
				}
			}
		}
	}

	if isDone {
		s_call.tn = this.tm.AfterFunc(*this.opt.Timeout*time.Second, func() {
			this.release_timeout(server_seq)
		})
	} else {
		frame.T = msgtype.Res
		frame.Seq = s_call.origin_seq
		frame.Bytes = nil
		frame.BodyCount = 0
		delete(this.pending, server_seq)
		this.write(sid, frame)
	}
}

func (this *server) res(_ uint32, serviceName string, msg *msg.Msg) {
	server_seq := msg.Seq
	this.Lock()
	defer this.Unlock()
	if s_call, ok := this.pending[server_seq]; ok {
		s_call.serverReqCount--
		leftCount := s_call.serverReqCount
		if e := msg.Err; e != "" {
			var ee error
			if *this.opt.is_wrap_error {
				ee = fmt.Errorf("Name:[%s],Err:[%s]", serviceName, e)
			} else {
				ee = errors.New(e)
			}
			s_call.errs = append(s_call.errs, ee)
		}
		if leftCount == 0 || msg.T == msgtype.ResSomeOne { //res
			if len(s_call.errs) > 0 {
				msg.Err = errors.Join(s_call.errs...).Error()
			}
			msg.Seq = s_call.origin_seq
			this.write(s_call.origin_sid, msg)
			s_call.tn.Stop()
			delete(this.pending, server_seq)
		}
	}
}

func (this *server) write(sid uint32, msg *msg.Msg) {
	if service, ok := this.services[sid]; ok {
		service.Write(msg)
	}
}

func (this *server) release_timeout(server_seq uint64) {
	this.Lock()
	defer this.Unlock()
	if s_call, ok := this.pending[server_seq]; ok {
		s_call.tn.Stop()
		delete(this.pending, server_seq)
		frame := &msg.Msg{
			T:   msgtype.Res,
			Seq: s_call.origin_seq,
			Err: fmt.Errorf("%w,timeout", ErrServer).Error(),
		}
		this.write(s_call.origin_sid, frame)
	}
}
