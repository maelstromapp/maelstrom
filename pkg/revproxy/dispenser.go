package revproxy

import (
	"context"
	"net/http/httputil"
	"sync"
	"time"
)

type Proxy func(req *Request)

func NewGetProxyRequest() *GetProxyRequest {
	return &GetProxyRequest{
		Proxy: make(chan Proxy),
	}
}

type GetProxyRequest struct {
	Proxy chan Proxy
}

func NewDispenser(maxConcurrency int, reqCh <-chan *GetProxyRequest,
	myNodeId string, componentName string, reqWaitGroup *sync.WaitGroup,
	proxy *httputil.ReverseProxy, statCh chan<- time.Duration, ctx context.Context) *Dispenser {

	doneChSize := maxConcurrency
	if doneChSize < 1 {
		doneChSize = 1
	}
	doneCh := make(chan bool, doneChSize)

	proxyFx := func(req *Request) {
		if reqWaitGroup != nil {
			reqWaitGroup.Add(1)
			defer reqWaitGroup.Done()
		}
		handleReq(req, myNodeId, componentName, proxy, statCh, ctx)
		doneCh <- true
	}

	return &Dispenser{
		maxConcurrency: maxConcurrency,
		reqCh:          reqCh,
		doneCh:         doneCh,
		proxy:          proxyFx,
	}
}

type Dispenser struct {
	maxConcurrency int
	reqCh          <-chan *GetProxyRequest
	doneCh         chan bool
	proxy          Proxy
}

func (d *Dispenser) Run(ctx context.Context) {
	concur := 0
	for {
		if d.maxConcurrency < 1 || concur < d.maxConcurrency {
			// under concurrency limit, accept a request, or a 'done' msg
			select {
			case <-ctx.Done():
				return
			case req := <-d.reqCh:
				concur++
				req.Proxy <- d.proxy
			case <-d.doneCh:
				concur--
			}
		} else {
			// at limit - wait for 'done' msg
			select {
			case <-ctx.Done():
				return
			case <-d.doneCh:
				concur--
			}
		}
	}
}
