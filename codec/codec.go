package codec

import (
	"io"
)

// 解码器
type Codec interface {
	Read(any) error
	Write(any) error
	Close() error
}

type CreateCodecFunc func(conn io.ReadWriteCloser) (Codec, error)
