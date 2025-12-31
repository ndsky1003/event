package event

import (
	"context"

	"github.com/ndsky1003/net/v2/conn"
	"github.com/ndsky1003/net/v2/server"
)

// Server 事件服务器
type Server struct {
	mgr *eventServer
	srv *server.Server
}

// NewServer 创建事件服务器
func NewServer(opts ...*ServerOption) *Server {
	mgr := newEventServer(opts...)

	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel // 保存以备将来使用
	srv := server.New(ctx, mgr,
		server.Options().
			WithConn(func(opt *conn.Option) {
				// 设置 buffer 生成函数
				opt.SetGenBufFn(func() []byte {
					return make([]byte, 1024*4)
				})
			}),
	)

	return &Server{
		mgr: mgr,
		srv: srv,
	}
}

// Listen 监听单个地址
func (s *Server) Listen(url string) {
	go func() {
		if err := s.srv.Listen(url); err != nil {
			panic(err)
		}
	}()
}

// Listens 监听多个地址
func (s *Server) Listens(addrs []string) {
	for _, addr := range addrs {
		go func(a string) {
			if err := s.srv.Listen(a); err != nil {
				panic(err)
			}
		}(addr)
	}
}

// Close 关闭服务器
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}
