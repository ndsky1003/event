package event

import (
	"fmt"

	"github.com/ndsky1003/event/v2/codec"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
	"github.com/sirupsen/logrus"
)

type service struct {
	id       uint32
	name     string
	done     chan struct{}
	server   *server
	codec    codec.Codec
	sendChan chan *msg.Msg
}

func newService(server *server, id uint32, codec codec.Codec) *service {
	done := make(chan struct{})
	s := &service{
		id:       id,
		server:   server,
		codec:    codec,
		done:     done,
		sendChan: make(chan *msg.Msg, 30),
	}
	go func() {
		for {
			select {
			case <-done:
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
		var frame msg.Msg
		err = this.read(&frame)
		if err != nil {
			break
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
	this.Close(true)
	logrus.Errorf("service id:%d is die end,err:%v\n", this.id, err)
}
func (this *service) Close(isRemoveFromMgr bool) {
	this.close(isRemoveFromMgr)
}

func (this *service) close(isRemoveFromMgr bool) {
	this.codec.Close() //下面write可能会server还是会写,置空会panic
	if this.done != nil {
		close(this.done)
		this.done = nil
	}
	if isRemoveFromMgr {
		this.server.Close(this.id, false)
	}
}

func (this *service) read(msg any) error {
	return this.codec.Read(msg)
}

func (this *service) Write(msg *msg.Msg) {
	this.sendChan <- msg
}

func (this *service) write(msg any) (err error) {
	if err = this.codec.Write(msg); err != nil {
		return fmt.Errorf("%w,codec write err:%w", ErrServer, err)
	}
	return
}
