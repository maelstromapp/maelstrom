package revproxy

import (
	"context"
	log "github.com/mgutz/logxi/v1"
	"net/http/httputil"
	"sync"
	"time"
)

func LocalRevProxy(reqCh <-chan *Request, statCh chan<- time.Duration, proxy *httputil.ReverseProxy,
	ctx context.Context, wg *sync.WaitGroup) {
	RevProxyLoop(reqCh, statCh, proxy, ctx, wg, "", "")
}

func RevProxyLoop(reqCh <-chan *Request, statCh chan<- time.Duration,
	proxy *httputil.ReverseProxy, ctx context.Context, wg *sync.WaitGroup, myNodeId string, componentName string) {

	if wg != nil {
		defer wg.Done()
	}

	for {
		select {
		case mr := <-reqCh:
			if mr == nil {
				// reqCh closed and drained - all rev proxy loops for this component can exit
				return
			}
			handleReq(mr, myNodeId, componentName, proxy, statCh, ctx)
		case <-ctx.Done():
			return
		}
	}
}

func handleReq(req *Request, myNodeId string, componentName string, proxy *httputil.ReverseProxy,
	statCh chan<- time.Duration, ctx context.Context) {

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

	proxy.ServeHTTP(req.Rw, req.Req)
	req.Done <- true
	if statCh != nil {
		select {
		case statCh <- time.Now().Sub(req.StartTime):
			// ok - sent
		case <-ctx.Done():
		}

	}
}
