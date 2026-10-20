package event

import (
	"testing"

	"github.com/ndsky1003/event/v2/coder"
	"github.com/ndsky1003/event/v2/msg"
	"github.com/ndsky1003/event/v2/msgtype"
)

func sampleMsg() *msg.Msg {
	return &msg.Msg{
		T:         msgtype.Req,
		EventName: "db_check.slow_query",
		Seq:       1234567,
		Name:      "node-a",
		BodyCount: 2,
		Coder:     coder.Msgp,
		Bytes:     []byte("payload-payload-payload"),
		Err:       "",
	}
}

func TestEnvelopeRoundTripAllCoders(t *testing.T) {
	for _, ct := range []coder.T{coder.Msgp, coder.JSON, coder.Raw} {
		body, err := encodeEnvelope(ct, sampleMsg())
		if err != nil {
			t.Fatalf("coder=%v encode: %v", ct, err)
		}
		// 标记字节必须就是实际用的 coder（Raw 编不了结构体，应被收敛成 Msgp）
		gotTag := coder.T(body[0])
		wantTag := envelopeCoder(ct)
		if gotTag != wantTag {
			t.Fatalf("coder=%v 标记字节应是 %v，实际 %v", ct, wantTag, gotTag)
		}
		var got msg.Msg
		used, err := decodeEnvelope(body, &got)
		if err != nil {
			t.Fatalf("coder=%v decode: %v", ct, err)
		}
		if used != wantTag {
			t.Fatalf("coder=%v decode 返回的 coder 不对: %v", ct, used)
		}
		src := sampleMsg()
		if got.T != src.T || got.EventName != src.EventName || got.Seq != src.Seq ||
			got.Name != src.Name || got.BodyCount != src.BodyCount || string(got.Bytes) != string(src.Bytes) {
			t.Fatalf("coder=%v 往返不一致: %+v", ct, got)
		}
	}
}

// 服务端要能按客户端选的 coder 解码，不需要预先协商
func TestEnvelopeSelfDescribing(t *testing.T) {
	for _, ct := range []coder.T{coder.Msgp, coder.JSON} {
		body, err := encodeEnvelope(ct, &msg.MsgVerifyReq{Name: "n", Secret: "s"})
		if err != nil {
			t.Fatal(err)
		}
		var req msg.MsgVerifyReq
		used, err := decodeEnvelope(body, &req)
		if err != nil {
			t.Fatalf("coder=%v: %v", ct, err)
		}
		if used != ct || req.Name != "n" || req.Secret != "s" {
			t.Fatalf("coder=%v 自描述失败: used=%v req=%+v", ct, used, req)
		}
	}
}

func TestEnvelopeBadInput(t *testing.T) {
	var m msg.Msg
	if _, err := decodeEnvelope(nil, &m); err == nil {
		t.Fatal("空 body 应报错")
	}
	if _, err := decodeEnvelope([]byte{byte(coder.Msgp)}, &m); err == nil {
		t.Fatal("只有标记字节应报错")
	}
	// 未知 coder 标记
	if _, err := decodeEnvelope([]byte{99, 0x80}, &m); err == nil {
		t.Fatal("未知 coder 标记应报错")
	}
	// 标记说 Msgp、内容却是垃圾
	if _, err := decodeEnvelope([]byte{byte(coder.Msgp), 0xff, 0xff}, &m); err == nil {
		t.Fatal("坏内容应报错")
	}
}

// 记录线上字节数，方便对比 coder 选择
func BenchmarkEnvelopeSize(b *testing.B) {
	src := sampleMsg()
	for _, ct := range []coder.T{coder.Msgp, coder.JSON} {
		body, err := encodeEnvelope(ct, src)
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("coder=%v 消息框架 %d 字节", ct, len(body))
	}
}

func BenchmarkEnvelopeEncode(b *testing.B) {
	src := sampleMsg()
	for _, ct := range []coder.T{coder.Msgp, coder.JSON} {
		b.Run(ct.String(), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := encodeEnvelope(ct, src); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEnvelopeDecode(b *testing.B) {
	src := sampleMsg()
	for _, ct := range []coder.T{coder.Msgp, coder.JSON} {
		body, err := encodeEnvelope(ct, src)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(ct.String(), func(b *testing.B) {
			var got msg.Msg
			for i := 0; i < b.N; i++ {
				if _, err := decodeEnvelope(body, &got); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
