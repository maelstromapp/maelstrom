package component

import (
	log "github.com/mgutz/logxi/v1"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"reflect"
	"time"
)

type reqHandler func(req *RequestInput)

// key=peerUrl, value=# of running instances
type remoteNodeCounts map[string]int

type componentHandler struct {
	handler reqHandler
	local   bool
}

func newComponentRing(componentName string, myNodeId string, remoteNodes remoteNodeCounts) *componentRing {
	ring := &componentRing{
		componentName: componentName,
		myNodeId:      myNodeId,
		idx:           0,
		localHandler:  nil,
		localCount:    0,
		handlers:      nil,
		remoteNodes:   remoteNodes,
	}
	ring.rebuildHandlers()
	return ring
}

type componentRing struct {
	componentName string
	myNodeId      string
	idx           int
	localCount    int
	localHandler  *componentHandler
	handlers      []*componentHandler
	remoteNodes   remoteNodeCounts
}

func (c *componentRing) size() int {
	return len(c.handlers)
}

func (c *componentRing) next(preferLocal bool) reqHandler {
	if len(c.handlers) == 0 {
		return nil
	} else if preferLocal && c.localHandler != nil {
		return c.localHandler.handler
	} else {
		return c.handlers[c.nextIdx()].handler
	}
}

func (c *componentRing) nextIdx() int {
	if c.idx >= len(c.handlers) {
		c.idx = 0
	}
	i := c.idx
	c.idx++
	return i
}

func (c *componentRing) setLocalCount(localCount int, handlerFx reqHandler) {
	if c.localCount == localCount {
		// no-op
		return
	}

	if localCount <= 0 {
		c.localHandler = nil
	} else if c.localHandler == nil {
		c.localHandler = &componentHandler{
			handler: handlerFx,
			local:   true,
		}
	}

	c.localCount = localCount
	c.rebuildHandlers()
}

func (c *componentRing) setRemoteNodes(remoteNodes remoteNodeCounts) {
	if reflect.DeepEqual(c.remoteNodes, remoteNodes) {
		// no change
		return
	}
	log.Info("ring: remote nodes updated", "component", c.componentName, "nodes", remoteNodes)
	c.remoteNodes = remoteNodes
	c.rebuildHandlers()
}

func (c *componentRing) rebuildHandlers() {
	localCount := 0
	remoteCount := 0

	handlers := make([]*componentHandler, 0)
	if c.localHandler != nil {
		for i := 0; i < c.localCount; i++ {
			handlers = append(handlers, c.localHandler)
			localCount++
		}
	}
	for peerUrl, count := range c.remoteNodes {
		target, err := url.Parse(peerUrl)
		if err == nil {
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.Transport = &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
			}
			compName := c.componentName
			handler := func(req *RequestInput) {

				defer func() {
					if r := recover(); r != nil {
						log.Warn("ring: recovered panic", "r", r)
					}
				}()

				relayPath := req.Req.Header.Get("MAELSTROM-RELAY-PATH")
				if relayPath == "" {
					relayPath = c.myNodeId
				} else {
					relayPath = relayPath + "|" + c.myNodeId
				}
				req.Req.Header.Set("MAELSTROM-COMPONENT", compName)
				req.Req.Header.Set("MAELSTROM-RELAY-PATH", relayPath)
				proxy.ServeHTTP(req.Resp, req.Req)
				req.Done <- true
			}
			compHandler := &componentHandler{
				handler: handler,
				local:   false,
			}
			for i := 0; i < count; i++ {
				handlers = append(handlers, compHandler)
				remoteCount++
			}
		} else {
			log.Error("router: cannot create peer url", "err", err, "url", peerUrl)
		}
	}
	c.handlers = handlers

	rand.Shuffle(len(c.handlers), func(i, j int) { c.handlers[i], c.handlers[j] = c.handlers[j], c.handlers[i] })
	log.Info("ring: updated", "component", c.componentName, "handlers", len(c.handlers), "local", localCount,
		"remote", remoteCount)
}
