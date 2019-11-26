package component

import (
	"context"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"sync"
	"time"
)

type maelComponentId uint64
type maelComponentStatus int

const (
	componentStatusActive maelComponentStatus = iota
	componentStatusExited
)

type componentMsg struct {
	request                 *RequestInput
	scaleReq                *scaleComponentInput
	instanceCountReq        *instanceCountRequest
	notifyContainersChanged *notifyContainersChanged
	dockerEventReq          *dockerEventRequest
	remoteNotesReq          *remoteNodesRequest
	componentUpdatedReq     *componentUpdatedInput
	pullState               *PullState
	shutdown                bool
}

type notifyContainersChanged struct{}

type remoteNodesRequest struct {
	counts remoteNodeCounts
}

type componentUpdatedInput struct {
	component *v1.Component
}

func NewComponent(id maelComponentId, dispatcher *Dispatcher, nodeSvc v1.NodeService, dockerClient *docker.Client,
	comp *v1.Component, maelstromUrl string, myNodeId string, targetContainers int,
	remoteCounts remoteNodeCounts, pullState *PullState,
	startLockAcquire ConvergeStartLockAcquire, postStartContainer ConvergePostStartContainer) *Component {

	ctx, ctxCancel := context.WithCancel(context.Background())
	localCtx, localCtxCancel := context.WithCancel(context.Background())

	c := &Component{
		id:                     id,
		status:                 componentStatusActive,
		dispatcher:             dispatcher,
		nodeSvc:                nodeSvc,
		dockerClient:           dockerClient,
		inbox:                  make(chan componentMsg),
		component:              comp,
		maelstromUrl:           maelstromUrl,
		wg:                     &sync.WaitGroup{},
		ctx:                    ctx,
		ctxCancel:              ctxCancel,
		ring:                   newComponentRing(comp.Name, myNodeId, remoteCounts),
		targetContainers:       targetContainers,
		waitingReqs:            make([]*RequestInput, 0),
		localReqCh:             make(chan *RequestInput),
		localReqWg:             &sync.WaitGroup{},
		localCtx:               localCtx,
		localCtxCancel:         localCtxCancel,
		pullState:              pullState,
		maelContainerIdCounter: maelContainerId(0),
		startLockAcquire:       startLockAcquire,
		postStartContainer:     postStartContainer,
	}

	c.converger = NewConverger(ComponentTarget{Component: comp, Count: targetContainers}, ctx).
		WithComponentCallbacks(c)
	c.converger.Start()

	go c.run()
	return c
}

type Component struct {
	dispatcher   *Dispatcher
	nodeSvc      v1.NodeService
	dockerClient *docker.Client

	id                     maelComponentId
	status                 maelComponentStatus
	inbox                  chan componentMsg
	component              *v1.Component
	converger              *Converger
	maelstromUrl           string
	wg                     *sync.WaitGroup
	ctx                    context.Context
	ctxCancel              context.CancelFunc
	ring                   *componentRing
	targetContainers       int
	waitingReqs            []*RequestInput
	localReqCh             chan *RequestInput
	localReqWg             *sync.WaitGroup
	localCtx               context.Context
	localCtxCancel         context.CancelFunc
	lastPlacedReq          time.Time
	pullState              *PullState
	maelContainerIdCounter maelContainerId
	startLockAcquire       ConvergeStartLockAcquire
	postStartContainer     ConvergePostStartContainer
}

func (c *Component) Request(req *RequestInput) {
	c.trySend(componentMsg{request: req})
}

func (c *Component) Scale(req *scaleComponentInput) {
	c.trySend(componentMsg{scaleReq: req})
}

func (c *Component) ComponentInfo(infoCh chan v1.ComponentInfo) bool {
	for _, info := range c.converger.GetComponentInfo() {
		infoCh <- info
	}
	return true
}

func (c *Component) InstanceCount(req *instanceCountRequest) {
	c.trySend(componentMsg{instanceCountReq: req})
}

func (c *Component) NotifyContainersChanged(count int) {
	c.trySend(componentMsg{notifyContainersChanged: &notifyContainersChanged{}})
}

func (c *Component) ComponentUpdated(comp *v1.Component) {
	c.trySend(componentMsg{componentUpdatedReq: &componentUpdatedInput{component: comp}})
}

func (c *Component) OnDockerEvent(req *dockerEventRequest) {
	c.converger.OnDockerEvent(req.Event)
}

func (c *Component) SetRemoteNodes(req *remoteNodesRequest) {
	c.trySend(componentMsg{remoteNotesReq: req})
}

func (c *Component) Shutdown() {
	c.trySend(componentMsg{shutdown: true})
	c.wg.Wait()
}

func (c *Component) Join() {
	c.wg.Wait()
}

func (c *Component) trySend(msg componentMsg) bool {
	select {
	case c.inbox <- msg:
		// ok - sent
		return true
	case <-c.ctx.Done():
		log.Warn("component: message not delivered, component canceled", "msg", msg, "component", c.component.Name)
		return false
	}
}

func (c *Component) run() {
	c.wg.Add(1)
	defer c.wg.Done()
	defer c.ctxCancel()

	running := true
	for running {
		select {
		case msg := <-c.inbox:
			if msg.request != nil {
				c.request(msg.request)
			} else if msg.scaleReq != nil {
				c.scale(msg.scaleReq)
			} else if msg.instanceCountReq != nil {
				c.instanceCount(msg.instanceCountReq)
			} else if msg.remoteNotesReq != nil {
				c.setRemoteNodes(msg.remoteNotesReq)
			} else if msg.notifyContainersChanged != nil {
				c.notifyContainersChanged()
			} else if msg.componentUpdatedReq != nil {
				c.componentUpdated(msg.componentUpdatedReq)
			} else if msg.shutdown {
				c.shutdown()
				running = false
			}
		case <-c.ctx.Done():
			running = false
		}
	}
	go c.dispatcher.SetComponentStatus(&componentStatusRequest{
		id:     c.id,
		status: componentStatusExited,
	})
	log.Info("component: exiting run loop gracefully", "component", c.component.Name)
}

func (c *Component) request(req *RequestInput) {
	// Delegate to next handler in ring, preferring local handler if the req
	// was routed from another maelstrom node to us
	preferLocal := req.Req.Header.Get("MAELSTROM-RELAY-PATH") != ""
	handler := c.ring.next(preferLocal)

	if handler == nil {
		// No containers running yet for this component
		// Add request to waiting list
		c.waitingReqs = append(c.waitingReqs, req)

		// If it's been a minute since last placement request, ask nodeSvc to place a container
		if time.Now().Sub(c.lastPlacedReq) > time.Minute {
			c.lastPlacedReq = time.Now()
			go c.placeComponent()
		}
	} else {
		go handler(req)
	}
}

func (c *Component) placeComponent() {
	log.Info("component: calling PlaceComponent", "component", c.component.Name)
	_, err := c.nodeSvc.PlaceComponent(v1.PlaceComponentInput{
		ComponentName: c.component.Name,
	})
	if err != nil {
		log.Error("component: Error calling PlaceComponent", "component", c.component.Name, "err", err)
	}
}

func (c *Component) instanceCount(req *instanceCountRequest) {
	req.Output <- c.ring.size()
}

func (c *Component) notifyContainersChanged() {
	c.flushWaitingRequests()
}

func (c *Component) setRemoteNodes(req *remoteNodesRequest) {
	c.ring.setRemoteNodes(req.counts)
	c.flushWaitingRequests()
}

func (c *Component) handleReq(req *RequestInput) {
	c.localReqWg.Add(1)
	select {
	case c.localReqCh <- req:
	// request sent
	case <-c.localCtx.Done():
		log.Warn("component: handleReq unable to send req to localReqCh", "component", c.component.Name)
	}
	c.localReqWg.Done()
}

func (c *Component) flushWaitingRequests() {
	waitingCount := len(c.waitingReqs)
	if c.ring.size() > 0 && waitingCount > 0 {
		for _, r := range c.waitingReqs {
			preferLocal := r.Req.Header.Get("MAELSTROM-RELAY-PATH") != ""
			handler := c.ring.next(preferLocal)
			go handler(r)
		}
		c.waitingReqs = make([]*RequestInput, 0)
		log.Info("component: flushed waiting requests", "count", waitingCount)
	}
}

func (c *Component) shutdown() {
	log.Info("component: shutting down all containers", "component", c.component.Name)

	// stop converger so we don't start/stop any containers
	c.converger.Stop()

	// notify containers to exit by closing input channel
	if c.localReqCh != nil {
		c.localCtxCancel()
		c.localReqWg.Wait()
		close(c.localReqCh)
		c.localReqCh = nil
	}

	// wait for all containers to exit
	for _, cn := range c.converger.getContainers() {
		cn.JoinAndStop("component shutdown")
	}

	// clear local handlers from ring
	c.ring.setLocalCount(0, c.handleReq)

	// if there are no active handlers for this component, exit run loop
	if c.ring.size() == 0 {
		c.ctxCancel()
	}

	log.Info("component: shutdown complete", "component", c.component.Name)
}

func (c *Component) componentUpdated(req *componentUpdatedInput) {
	c.component = req.component
	c.convertSetTarget()
}

func (c *Component) scale(req *scaleComponentInput) {
	c.targetContainers = req.targetCount
	c.ring.setLocalCount(c.targetContainers, c.handleReq)
	c.convertSetTarget()
}

func (c *Component) convertSetTarget() {
	c.converger.SetTarget(ComponentTarget{
		Component: c.component,
		Count:     c.targetContainers,
	})
}

func (c *Component) pullImage(comp *v1.Component) error {
	pull := c.component.Docker.PullImageOnStart || c.component.Docker.PullImageOnPut
	force := c.component.Docker.PullImageOnStart
	if !force {
		exists, err := common.ImageExistsLocally(c.dockerClient, c.component.Docker.Image)
		if err == nil {
			// if image isn't present locally, pull it now
			if !exists {
				pull = true
				force = true
			}
		} else {
			return errors.Wrap(err, "unable to list images")
		}
	}
	if pull {
		c.pullState.Pull(*c.component, force)
	}
	return nil
}

func (c *Component) startContainerAndHealthCheck(ctx context.Context, comp *v1.Component) (*Container, error) {
	c.maelContainerIdCounter++
	cn := NewContainer(c.dockerClient, c.component, c.maelstromUrl, c.localReqCh, c.maelContainerIdCounter)
	err := cn.startAndHealthCheck(ctx)
	if err != nil {
		return nil, err
	}
	go cn.run()
	return cn, nil
}

func (c *Component) stopContainer(cn *Container, reason string) {
	cn.CancelAndStop(reason)
}
