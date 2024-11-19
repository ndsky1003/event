package event

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ndsky1003/event/v2/codec"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/sirupsen/logrus"
)

type service struct {
	id         uint32
	name       string
	done       chan struct{}
	server     *server
	codec      codec.Codec
	sync.Mutex //读是单线程，写加锁
	sendChan   chan *msg.Msg
}

func newService(server *server, id uint32, codec codec.Codec) *service {
	s := &service{
		id:       id,
		server:   server,
		codec:    codec,
		done:     make(chan struct{}),
		sendChan: make(chan *msg.Msg, 30),
	}
	go func() {
		for {
			select {
			case <-s.done:
				return
			case msg := <-s.sendChan:
				s.write(msg)
			}
		}

	}()

	return s
}

func (this *service) serve() {
	var err error
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
			case msgtype.Ping:
				retFrame := &msg.Msg{T: msgtype.Pong, Seq: frame.Seq}
				this.Write(retFrame)
			case msgtype.On, msgtype.Req, msgtype.ReqSomeOne, msgtype.Res, msgtype.ResSomeOne:
				go this.server.handle(this.id, this.name, &frame)
			default:
				logrus.Infof("%v,invalid msg:%+v", ErrServer, frame)
			}
		}
	}
	this.Close(true)
	// this.server.close(this.id)
	logrus.Errorf("service id:%d is die,err:%v\n", this.id, err)
}
func (this *service) Close(isRemoveFromMgr bool) error {
	this.Lock()
	defer this.Unlock()
	return this.close(isRemoveFromMgr)
}

func (this *service) close(isRemoveFromMgr bool) error {
	if this.codec != nil {
		this.codec.Close()
		this.codec = nil
	}
	if this.done != nil {
		close(this.done)
		this.done = nil
	}
	if isRemoveFromMgr {
		this.server.Close(this.id, false)
	}
	return nil
}

func (this *service) read(msg any) error {
	return this.codec.Read(msg)
}

func (this *service) Write(msg *msg.Msg) {
	// this.Lock()
	// defer this.Unlock()
	this.sendChan <- msg
	// return this.write(msg)
}

func (this *service) write(msg any) (err error) {
	if this.codec == nil {
		return
	}
	this.Lock()
	defer this.Unlock()
	if this.codec == nil {
		return
	}
	if err = this.codec.Write(msg); err != nil {
		return fmt.Errorf("%w,codec write err:%w", ErrServer, err)
	}
	return
}
