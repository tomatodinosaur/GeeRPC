package codec

import "io"

//消息的编解码
//消息的序列化与反序列化

/*
一个典型的RPC调用：
err = client.Call("Arith.Muliply",arg,&reply)

客户端请求
	服务名：Arith
	方法名：Multiply
	参数：args

服务端响应：
	error
	返回值：reply
*/

type Header struct {
	ServiceMethod string //服务名和方法名
	Seq           uint64 //请求的序号
	Error         string
}

// 对消息体进行编解码的接口Codec
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

// Codec的构造函数
/*
	根据Codec的Type得到构造函数，从而创造Codec实例
*/
type NewCodeFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json" //not implemented
)

var NewCodeFuncMap map[Type]NewCodeFunc

func init() {
	NewCodeFuncMap = make(map[Type]NewCodeFunc)
	NewCodeFuncMap[GobType] = NewGobCodec
}
