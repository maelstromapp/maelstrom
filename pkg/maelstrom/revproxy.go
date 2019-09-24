package maelstrom

import (
	"context"
	log "github.com/mgutz/logxi/v1"
	"net/http/httputil"
	"sync"
	"time"
)

func localRevProxy(reqCh <-chan *MaelRequest, statCh chan<- time.Duration, proxy *httputil.ReverseProxy,
	ctx context.Context, wg *sync.WaitGroup) {
	revProxyLoop(reqCh, statCh, proxy, ctx, wg, "", "")
}

func revProxyLoop(reqCh <-chan *MaelRequest, statCh chan<- time.Duration,
	proxy *httputil.ReverseProxy, ctx context.Context,
	wg *sync.WaitGroup, myNodeId string, componentName string) {

	defer wg.Done()

	for {
		select {
		case mr := <-reqCh:
			handleReq(mr, myNodeId, componentName, proxy, statCh)
		case <-ctx.Done():
			return
		}
	}
}

func handleReq(mr *MaelRequest, myNodeId string, componentName string, proxy *httputil.ReverseProxy,
	statCh chan<- time.Duration) {
	// stop loop if request channel closed
	if mr == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Warn("revproxy: recovered panic", "r", r)
		}
	}()

	if myNodeId != "" {
		relayPath := mr.req.Header.Get("MAELSTROM-RELAY-PATH")
		if relayPath == "" {
			relayPath = myNodeId
		} else {
			relayPath = relayPath + "|" + myNodeId
		}
		mr.req.Header.Set("MAELSTROM-COMPONENT", componentName)
		mr.req.Header.Set("MAELSTROM-RELAY-PATH", relayPath)

		// TODO: need to set a header with time of request deadline
		// so that receiving node can set the request deadline appropriately to account for time already spent
	}

	proxy.ServeHTTP(mr.rw, mr.req)
	mr.complete <- true
	if statCh != nil {
		statCh <- time.Now().Sub(mr.startTime)
	}
}
