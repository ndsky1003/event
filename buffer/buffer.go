package buffer

import (
	"github.com/ndsky1003/buffer/v3"
)

var pool = buffer.NewBytePool(
	buffer.Options().
		SetMinSize(512).          // 最小 512B
		SetMaxSize(64 << 21).     // 最大 128MB
		SetMaxPercent(1.5).       // 推荐：1.5 倍冗余 (比之前的 2.0 更激进一点，利于回收)
		SetCalibratePeriod(1000), // 每 1000 次调用校准一次
)

func Release(b []byte) {
	clear(b)
	pool.Put(b)
}

func Get() []byte {
	return pool.Get()
}
