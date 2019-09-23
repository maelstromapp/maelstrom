package maelstrom

import (
	"context"
	"fmt"
	linuxproc "github.com/c9s/goprocinfo/linux"
	"github.com/coopernurse/barrister-go"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"math/rand"
	"sort"
	"sync"
	"time"
)

const rolePlacement = "placement"
const roleCron = "cron"

func NewNodeServiceImpl(handlerFactory *DockerHandlerFactory, db Db, dockerClient *docker.Client, nodeId string,
	peerUrl string, startTime time.Time, numCPUs int64, totalMemAllowed int64) (*NodeServiceImpl, error) {
	nodeSvc := &NodeServiceImpl{
		handlerFactory:  handlerFactory,
		db:              db,
		dockerClient:    dockerClient,
		nodeId:          nodeId,
		peerUrl:         peerUrl,
		totalMemAllowed: totalMemAllowed,
		startTimeMillis: common.TimeToMillis(startTime),
		numCPUs:         numCPUs,
		loadStatusLock:  &sync.Mutex{},
	}

	cluster := NewCluster(nodeId, nodeSvc)
	nodeSvc.cluster = cluster
	_, err := nodeSvc.resolveAndBroadcastNodeStatus(context.Background())
	if err != nil {
		return nil, err
	}
	return nodeSvc, nil
}

func NewNodeServiceImplFromDocker(handlerFactory *DockerHandlerFactory, db Db, dockerClient *docker.Client,
	peerUrl string, totalMemAllowed int64) (*NodeServiceImpl, error) {

	info, err := dockerClient.Info(context.Background())
	if err != nil {
		return nil, err
	}

	return NewNodeServiceImpl(handlerFactory, db, dockerClient, info.ID, peerUrl, time.Now(), int64(info.NCPU),
		totalMemAllowed)
}

type NodeServiceImpl struct {
	handlerFactory  *DockerHandlerFactory
	dockerClient    *docker.Client
	db              Db
	cluster         *Cluster
	nodeId          string
	peerUrl         string
	totalMemAllowed int64
	startTimeMillis int64
	numCPUs         int64
	loadStatusLock  *sync.Mutex
}

func (n *NodeServiceImpl) NodeId() string {
	return n.nodeId
}

func (n *NodeServiceImpl) Cluster() *Cluster {
	return n.cluster
}

func (n *NodeServiceImpl) LogPairs() []interface{} {
	return []interface{}{"nodeId", n.nodeId, "peerUrl", n.peerUrl, "numCPUs", n.numCPUs}
}

func (n *NodeServiceImpl) ListNodeStatus(input v1.ListNodeStatusInput) (v1.ListNodeStatusOutput, error) {
	var nodes []v1.NodeStatus
	var err error
	if input.ForceRefresh {
		nodes, err = n.refreshNodes()
		if err != nil {
			code := MiscError
			msg := "nodesvc: ListNodeStatus - error refreshing node status"
			log.Error(msg, "code", code, "err", err)
			return v1.ListNodeStatusOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
		}
	} else {
		nodes = n.cluster.GetNodes()
	}
	sort.Sort(NodeStatusByStartedAt(nodes))
	return v1.ListNodeStatusOutput{RespondingNodeId: n.nodeId, Nodes: nodes}, nil
}

func (n *NodeServiceImpl) refreshNodes() ([]v1.NodeStatus, error) {
	type nodeStatusOrError struct {
		Node  v1.NodeStatus
		Error error
	}
	type nodeStatusListOrError struct {
		Nodes []v1.NodeStatus
		Error error
	}
	singleNodeChan := make(chan nodeStatusOrError)
	finalResultChan := make(chan nodeStatusListOrError)

	go func() {
		nodes := make([]v1.NodeStatus, 0)
		var err error
		for res := range singleNodeChan {
			if res.Error == nil {
				nodes = append(nodes, res.Node)
				n.cluster.SetNode(res.Node)
			} else {
				err = res.Error
			}
		}
		if err == nil {
			finalResultChan <- nodeStatusListOrError{Nodes: nodes}
		} else {
			finalResultChan <- nodeStatusListOrError{Error: err}
		}
	}()

	wg := &sync.WaitGroup{}
	for _, node := range n.cluster.GetNodes() {
		wg.Add(1)
		go func(node v1.NodeStatus) {
			nodeSvc := n.cluster.GetNodeServiceWithTimeout(node, 30*time.Second)
			out, err := nodeSvc.GetStatus(v1.GetNodeStatusInput{})
			if err == nil {
				singleNodeChan <- nodeStatusOrError{Node: out.Status}
			} else {
				singleNodeChan <- nodeStatusOrError{Error: err}
			}
			wg.Done()
		}(node)
	}
	wg.Wait()
	close(singleNodeChan)

	res := <-finalResultChan
	return res.Nodes, res.Error
}

func (n *NodeServiceImpl) GetStatus(input v1.GetNodeStatusInput) (v1.GetNodeStatusOutput, error) {
	status, err := n.resolveNodeStatus(context.Background())
	if err != nil {
		code := MiscError
		msg := "nodesvc: GetStatus - error loading node status"
		log.Error(msg, "code", code, "err", err)
		return v1.GetNodeStatusOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
	}
	return v1.GetNodeStatusOutput{Status: status}, nil
}

func (n *NodeServiceImpl) StatusChanged(input v1.StatusChangedInput) (v1.StatusChangedOutput, error) {
	if input.Exiting {
		n.cluster.RemoveNode(input.NodeId)
	} else {
		n.cluster.SetNode(*input.Status)
	}
	return v1.StatusChangedOutput{NodeId: input.NodeId}, nil
}

func (n *NodeServiceImpl) PlaceComponent(input v1.PlaceComponentInput) (v1.PlaceComponentOutput, error) {
	// determine if we're the placement node
	roleOk, roleNode, err := n.db.AcquireOrRenewRole(rolePlacement, n.nodeId, time.Minute)
	if err != nil {
		msg := "nodesvc: db.AcquireOrRenewRole error"
		log.Error(msg, "component", input.ComponentName, "role", rolePlacement, "err", err)
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(DbError), Message: msg}
	}
	if !roleOk {
		if roleNode == n.nodeId {
			msg := "nodesvc: db.AcquireOrRenewRole returned false, but also returned our nodeId"
			log.Error(msg, "component", input.ComponentName, "role", rolePlacement, "node", n.nodeId)
			return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(MiscError), Message: msg}
		} else {
			peerSvc := n.cluster.GetNodeServiceById(roleNode)
			if peerSvc == nil {
				msg := "nodesvc: PlaceComponent can't find peer node"
				log.Error(msg, "component", input.ComponentName, "peerNode", roleNode)
				return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(MiscError), Message: msg}
			} else {
				return peerSvc.PlaceComponent(input)
			}
		}
	}

	// get component
	comp, err := n.db.GetComponent(input.ComponentName)
	if err == NotFound {
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{
			Code:    1003,
			Message: "No Component found with name: " + input.ComponentName}
	} else if err != nil {
		code := MiscError
		msg := "nodesvc: PlaceComponent:GetComponent error"
		log.Error(msg, "component", input.ComponentName, "code", code, "err", err)
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
	}

	requiredRAM := comp.Docker.ReserveMemoryMiB
	if requiredRAM <= 0 {
		requiredRAM = 128
	}

	startTime := time.Now()
	deadline := startTime.Add(time.Minute * 3)
	for time.Now().Before(deadline) {
		placedNode, retry := n.placeComponentInternal(input.ComponentName, requiredRAM)
		if placedNode != nil {
			log.Info("nodesvc: PlaceComponent successful", "elapsedMillis", time.Now().Sub(startTime)/1e6,
				"component", input.ComponentName, "clientNode", n.nodeId, "placedNode", placedNode.NodeId)
			return v1.PlaceComponentOutput{
				ComponentName: input.ComponentName,
				Node:          *placedNode,
			}, nil
		}
		if !retry {
			code := MiscError
			msg := "nodesvc: PlaceComponent:placeComponentInternal error"
			log.Error(msg, "component", input.ComponentName, "code", code)
			return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
		}
		sleepDur := time.Millisecond * time.Duration(rand.Intn(3000))
		log.Warn("nodesvc: PlaceComponent:placeComponentInternal - will retry",
			"component", input.ComponentName, "nodeId", n.nodeId, "sleep", sleepDur)
		time.Sleep(sleepDur)
	}
	code := MiscError
	msg := "nodesvc: PlaceComponent deadline reached - component not started"
	log.Error(msg, "component", input.ComponentName, "code", code)
	return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func (n *NodeServiceImpl) placeComponentInternal(componentName string, requiredRAM int64) (*v1.NodeStatus, bool) {
	// filter nodes to subset whose total ram is > required
	nodes := make([]v1.NodeStatus, 0)

	// also look for components that may already be running this component
	nodesWithComponent := make([]v1.NodeStatus, 0)

	// track largest RAM found
	maxNodeRAM := int64(0)
	for _, n := range n.cluster.GetNodes() {
		if n.TotalMemoryMiB > requiredRAM {
			nodes = append(nodes, n)
		}
		for _, c := range n.RunningComponents {
			if c.ComponentName == componentName {
				nodesWithComponent = append(nodesWithComponent, n)
			}
		}
		if n.TotalMemoryMiB > maxNodeRAM {
			maxNodeRAM = n.TotalMemoryMiB
		}
	}

	// if we found any candidates, contact them and verify
	for _, node := range nodesWithComponent {
		output, err := n.cluster.GetNodeService(node).GetStatus(v1.GetNodeStatusInput{})
		if err == nil {
			n.cluster.SetNode(output.Status)
			for _, c := range output.Status.RunningComponents {
				if c.ComponentName == componentName {
					return &output.Status, false
				}
			}
		} else {
			log.Error("nodesvc: PlaceComponent:GetStatus failed", "component", componentName,
				"clientNode", n.nodeId, "remoteNode", node.NodeId, "peerUrl", node.PeerUrl, "err", err)
		}
	}

	// fail if component too large to place on any node
	if len(nodes) == 0 {
		log.Error("nodesvc: PlaceComponent failed - component RAM larger than max node RAM",
			"component", componentName, "requiredRAM", requiredRAM, "nodeMaxRAM", maxNodeRAM)
		return nil, false
	}

	option := BestStartComponentOption(nodes, map[string]*PlacementOption{}, componentName, requiredRAM, true)
	if option == nil {
		log.Error("nodesvc: PlaceComponent failed - BestPlacementOption returned nil",
			"component", componentName, "requiredRAM", requiredRAM, "nodeMaxRAM", maxNodeRAM, "nodeCount", len(nodes))
		return nil, false
	}

	// try first option
	node := option.TargetNode
	option.Input.ClientNodeId = n.nodeId

	output, err := n.cluster.GetNodeService(node).StartStopComponents(option.Input)

	if output.TargetStatus != nil {
		n.cluster.SetNode(*output.TargetStatus)
	}

	if err == nil {
		if output.TargetVersionMismatch && output.TargetStatus != nil {
			log.Warn("nodesvc: target version mismatch. component not started.", "req", option.Input,
				"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId)
		} else if len(output.Errors) == 0 {
			for _, c := range output.TargetStatus.RunningComponents {
				if c.ComponentName == componentName {
					// Success
					return output.TargetStatus, false
				}
			}
			log.Warn("nodesvc: started component, but node doesn't report it running",
				"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId,
				"status", output.TargetStatus)
		} else {
			// some aspect of placement failed. we'll retry with any updated cluster state
			log.Error("nodesvc: error in StartStopComponents", "errors", output.Errors,
				"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId)
		}
	} else {
		log.Error("nodesvc: error in StartStopComponents", "err", err,
			"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId)
	}

	// retry
	return nil, true
}

func (n *NodeServiceImpl) autoscale() {
	roleOk, _, err := n.db.AcquireOrRenewRole(rolePlacement, n.nodeId, time.Minute)
	if err != nil {
		log.Error("nodesvc: autoscale AcquireOrRenewRole error", "role", rolePlacement, "err", err)
		return
	}
	if !roleOk {
		return
	}

	nodes := n.cluster.GetNodes()
	componentsByName, err := loadActiveComponents(nodes, n.db)
	if err != nil {
		log.Error("nodesvc: autoscale loadActiveComponents error", "err", err)
		return
	}

	inputs := CalcAutoscalePlacement(nodes, componentsByName)

	for _, input := range inputs {
		output, err := n.cluster.GetNodeService(input.TargetNode).StartStopComponents(input.Input)
		if err == nil {
			log.Info("autoscale: StartStopComponents success", "targetNode", input.TargetNode.NodeId,
				"targetCounts", input.Input.TargetCounts)
			if output.TargetStatus != nil {
				n.cluster.SetNode(*output.TargetStatus)
			}
		} else {
			log.Error("autoscale: StartStopComponents failed", "err", err, "targetNode", input.TargetNode.NodeId,
				"input", input)
		}
	}
}

func (n *NodeServiceImpl) StartStopComponents(input v1.StartStopComponentsInput) (v1.StartStopComponentsOutput, error) {

	prevVersion := n.handlerFactory.IncrementVersion()

	if prevVersion != input.TargetVersion {
		status, err := n.resolveNodeStatus(context.Background())
		if err != nil {
			return v1.StartStopComponentsOutput{}, fmt.Errorf("nodesvc: StartStopComponents:resolveNodeStatus failed: %v", err)
		}
		return v1.StartStopComponentsOutput{
			TargetVersionMismatch: true,
			TargetStatus:          &status,
			Started:               []v1.ComponentDelta{},
			Stopped:               []v1.ComponentDelta{},
			Errors:                []v1.ComponentDeltaError{},
		}, nil
	}

	startedCount := map[string]int64{}
	stoppedCount := map[string]int64{}
	errors := make([]v1.ComponentDeltaError, 0)
	var stopRequests []v1.ComponentDelta
	var startRequests []v1.ComponentDelta

	for _, target := range input.TargetCounts {
		if target.Delta > 0 {
			startRequests = append(startRequests, target)
		} else if target.Delta < 0 {
			stopRequests = append(stopRequests, target)
		}
	}

	// stop first - in parallel
	if len(stopRequests) > 0 {
		stopWg := &sync.WaitGroup{}
		stoppedComponentNamesCh := make(chan string, len(input.TargetCounts))
		stopHandlerFx := func(target v1.ComponentDelta) {
			comp, err := n.db.GetComponent(target.ComponentName)
			if err != nil {
				msg := "error loading component: " + target.ComponentName
				log.Error("nodesvc: unable to get component", "component", target.ComponentName, "err", err)
				errors = append(errors, v1.ComponentDeltaError{ComponentDelta: target, Error: msg})
			} else {
				_, stopped, err := n.handlerFactory.ConvergeToTarget(target, comp, false)
				if err != nil {
					msg := "handlerFactory.ConvergeToTarget stop error"
					log.Error("nodesvc: handlerFactory.ConvergeToTarget stop", "target", target, "err", err)
					errors = append(errors, v1.ComponentDeltaError{ComponentDelta: target, Error: msg})
				}
				if stopped > 0 {
					stoppedComponentNamesCh <- target.ComponentName
				}
			}
			stopWg.Done()
		}
		for _, target := range stopRequests {
			if target.Delta < 0 {
				stopWg.Add(1)
				go stopHandlerFx(target)
			}
		}
		stopWg.Wait()
		close(stoppedComponentNamesCh)
		for componentName := range stoppedComponentNamesCh {
			count := stoppedCount[componentName]
			stoppedCount[componentName] = count + 1
		}
	}

	// then start
	for _, target := range startRequests {
		comp, err := n.db.GetComponent(target.ComponentName)
		if err != nil {
			msg := "error loading component: " + target.ComponentName
			log.Error("nodesvc: unable to get component", "component", target.ComponentName, "err", err)
			errors = append(errors, v1.ComponentDeltaError{ComponentDelta: target, Error: msg})
		} else {
			started, _, err := n.handlerFactory.ConvergeToTarget(target, comp, true)
			if err != nil {
				msg := "handlerFactory.ConvergeToTarget start error"
				log.Error("nodesvc: handlerFactory.ConvergeToTarget start", "target", target, "err", err)
				errors = append(errors, v1.ComponentDeltaError{ComponentDelta: target, Error: msg})
			} else {
				count := startedCount[target.ComponentName]
				startedCount[target.ComponentName] = count + int64(started)
			}
		}
	}

	started := make([]v1.ComponentDelta, 0)
	stopped := make([]v1.ComponentDelta, 0)
	for name, count := range startedCount {
		started = append(started, v1.ComponentDelta{ComponentName: name, Delta: count})
	}
	for name, count := range stoppedCount {
		stopped = append(stopped, v1.ComponentDelta{ComponentName: name, Delta: -1 * count})
	}

	status, err := n.resolveNodeStatus(context.Background())
	if err != nil {
		return v1.StartStopComponentsOutput{}, fmt.Errorf("nodesvc: StartStopComponents:resolveNodeStatus failed: %v", err)
	}

	n.cluster.SetNode(status)

	return v1.StartStopComponentsOutput{
		TargetVersionMismatch: false,
		TargetStatus:          &status,
		Started:               started,
		Stopped:               stopped,
		Errors:                errors,
	}, nil
}

func (n *NodeServiceImpl) RunAutoscaleLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.Tick(interval)
	for {
		select {
		case <-ticker:
			n.autoscale()
		case <-ctx.Done():
			log.Info("nodesvc: autoscale loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) RunNodeStatusLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	logTicker := time.Tick(interval)
	n.logStatusAndRefreshClusterNodeList(ctx)
	for {
		select {
		case <-logTicker:
			n.logStatusAndRefreshClusterNodeList(ctx)
		case <-ctx.Done():
			if n.nodeId != "" {
				_, err := n.db.RemoveNodeStatus(n.nodeId)
				if err != nil {
					log.Error("nodesvc: error removing status", "err", err, "nodeId", n.nodeId)
				}
				n.cluster.RemoveAndBroadcast()
			}
			log.Info("nodesvc: status loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) logStatusAndRefreshClusterNodeList(ctx context.Context) {
	err := n.logStatus(ctx)
	if err != nil && !docker.IsErrContainerNotFound(err) {
		log.Error("nodesvc: error logging status", "err", err)
	}

	allNodes, err := n.db.ListNodeStatus()
	if err != nil {
		// don't update cluster
		log.Error("nodesvc: error listing nodes", "err", err)
		return
	}
	n.cluster.SetAllNodes(allNodes)
}

func (n *NodeServiceImpl) logStatus(ctx context.Context) error {
	status, err := n.resolveNodeStatus(ctx)
	if err != nil {
		return err
	}
	return n.db.PutNodeStatus(status)
}

func (n *NodeServiceImpl) resolveAndBroadcastNodeStatus(ctx context.Context) (v1.NodeStatus, error) {
	status, err := n.resolveNodeStatus(ctx)
	if err != nil {
		return status, err
	}

	err = n.db.PutNodeStatus(status)
	if err != nil {
		return status, err
	}

	err = n.cluster.SetAndBroadcastStatus(status)
	return status, err
}

func (n *NodeServiceImpl) resolveNodeStatus(ctx context.Context) (v1.NodeStatus, error) {

	components, version := n.handlerFactory.HandlerComponentInfo()
	nodeStatus := v1.NodeStatus{
		NodeId:            n.nodeId,
		PeerUrl:           n.peerUrl,
		StartedAt:         n.startTimeMillis,
		ObservedAt:        common.NowMillis(),
		NumCPUs:           n.numCPUs,
		Version:           version,
		RunningComponents: components,
	}

	meminfo, err := linuxproc.ReadMemInfo("/proc/meminfo")
	if err != nil {
		return v1.NodeStatus{}, fmt.Errorf("ReadMemInfo error: %v", err)
	}
	nodeStatus.TotalMemoryMiB = int64(meminfo.MemTotal / 1024)
	nodeStatus.FreeMemoryMiB = int64(meminfo.MemAvailable / 1024)

	if n.totalMemAllowed >= 0 {
		delta := nodeStatus.TotalMemoryMiB - n.totalMemAllowed
		if delta > 0 {
			nodeStatus.TotalMemoryMiB -= delta
			nodeStatus.FreeMemoryMiB -= delta
			if nodeStatus.FreeMemoryMiB < 0 {
				nodeStatus.FreeMemoryMiB = 0
			}
		}
	}

	loadavg, err := linuxproc.ReadLoadAvg("/proc/loadavg")
	if err != nil {
		return v1.NodeStatus{}, fmt.Errorf("ReadLoadAvg error: %v", err)
	}
	nodeStatus.LoadAvg1m = loadavg.Last1Min
	nodeStatus.LoadAvg5m = loadavg.Last5Min
	nodeStatus.LoadAvg15m = loadavg.Last15Min

	return nodeStatus, nil
}
