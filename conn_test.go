package event

import (
	"errors"
	"net"
	"testing"
	"time"
)

// 连接关掉之后 write 必须返回真错误。
// 返回 nil 的话，上层 writeLocked 会以为"已经发出去了"，
// 消息其实没进发送队列 —— 静默丢数据。
func TestConnWriteAfterCloseIsNotSilentlyOK(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	c, err := New(a, nil, ConnOptions().SetHeartInterval(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}

	if err = c.write(flag_msg, []byte{1, 2, 3}); err == nil {
		t.Fatal("连接已关闭，write 不能返回 nil")
	} else if !errors.Is(err, ErrConnClosed) {
		t.Fatalf("期望 ErrConnClosed，实际 %v", err)
	}

	// Close 幂等：第二次不报错，且原因不会被覆盖
	if err = c.Close(); err != nil {
		t.Fatalf("第二次 Close 应返回 nil，实际 %v", err)
	}
	if err = c.CloseWithError(errors.New("later")); err != nil {
		t.Fatalf("第二次 CloseWithError 应返回 nil，实际 %v", err)
	}
	if err = c.Err(); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("第一个关闭原因必须保留，实际 %v", err)
	}
	if !c.IsClosed() {
		t.Fatal("IsClosed 应为 true")
	}
}

// 帧超过上限必须被拒掉，而不是把长度字段写截断、把字节流搞乱。
func TestConnOversizedFrameRejected(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	c, err := New(a, nil, ConnOptions().SetHeartInterval(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err = newFrame(flag_msg, make([]byte, maxFrameSize+1)); err == nil {
		t.Fatal("超限帧应该被拒绝")
	}
	if f, err := newFrame(flag_msg, make([]byte, maxFrameSize)); err != nil {
		t.Fatalf("刚好到上限的帧不该被拒: %v", err)
	} else {
		f.Release()
	}
}
