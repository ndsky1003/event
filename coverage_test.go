package event

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/ndsky1003/event/codec"
	"github.com/ndsky1003/event/msg"
)

// newTestClient 构造一个不启动 keepAlive 的裸 Client,用于纯内部状态测试。
func newTestClient() *Client {
	return &Client{
		events:  make(map[*msg.EventTopic][]*method),
		pending: make(map[uint64]*msg.Call),
	}
}

func TestStopHeart(t *testing.T) {
	c := newTestClient()
	c.StopHeart()
	if !c.isStopHeart {
		t.Error("StopHeart 应设置 isStopHeart=true")
	}
	c.StopHeart() // 幂等,不 panic
}

func TestPrintCall(t *testing.T) {
	c := newTestClient()
	c.PrintCall() // 空 pending 不 panic

	c.pending[1] = msg.NewCall(&msg.Msg{T: msg.MsgType_req})
	c.pending[2] = msg.NewCall(&msg.Msg{T: msg.MsgType_ping})
	c.PrintCall() // 非空 pending 不 panic
}

func TestSeq(t *testing.T) {
	c := newTestClient()
	if got := c.Seq(); got != 0 {
		t.Errorf("初始 Seq 应为 0,得到 %d", got)
	}
	c.seq = 42
	if got := c.Seq(); got != 42 {
		t.Errorf("Seq 应为 42,得到 %d", got)
	}
}

// localIPv4 返回本机第一个非 loopback 的 IPv4 地址,找不到则跳过。
func localIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("无法获取网卡地址: %v", err)
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	t.Skip("未找到非 loopback IPv4 地址,跳过 Listens 多地址测试")
	return ""
}

func TestServerListens(t *testing.T) {
	ip := localIPv4(t)
	port := freePort(t)
	s := NewServer()
	// addrs[0] 为同步监听(阻塞),addrs[1] 用 goroutine 监听。
	// 用本机 IP + 127.0.0.1 两个不同地址,可共享同一端口。
	go s.Listens([]string{ip, "127.0.0.1"}, port)

	// 轮询等待 goroutine 监听的 127.0.0.1:port 可连接
	url := fmt.Sprintf("127.0.0.1:%d", port)
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", url)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("Listens 未能在 127.0.0.1 监听")
	}
	conn.Close()
}

func TestServiceClose(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	s := newService(nil, 1, codec.NewGobCodec(serverConn))
	if err := s.close(); err != nil {
		t.Fatalf("service.close: %v", err)
	}
	// 二次关闭不应 panic
	_ = s.close()
}
