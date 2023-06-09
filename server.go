package event

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ndsky1003/event/codec"
	"github.com/ndsky1003/event/msg"
	"github.com/ndsky1003/event/options"
	"github.com/sirupsen/logrus"
)

type server struct {
	codecFunc options.CreateServerCodecFunc
	mutex     sync.RWMutex
	seq       uint64
	monitor   map[*msg.EventTopic]map[uint64]struct{}
	services  map[uint64]*service

	reqTimeOut time.Duration //请求超时
	reqSeq     uint64
	reqMetas   map[uint64]*serverReqMeta
}

type serverReqMeta struct {
	reqServerID    uint64
	serverReqSeq   uint64
	serverReqCount uint64
	existErr       bool
	errs           []string
	Time           time.Time
	//clien info
	clientSeq uint64
	msg.EventType
}

func NewServer(opts ...*options.ServerOptions) *server {
	c := &server{
		services:   map[uint64]*service{},
		monitor:    map[*msg.EventTopic]map[uint64]struct{}{},
		reqTimeOut: 10,
		reqMetas:   map[uint64]*serverReqMeta{},
	}

	opt := options.Server().SetCodecFunc(func(conn io.ReadWriteCloser) (codec.Codec, error) {
		return codec.NewGobCodec(conn), nil
	}).Merge(opts...)
	if opt.CodecFunc != nil {
		c.codecFunc = *opt.CodecFunc
	}
	if opt.ReqTimeout != nil {
		c.reqTimeOut = *opt.ReqTimeout
	}
	go c.checkTimeOut()
	return c
}

// addrs ["192.168.0.1","192.168.0.2"]
// port 8080
func (this *server) Listens(addrs []string, port int) {
	for i := len(addrs) - 1; i >= 0; i-- {
		addr := addrs[i]
		listenAddr := fmt.Sprintf("%v:%v", addr, port)
		if i != 0 {
			go this.listen(listenAddr)
		} else {
			this.listen(listenAddr)
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
			continue
		}
		codec, err := this.codecFunc(conn)
		if err != nil {
			logrus.Error(err)
			continue
		}
		this.mutex.Lock()
		seq := this.seq
		seq = incSeqID(seq)
		this.seq = seq
		service := newService(this, seq, codec)
		this.services[this.seq] = service
		this.mutex.Unlock()
		go service.serve()
	}
}

var errTimeout = errors.New("req timeout")

func (this *server) checkTimeOut() {
	for {
		this.mutex.Lock()
		for _, reqMeta := range this.reqMetas {
			if time.Now().Sub(reqMeta.Time) > this.reqTimeOut*time.Second { //
				gotMsg := &msg.Msg{
					T:         msg.MsgType_res,
					ServerSeq: reqMeta.serverReqSeq,
					ClientSeq: reqMeta.clientSeq,
					EventType: reqMeta.EventType,
					Error:     errTimeout.Error(),
				}
				delete(this.reqMetas, reqMeta.serverReqSeq)
				if err := this.write(reqMeta.reqServerID, gotMsg); err != nil {
					this.close(reqMeta.reqServerID)
				}
			}

		}
		this.mutex.Unlock()
		time.Sleep(2 * time.Second)
	}
}

func (this *server) close(id uint64) {
	for _, m := range this.monitor {
		delete(m, id)
	}
	delete(this.services, id)
}

// case msgType_on, msgType_req, msgType_res:
func (this *server) handle(serviceID uint64, serviceName string, frame *msg.Msg) {
	switch frame.T {
	case msg.MsgType_on:
		this.on(serviceID, frame)
	case msg.MsgType_req:
		this.req(serviceID, frame)
	case msg.MsgType_res:
		this.res(serviceID, serviceName, frame)
	default:
		logrus.Infof("丢弃：%d,msg:%+v\n", serviceID, frame)
	}

}

func (this *server) on(serviceID uint64, frame *msg.Msg) {
	et := frame.EventType
	if et == "" {
		return
	}
	this.mutex.Lock()
	defer this.mutex.Unlock()
	var isExistET bool
	for tp, sids := range this.monitor {
		if tp.Equal(et) { //这里比较不能用equal
			sids[serviceID] = struct{}{}
			isExistET = true
		}
	}
	if !isExistET {
		v := map[uint64]struct{}{serviceID: {}}
		this.monitor[msg.NewEventTopic(et)] = v
	}
	if err := this.write(serviceID, frame); err != nil {
		logrus.Error(err)
		this.close(serviceID)
	}
}

func (this *server) req(serviceID uint64, frame *msg.Msg) {
	et := frame.EventType
	if et == "" {
		return
	}
	this.mutex.Lock()
	serverSeq := this.reqSeq
	serverSeq = incSeqID(serverSeq)
	this.reqSeq = serverSeq
	frame.ServerSeq = serverSeq
	reqMeta := &serverReqMeta{
		reqServerID:    serviceID,
		clientSeq:      frame.ClientSeq,
		serverReqSeq:   serverSeq,
		serverReqCount: 0,
		Time:           time.Now(),
	}
	this.reqMetas[serverSeq] = reqMeta
	this.mutex.Unlock()
	this.mutex.RLock()
	var isDone bool
	var needDeleteServiceID []uint64
	var hasSendServiceID = map[uint64]struct{}{}
	for tp, v := range this.monitor {
		if tp.Match(et) {
			for sid := range v {
				if _, ok := hasSendServiceID[sid]; !ok {
					if err := this.write(sid, frame); err == nil {
						hasSendServiceID[sid] = struct{}{}
						isDone = true
						reqMeta.serverReqCount++
					} else {
						reqMeta.serverReqCount--
						needDeleteServiceID = append(needDeleteServiceID, sid)
					}
				}
			}
		}
	}
	if !isDone {
		frame.T = msg.MsgType_res
		frame.Bytes = nil
		frame.BodyCount = 0
		if err := this.write(serviceID, frame); err != nil {
			needDeleteServiceID = append(needDeleteServiceID, serviceID)
		}
	}
	this.mutex.RUnlock()
	this.mutex.Lock()
	defer this.mutex.Unlock()
	for _, sid := range needDeleteServiceID {
		this.close(sid)
	}
	if isDone && reqMeta.serverReqCount == 0 { //接收比较快
		res := &msg.Msg{
			T:         msg.MsgType_res,
			ServerSeq: serverSeq,
			ClientSeq: frame.ClientSeq,
			EventType: frame.EventType,
		}
		if reqMeta.existErr {
			res.Error = strings.Join(reqMeta.errs, ";")
		}
		this.write(reqMeta.reqServerID, res)
	}
}

func (this *server) res(serviceID uint64, serviceName string, msg *msg.Msg) {
	reqSeq := msg.ServerSeq
	this.mutex.Lock()
	reqMeta, ok := this.reqMetas[reqSeq]
	if ok {
		reqMeta.serverReqCount--
		leftCount := reqMeta.serverReqCount
		if msg.Error != "" {
			reqMeta.existErr = true
			reqMeta.errs = append(reqMeta.errs, fmt.Sprintf("Name:【%s】,Err:【%s】", serviceName, msg.Error))
		}
		this.mutex.Unlock()
		if leftCount == 0 { //res
			if reqMeta.existErr {
				msg.Error = strings.Join(reqMeta.errs, ";")
			}
			err := this.write(reqMeta.reqServerID, msg)
			this.mutex.Lock()
			if err != nil {
				logrus.Errorf("res senderID:%d,msg:%+v", reqMeta.reqServerID, msg)
				this.close(serviceID)
			}
			delete(this.reqMetas, reqSeq)
			this.mutex.Unlock()
		}
	} else {
		this.mutex.Unlock()
	}
}

func (this *server) write(serviceID uint64, msg *msg.Msg) (err error) {
	if service, ok := this.services[serviceID]; ok {
		if err = service.write(msg); err == nil {
			return
		}
	}
	return
}

//-----------------------------service----------------------------

type service struct {
	id     uint64
	name   string
	done   chan struct{}
	server *server
	codec  codec.Codec
	mutex  sync.Mutex //读是单线程，写加锁
}

func newService(server *server, id uint64, codec codec.Codec) *service {
	s := &service{
		id:     id,
		server: server,
		codec:  codec,
		done:   make(chan struct{}),
	}
	return s
}

func (this *service) serve() {
	if this == nil {
		return
	}
	var err error
	var firstFrame msg.Msg
	if err = this.read(&firstFrame); err == nil {
		//TODO varify
		this.name = firstFrame.Name
	}
	err = this.write(&msg.Msg{T: msg.MsgType_prepared})
	for err == nil {
		select {
		case <-this.done:
			err = errors.New("stop service")
		default:
			var frame msg.Msg
			err = this.read(&frame)
			if err != nil {
				continue
			}
			switch frame.T {
			case msg.MsgType_ping:
				retFrame := &msg.Msg{T: msg.MsgType_pong, ServerSeq: frame.ServerSeq, ClientSeq: frame.ClientSeq}
				err = this.write(retFrame)
			case msg.MsgType_on, msg.MsgType_req, msg.MsgType_res:
				go this.server.handle(this.id, this.name, &frame)
			default:
				logrus.Infof("invalid msg:%+v", frame)
			}
		}
	}
	this.server.close(this.id)
	logrus.Errorf("service id:%d is die,err:%v\n", this.id, err)
}

func (this *service) close() error {
	return this.codec.Close()
}

func (this *service) read(msg *msg.Msg) error {
	return this.codec.Read(msg)
}

func (this *service) write(msg *msg.Msg) error {
	this.mutex.Lock()
	defer this.mutex.Unlock()
	err := this.codec.Write(msg)
	if err != nil {
		logrus.Error(err)
		close(this.done)
		this.close()
	}
	return err
}
