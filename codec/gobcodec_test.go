package codec

import (
	"net"
	"testing"

	"github.com/ndsky1003/event/msg"
)

func TestGobCodecWriteRead(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	server := NewGobCodec(serverConn)
	client := NewGobCodec(clientConn)

	writeMsg := &msg.Msg{
		T:         msg.MsgType_req,
		ServerSeq: 123,
		ClientSeq: 456,
		Name:      "client1",
		EventType: "db_op",
		BodyCount: 2,
		Bytes:     []byte{1, 2, 3, 4},
		Error:     "some error",
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- client.Write(writeMsg)
	}()

	var readMsg msg.Msg
	if err := server.Read(&readMsg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write: %v", err)
	}

	if readMsg.T != writeMsg.T ||
		readMsg.ServerSeq != writeMsg.ServerSeq ||
		readMsg.ClientSeq != writeMsg.ClientSeq ||
		readMsg.Name != writeMsg.Name ||
		readMsg.EventType != writeMsg.EventType ||
		readMsg.BodyCount != writeMsg.BodyCount ||
		readMsg.Error != writeMsg.Error ||
		len(readMsg.Bytes) != len(writeMsg.Bytes) {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", readMsg, writeMsg)
	}
}

func TestGobCodecMultipleMessages(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	server := NewGobCodec(serverConn)
	client := NewGobCodec(clientConn)

	const n = 100
	msgs := make([]*msg.Msg, n)
	for i := 0; i < n; i++ {
		msgs[i] = &msg.Msg{
			T:         msg.MsgType_req,
			ServerSeq: uint64(i),
			EventType: "ev",
		}
	}

	go func() {
		for _, m := range msgs {
			if err := client.Write(m); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
	}()

	for i := 0; i < n; i++ {
		var got msg.Msg
		if err := server.Read(&got); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got.ServerSeq != uint64(i) {
			t.Errorf("msg %d: got ServerSeq %d, want %d", i, got.ServerSeq, i)
		}
	}
}

func TestGobCodecClose(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	codec := NewGobCodec(clientConn)
	if err := codec.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	serverConn.Close()

	if err := codec.Write(&msg.Msg{T: msg.MsgType_ping}); err == nil {
		t.Errorf("write after close should fail")
	}
}

func TestGobCodecCloseNilReceiver(t *testing.T) {
	var c *gobCodeC
	if err := c.Close(); err != nil {
		t.Errorf("nil receiver close should be nil, got %v", err)
	}
}

func TestGobCodecReadError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := NewGobCodec(serverConn)
	clientConn.Close()

	var got msg.Msg
	if err := server.Read(&got); err == nil {
		t.Errorf("expected error reading from closed conn")
	}
	serverConn.Close()
}
