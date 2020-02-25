package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/coopernurse/maelstrom/pkg/revproxy"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
)

type StartComponentFunc func(componentName string)

/////////////////////////////////

type State int

var stateLabels = []string{"off", "pending", "on"}

func (s State) String() string {
	if s < 0 || s >= State(len(stateLabels)) {
		return fmt.Sprintf("unknown state: %d", s)
	}
	return stateLabels[s]
}

const (
	StateOff State = iota
	StatePending
	StateOn
)

/////////////////////////////////

func NewRouter(componentName string, nodeId string, bufferPool *revproxy.ProxyBufferPool,
	startCompFunc StartComponentFunc) *Router {
	return &Router{
		componentName:       componentName,
		nodeId:              nodeId,
		startComponentFunc:  startCompFunc,
		state:               StateOff,
		remoteReqCh:         nil,
		localReqCh:          nil,
		inflightReqs:        0,
		bufferPool:          bufferPool,
		remoteHandlersByUrl: make(map[string][]context.CancelFunc),
		lock:                &sync.Mutex{},
	}
}

type Router struct {
	componentName      string
	nodeId             string
	startComponentFunc StartComponentFunc
	state              State
	remoteReqCh        chan *revproxy.GetProxyRequest
	localReqCh         chan *revproxy.GetProxyRequest
	inflightReqs       int64
	activeHandlers     int64
	bufferPool         *revproxy.ProxyBufferPool

	// key=peerUrl value=slice of cancel funcs (one per rev proxy goroutine)
	remoteHandlersByUrl map[string][]context.CancelFunc

	lock *sync.Mutex
}

func (r *Router) WithStartComponentFunc(f StartComponentFunc) *Router {
	r.startComponentFunc = f
	return r
}

func (r *Router) GetState() (state State) {
	r.lock.Lock()
	state = r.state
	r.lock.Unlock()
	return
}

func (r *Router) GetHandlerCount() (count int64) {
	r.lock.Lock()
	count = r.activeHandlers
	r.lock.Unlock()
	return
}

func (r *Router) GetInflightReqs() (count int64) {
	r.lock.Lock()
	count = r.inflightReqs
	r.lock.Unlock()
	return
}

func (r *Router) SetRemoteHandlerCounts(urlToHandlerCount map[string]int) {
	r.lock.Lock()
	defer r.lock.Unlock()

	for targetUrl, targetCount := range urlToHandlerCount {
		cancelFuncs := r.remoteHandlersByUrl[targetUrl]
		if cancelFuncs == nil {
			cancelFuncs = make([]context.CancelFunc, 0)
		}

		var cancelFunc context.CancelFunc
		change := false

		if log.IsDebug() {
			log.Debug("router: setRemoteNodes", "component", r.componentName, "targetUrl", targetUrl,
				"targetCount", targetCount, "oldCount", len(cancelFuncs))
		}

		u, err := url.Parse(targetUrl)
		if err == nil {
			for targetCount > len(cancelFuncs) {
				cancelFunc = r.startRemoteHandler(u, targetCount)
				cancelFuncs = append(cancelFuncs, cancelFunc)
				r.remoteHandlersByUrl[targetUrl] = cancelFuncs
				change = true
			}
		} else {
			log.Error("router: cannot create remote url", "err", err, "url", targetUrl)
		}

		for targetCount < len(cancelFuncs) {
			cancelFunc, cancelFuncs = cancelFuncs[0], cancelFuncs[1:]
			cancelFunc()
			r.remoteHandlersByUrl[targetUrl] = cancelFuncs
			change = true
		}

		if change {
			log.Info("router: updated remoteHandlersByUrl", "component", r.componentName,
				"handlerCount", urlToHandlerCount)
		}
	}
	for targetUrl, cancelFuncs := range r.remoteHandlersByUrl {
		_, ok := urlToHandlerCount[targetUrl]
		if !ok {
			for _, fx := range cancelFuncs {
				fx()
			}
			delete(r.remoteHandlersByUrl, targetUrl)
			log.Info("router: removed remoteHandlersByUrl", "component", r.componentName, "url", targetUrl)
		}
	}
}

func (r *Router) startRemoteHandler(targetUrl *url.URL, targetCount int) context.CancelFunc {
	proxy := httputil.NewSingleHostReverseProxy(targetUrl)
	proxy.BufferPool = r.bufferPool
	proxy.Transport = &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: targetCount,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	go func() {
		reqCh := r.HandlerStartRemote()
		defer r.HandlerStop()
		dispenser := revproxy.NewDispenser(targetCount, reqCh, r.nodeId,
			r.componentName, nil, proxy, nil, ctx)
		dispenser.Run(ctx)
	}()
	return cancelFunc
}

func (r *Router) HandlerStartRemote() (ch <-chan *revproxy.GetProxyRequest) {
	return r.handlerStart(true)
}

func (r *Router) HandlerStartLocal() (ch <-chan *revproxy.GetProxyRequest) {
	return r.handlerStart(false)
}

func (r *Router) handlerStart(remote bool) (ch <-chan *revproxy.GetProxyRequest) {
	r.lock.Lock()
	r.activeHandlers++
	if r.state != StateOn {
		log.Info("router: state changed to on", "component", r.componentName)
		r.state = StateOn
	}
	r.ensureReqChan()
	if remote {
		ch = r.remoteReqCh
	} else {
		ch = r.localReqCh
	}
	r.lock.Unlock()
	return
}

func (r *Router) HandlerStop() {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.activeHandlers <= 0 {
		panic("router: HandlerStop called when activeHandlers <= 0: componentName=" + r.componentName)
	}

	r.activeHandlers--
	if r.activeHandlers == 0 {
		if r.inflightReqs > 0 {
			r.setPendingState(0)
		} else {
			log.Info("router: state changed to off", "component", r.componentName)
			r.state = StateOff
		}
	}
}

func (r *Router) DestroyIfIdle() bool {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.state != StateOff || r.inflightReqs != 0 {
		return false
	}
	if r.remoteReqCh != nil {
		log.Info("router: closing channels", "component", r.componentName)
		close(r.remoteReqCh)
		close(r.localReqCh)
		r.remoteReqCh = nil
		r.localReqCh = nil
	}
	return true
}

func (r *Router) Route(ctx context.Context, req *revproxy.Request) {
	r.routeStart(req.Component)
	defer r.routeDone()

	haveProxy := false
	getProxyReq := revproxy.NewGetProxyRequest()

	if req.PreferLocal {
		localSecs := req.Component.MaxDurationSeconds / 10
		if localSecs < 1 {
			localSecs = 1
		}
		timeout := time.After(time.Duration(localSecs) * time.Second)
		select {
		case r.localReqCh <- getProxyReq:
			// ok - done
			haveProxy = true
		case <-ctx.Done():
			// timeout or shutdown
			return
		case <-timeout:
			// fall through - give remote handlers a chance
		}
	}

	if !haveProxy {
		select {
		case r.localReqCh <- getProxyReq:
			// ok
		case r.remoteReqCh <- getProxyReq:
			// ok
		case <-ctx.Done():
			// timeout or shutdown
			return
		}
	}

	// handle request
	proxyFx := <-getProxyReq.Proxy
	proxyFx(req)
}

func (r *Router) routeStart(comp *v1.Component) {
	r.lock.Lock()
	r.inflightReqs++
	r.ensureReqChan()
	if r.state == StateOff {
		r.setPendingState(comp.Docker.HttpStartHealthCheckSeconds)
	}
	r.lock.Unlock()
}

func (r *Router) routeDone() {
	r.lock.Lock()
	r.inflightReqs--
	if r.inflightReqs == 0 && r.activeHandlers == 0 {
		r.state = StateOff
	}
	r.lock.Unlock()
}

func (r *Router) ensureReqChan() {
	if r.remoteReqCh == nil {
		r.remoteReqCh = make(chan *revproxy.GetProxyRequest)
	}
	if r.localReqCh == nil {
		r.localReqCh = make(chan *revproxy.GetProxyRequest)
	}
}

func (r *Router) setPendingState(retrySeconds int64) {
	if r.state != StatePending {
		log.Info("router: state changed to pending", "component", r.componentName)
		r.state = StatePending
		if retrySeconds <= 0 {
			retrySeconds = 30
		}
		go r.runPendingLoop(time.Duration(retrySeconds) * time.Second)
	}
}

func (r *Router) runPendingLoop(retryDur time.Duration) {
	for r.GetState() == StatePending {
		r.startComponentFunc(r.componentName)
		time.Sleep(retryDur)
	}
}
