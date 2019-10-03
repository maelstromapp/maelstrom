package maelstrom

import (
	"context"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
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
	idx          int
	localHandler http.Handler
	handlers     []http.Handler
	lock         *sync.Mutex
}

func (c *componentRing) next(preferLocal bool) (handler http.Handler) {
	if len(c.handlers) == 0 {
		return nil
	} else if preferLocal && c.localHandler != nil {
		return c.localHandler
	} else {
		return c.handlers[c.nextIdx()]
	}
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

func NewRouter(nodeService v1.NodeService, handlerFactory *DockerHandlerFactory, myNodeId string, myIpAddr string,
	ctx context.Context) *Router {
	return &Router{
		nodeService:              nodeService,
		handlerFactory:           handlerFactory,
		myNodeId:                 myNodeId,
		myIpAddr:                 myIpAddr,
		ringByComponent:          make(map[string]*componentRing),
		waitersByComponent:       make(map[string][]chan http.Handler),
		placementTimeByComponent: make(map[string]time.Time),
		placementInterval:        time.Second * 30,
		wg:                       &sync.WaitGroup{},
		ctx:                      ctx,
		lock:                     &sync.Mutex{},
	}
}

type Router struct {
	nodeService              v1.NodeService
	handlerFactory           *DockerHandlerFactory
	myNodeId                 string
	myIpAddr                 string
	ringByComponent          map[string]*componentRing
	waitersByComponent       map[string][]chan http.Handler
	placementTimeByComponent map[string]time.Time
	placementInterval        time.Duration
	wg                       *sync.WaitGroup
	ctx                      context.Context
	lock                     *sync.Mutex
}

func (r *Router) SetNodeService(svc v1.NodeService, myNodeId string) {
	r.nodeService = svc
	r.myNodeId = myNodeId
}

func (r *Router) GetNodeService() v1.NodeService {
	return r.nodeService
}

func (r *Router) InstanceCountForComponent(componentName string) int {
	r.lock.Lock()
	ring, ok := r.ringByComponent[componentName]
	r.lock.Unlock()
	if ok {
		return len(ring.handlers)
	}
	return 0
}

func (r *Router) OnClusterUpdated(nodes map[string]v1.NodeStatus) {
	newRing := map[string]*componentRing{}
	componentNames := make([]string, 0)
	containerCount := 0
	for _, node := range nodes {
		for _, rc := range node.RunningComponents {
			var h http.Handler
			var localHandler http.Handler
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
				localHandler = h
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
				containerCount++
				ring := newRing[rc.ComponentName]
				if ring == nil {
					newRing[rc.ComponentName] = &componentRing{
						idx:          0,
						localHandler: localHandler,
						handlers:     []http.Handler{h},
						lock:         &sync.Mutex{},
					}
					componentNames = append(componentNames, rc.ComponentName)
				} else {
					ring.handlers = append(ring.handlers, h)
					if localHandler != nil {
						ring.localHandler = localHandler
					}
				}
			}
		}
	}

	if log.IsDebug() {
		log.Debug("router: updated routes", "containers", containerCount, "components", componentNames,
			"node", r.myNodeId)
	}

	// shuffle ring handlers so that requests are distributed differently by each node
	for _, ring := range newRing {
		if len(ring.handlers) > 1 {
			rand.Shuffle(len(ring.handlers), func(i, j int) {
				ring.handlers[i], ring.handlers[j] = ring.handlers[j], ring.handlers[i]
			})
		}
	}

	r.lock.Lock()
	r.ringByComponent = newRing
	for compName, ring := range newRing {
		waiters := r.waitersByComponent[compName]
		if waiters != nil {
			for _, w := range waiters {
				w <- ring.next(false)
			}
			delete(r.waitersByComponent, compName)
			delete(r.placementTimeByComponent, compName)
		}
	}
	r.lock.Unlock()
}

// getHandlerOrActivate returns a handler from the routing ring for this component, or returns a channel
// if no handler is running yet for this component. The channel will be notified when component activation
// succeeds.
func (r *Router) getHandlerOrActivate(comp *v1.Component, preferLocal bool) (http.Handler, chan http.Handler) {
	r.lock.Lock()
	defer r.lock.Unlock()

	ring := r.ringByComponent[comp.Name]
	if ring == nil || len(ring.handlers) == 0 {
		// No handler available - return waitCh which will be notified with handler once activation finishes
		waitCh := make(chan http.Handler, 1)
		waiters := r.waitersByComponent[comp.Name]

		if waiters == nil {
			r.waitersByComponent[comp.Name] = []chan http.Handler{waitCh}
		} else {
			r.waitersByComponent[comp.Name] = append(waiters, waitCh)
		}

		// If placement interval as elapsed, ask placement node to activate component
		now := time.Now()
		if now.Sub(r.placementTimeByComponent[comp.Name]) > r.placementInterval {
			r.placementTimeByComponent[comp.Name] = now
			go func() {
				_, err := r.nodeService.PlaceComponent(v1.PlaceComponentInput{ComponentName: comp.Name})
				if err != nil {
					log.Error("router: PlaceComponent error", "err", err, "component", comp.Name)
				}
			}()
		}

		return nil, waitCh
	} else {
		return ring.next(preferLocal), nil
	}
}

func (r *Router) Route(rw http.ResponseWriter, req *http.Request, c v1.Component, publicGateway bool) {
	var deadlineNano int64
	if !publicGateway {
		deadlineStr := req.Header.Get("MAELSTROM-DEADLINE-NANO")
		if deadlineStr != "" {
			deadlineNano, _ = strconv.ParseInt(deadlineStr, 10, 64)
		}
	}

	deadline := componentReqDeadline(deadlineNano, c)
	ctx, _ := context.WithDeadline(context.Background(), deadline)

	preferLocal := req.Header.Get("MAELSTROM-RELAY-PATH") != ""

	// Get handler from routing ring for this component, or request 0->1 activation if no containers are
	// running for this component
	handler, waitCh := r.getHandlerOrActivate(&c, preferLocal)

	if waitCh != nil {
		// No containers are running. We're waiting for activation on waitCh
		select {
		case <-ctx.Done():
			msg := "router: Timeout getting handler for component: " + c.Name
			log.Warn(msg, "nodeId", r.myNodeId, "component", c.Name, "version", c.Version)
			respondText(rw, http.StatusGatewayTimeout, msg)
			return
		case handler = <-waitCh:
			// happy branch - container activated
		}
	}

	// round trip request to the handler (could be remote or local)
	if handler != nil {
		if req.Header.Get("MAELSTROM-DEADLINE-NANO") == "" {
			req.Header.Set("MAELSTROM-DEADLINE-NANO", strconv.FormatInt(deadline.UnixNano(), 10))
		}

		xForward := req.Header.Get("X-Forwarded-For")
		if xForward != "" {
			xForward += ", "
		}
		xForward += r.myIpAddr
		req.Header.Set("X-Forwarded-For", xForward)

		done := make(chan bool, 1)
		go func() {
			handler.ServeHTTP(rw, req)
			done <- true
		}()
		select {
		case <-done:
			return
		case <-ctx.Done():
			msg := "router: Timeout proxying component: " + c.Name
			log.Warn(msg, "nodeId", r.myNodeId, "component", c.Name, "version", c.Version)
			respondText(rw, http.StatusGatewayTimeout, msg)
			return
		}
	}
}

func componentReqDeadline(deadlineNanoFromHeader int64, component v1.Component) time.Time {
	if deadlineNanoFromHeader == 0 {
		maxDur := component.MaxDurationSeconds
		if maxDur <= 0 {
			maxDur = 300
		}
		startTime := time.Now()
		return startTime.Add(time.Duration(maxDur) * time.Second)
	} else {
		return time.Unix(0, deadlineNanoFromHeader)
	}
}
