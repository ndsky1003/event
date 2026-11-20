package coder

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/tinylib/msgp/msgp"
)

// 本文件验证 msgp 快路径与 msgpack 反射路径之间的互通性：
//
// 真实场景是「同一业务类型，一端 codegen 过（走 msgp 快路径）、另一端没 codegen（走 msgpack 反射）」，
// 例如一个进程用 msgp 编码、另一个进程用 msgpack 解码，或反之。
// 由于 msgpack_coder 的路径由传入 v 的动态类型决定，这里用两个「同构但接口不同」的类型来分别强制两条路径：
//
//   - codegenUser：实现 msgp.Marshaler / msgp.Unmarshaler（模拟 codegen 产物，map 格式）
//   - plainUser：  普通 struct（走 msgpack 反射）
//
// 两者字段一一对应、map key 一致（均用字段名），从而验证互通。

type codegenUser struct {
	Name   string
	Age    int32
	Score  float64
	OK     bool
	Data   []byte
	Tags   []string
	Counts map[string]int64
}

type plainUser struct {
	Name   string
	Age    int32
	Score  float64
	OK     bool
	Data   []byte
	Tags   []string
	Counts map[string]int64
}

func (u *codegenUser) MarshalMsg(b []byte) ([]byte, error) {
	b = msgp.AppendMapHeader(b, 7)
	b = msgp.AppendString(b, "Name")
	b = msgp.AppendString(b, u.Name)
	b = msgp.AppendString(b, "Age")
	b = msgp.AppendInt32(b, u.Age)
	b = msgp.AppendString(b, "Score")
	b = msgp.AppendFloat64(b, u.Score)
	b = msgp.AppendString(b, "OK")
	b = msgp.AppendBool(b, u.OK)
	b = msgp.AppendString(b, "Data")
	b = msgp.AppendBytes(b, u.Data)
	b = msgp.AppendString(b, "Tags")
	b = msgp.AppendArrayHeader(b, uint32(len(u.Tags)))
	for _, s := range u.Tags {
		b = msgp.AppendString(b, s)
	}
	b = msgp.AppendString(b, "Counts")
	b = msgp.AppendMapHeader(b, uint32(len(u.Counts)))
	for k, v := range u.Counts {
		b = msgp.AppendString(b, k)
		b = msgp.AppendInt64(b, v)
	}
	return b, nil
}

func (u *codegenUser) UnmarshalMsg(b []byte) ([]byte, error) {
	sz, b, err := msgp.ReadMapHeaderBytes(b)
	if err != nil {
		return b, err
	}
	for i := uint32(0); i < sz; i++ {
		var key []byte
		key, b, err = msgp.ReadMapKeyZC(b)
		if err != nil {
			return b, err
		}
		switch string(key) {
		case "Name":
			u.Name, b, err = msgp.ReadStringBytes(b)
		case "Age":
			u.Age, b, err = msgp.ReadInt32Bytes(b)
		case "Score":
			u.Score, b, err = msgp.ReadFloat64Bytes(b)
		case "OK":
			u.OK, b, err = msgp.ReadBoolBytes(b)
		case "Data":
			u.Data, b, err = msgp.ReadBytesZC(b)
		case "Tags":
			var n uint32
			n, b, err = msgp.ReadArrayHeaderBytes(b)
			if err != nil {
				return b, err
			}
			u.Tags = make([]string, n)
			for j := uint32(0); j < n; j++ {
				u.Tags[j], b, err = msgp.ReadStringBytes(b)
				if err != nil {
					return b, err
				}
			}
		case "Counts":
			var n uint32
			n, b, err = msgp.ReadMapHeaderBytes(b)
			if err != nil {
				return b, err
			}
			u.Counts = make(map[string]int64, n)
			for j := uint32(0); j < n; j++ {
				var k string
				var v int64
				k, b, err = msgp.ReadStringBytes(b)
				if err != nil {
					return b, err
				}
				v, b, err = msgp.ReadInt64Bytes(b)
				if err != nil {
					return b, err
				}
				u.Counts[k] = v
			}
		default:
			b, err = msgp.Skip(b)
		}
		if err != nil {
			return b, err
		}
	}
	return b, nil
}

func equalUserPlain(u codegenUser, p plainUser) bool {
	return u.Name == p.Name && u.Age == p.Age && u.Score == p.Score && u.OK == p.OK &&
		bytes.Equal(u.Data, p.Data) &&
		reflect.DeepEqual(u.Tags, p.Tags) &&
		reflect.DeepEqual(u.Counts, p.Counts)
}

func sampleCodegen() codegenUser {
	return codegenUser{
		Name:   "alice",
		Age:    30,
		Score:  1.5,
		OK:     true,
		Data:   []byte{0xde, 0xad, 0xbe, 0xef},
		Tags:   []string{"a", "b", "c"},
		Counts: map[string]int64{"x": 1, "y": -2},
	}
}

func samplePlain() plainUser {
	return plainUser{
		Name:   "alice",
		Age:    30,
		Score:  1.5,
		OK:     true,
		Data:   []byte{0xde, 0xad, 0xbe, 0xef},
		Tags:   []string{"a", "b", "c"},
		Counts: map[string]int64{"x": 1, "y": -2},
	}
}

// TestMsgpackMsgpInterop 验证 msgp 编码 → msgpack 解码、msgpack 编码 → msgp 解码两个方向。
func TestMsgpackMsgpInterop(t *testing.T) {
	t.Run("msgp_encode_msgpack_decode", func(t *testing.T) {
		b, err := Encode(Msgp, nil, sampleCodegen()) // codegenUser 走 msgp 快路径
		if err != nil {
			t.Fatal(err)
		}
		var p plainUser // *plainUser 不实现 UnmarshalMsg，走 msgpack 反射
		rest, err := Decode(Msgp, b, &p)
		if err != nil {
			t.Fatal(err)
		}
		if len(rest) != 0 {
			t.Fatalf("剩余 %d 字节", len(rest))
		}
		if !equalUserPlain(sampleCodegen(), p) {
			t.Fatalf("msgp→msgpack 不一致:\n got %+v", p)
		}
	})

	t.Run("msgpack_encode_msgp_decode", func(t *testing.T) {
		b, err := Encode(Msgp, nil, samplePlain()) // plainUser 走 msgpack 反射
		if err != nil {
			t.Fatal(err)
		}
		var c codegenUser // *codegenUser 实现 UnmarshalMsg，走 msgp 快路径
		rest, err := Decode(Msgp, b, &c)
		if err != nil {
			t.Fatal(err)
		}
		if len(rest) != 0 {
			t.Fatalf("剩余 %d 字节", len(rest))
		}
		if !equalUserPlain(c, samplePlain()) {
			t.Fatalf("msgpack→msgp 不一致:\n got %+v", c)
		}
	})
}

// TestMsgpackMsgpInteropBatch 验证连续 encode 一批（msgp 与 msgpack 混用）、
// 再以交叉路径连续 decode，确认边界分割与互通都正确。
// 单进程测试等价于跨进程：encode 产出的 []byte 就是「进程 A 发送的字节流」，decode 只消费它。
func TestMsgpackMsgpInteropBatch(t *testing.T) {
	var b []byte

	// 进程 A：连续 encode，第 1/3 个走 msgp，第 2/4 个走 msgpack。
	var err error
	b, err = Encode(Msgp, b, codegenUser{Name: "a", Age: 1, Score: 1.5, OK: true, Data: []byte{1, 2}, Tags: []string{"x"}, Counts: map[string]int64{"n": 1}})
	if err != nil {
		t.Fatal(err)
	}
	b, err = Encode(Msgp, b, plainUser{Name: "b", Age: 2, Score: 2.5, OK: false, Data: []byte{3}, Tags: []string{"y"}, Counts: map[string]int64{"m": 2}})
	if err != nil {
		t.Fatal(err)
	}
	b, err = Encode(Msgp, b, codegenUser{Name: "c", Age: 3, Score: 3.5, OK: true, Data: []byte{}, Tags: []string{}, Counts: map[string]int64{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err = Encode(Msgp, b, plainUser{Name: "d", Age: 4, Score: 4.5, OK: false, Data: []byte{}, Tags: []string{}, Counts: map[string]int64{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err = Encode(Msgp, b, int64(42))
	if err != nil {
		t.Fatal(err)
	}
	b, err = Encode(Msgp, b, "tail")
	if err != nil {
		t.Fatal(err)
	}

	// 进程 B：用与编码端「相反」的路径 decode。
	rest := b

	var p1 plainUser // 参数1 是 msgp 编码 → msgpack 解码
	if rest, err = Decode(Msgp, rest, &p1); err != nil {
		t.Fatalf("decode[0]: %v", err)
	}
	if !equalUserPlain(codegenUser{Name: "a", Age: 1, Score: 1.5, OK: true, Data: []byte{1, 2}, Tags: []string{"x"}, Counts: map[string]int64{"n": 1}}, p1) {
		t.Fatalf("decode[0] 不一致: %+v", p1)
	}

	var c2 codegenUser // 参数2 是 msgpack 编码 → msgp 解码
	if rest, err = Decode(Msgp, rest, &c2); err != nil {
		t.Fatalf("decode[1]: %v", err)
	}
	if !equalUserPlain(c2, plainUser{Name: "b", Age: 2, Score: 2.5, OK: false, Data: []byte{3}, Tags: []string{"y"}, Counts: map[string]int64{"m": 2}}) {
		t.Fatalf("decode[1] 不一致: %+v", c2)
	}

	var p3 plainUser // 参数3 是 msgp 编码 → msgpack 解码（含 nil Data / nil Tags / 空 Counts）
	if rest, err = Decode(Msgp, rest, &p3); err != nil {
		t.Fatalf("decode[2]: %v", err)
	}
	if !equalUserPlain(codegenUser{Name: "c", Age: 3, Score: 3.5, OK: true, Data: []byte{}, Tags: []string{}, Counts: map[string]int64{}}, p3) {
		t.Fatalf("decode[2] 不一致: %+v", p3)
	}

	var c4 codegenUser // 参数4 是 msgpack 编码 → msgp 解码（含空 Data / 空 Tags / nil Counts）
	if rest, err = Decode(Msgp, rest, &c4); err != nil {
		t.Fatalf("decode[3]: %v", err)
	}
	if !equalUserPlain(c4, plainUser{Name: "d", Age: 4, Score: 4.5, OK: false, Data: []byte{}, Tags: []string{}, Counts: map[string]int64{}}) {
		t.Fatalf("decode[3] 不一致: %+v", c4)
	}

	var i5 int64
	if rest, err = Decode(Msgp, rest, &i5); err != nil {
		t.Fatalf("decode[4]: %v", err)
	}
	if i5 != 42 {
		t.Fatalf("decode[4] = %d, want 42", i5)
	}

	var s6 string
	if rest, err = Decode(Msgp, rest, &s6); err != nil {
		t.Fatalf("decode[5]: %v", err)
	}
	if s6 != "tail" {
		t.Fatalf("decode[5] = %q, want tail", s6)
	}

	if len(rest) != 0 {
		t.Fatalf("整批解码后应无剩余字节，实际 %d", len(rest))
	}
}

// TestMsgpVsMsgpackBytes 验证「不含 map 字段」时，msgp 快路径与 msgpack 反射产出的字节完全一致。
func TestMsgpVsMsgpackBytes(t *testing.T) {
	u := msgpUser{Name: "alice", Age: 30}
	p := struct {
		Name string
		Age  int32
	}{Name: "alice", Age: 30}

	b1, err := Encode(Msgp, nil, u) // msgpUser 走 msgp 快路径
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Encode(Msgp, nil, p) // 匿名 struct 走 msgpack 反射
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("msgp 与 msgpack 编码字节不一致:\n msgp:    %x\n msgpack: %x", b1, b2)
	}
}

// TestMsgpackMsgpNilContainer 记录 nil 容器（map/slice）在两条路径下的编码差异。
//
// 这是 tinylib/msgp 与 vmihailenco/msgpack 的固有约定差异，不是 msgpack_coder 的 bug：
//   - msgpack 反射把 nil 容器编码为 nil（0xc0）；
//   - msgp 把 nil 容器编码为 empty（empty map/array/bin）。
//
// 跨路径互用时的不对称后果：
//   - msgpack 编码的 nil 容器交给 msgp 解码会报错（ReadMapHeaderBytes/ReadArrayHeaderBytes 无法读 nil）；
//   - msgp 编码的 nil 容器交给 msgpack 解码，因 vmihailenco 把空容器解码回 nil，nil 语义恰好保持。
func TestMsgpackMsgpNilContainer(t *testing.T) {
	t.Run("msgpack_nil_map_to_msgp_decode_error", func(t *testing.T) {
		// Data/Tags 给非 nil 空值，只留 Counts 为 nil，确保报错来自 map 字段。
		b, err := Encode(Msgp, nil, plainUser{Name: "x", Data: []byte{}, Tags: []string{}, Counts: nil})
		if err != nil {
			t.Fatal(err)
		}
		var c codegenUser
		_, err = Decode(Msgp, b, &c)
		if err == nil {
			t.Fatal("预期 msgpack 编码的 nil map 交给 msgp 解码会报错，但实际成功了")
		}
		t.Logf("如预期报错: %v", err)
	})

	t.Run("msgp_nil_map_via_msgpack_keeps_nil", func(t *testing.T) {
		b, err := Encode(Msgp, nil, codegenUser{Name: "x", Counts: nil})
		if err != nil {
			t.Fatal(err)
		}
		var p plainUser
		if _, err := Decode(Msgp, b, &p); err != nil {
			t.Fatal(err)
		}
		if p.Counts != nil {
			t.Fatalf("msgp 编码的 nil map 经 msgpack 解码后应保持 nil, got %v", p.Counts)
		}
	})

	t.Run("msgpack_nil_slice_to_msgp_decode_error", func(t *testing.T) {
		// Data/Counts 给非 nil 空值，只留 Tags 为 nil，确保报错来自 array(slice) 字段。
		b, err := Encode(Msgp, nil, plainUser{Name: "x", Data: []byte{}, Tags: nil, Counts: map[string]int64{}})
		if err != nil {
			t.Fatal(err)
		}
		var c codegenUser
		_, err = Decode(Msgp, b, &c)
		if err == nil {
			t.Fatal("预期 msgpack 编码的 nil slice 交给 msgp 解码会报错，但实际成功了")
		}
		t.Logf("如预期报错: %v", err)
	})

	t.Run("msgp_nil_slice_via_msgpack_keeps_nil", func(t *testing.T) {
		b, err := Encode(Msgp, nil, codegenUser{Name: "x", Tags: nil})
		if err != nil {
			t.Fatal(err)
		}
		var p plainUser
		if _, err := Decode(Msgp, b, &p); err != nil {
			t.Fatal(err)
		}
		if p.Tags != nil {
			t.Fatalf("msgp 编码的 nil slice 经 msgpack 解码后应保持 nil, got %v", p.Tags)
		}
	})
}
