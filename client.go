package geerpc

import (
	"GeeRPC/codec"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
)

//net/rpc 可以被远程调用的函数模板：
//func (t *T) MethodName(argType T1, replyType *T2) error

type Call struct {
	Seq           uint64
	ServiceMethod string      //format "<service>.<method>"
	Args          interface{} //arguments to the function
	Reply         interface{} //reply from the function
	Error         error       //if error occurs,it will be set
	Done          chan *Call  //Strobes when call is complete
}

// 为了支持异步调用，当调用结束后，调用done()通知调用方。
func (call *Call) done() {
	call.Done <- call
}

//Client represents an RPC Client.
//There may be multiple outstanding Calls associated
//with a single Client,and a Client may be used by
//multiple goroutines simulaneously
//Client代表一个RPC客户端。
//单个Client可能有多个未完成的Recall，
//并且一个Client可能被多个goroutines同时使用

type Client struct {
	//消息的编解码器，和服务端类似，
	//序列化要发出去的请求，反序列化接收到的响应
	cc codec.Codec

	//配置字段
	opt *Option

	//和服务端类似，保证请求的有序发送，防止多个请求报文混淆
	sending sync.Mutex

	//消息头，只有在请求发送时才需要，而请求发送时互斥的，
	//因此每个客户端只需一个，可以复用
	header codec.Header

	//保护client的各属性
	mu sync.Mutex

	//每个请求的唯一编号
	seq uint64

	//存储注册但是没有发送的请求<seq,Call>
	pending map[uint64]*Call

	//closing和shutdown 任意一个值为true,则表示client不可用

	//主动关闭即调用Close()
	closing bool //user has called close
	//有错误关闭
	shutdown bool //user has told us to stop
}

// var _ io.Closer = (*Client)(nil)
var ErrShutdown = errors.New("connection is shut down")

// Close the connection
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// IsAvailable return true if the client does work
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 注册:将参数 call 添加到 client.pending 中，并更新 client.seq。
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	//fmt.Println(len(client.pending))
	client.seq++
	return call.Seq, nil
}

// 移除:根据 seq，从 client.pending 中移除对应的 call，并返回
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 终止:服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call。
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

//对一个客户端端来说，接收响应、发送请求是最重要的 2 个功能。

/*
接受响应：即接受call的结果

三种情况：

call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了。
call 存在，但服务端处理出错，即 h.Error 不为空。
call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值。
*/
func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.pending[h.Seq]
		switch {
		case call == nil:
			// it usually means that Write partially failed
			// and call was already removed.
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err)
}

/*
创建 Client 实例时，首先需要完成一开始的协议交换，即发送 Option 信息给服务端。
协商好消息的编解码方式之后，再创建一个子协程调用 receive() 接收响应
*/

// conn由Dial根据服务器的ip地址创建，通过json.NewEncoder发送Option协商格式
// 开启接收响应协程
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodeFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	// send options with sever
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

// Dial 函数，便于用户传入服务端地址，创建 Client 实例。
// 为了简化用户调用，通过 ...*Option 将 Option 实现为可选参数。
func parseOptions(opts ...*Option) (*Option, error) {
	// if opts is nil or pass nil as parameter
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// Dial connects to an RPC server at the specified network address
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	//创建到服务器的连接
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}

	defer func() {
		if client == nil {
			conn.Close()
		}
	}()

	return NewClient(conn, opt)
}

// 发送请求
func (client *Client) send(call *Call) {
	//按序发送，完整发送
	client.sending.Lock()
	defer client.sending.Unlock()

	//register this call
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	//prepare request header
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	//encode and send the request
	//利用gob.Write发送,发送失败移除Call
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		// call may be nil, it usually means that Write partially failed,
		// client has received the response and handled
		if call != nil {
			call.Error = err
			call.done()
		}
	}

}

// Go invokes the function asynchronously.
// It returns the Call structure representing the invocation.
// 根据参数生成Call
// 发送Call

func (client *Client) Go(ServiceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}

	call := &Call{
		ServiceMethod: ServiceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}

	client.send(call)
	return call
}

// Call invokes the named function, waits for it to complete,
// and returns its error status.
func (client *Client) Call(ServiceMethod string, args, reply interface{}) error {
	//创建call,注册call,移除call，等待发送call完成
	call := <-client.Go(ServiceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

/*
Go 和 Call 是客户端暴露给用户的两个 RPC 服务调用接口:
Go 是一个异步接口，返回 call 实例。
Call 是对 Go 的封装，阻塞 call.Done，等待响应返回，是一个同步接口。

异步example:
						call := client.Go( ... )
						# 新启动协程，异步等待
						go func(call *Call) {
							select {
								<-call.Done:
									# do something
								<-otherChan:
									# do something
							}
						}(call)

						otherFunc() # 不阻塞，继续执行其他函数。
*/
