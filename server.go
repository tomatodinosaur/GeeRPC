package geerpc

import (
	"GeeRPC/codec"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
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
type Server struct {
	serviceMap sync.Map //服务池
}

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
	h            *codec.Header //header :header of request:   <service.Method,seq,Error>
	argv, replyv reflect.Value //body :argv and replyv of request
	svc          *service      //服务
	mtype        *methodType   //方法
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

/*
通过 newArgv() 和 newReplyv() 两个方法创建出两个入参实例，
然后通过 cc.ReadBody() 将请求报文反序列化为第一个入参 argv，
在这里同样需要注意 argv 可能是值类型，也可能是指针类型，
所以处理方式有点差异。
*/

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	//make sure that argvi is a pointer,
	//ReadBody need a pointer as parameter
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}

	//将请求报文中的Body 反序列化为 argv
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read body err:", err)
		return req, err
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
	defer wg.Done()
	err := req.svc.call(req.mtype, req.argv, req.replyv)
	if err != nil {
		req.h.Error = err.Error()
		server.sendResponse(cc, req.h, invalidRequest, sending)
		return
	}
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)

}

// 在Server端注册服务，加入Service池
func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	//LoadOrStore 返回键的现有值（如果存在）。 否则，它存储并返回给定值。
	//如果值已加载，则加载结果为 true；如果已存储，则加载结果为 false。
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

// Register publishes the receiver's methods in the DefaultServer.
func Register(rcvr interface{}) error { return DefaultSever.Register(rcvr) }

/*
因为 ServiceMethod 的构成是 “Service.Method”，
因此先将其分割成 2 部分，
第一部分是 Service 的名称，第二部分即方法名。
现在 serviceMap 中找到对应的 service 实例，
再从 service 实例的 method 中，找到对应的 methodType。

*/

// 发现服务：
// 1、在Server的服务池中查询服务
// 2、在Service的方法池中查询方法
func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}

	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}
