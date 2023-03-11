package codec

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"io"

	"github.com/ndsky1003/event/msg"
)

type gobCodeC struct {
	conn io.ReadWriteCloser
	//codec
	encbuf *bufio.Writer
	enc    *gob.Encoder
	dec    *gob.Decoder
}

// 只支持网络连接，buffer会出问题
func NewGobCodec(conn io.ReadWriteCloser) *gobCodeC {
	buf := bufio.NewWriter(conn)
	return &gobCodeC{
		conn:   conn,
		encbuf: buf,
		enc:    gob.NewEncoder(buf),
		dec:    gob.NewDecoder(conn),
	}
}

func (this *gobCodeC) Read(b *msg.Msg) error {
	return this.dec.Decode(b)
}

func (this *gobCodeC) Write(m *msg.Msg) (err error) {
	err = this.enc.Encode(m)
	if err != nil {
		err = fmt.Errorf("msg type:%v,eventType:%s,err:%w", m.T, m.EventType, err)
		return
	}
	return this.encbuf.Flush()
}

func (this *gobCodeC) Close() error {
	if this == nil {
		return nil
	}
	return this.conn.Close()
}
