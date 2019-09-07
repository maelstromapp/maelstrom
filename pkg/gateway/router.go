package gateway

import (
	"context"
	log "github.com/mgutz/logxi/v1"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type MaelRequest struct {
	complete  chan bool
	startTime time.Time
	rw        http.ResponseWriter
	req       *http.Request
}

type componentRing struct {
	idx      int
	handlers []http.Handler
	lock     *sync.Mutex
}

func (c *componentRing) next() (handler http.Handler) {
	if len(c.handlers) == 0 {
		return nil
	}
	return c.handlers[c.nextIdx()]
}

func (c *componentRing) nextIdx() int {
	c.lock.Lock()
	if c.idx >= len(c.handlers) {
		c.idx = 0
	}
	i := c.idx
	c.idx++
	c.lock.Unlock()
	return i
}

func NewRouter(nodeService v1.NodeService, handlerFactory *DockerHandlerFactory, myNodeId string, ctx context.Context) *Router {
	return &Router{
		nodeService:        nodeService,
		handlerFactory:     handlerFactory,
		myNodeId:           myNodeId,
		ringByComponent:    make(map[string]*componentRing),
		waitersByComponent: make(map[string][]chan http.Handler),
		wg:                 &sync.WaitGroup{},
		ctx:                ctx,
		lock:               &sync.Mutex{},
	}
}

type Router struct {
	nodeService        v1.NodeService
	handlerFactory     *DockerHandlerFactory
	myNodeId           string
	ringByComponent    map[string]*componentRing
	waitersByComponent map[string][]chan http.Handler
	wg                 *sync.WaitGroup
	ctx                context.Context
	lock               *sync.Mutex
}

func (r *Router) SetNodeService(svc v1.NodeService, myNodeId string) {
	r.nodeService = svc
	r.myNodeId = myNodeId
}

func (r *Router) GetNodeService() v1.NodeService {
	return r.nodeService
}

func (r *Router) OnClusterUpdated(nodes map[string]v1.NodeStatus) {
	newRing := map[string]*componentRing{}
	for _, node := range nodes {
		for _, rc := range node.RunningComponents {
			var h http.Handler
			if node.NodeId == r.myNodeId {
				reqCh := r.handlerFactory.ReqChanByComponent(rc.ComponentName)
				h = http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
					mr := &MaelRequest{
						complete:  make(chan bool, 1),
						startTime: time.Now(),
						rw:        rw,
						req:       req,
					}
					reqCh <- mr
					<-mr.complete
				})
			} else {
				target, err := url.Parse(node.PeerUrl)
				if err == nil {
					proxy := httputil.NewSingleHostReverseProxy(target)
					compName := rc.ComponentName
					h = http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
						relayPath := req.Header.Get("MAELSTROM-RELAY-PATH")
						if relayPath == "" {
							relayPath = r.myNodeId
						} else {
							relayPath = relayPath + "|" + r.myNodeId
						}
						req.Header.Set("MAELSTROM-COMPONENT", compName)
						req.Header.Set("MAELSTROM-RELAY-PATH", relayPath)
						proxy.ServeHTTP(rw, req)
					})
				} else {
					log.Error("router: cannot create peer url", "err", err, "url", node.PeerUrl,
						"peerNodeId", node.NodeId)
				}
			}
			if h != nil {
				ring := newRing[rc.ComponentName]
				if ring == nil {
					newRing[rc.ComponentName] = &componentRing{
						idx:      0,
						handlers: []http.Handler{h},
						lock:     &sync.Mutex{},
					}
				} else {
					ring.handlers = append(ring.handlers, h)
				}
			}
		}
	}

	r.lock.Lock()
	r.ringByComponent = newRing
	for compName, ring := range newRing {
		waiters := r.waitersByComponent[compName]
		if waiters != nil {
			for _, w := range waiters {
				w <- ring.next()
			}
			delete(r.waitersByComponent, compName)
		}
	}
	r.lock.Unlock()
}

func (r *Router) getHandlerOrActivate(comp *v1.Component) (http.Handler, chan http.Handler) {
	r.lock.Lock()
	defer r.lock.Unlock()

	// TODO: need to route local requests to local handler

	ring := r.ringByComponent[comp.Name]
	if ring == nil {
		waitCh := make(chan http.Handler, 1)
		waiters := r.waitersByComponent[comp.Name]
		if waiters == nil {
			r.waitersByComponent[comp.Name] = []chan http.Handler{waitCh}
			_, err := r.nodeService.PlaceComponent(v1.PlaceComponentInput{ComponentName: comp.Name})
			if err != nil {
				log.Error("router: PlaceComponent error", "err", err, "component", comp.Name)
			}
		} else {
			r.waitersByComponent[comp.Name] = append(waiters, waitCh)
		}
		return nil, waitCh
	} else {
		return ring.next(), nil
	}
}

func (r *Router) Route(rw http.ResponseWriter, req *http.Request, c v1.Component) {
	//maxDur := c.MaxDurationSeconds
	//if maxDur <= 0 {
	//	maxDur = 300
	//}
	//startTime := time.Now()
	//deadline := startTime.Add(time.Duration(maxDur) * time.Second)
	//ctx, _ := context.WithDeadline(r.ctx, deadline)

	handler, waitCh := r.getHandlerOrActivate(&c)
	if waitCh != nil {
		// TODO: add select w/deadline timer - blow up if we don't get a handler back in time
		handler = <-waitCh
	}
	if handler != nil {
		// TODO: set timeout context
		// see: https://stackoverflow.com/questions/16895294/how-to-set-timeout-for-http-get-requests-in-golang
		handler.ServeHTTP(rw, req)
	}
}
