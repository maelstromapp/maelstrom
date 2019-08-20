package gateway

import (
	"fmt"
	log "github.com/mgutz/logxi/v1"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sync"
)

func NewRouter(nodeId string, localFactory HandlerFactory, nodeService v1.NodeService) *Router {
	return &Router{
		nodeId:          nodeId,
		localFactory:    localFactory,
		nodeService:     nodeService,
		ringByComponent: map[string]*componentHandlerRing{},
		nodes:           map[string]v1.NodeStatus{},
		lock:            &sync.Mutex{},
	}
}

type Router struct {
	nodeId          string
	localFactory    HandlerFactory
	ringByComponent map[string]*componentHandlerRing
	nodes           map[string]v1.NodeStatus
	nodeService     v1.NodeService
	lock            *sync.Mutex
}

func (r *Router) GetHandlerAndRegisterRequest(c v1.Component) (Handler, error) {
	r.lock.Lock()
	ring, ok := r.ringByComponent[c.Name]
	r.lock.Unlock()

	if ok {
		return ring.Next().GetHandlerAndRegisterRequest(c)
	} else {
		// component not in route table
		output, err := r.nodeService.PlaceComponent(v1.PlaceComponentInput{ComponentName: c.Name})
		if err != nil {
			return nil, fmt.Errorf("router: unable to start component: %s - %v", c.Name, err)
		}
		r.lock.Lock()
		r.nodes[output.Node.NodeId] = output.Node
		r.ringByComponent = toRingMap(r.nodes, r.nodeId, r.localFactory)
		ring, ok = r.ringByComponent[c.Name]
		r.lock.Unlock()
		if ok {
			return ring.Next().GetHandlerAndRegisterRequest(c)
		} else {
			return nil, fmt.Errorf("router: component not in ring after starting: %s", c.Name)
		}
	}
}

func (r *Router) OnClusterUpdated(nodes map[string]v1.NodeStatus) {
	r.lock.Lock()
	r.nodes = nodes
	r.ringByComponent = toRingMap(nodes, r.nodeId, r.localFactory)
	r.lock.Unlock()
}

//////////////////////////

func toRingMap(nodes map[string]v1.NodeStatus, myNodeId string,
	localFactory HandlerFactory) map[string]*componentHandlerRing {
	byComponent := map[string]*componentHandlerRing{}
	for _, node := range nodes {
		for _, comp := range node.RunningComponents {
			var factory HandlerFactory = nil
			if node.NodeId == myNodeId {
				factory = localFactory
			} else {
				remoteFactory, err := newRemoteHandlerFactory(comp.ComponentName, node.PeerUrl)
				if err == nil {
					factory = remoteFactory
				} else {
					log.Error("router: error creating remote factory", "nodeId", node.NodeId, "peerUrl", node.PeerUrl,
						"err", err)
				}
			}

			if factory != nil {
				ring, ok := byComponent[comp.ComponentName]
				if !ok {
					ring = newComponentHandlerRing()
					byComponent[comp.ComponentName] = ring
				}
				ring.factories = append(ring.factories, factory)
			}
		}
	}
	return byComponent
}

func newComponentHandlerRing() *componentHandlerRing {
	return &componentHandlerRing{
		factories: make([]HandlerFactory, 0),
		offset:    0,
		lock:      &sync.Mutex{},
	}
}

type componentHandlerRing struct {
	factories []HandlerFactory
	offset    int
	lock      *sync.Mutex
}

func (r *componentHandlerRing) Next() HandlerFactory {
	r.lock.Lock()
	handler := r.factories[r.offset]
	r.offset++
	if r.offset >= len(r.factories) {
		r.offset = 0
	}
	r.lock.Unlock()
	return handler
}
