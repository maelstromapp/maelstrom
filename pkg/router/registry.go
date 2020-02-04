package router

import (
	"github.com/coopernurse/maelstrom/pkg/revproxy"
	"sync"
	"time"
)

func NewRegistry(nodeId string, startCompFunc StartComponentFunc) *Registry {
	return &Registry{
		nodeId:        nodeId,
		startCompFunc: startCompFunc,
		byCompName:    make(map[string]*Router),
		bufferPool:    revproxy.NewProxyBufferPool(),
		lock:          &sync.Mutex{},
	}
}

type Registry struct {
	nodeId        string
	startCompFunc StartComponentFunc
	byCompName    map[string]*Router
	bufferPool    *revproxy.ProxyBufferPool
	lock          *sync.Mutex
}

func (r *Registry) ByComponent(componentName string) (router *Router) {
	r.lock.Lock()
	router = r.byCompName[componentName]
	if router == nil {
		router = NewRouter(componentName, r.nodeId, r.bufferPool, r.startCompFunc)
		r.byCompName[componentName] = router
	}
	r.lock.Unlock()
	return
}

func (r *Registry) WaitForInflightToDrain() {
	for {
		r.lock.Lock()
		byName := r.byCompName
		r.lock.Unlock()

		drained := true
		for _, router := range byName {
			if router.GetInflightReqs() > 0 {
				drained = false
				break
			}
		}
		if drained {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
