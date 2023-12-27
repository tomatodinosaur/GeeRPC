package main

import (
	geerpc "GeeRPC"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// 协程负责监听、处理信息
func startServer(addr chan string) {
	l, _ := net.Listen("tcp", ":0")
	log.Println("start rpc server on", l.Addr())
	addr <- l.Addr().String()
	geerpc.Accept(l)
}

func main() {
	log.SetFlags(0)
	addr := make(chan string)
	go startServer(addr)

	//主进程负责连接，传递信息
	client, _ := geerpc.Dial("tcp", <-addr)
	defer func() { client.Close() }()

	time.Sleep(time.Second)

	//send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf("geerpc req %d", i)
			var reply string
			if err := client.Call("Foo.sum", args, &reply); err != nil {
				log.Fatal("call Foo.sum error:", err)
			}
			log.Println("reply:", reply)
		}(i)
	}
	wg.Wait()

}
