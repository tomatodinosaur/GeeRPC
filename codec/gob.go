package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

// Gob类型
type GobCodec struct {

	//由构造函数传入，通常是通过TCP或者Uinx
	//建立Socket时得到的链接实例
	conn io.ReadWriteCloser

	//防止阻塞而创建的带缓冲的Writer，提升性能
	buf *bufio.Writer

	//Decoder
	dec *gob.Decoder

	//Encoder
	enc *gob.Encoder
}

//var _ Codec = (*GobCodec)(nil)

func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(conn),
	}
}

func (c *GobCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}
func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}
func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		c.buf.Flush()
		if err != nil {
			c.Close()
		}
	}()
	if err = c.enc.Encode(h); err != nil {
		log.Println("rpc codec :gob error encoding header:", err)
		return err
	}
	if err = c.enc.Encode(body); err != nil {
		log.Println("rpc codec :gob error encoding body:", err)
		return err
	}
	return nil
}
func (c *GobCodec) Close() error {
	return c.conn.Close()
}
