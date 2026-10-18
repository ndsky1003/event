package msg

import "testing"

func TestCallDo(t *testing.T) {
	c := NewCall(&Msg{T: MsgType_req})
	c.Do()

	select {
	case got := <-c.Done:
		if got != c {
			t.Error("Do 应把自身发送到 Done")
		}
	default:
		t.Error("Do 后 Done 应有值")
	}

	// 第二次 Do:channel 已满(容量 1),走 default 分支,不阻塞、不 panic
	c.Do()
}

func TestCallDoNilReceiver(t *testing.T) {
	var c *Call
	c.Do() // nil 接收者不 panic
}
