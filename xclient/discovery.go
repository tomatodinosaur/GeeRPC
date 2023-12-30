package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

// 负载均衡策略
type SelectMode int

const (
	RandomSelect SelectMode = iota
	RoundRobinSelect
)

// 服务发现接口
type Discovery interface {
	//从注册中心更新服务列表
	Refresh() error

	//手动更新服务列表
	Update(severs []string) error

	//根据负载均衡策略，选择一个服务实例
	Get(mode SelectMode) (string, error)

	//返回所有的服务实例
	GetAll() ([]string, error)
}

// NoRegister
type MultiServerDiscovery struct {
	r       *rand.Rand
	mu      sync.RWMutex
	servers []string
	index   int
}

func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
	d := &MultiServerDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

// 从注册中心更新服务列表
func (d *MultiServerDiscovery) Refresh() error {
	return nil
}

// 手动更新服务列表
func (d *MultiServerDiscovery) Update(severs []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = severs
	return nil
}

// 根据负载均衡策略，选择一个服务实例
func (d *MultiServerDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}

	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index]
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}

}

// 返回所有的服务实例
func (d *MultiServerDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	servers := make([]string, len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
