package component

import "sync"

func NewProxyBufferPool() *ProxyBufferPool {
	pool := &sync.Pool{
		New: func() interface{} {
			return make([]byte, 32*1024)
		},
	}
	return &ProxyBufferPool{pool: pool}
}

type ProxyBufferPool struct {
	pool *sync.Pool
}

func (p *ProxyBufferPool) Get() []byte {
	b, ok := p.pool.Get().([]byte)
	if !ok {
		panic("pool didn't not return a []byte")
	}
	return b
}

func (p *ProxyBufferPool) Put(b []byte) {
	p.pool.Put(b)
}
