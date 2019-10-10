package component

import (
	"context"
	log "github.com/mgutz/logxi/v1"
	"net/http/httputil"
	"sync"
	"time"
)

func localRevProxy(reqCh <-chan *RequestInput, statCh chan<- time.Duration, proxy *httputil.ReverseProxy,
	ctx context.Context, wg *sync.WaitGroup) {
	revProxyLoop(reqCh, statCh, proxy, ctx, wg, "", "")
}

func revProxyLoop(reqCh <-chan *RequestInput, statCh chan<- time.Duration,
	proxy *httputil.ReverseProxy, ctx context.Context, wg *sync.WaitGroup, myNodeId string, componentName string) {

	defer wg.Done()

	for {
		select {
		case mr := <-reqCh:
			if mr == nil {
				// reqCh closed and drained - all rev proxy loops for this component can exit
				return
			}
			handleReq(mr, myNodeId, componentName, proxy, statCh)
		case <-ctx.Done():
			return
		}
	}
}

func handleReq(req *RequestInput, myNodeId string, componentName string, proxy *httputil.ReverseProxy,
	statCh chan<- time.Duration) {

	defer func() {
		if r := recover(); r != nil {
			log.Warn("revproxy: recovered panic", "r", r)
		}
	}()

	if myNodeId != "" {
		relayPath := req.Req.Header.Get("MAELSTROM-RELAY-PATH")
		if relayPath == "" {
			relayPath = myNodeId
		} else {
			relayPath = relayPath + "|" + myNodeId
		}
		req.Req.Header.Set("MAELSTROM-COMPONENT", componentName)
		req.Req.Header.Set("MAELSTROM-RELAY-PATH", relayPath)

		// TODO: need to set a header with time of request deadline
		// so that receiving node can set the request deadline appropriately to account for time already spent
	}

	proxy.ServeHTTP(req.Resp, req.Req)
	req.Done <- true
	if statCh != nil {
		statCh <- time.Now().Sub(req.StartTime)
	}
}
