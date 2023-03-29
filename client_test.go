package event

import (
	"fmt"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	fmt.Println("start")
	go NewServer().Listen("127.0.0.1:8080")
	time.Sleep(1e8)
	c := Dial("127.0.0.1:8080")
	c.On("ppxia", func(name string) {
		fmt.Println("receive:", name)
	})
	time.Sleep(1e8)

	fmt.Println("start end")

	m.Run()
	fmt.Println("end")
}

func BenchmarkEmit(b *testing.B) {
	c := Dial("127.0.0.1:8080")
	time.Sleep(1e8)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Emit("ppxia", "lppp")
	}
}
