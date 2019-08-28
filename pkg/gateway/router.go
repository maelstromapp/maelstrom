package gateway

import (
	"context"
	log "github.com/mgutz/logxi/v1"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"sync"
	"time"
)

type MaelRequest struct {
	ready chan bool
	rw    http.ResponseWriter
	req   *http.Request
	comp  *v1.Component
	ctx   context.Context
}

type resolveChannels struct {
	componentName string
	reply         chan *componentChannels
}

func NewRouter(nodeService v1.NodeService, myNodeId string, ctx context.Context) *Router {
	return &Router{
		nodeService:         nodeService,
		myNodeId:            myNodeId,
		componentToReqChans: make(map[string]*componentChannels),
		requestCh:           make(chan resolveChannels),
		wg:                  &sync.WaitGroup{},
		ctx:                 ctx,
	}
}

type Router struct {
	nodeService         v1.NodeService
	myNodeId            string
	componentToReqChans map[string]*componentChannels
	requestCh           chan resolveChannels
	wg                  *sync.WaitGroup
	ctx                 context.Context
}

func (r *Router) SetNodeService(svc v1.NodeService, myNodeId string) {
	r.nodeService = svc
	r.myNodeId = myNodeId
}

func (r *Router) GetNodeService() v1.NodeService {
	return r.nodeService
}

func (r *Router) Route(rw http.ResponseWriter, req *http.Request, c v1.Component) {
	maxDur := c.MaxDurationSeconds
	if maxDur <= 0 {
		maxDur = 300
	}
	ctx, _ := context.WithDeadline(r.ctx, time.Now().Add(time.Duration(maxDur)*time.Second))

	mr := &MaelRequest{
		ready: make(chan bool, 1),
		rw:    rw,
		req:   req,
		comp:  &c,
		ctx:   ctx,
	}

	go r.getComponentChannels(c.Name).send(mr)

	select {
	case <-ctx.Done():
		respondText(rw, http.StatusGatewayTimeout, "Timeout handling component: "+c.Name)
		return
	case <-mr.ready:
		return
	}
}

func (r *Router) Run(daemonWG *sync.WaitGroup) {
	defer daemonWG.Done()
	cleanupTicker := time.Tick(time.Minute * 5)
	for {
		select {
		case chanReq := <-r.requestCh:
			chanReq.reply <- r.componentRequestChannels(chanReq.componentName)
		case <-cleanupTicker:
			r.removeStaleComponents()
		case <-r.ctx.Done():
			log.Info("router: shutdown gracefully")
			return
		}
	}
}

func (r *Router) getComponentChannels(componentName string) *componentChannels {
	chanReq := resolveChannels{componentName: componentName, reply: make(chan *componentChannels, 1)}
	r.requestCh <- chanReq
	return <-chanReq.reply
}

func (r *Router) componentRequestChannels(componentName string) *componentChannels {
	reqCh, ok := r.componentToReqChans[componentName]
	if !ok {
		reqCh = newComponentChannels(r.GetNodeService)
		log.Info("router: newComponentChannels", "component", componentName)
		r.componentToReqChans[componentName] = reqCh
	}
	return reqCh
}

func (r *Router) removeStaleComponents() {
	keep := map[string]*componentChannels{}
	minTime := time.Now().Add(time.Minute)
	for componentName, reqChans := range r.componentToReqChans {
		if reqChans.getLastConsumerHeartbeat().Before(minTime) {
			log.Info("router: destroying idle componentChannels", "component", componentName)
			reqChans.destroy()
		} else {
			keep[componentName] = reqChans
		}
	}
	r.componentToReqChans = keep
}

//func (r *Router) updateRoutes(node v1.NodeStatus) {
//	peerUrl, err := url.Parse(node.PeerUrl)
//	if err != nil {
//		log.Error("router: updateRoutes parse url failed", "peerNodeId", node.NodeId, "peerUrl", node.PeerUrl,
//			"err", err)
//		return
//	}
//	proxy := httputil.NewSingleHostReverseProxy(peerUrl)
//
//	targetProxiesByComponent := map[string]int{}
//	for _, c := range node.RunningComponents {
//		target := targetProxiesByComponent[c.ComponentName]
//		targetProxiesByComponent[c.ComponentName] = target + int(c.MaxConcurrency)
//	}
//
//	for component, target := range targetProxiesByComponent {
//		reqChans := r.getReqChannels(component)
//		proxies := reqChans.nodeIdToProxyChancelFuncs[node.NodeId]
//		if proxies == nil {
//			proxies = make([]context.CancelFunc, 0)
//		}
//
//		delta := target - len(proxies)
//		if delta > 0 {
//			for i := 0; i < delta; i++ {
//				ctx, cancelFx := context.WithCancel(r.ctx)
//				go revProxyLoop(reqChans.allCh, proxy, ctx, r.wg, r.myNodeId, component)
//				proxies = append(proxies, cancelFx)
//			}
//		} else if delta < 0 {
//			delta = delta * -1
//			for i := 0; i < delta; i++ {
//				proxies[i]()
//			}
//			proxies = proxies[delta:]
//		}
//		reqChans.nodeIdToProxyChancelFuncs[node.NodeId] = proxies
//	}
//
//	for componentName, reqChan := range r.componentToReqChans {
//		_, ok := targetProxiesByComponent[componentName]
//		if !ok {
//			cancelFuncs := reqChan.nodeIdToProxyChancelFuncs[node.NodeId]
//			if len(cancelFuncs) > 0 {
//				for _, cancelFx := range cancelFuncs {
//					cancelFx()
//				}
//				reqChan.nodeIdToProxyChancelFuncs[node.NodeId] = []context.CancelFunc{}
//			}
//		}
//	}
//}
