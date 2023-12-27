package geerpc

import (
	"GeeRPC/codec"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber int        //MagicNumber marks this's a geerpc request
	CodecType   codec.Type //client may choose different codec to encode body
}

var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

/*
GeeRPC 客户端固定采用 JSON 编码 Option，
后续的 header 和 body 的编码方式由 Option 中的 CodeType 指定，
服务端首先使用 JSON 解码 Option，
然后通过 Option 的 CodeType 解码剩余的内容。
即报文将以这样的形式发送：
| Option{MagicNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |
| <------      固定 JSON 编码      ------>  | <-------   编码方式由 CodeType 决定   ------->|

在一次连接中，Option 固定在报文的最开始，Header 和 Body 可以有多个，即报文可能是这样的。
| Option | Header1 | Body1 | Header2 | Body2 | ...
*/

// sever represents an RPC Server
type Server struct{}

// NewServer returns a new Server
func NewSever() *Server {
	return &Server{}
}

var DefaultSever = NewSever()

// Accept 接受侦听器上的连接并为每个传入连接提供请求。
func (sever *Server) Accept(lis net.Listener) {
	//for 循环等待 socket 连接建立，并开启子协程处理，
	//处理过程交给了 ServerConn 方法。
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc sever:accept error:", err)
			return
		}
		go sever.ServeConn(conn)
	}
}

func Accept(lis net.Listener) { DefaultSever.Accept(lis) }

/*
	example :启动服务
	lis, _ := net.Listen("tcp", ":9999")
	geerpc.Accept(lis)
*/

// ServeConn runs the sever on a single connection.
// ServeConn blocks,serving the connection until th client hangs up.
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		conn.Close()
	}()
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server:options error:", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Println("rpc server:invalid magic number :", opt.MagicNumber)
		return
	}
	f := codec.NewCodeFuncMap[opt.CodecType]
	if f == nil {
		log.Println("rpc server:invalid codec type :", opt.CodecType)
		return
	}
	server.severCodec(f(conn))
}

// invalidRequest是发生错误时响应argv的占位符
var invalidRequest = struct{}{}

/*
主要包含三个阶段

读取请求 readRequest
处理请求 handleRequest
回复请求 sendResponse

handleRequest 使用了协程并发执行请求。
处理请求是并发的，但是回复请求的报文必须是逐个发送的，
并发容易导致多个回复报文交织在一起，客户端无法解析。
			(在这里使用锁(sending)保证。)
尽力而为，只有在 header 解析失败时，才终止循环。
*/

func (server *Server) severCodec(cc codec.Codec) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait()
	cc.Close()
}

// request stores all information of a call
type request struct {
	h            *codec.Header //header :header of request
	argv, replyv reflect.Value //body :argv and replyv of request
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	// TODO: now we don't know the type of request argv
	// day 1, just suppose it's string
	req.argv = reflect.New(reflect.TypeOf(""))
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}

}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// TODO, should call registered rpc methods to get the right replyv
	// day 1, just print argv and send a hello message
	defer wg.Done()
	log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq))
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)

}

/*
目前还不能判断 body 的类型，
因此在 readRequest 和 handleRequest 中，
day1 将 body 作为字符串处理。
接收到请求，打印 header，
并回复 geerpc resp ${req.h.Seq}。这一部分后续再实现。
*/
