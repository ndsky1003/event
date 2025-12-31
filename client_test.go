package event

import (
	"fmt"
	"testing"
	"time"

	"github.com/ndsky1003/event/v2/eventname"
)

func TestMain(m *testing.M) {
	fmt.Println("start")
	go NewServer().Listen("127.0.0.1:8080")
	time.Sleep(1e8)
	c := Dial("127.0.0.1:8080", ClientOptions().SetName("test-client"))
	c.On("ppxia", func(name string) error {
		fmt.Println("receive:", name)
		return nil
	})
	time.Sleep(1e8)

	fmt.Println("start end")

	m.Run()
	fmt.Println("end")
}

func BenchmarkEmit(b *testing.B) {
	c := Dial("127.0.0.1:8080", ClientOptions().SetName("bench-client"))
	time.Sleep(1e8)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
			_ = c.EmitOne(eventname.T("ppxia"), "lppp")
	}
}
