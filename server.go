package event

import (
	"context"

	"github.com/ndsky1003/event/v3/buffer"
	"github.com/ndsky1003/net/v2/conn"
	"github.com/ndsky1003/net/v2/server"
)

// Server 事件服务器
type Server struct {
	mgr        *server_mgr
	srv        *server.Server
	cacel_func context.CancelFunc
}

// NewServer 创建事件服务器
func NewServer(opts ...*ServerOption) *Server {
	mgr := newEventServer(opts...)
	ctx, cancel := context.WithCancel(context.Background())
	srv := server.New(ctx, mgr,
		server.Options().
			WithConn(func(opt *conn.Option) {
				opt.SetGenBufFn(func() []byte {
					return buffer.Get()
				})
			}),
	)
	return &Server{
		cacel_func: cancel,
		mgr:        mgr,
		srv:        srv,
	}
}

// Listen 监听单个地址
func (s *Server) Listen(url string) error {
	return s.srv.Listen(url)
}

// Listens 监听多个地址
func (s *Server) Listens(addrs ...string) error {
	return s.srv.Listen(addrs...)
}

// Close 关闭服务器
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	if s.cacel_func != nil {
		s.cacel_func()
	}
	return nil
}
