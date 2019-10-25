package component

import (
	"context"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
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
	request            *RequestInput
	infoReq            *nestedInfoRequest
	scaleReq           *scaleComponentInput
	instanceCountReq   *instanceCountRequest
	containerStatusReq *containerStatusRequest
	dockerEventReq     *dockerEventRequest
	remoteNotesReq     *remoteNodesRequest
	pullState          *PullState
	shutdown           bool
}

type nestedInfoRequest struct {
	infoCh chan v1.ComponentInfo
	done   chan bool
}

type containerStatusRequest struct {
	id     maelContainerId
	status maelContainerStatus
}

type remoteNodesRequest struct {
	counts remoteNodeCounts
}

func NewComponent(id maelComponentId, dispatcher *Dispatcher, nodeSvc v1.NodeService, dockerClient *docker.Client,
	comp *v1.Component, maelstromUrl string, myNodeId string, targetContainers int,
	remoteCounts remoteNodeCounts, pullState *PullState) *Component {

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
		containers:             make([]*Container, 0),
		waitingReqs:            make([]*RequestInput, 0),
		localReqCh:             make(chan *RequestInput),
		localReqWg:             &sync.WaitGroup{},
		localCtx:               localCtx,
		localCtxCancel:         localCtxCancel,
		pullState:              pullState,
		maelContainerIdCounter: maelContainerId(0),
	}
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
	maelstromUrl           string
	wg                     *sync.WaitGroup
	ctx                    context.Context
	ctxCancel              context.CancelFunc
	ring                   *componentRing
	targetContainers       int
	containers             []*Container
	waitingReqs            []*RequestInput
	localReqCh             chan *RequestInput
	localReqWg             *sync.WaitGroup
	localCtx               context.Context
	localCtxCancel         context.CancelFunc
	lastPlacedReq          time.Time
	pullState              *PullState
	maelContainerIdCounter maelContainerId
}

func (c *Component) Request(req *RequestInput) {
	c.trySend(componentMsg{request: req})
}

func (c *Component) Scale(req *scaleComponentInput) {
	c.trySend(componentMsg{scaleReq: req})
}

func (c *Component) ComponentInfo(infoCh chan v1.ComponentInfo) bool {
	req := &nestedInfoRequest{
		infoCh: infoCh,
		done:   make(chan bool, 1),
	}
	if c.trySend(componentMsg{infoReq: req}) {
		<-req.done
		return true
	}
	return false
}

func (c *Component) InstanceCount(req *instanceCountRequest) {
	c.trySend(componentMsg{instanceCountReq: req})
}

func (c *Component) OnDockerEvent(req *dockerEventRequest) {
	c.trySend(componentMsg{dockerEventReq: req})
}

func (c *Component) SetContainerStatus(req *containerStatusRequest) {
	c.trySend(componentMsg{containerStatusReq: req})
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

	// scale to target on startup
	if c.targetContainers > 0 {
		c.scale(&scaleComponentInput{targetCount: c.targetContainers})
	}

	running := true
	for running {
		select {
		case msg := <-c.inbox:
			if msg.request != nil {
				c.request(msg.request)
			} else if msg.infoReq != nil {
				c.componentInfo(msg.infoReq)
			} else if msg.scaleReq != nil {
				c.scale(msg.scaleReq)
			} else if msg.instanceCountReq != nil {
				c.instanceCount(msg.instanceCountReq)
			} else if msg.containerStatusReq != nil {
				c.setContainerStatus(msg.containerStatusReq)
			} else if msg.dockerEventReq != nil {
				c.dockerEvent(msg.dockerEventReq)
			} else if msg.remoteNotesReq != nil {
				c.setRemoteNodes(msg.remoteNotesReq)
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

func (c *Component) componentInfo(req *nestedInfoRequest) {
	for _, cn := range c.containers {
		req.infoCh <- cn.ComponentInfo()
	}
	req.done <- true
}

func (c *Component) instanceCount(req *instanceCountRequest) {
	req.Output <- c.ring.size()
}

func (c *Component) setContainerStatus(req *containerStatusRequest) {
	activeCount := 0
	matchIdx := -1
	var matchCn *Container
	for i, cn := range c.containers {
		if cn.id == req.id {
			matchIdx = i
			matchCn = cn
			c.containers[i].status = req.status
		}
		if c.containers[i].status == containerStatusActive {
			activeCount++
		}
	}

	if req.status == containerStatusExited {
		if matchCn != nil {
			c.containers = append(c.containers[:matchIdx], c.containers[matchIdx+1:]...)
			if len(c.containers) == 0 {
				// reset placement timer so we can immediately request placement if necessary
				c.lastPlacedReq = time.Time{}
			}
			log.Info("component: removed container", "id", req.id,
				"containerId", common.StrTruncate(matchCn.containerId, 8))
		}
	}

	c.ring.setLocalCount(activeCount, c.handleReq)
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

	// notify containers to exit by closing input channel
	if c.localReqCh != nil {
		c.localCtxCancel()
		c.localReqWg.Wait()
		close(c.localReqCh)
		c.localReqCh = nil
	}

	// wait for all containers to exit
	for _, cn := range c.containers {
		cn.JoinAndStop("component shutdown")
	}
	c.containers = make([]*Container, 0)

	// clear local handlers from ring
	c.ring.setLocalCount(0, c.handleReq)

	// if there are no active handlers for this component, exit run loop
	if c.ring.size() == 0 {
		c.ctxCancel()
	}

	log.Info("component: shutdown complete", "component", c.component.Name)
}

func (c *Component) scale(req *scaleComponentInput) {
	c.targetContainers = req.targetCount
	c.ring.setLocalCount(c.targetContainers, c.handleReq)
	if req.targetCount > len(c.containers) {
		c.scaleUp(req.targetCount - len(c.containers))
	} else if req.targetCount < len(c.containers) {
		c.scaleDown(len(c.containers) - req.targetCount)
	}
}

func (c *Component) scaleUp(num int) {
	if c.localReqCh == nil {
		c.localReqCh = make(chan *RequestInput)
	}

	if num > 0 {
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
				log.Error("component: unable to list image", "err", err, "component", c.component.Name)
			}
		}
		if pull {
			c.pullState.Pull(*c.component, force)
		}
	}

	for i := 0; i < num; i++ {
		c.maelContainerIdCounter++
		c.containers = append(c.containers, NewContainer(c.dockerClient, c.component, c.maelstromUrl, c.localReqCh,
			c.maelContainerIdCounter, c))
	}
}

func (c *Component) dockerEvent(req *dockerEventRequest) {
	if req.Event.ContainerExited != nil && req.Event.ContainerExited.ContainerId != "" {
		for _, cn := range c.containers {
			if cn.containerId == req.Event.ContainerExited.ContainerId {
				c.stopAndRemoveContainer(cn, "container exited")
				break
			}
		}
	}
}

func (c *Component) scaleDown(num int) {
	for i := 0; i < num && i < len(c.containers); i++ {
		c.stopAndRemoveContainer(c.containers[i], "scale down")
	}
}

func (c *Component) stopAndRemoveContainer(cn *Container, reason string) {
	if len(c.containers) == 1 && c.containers[0].id == cn.id {
		c.shutdown()
	} else {
		c.setContainerStatus(&containerStatusRequest{
			id:     cn.id,
			status: containerStatusExited,
		})
		go cn.CancelAndStop(reason)
	}
}
