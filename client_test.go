package event

import (
	"fmt"
	"testing"
	"time"

	"github.com/ndsky1003/event/v3/eventname"
)

func TestMain(m *testing.M) {
	fmt.Println("start")
	go NewServer().Listen("127.0.0.1:8080")
	time.Sleep(100 * time.Millisecond)
	c := Dial("127.0.0.1:8080", ClientOptions().SetName("testmain").SetListenAddr("127.0.0.1:18081"))
	if err := c.On("ppxia", func(name string) error {
		fmt.Println("receive:", name)
		return nil
	}); err != nil {
		fmt.Println("On err:", err)
	}
	time.Sleep(100 * time.Millisecond)

	fmt.Println("start end")

	m.Run()
	fmt.Println("end")
}

func BenchmarkEmit(b *testing.B) {
	c := Dial("127.0.0.1:8080", ClientOptions().SetName("bench").SetListenAddr("127.0.0.1:18082"))
	time.Sleep(100 * time.Millisecond)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Emit(eventname.T("ppxia"), "lppp")
	}
}
