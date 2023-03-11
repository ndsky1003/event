package codec

import (
	"fmt"
	"testing"
)

type Person struct {
	Name string
	Age  int
}

func (this *Person) String() string {
	return fmt.Sprintf("%+v", *this)
}

func TestGobcodec(t *testing.T) {
	//data := []Person{{Name: "li1", Age: 33}, {Name: "luo", Age: 18}}
	//var buf bytes.Buffer
	//codec := NewGobCodec(&buf)
	//if err := codec.Write(&Msg{LocalSeq: 100}); err != nil {
	//t.Error(err)
	//}
	//var h Msg
	//if err := codec.Read(&h); err != nil {
	//t.Error(err)
	//}
	//t.Error(h)
	//reader := bytes.NewReader(h.Bytes)
	//decoder := gob.NewDecoder(reader)

	//var a int
	//decoder.Decode(&a)
	//var s string
	//decoder.Decode(&s)
	//decoder.Decode(nil)
	//decoder.Decode(nil)
	//decoder.Decode(nil)
	//decoder.Decode(nil)

	//var data1 []struct {
	//Name string
	//}
	//decoder.Decode(&data1)

	//t.Error(a)
	//t.Error(s)
	//t.Errorf("%+v\n", data1)
}
