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
	myNodeId string, componentName string,
	proxy *httputil.ReverseProxy, statCh chan<- time.Duration, ctx context.Context) *Dispenser {

	doneChSize := maxConcurrency
	if doneChSize < 1 {
		doneChSize = 1
	}
	doneCh := make(chan bool, doneChSize)
	doneFx := func() { doneCh <- true }

	proxyFx := func(req *Request) {
		defer doneFx()
		handleReq(req, myNodeId, componentName, proxy, statCh, ctx)
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

func (d *Dispenser) Run(ctx context.Context, reqWaitGroup *sync.WaitGroup) {
	if reqWaitGroup != nil {
		defer reqWaitGroup.Done()
	}

	concur := 0
	active := true
	for active {
		if d.maxConcurrency < 1 || concur < d.maxConcurrency {
			// under concurrency limit, accept a request, or a 'done' msg
			select {
			case <-ctx.Done():
				active = false
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
				active = false
			case <-d.doneCh:
				concur--
			}
		}
	}

	// wait for all in-flight reqs to finish
	for concur > 0 {
		<-d.doneCh
		concur--
	}
}
