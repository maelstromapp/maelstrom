package component

import (
	"context"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"
)

type dispatcherMsg struct {
	request            *RequestInput
	scaleReq           *scaleInputInternal
	infoReq            *infoRequest
	instanceCountReq   *instanceCountRequest
	dockerEventReq     *dockerEventRequest
	dataChangedReq     *dataChangedRequest
	componentStatusReq *componentStatusRequest
	clusterUpdatedReq  *clusterUpdatedRequest
	shutdown           bool
}

type instanceCountRequest struct {
	Component *v1.Component
	Output    chan int
}

type dockerEventRequest struct {
	Event *common.DockerEvent
}

type dataChangedRequest struct {
	Event *v1.DataChangedUnion
}

type componentStatusRequest struct {
	id     maelComponentId
	status maelComponentStatus
}

type clusterUpdatedRequest struct {
	nodes map[string]v1.NodeStatus
}

func NewDispatcher(nodeSvc v1.NodeService, dockerClient *docker.Client, maelstromUrl string,
	myNodeId string) (*Dispatcher, error) {
	ctx, ctxCancel := context.WithCancel(context.Background())
	d := &Dispatcher{
		nodeSvc:                 nodeSvc,
		dockerClient:            dockerClient,
		inbox:                   make(chan dispatcherMsg),
		wg:                      &sync.WaitGroup{},
		ctx:                     ctx,
		ctxCancel:               ctxCancel,
		componentsByName:        make(map[string]*Component),
		version:                 common.NowMillis(),
		maelstromUrl:            maelstromUrl,
		myNodeId:                myNodeId,
		maelComponentIdCounter:  maelComponentId(0),
		targetCountByComponent:  make(map[string]int),
		remoteCountsByComponent: make(map[string]remoteNodeCounts),
	}

	rmCount, err := common.RemoveMaelstromContainers(dockerClient, "removing stale containers")
	if err != nil {
		return nil, errors.Wrap(err, "dispatcher: remove containers failed")
	}
	if rmCount > 0 {
		log.Info("dispatcher: removed stale containers", "count", rmCount)
	}

	go d.run()
	return d, nil
}

type Dispatcher struct {
	nodeSvc                 v1.NodeService
	dockerClient            *docker.Client
	inbox                   chan dispatcherMsg
	wg                      *sync.WaitGroup
	ctx                     context.Context
	ctxCancel               context.CancelFunc
	componentsByName        map[string]*Component
	version                 int64
	maelstromUrl            string
	myNodeId                string
	maelComponentIdCounter  maelComponentId
	targetCountByComponent  map[string]int
	remoteCountsByComponent map[string]remoteNodeCounts
}

func (d *Dispatcher) Route(rw http.ResponseWriter, req *http.Request, comp *v1.Component, publicGateway bool) {
	// Set Deadline
	var deadlineNano int64
	if !publicGateway {
		deadlineStr := req.Header.Get("MAELSTROM-DEADLINE-NANO")
		if deadlineStr != "" {
			deadlineNano, _ = strconv.ParseInt(deadlineStr, 10, 64)
		}
	}
	deadline := componentReqDeadline(deadlineNano, comp)
	ctx, _ := context.WithDeadline(context.Background(), deadline)
	if req.Header.Get("MAELSTROM-DEADLINE-NANO") == "" {
		req.Header.Set("MAELSTROM-DEADLINE-NANO", strconv.FormatInt(deadline.UnixNano(), 10))
	}

	// Send request to dispatcher
	compReq := &RequestInput{
		Resp:          rw,
		Req:           req,
		Component:     comp,
		PublicGateway: publicGateway,
		StartTime:     time.Now(),
		Done:          make(chan bool, 1),
	}
	go func() {
		d.Request(compReq)
	}()

	// Block on result, or timeout
	select {
	case <-compReq.Done:
		return
	case <-ctx.Done():
		msg := "gateway: Timeout proxying component: " + comp.Name
		log.Warn(msg, "component", comp.Name, "version", comp.Version)
		respondText(rw, http.StatusGatewayTimeout, msg)
		return
	}
}

func (d *Dispatcher) Request(req *RequestInput) {
	d.trySend(dispatcherMsg{request: req})
}

func (d *Dispatcher) Scale(input *ScaleInput) ScaleOutput {
	req := &scaleInputInternal{
		ScaleInput: input,
		Output:     make(chan ScaleOutput, 1),
	}
	d.trySend(dispatcherMsg{scaleReq: req})
	return <-req.Output
}

func (d *Dispatcher) ComponentInfo() InfoResponse {
	req := &infoRequest{
		resp: make(chan InfoResponse, 1),
	}
	d.trySend(dispatcherMsg{infoReq: req})
	return <-req.resp
}

func (d *Dispatcher) InstanceCountForComponent(comp v1.Component) (int, error) {
	req := &instanceCountRequest{
		Component: &comp,
		Output:    make(chan int),
	}
	if d.trySend(dispatcherMsg{instanceCountReq: req}) {
		select {
		case count := <-req.Output:
			return count, nil
		case <-d.ctx.Done():
			log.Warn("dispatcher: InstanceCountForComponent not delivered, dispatcher canceled")
		}
	}
	return 0, fmt.Errorf("dispatcher: InstanceCountForComponent unable to send message to dispatcher loop")

}

func (d *Dispatcher) SetComponentStatus(req *componentStatusRequest) {
	d.trySend(dispatcherMsg{componentStatusReq: req})
}

func (d *Dispatcher) OnDockerEvent(msg common.DockerEvent) {
	d.trySend(dispatcherMsg{dockerEventReq: &dockerEventRequest{Event: &msg}})
}

func (d *Dispatcher) OnClusterUpdated(nodes map[string]v1.NodeStatus) {
	d.trySend(dispatcherMsg{clusterUpdatedReq: &clusterUpdatedRequest{nodes: nodes}})
}

func (d *Dispatcher) OnComponentNotification(change v1.DataChangedUnion) {
	d.trySend(dispatcherMsg{dataChangedReq: &dataChangedRequest{Event: &change}})
}

func (d *Dispatcher) Shutdown() {
	d.trySend(dispatcherMsg{shutdown: true})
	d.wg.Wait()
}

func (d *Dispatcher) trySend(msg dispatcherMsg) bool {
	select {
	case d.inbox <- msg:
		// ok - sent
		return true
	case <-d.ctx.Done():
		log.Warn("dispatcher: message not delivered, dispatcher canceled", "msg", msg)
	}
	return false
}

func (d *Dispatcher) run() {
	d.wg.Add(1)
	defer d.wg.Done()

	running := true
	for running {
		select {
		case msg := <-d.inbox:
			if msg.request != nil {
				d.request(msg.request)
			} else if msg.scaleReq != nil {
				d.scale(msg.scaleReq)
			} else if msg.infoReq != nil {
				d.componentInfo(msg.infoReq)
			} else if msg.instanceCountReq != nil {
				d.instanceCountForComponent(msg.instanceCountReq)
			} else if msg.dockerEventReq != nil {
				d.dockerEvent(msg.dockerEventReq)
			} else if msg.dataChangedReq != nil {
				d.dataChanged(msg.dataChangedReq)
			} else if msg.componentStatusReq != nil {
				d.setComponentStatus(msg.componentStatusReq)
			} else if msg.clusterUpdatedReq != nil {
				d.clusterUpdated(msg.clusterUpdatedReq)
			} else if msg.shutdown {
				d.shutdown()
				running = false
			}
		}
	}
	log.Info("dispatcher: exiting run loop gracefully")
}

func (d *Dispatcher) component(dbComp *v1.Component) *Component {
	c, ok := d.componentsByName[dbComp.Name]
	if !ok {
		c = d.startComponent(dbComp)
	} else if dbComp.Version > c.component.Version {
		c = d.restartComponent(c, dbComp, "component updated to version: "+strconv.Itoa(int(dbComp.Version)))
	}
	return c
}

func (d *Dispatcher) startComponent(dbComp *v1.Component) *Component {
	dbComp.Docker.Image = common.NormalizeImageName(dbComp.Docker.Image)
	d.maelComponentIdCounter++
	c := NewComponent(d.maelComponentIdCounter, d, d.nodeSvc, d.dockerClient, dbComp, d.maelstromUrl, d.myNodeId,
		d.targetCountByComponent[dbComp.Name], d.remoteCountsByComponent[dbComp.Name])
	d.componentsByName[dbComp.Name] = c
	return c
}

func (d *Dispatcher) request(req *RequestInput) {
	d.component(req.Component).Request(req)
}

func (d *Dispatcher) scale(req *scaleInputInternal) {
	out := ScaleOutput{
		TargetVersionMismatch: false,
		Started:               make([]v1.ComponentDelta, 0),
		Stopped:               make([]v1.ComponentDelta, 0),
		Errors:                make([]v1.ComponentDeltaError, 0),
	}
	if d.version == req.TargetVersion {
		d.version++
		for _, target := range req.TargetCounts {
			comp := d.component(target.Component)
			oldTarget := d.targetCountByComponent[target.Component.Name]
			newTarget := oldTarget + int(target.Delta)
			if newTarget < 0 {
				newTarget = 0
			}
			d.targetCountByComponent[target.Component.Name] = newTarget
			go comp.Scale(&scaleComponentInput{targetCount: newTarget})
			if target.Delta > 0 {
				out.Started = append(out.Started, target.ToV1ComponentDelta())
			} else if target.Delta < 0 {
				out.Stopped = append(out.Stopped, target.ToV1ComponentDelta())
			}
		}
	} else {
		out.TargetVersionMismatch = true
	}
	req.Output <- out
}

func (d *Dispatcher) componentInfo(req *infoRequest) {
	infoCh := make(chan v1.ComponentInfo, len(d.componentsByName))
	aggCh := make(chan []v1.ComponentInfo, 1)
	wg := &sync.WaitGroup{}
	for _, comp := range d.componentsByName {
		wg.Add(1)
		go func(c *Component) {
			defer wg.Done()
			c.ComponentInfo(infoCh)
		}(comp)
	}
	go func() {
		infos := make([]v1.ComponentInfo, 0)
		for inf := range infoCh {
			infos = append(infos, inf)
		}
		aggCh <- infos
	}()
	wg.Wait()
	close(infoCh)
	req.resp <- InfoResponse{
		Info:    <-aggCh,
		Version: d.version,
	}
}

func (d *Dispatcher) instanceCountForComponent(req *instanceCountRequest) {
	d.component(req.Component).InstanceCount(req)
}

func (d *Dispatcher) dockerEvent(req *dockerEventRequest) {
	if req.Event.ImageUpdated != nil {
		for _, comp := range d.componentsByName {
			if comp.component.Docker.Image == req.Event.ImageUpdated.ImageName {
				d.restartComponent(comp, comp.component, "image updated")
			}
		}
	}
	if req.Event.ContainerExited != nil {
		for _, comp := range d.componentsByName {
			comp.OnDockerEvent(req)
		}
	}
}

func (d *Dispatcher) dataChanged(req *dataChangedRequest) {
	if req.Event.RemoveComponent != nil {
		cn, ok := d.componentsByName[req.Event.RemoveComponent.Name]
		if ok {
			log.Info("dispatcher: shutting down component - component removed", "component", cn.component.Name)
			delete(d.componentsByName, req.Event.RemoveComponent.Name)
			go cn.Shutdown()
		}
	}
}

func (d *Dispatcher) restartComponent(comp *Component, dbComp *v1.Component, reason string) *Component {
	log.Info("dispatcher: restarting component", "component", comp.component.Name,
		"reason", reason, "image", comp.component.Docker.Image)
	go comp.Shutdown()
	return d.startComponent(dbComp)
}

func (d *Dispatcher) shutdown() {
	log.Info("dispatcher: shutdown called - stopping all components")
	for _, c := range d.componentsByName {
		go c.Shutdown()
	}
	for _, c := range d.componentsByName {
		c.Join()
	}
}

func (d *Dispatcher) setComponentStatus(req *componentStatusRequest) {
	for name, c := range d.componentsByName {
		if c.id == req.id {
			switch req.status {
			case componentStatusExited:
				log.Info("dispatcher: removing component", "component", c.component.Name,
					"version", c.component.Version)
				delete(d.componentsByName, name)
			}
		}
	}
}

func (d *Dispatcher) clusterUpdated(req *clusterUpdatedRequest) {
	remoteCountsByComponent := toRemoteCounts(req.nodes, d.myNodeId)
	if reflect.DeepEqual(remoteCountsByComponent, d.remoteCountsByComponent) {
		// no-op - counts are unchanged
		return
	}
	d.remoteCountsByComponent = remoteCountsByComponent
	log.Info("dispatcher: cluster routes updated", "countsByComponent", d.remoteCountsByComponent)
	for componentName, cn := range d.componentsByName {
		go cn.SetRemoteNodes(&remoteNodesRequest{counts: remoteCountsByComponent[componentName]})
	}
}

func toRemoteCounts(nodes map[string]v1.NodeStatus, myNodeId string) map[string]remoteNodeCounts {
	remoteCountsByComponent := make(map[string]remoteNodeCounts)
	for _, node := range nodes {
		if node.NodeId != myNodeId {
			for _, rc := range node.RunningComponents {
				remoteCounts, ok := remoteCountsByComponent[rc.ComponentName]
				if !ok {
					remoteCounts = make(remoteNodeCounts)
					remoteCountsByComponent[rc.ComponentName] = remoteCounts
				}
				remoteCounts[node.PeerUrl] = remoteCounts[node.PeerUrl] + 1
			}
		}
	}
	return remoteCountsByComponent
}

func componentReqDeadline(deadlineNanoFromHeader int64, component *v1.Component) time.Time {
	if deadlineNanoFromHeader == 0 {
		maxDur := component.MaxDurationSeconds
		if maxDur <= 0 {
			maxDur = 300
		}
		startTime := time.Now()
		return startTime.Add(time.Duration(maxDur) * time.Second)
	} else {
		return time.Unix(0, deadlineNanoFromHeader)
	}
}

func respondText(rw http.ResponseWriter, statusCode int, body string) {
	rw.Header().Add("content-type", "text/plain")
	rw.WriteHeader(statusCode)
	_, err := rw.Write([]byte(body))
	if err != nil {
		log.Warn("dispatcher: respondText.Write error", "err", err.Error())
	}
}
