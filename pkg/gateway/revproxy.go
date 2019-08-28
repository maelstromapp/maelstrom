package gateway

import (
	"context"
	"net/http/httputil"
	"sync"
)

func localRevProxy(reqCh <-chan *MaelRequest, proxy *httputil.ReverseProxy, ctx context.Context, wg *sync.WaitGroup) {
	revProxyLoop(reqCh, proxy, ctx, wg, "", "")
}

func revProxyLoop(reqCh <-chan *MaelRequest, proxy *httputil.ReverseProxy, ctx context.Context,
	wg *sync.WaitGroup, myNodeId string, componentName string) {

	defer wg.Done()

	for {
		select {
		case mr := <-reqCh:

			// stop loop if request channel closed
			if mr == nil {
				return
			}

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

			success := make(chan bool, 1)
			go func() {
				proxy.ServeHTTP(mr.rw, mr.req)
				success <- true
			}()

			// wait until round trip completes or deadline reached
			select {
			case <-success:
				mr.ready <- true
			case <-mr.ctx.Done():
			}

		case <-ctx.Done():
			return
		}
	}
}
