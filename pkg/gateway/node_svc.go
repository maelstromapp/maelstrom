package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	linuxproc "github.com/c9s/goprocinfo/linux"
	"github.com/coopernurse/barrister-go"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io/ioutil"
	"math/rand"
	"sync"
	"time"
)

func NewNodeServiceImpl(handlerFactory *DockerHandlerFactory, db v1.Db, dockerClient *docker.Client, nodeId string,
	peerUrl string, startTime time.Time, numCPUs int64) (*NodeServiceImpl, error) {
	nodeSvc := &NodeServiceImpl{
		handlerFactory:     handlerFactory,
		db:                 db,
		dockerClient:       dockerClient,
		nodeId:             nodeId,
		peerUrl:            peerUrl,
		startTimeMillis:    common.TimeToMillis(startTime),
		numCPUs:            numCPUs,
		statsByContainerId: map[string]containerStats{},
		loadStatusLock:     &sync.Mutex{},
	}
	status, err := nodeSvc.loadNodeStatus(true, false, context.Background())
	if err != nil {
		return nil, fmt.Errorf("nodesvc: unable to load node status: %v", err)
	}
	cluster := NewCluster(nodeId, nodeSvc)
	cluster.SetNode(status)
	nodeSvc.cluster = cluster
	return nodeSvc, nil
}

func NewNodeServiceImplFromDocker(handlerFactory *DockerHandlerFactory, db v1.Db, dockerClient *docker.Client,
	peerUrl string) (*NodeServiceImpl, error) {

	info, err := dockerClient.Info(context.Background())
	if err != nil {
		return nil, err
	}

	return NewNodeServiceImpl(handlerFactory, db, dockerClient, info.ID, peerUrl, time.Now(), int64(info.NCPU))
}

type NodeServiceImpl struct {
	handlerFactory     *DockerHandlerFactory
	dockerClient       *docker.Client
	db                 v1.Db
	cluster            *Cluster
	nodeId             string
	peerUrl            string
	startTimeMillis    int64
	numCPUs            int64
	statsByContainerId map[string]containerStats
	loadStatusLock     *sync.Mutex
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

func (n *NodeServiceImpl) GetStatus(input v1.GetNodeStatusInput) (v1.GetNodeStatusOutput, error) {
	status, err := n.loadNodeStatus(input.IncludeContainerStatus, false, context.Background())
	if err != nil {
		code := v1.MiscError
		msg := "nodesvc: GetStatus - error loading node status"
		log.Error(msg, "code", code, "err", err)
		return v1.GetNodeStatusOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
	}
	return v1.GetNodeStatusOutput{Status: status}, nil
}

func (n *NodeServiceImpl) PlaceComponent(input v1.PlaceComponentInput) (v1.PlaceComponentOutput, error) {
	// get component
	comp, err := n.db.GetComponent(input.ComponentName)
	if err == v1.NotFound {
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{
			Code:    1003,
			Message: "No Component found with name: " + input.ComponentName}
	} else if err != nil {
		code := v1.MiscError
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
			code := v1.MiscError
			msg := "nodesvc: PlaceComponent:placeComponentInternal error"
			log.Error(msg, "component", input.ComponentName, "code", code)
			return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
		}
		sleepDur := time.Millisecond * time.Duration(rand.Intn(3000))
		log.Warn("nodesvc: PlaceComponent:placeComponentInternal - will retry",
			"component", input.ComponentName, "nodeId", n.nodeId, "sleep", sleepDur)
		time.Sleep(sleepDur)
	}
	code := v1.MiscError
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
		output, err := n.cluster.GetNodeService(node).GetStatus(v1.GetNodeStatusInput{IncludeContainerStatus: true})
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

	option := BestPlacementOption(nodes, componentName, requiredRAM)
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
	for _, node := range n.cluster.GetNodes() {
		var targetCounts []v1.ComponentCount
		countByComponent := make(map[string]int)
		deltaByComponent := make(map[string]int)
		for _, info := range node.RunningComponents {
			c, err := n.db.GetComponent(info.ComponentName)
			if err == nil {
				countByComponent[info.ComponentName] = countByComponent[info.ComponentName] + 1
				minTime := common.TimeToMillis(time.Now().Add(-1 * time.Duration(c.Docker.IdleTimeoutSeconds) * time.Second))
				if info.LastRequestTime < minTime {
					deltaByComponent[info.ComponentName] = deltaByComponent[info.ComponentName] - 1
				}
			} else {
				log.Error("nodesvc: autoscale error getting component", "err", err, "component", info.ComponentName)
			}
		}

		for componentName, delta := range deltaByComponent {
			targetCounts = append(targetCounts, v1.ComponentCount{
				ComponentName: componentName,
				Count:         int64(countByComponent[componentName] + delta),
			})
		}

		if len(targetCounts) > 0 {
			input := v1.StartStopComponentsInput{
				ClientNodeId:  n.nodeId,
				TargetVersion: node.Version,
				TargetCounts:  targetCounts,
				ReturnStatus:  true,
			}
			output, err := n.cluster.GetNodeService(node).StartStopComponents(input)
			if err == nil {
				log.Info("autoscale: StartStopComponents success", "targetNode", node.NodeId,
					"targetCounts", targetCounts)
				if output.TargetStatus != nil {
					n.cluster.SetNode(*output.TargetStatus)
				}
			} else {
				log.Error("autoscale: StartStopComponents failed", "err", err, "targetNode", node.NodeId,
					"input", input)
			}
		}
	}
}

func (n *NodeServiceImpl) StartStopComponents(input v1.StartStopComponentsInput) (v1.StartStopComponentsOutput, error) {

	handlers, version := n.handlerFactory.HandlerComponentInfo()

	if version != input.TargetVersion {
		status, err := n.loadNodeStatus(true, false, context.Background())
		if err != nil {
			return v1.StartStopComponentsOutput{}, fmt.Errorf("nodesvc: StartStopComponents:loadNodeStatus failed: %v", err)
		}
		return v1.StartStopComponentsOutput{
			TargetVersionMismatch: true,
			TargetStatus:          &status,
			Started:               []v1.ComponentCount{},
			Stopped:               []v1.ComponentCount{},
			Errors:                []v1.ComponentCountError{},
		}, nil
	}

	startedCount := map[string]int64{}
	stoppedCount := map[string]int64{}
	var errors []v1.ComponentCountError
	var stopRequests []v1.ComponentCount
	var startRequests []v1.ComponentCount
	countsByComponent := map[string]int{}

	for _, handler := range handlers {
		count := countsByComponent[handler.ComponentName]
		countsByComponent[handler.ComponentName] = count + 1
	}

	for _, target := range input.TargetCounts {
		currentCount := int64(countsByComponent[target.ComponentName])
		if target.Count > currentCount {
			startRequests = append(startRequests, target)
		} else if target.Count < currentCount {
			stopRequests = append(stopRequests, target)
		}
	}

	// stop first - in parallel
	if len(stopRequests) > 0 {
		stopWg := &sync.WaitGroup{}
		stoppedComponentNamesCh := make(chan string, len(input.TargetCounts))
		stopHandlerFx := func(target v1.ComponentCount) {
			comp, err := n.db.GetComponent(target.ComponentName)
			if err != nil {
				msg := "error loading component: " + target.ComponentName
				log.Error("nodesvc: unable to get component", "component", target.ComponentName, "err", err)
				errors = append(errors, v1.ComponentCountError{ComponentCount: target, Error: msg})
			} else {
				_, stopped, err := n.handlerFactory.ConvergeToTarget(target, comp, false)
				if err != nil {
					msg := "handlerFactory.ConvergeToTarget stop error"
					log.Error("nodesvc: handlerFactory.ConvergeToTarget stop", "target", target, "err", err)
					errors = append(errors, v1.ComponentCountError{ComponentCount: target, Error: msg})
				}
				if stopped > 0 {
					stoppedComponentNamesCh <- target.ComponentName
				}
			}
			stopWg.Done()
		}
		for _, target := range stopRequests {
			if target.Count <= 0 {
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
			errors = append(errors, v1.ComponentCountError{ComponentCount: target, Error: msg})
		} else {
			started, _, err := n.handlerFactory.ConvergeToTarget(target, comp, true)
			if err != nil {
				msg := "handlerFactory.ConvergeToTarget start error"
				log.Error("nodesvc: handlerFactory.ConvergeToTarget start", "target", target, "err", err)
				errors = append(errors, v1.ComponentCountError{ComponentCount: target, Error: msg})
			} else {
				count := startedCount[target.ComponentName]
				startedCount[target.ComponentName] = count + int64(started)
			}
		}
	}

	started := make([]v1.ComponentCount, 0)
	stopped := make([]v1.ComponentCount, 0)
	for name, count := range startedCount {
		started = append(started, v1.ComponentCount{ComponentName: name, Count: count})
	}
	for name, count := range stoppedCount {
		stopped = append(stopped, v1.ComponentCount{ComponentName: name, Count: count})
	}

	status, err := n.loadNodeStatus(true, false, context.Background())
	if err != nil {
		return v1.StartStopComponentsOutput{}, fmt.Errorf("nodesvc: StartStopComponents:loadNodeStatus failed: %v", err)
	}

	return v1.StartStopComponentsOutput{
		TargetStatus: &status,
		Started:      started,
		Stopped:      stopped,
		Errors:       errors,
	}, nil
}

func (n *NodeServiceImpl) RunAutoscaleLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	done := ctx.Done()
	ticker := time.Tick(interval)
	for {
		select {
		case <-ticker:
			n.autoscale()
		case <-done:
			log.Info("nodesvc: autoscale loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) RunNodeStatusLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	done := ctx.Done()
	logTicker := time.Tick(interval)
	n.logStatusAndRefreshClusterNodeList(ctx)
	for {
		select {
		case <-logTicker:
			n.logStatusAndRefreshClusterNodeList(ctx)
		case <-done:
			if n.nodeId != "" {
				_, err := n.db.RemoveNodeStatus(n.nodeId)
				if err != nil {
					log.Error("nodesvc: error removing status", "err", err, "nodeId", n.nodeId)
				}
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
	input := v1.ListNodeStatusInput{
		Limit:     1000,
		NextToken: "",
	}
	removeNodeIds := map[string]bool{}
	for _, node := range n.cluster.GetNodes() {
		removeNodeIds[node.NodeId] = true
	}
	running := true
	for running {
		output, err := n.db.ListNodeStatus(input)
		if err == nil {
			for _, node := range output.Nodes {
				n.cluster.SetNode(node)
				delete(removeNodeIds, node.NodeId)
			}
			input.NextToken = output.NextToken
			running = input.NextToken != ""
		} else {
			log.Error("nodesvc: error listing nodes", "err", err)
			running = false
			removeNodeIds = map[string]bool{}
		}
	}
	for nodeId, _ := range removeNodeIds {
		n.cluster.RemoveNode(nodeId)
	}
}

func (n *NodeServiceImpl) logStatus(ctx context.Context) error {
	start := time.Now()
	log.Info("logStatus start")
	status, err := n.loadNodeStatus(true, true, ctx)
	if err != nil {
		return err
	}
	log.Info("logStatus   end", "elapsed", time.Now().Sub(start).String())
	return n.db.PutNodeStatus(status)
}

func (n *NodeServiceImpl) loadNodeStatus(includeContainerStatus bool, mutateLocalStats bool,
	ctx context.Context) (v1.NodeStatus, error) {

	components, version := n.handlerFactory.HandlerComponentInfo()
	nodeStatus := v1.NodeStatus{
		NodeId:            n.nodeId,
		PeerUrl:           n.peerUrl,
		StartedAt:         n.startTimeMillis,
		ObservedAt:        common.NowMillis(),
		NumCPUs:           n.numCPUs,
		Version:           version,
		RunningComponents: components,
		Containers:        nil,
	}

	meminfo, err := linuxproc.ReadMemInfo("/proc/meminfo")
	if err != nil {
		return v1.NodeStatus{}, fmt.Errorf("ReadMemInfo error: %v", err)
	}
	nodeStatus.TotalMemoryMiB = int64(meminfo.MemTotal / 1024)
	nodeStatus.FreeMemoryMiB = int64(meminfo.MemAvailable / 1024)

	loadavg, err := linuxproc.ReadLoadAvg("/proc/loadavg")
	if err != nil {
		return v1.NodeStatus{}, fmt.Errorf("ReadLoadAvg error: %v", err)
	}
	nodeStatus.LoadAvg1m = loadavg.Last1Min
	nodeStatus.LoadAvg5m = loadavg.Last5Min
	nodeStatus.LoadAvg15m = loadavg.Last15Min

	if includeContainerStatus {
		n.loadStatusLock.Lock()
		defer n.loadStatusLock.Unlock()

		nodeStatus.Containers = make([]v1.ContainerStatus, 0)

		containers, err := n.dockerClient.ContainerList(ctx, types.ContainerListOptions{})
		if err != nil {
			return v1.NodeStatus{}, fmt.Errorf("docker.ContainerList error: %v", err)
		}

		for _, c := range containers {
			contStats := n.statsByContainerId[c.ID]
			maelStats := v1.ContainerStatus{
				ContainerId:       c.ID,
				Image:             c.Image,
				ImageId:           c.ImageID,
				StartedAt:         c.Created * 1000,
				ComponentName:     c.Labels["maelstrom_component"],
				ComponentVersion:  int64(common.ToIntOrDefault(c.Labels["maelstrom_version"], 0)),
				CPUPct:            0,
				MemoryReservedMiB: 0,
				MemoryUsedMiB:     0,
				MemoryPeakMiB:     0,
				TotalRequests:     0,
				LastRequestTime:   0,
			}

			compInfo := n.handlerFactory.GetComponentInfo(maelStats.ComponentName, c.ID)
			if compInfo.TotalRequests > 0 {
				maelStats.TotalRequests = compInfo.TotalRequests
				maelStats.LastRequestTime = compInfo.LastRequestTime
			}

			containerInfo, err := n.dockerClient.ContainerInspect(ctx, c.ID)
			if err != nil {
				if !docker.IsErrContainerNotFound(err) {
					err = fmt.Errorf("docker.ContainerInspect error: %v", err)
				}
				return v1.NodeStatus{}, err
			}
			maelStats.MemoryReservedMiB = containerInfo.HostConfig.MemoryReservation / 1024 / 1024

			stats, err := n.dockerClient.ContainerStats(ctx, c.ID, false)
			if err != nil {
				if !docker.IsErrContainerNotFound(err) {
					err = fmt.Errorf("docker.ContainerInspect error: %v", err)
				}
				return v1.NodeStatus{}, err
			}
			var containerStats types.StatsJSON
			body, err := ioutil.ReadAll(stats.Body)
			if err != nil {
				return v1.NodeStatus{}, fmt.Errorf("json stats ReadAll containerId: %s error: %v", c.ID, err)
			}
			if len(body) > 0 {
				err = json.Unmarshal(body, &containerStats)
				if err != nil {
					return v1.NodeStatus{}, fmt.Errorf("json unmarshal containerId: %s json: %s error: %v", c.ID,
						string(body), err)
				}
			}
			err = stats.Body.Close()
			if err != nil {
				return v1.NodeStatus{}, fmt.Errorf("json body close error: %v", err)
			}

			if contStats.systemCPU > 0 {
				maelStats.CPUPct = calculateCPUPercent(contStats.containerCPU, contStats.systemCPU, &containerStats)
			}

			maelStats.MemoryUsedMiB = int64(containerStats.MemoryStats.Usage / 1024 / 1024)
			if maelStats.MemoryUsedMiB > contStats.memoryPeakMB {
				contStats.memoryPeakMB = maelStats.MemoryUsedMiB
			}
			maelStats.MemoryPeakMiB = contStats.memoryPeakMB
			contStats.containerCPU = containerStats.CPUStats.CPUUsage.TotalUsage
			contStats.systemCPU = containerStats.CPUStats.SystemUsage
			n.statsByContainerId[c.ID] = contStats

			nodeStatus.Containers = append(nodeStatus.Containers, maelStats)
		}
	}

	return nodeStatus, nil
}

/////////////////////////////////////////////////

type containerStats struct {
	containerCPU uint64
	systemCPU    uint64
	memoryPeakMB int64
}

func calculateCPUPercent(previousCPU, previousSystem uint64, v *types.StatsJSON) float64 {
	var (
		cpuPercent = 0.0
		// calculate the change for the cpu usage of the container in between readings
		cpuDelta = float64(v.CPUStats.CPUUsage.TotalUsage) - float64(previousCPU)
		// calculate the change for the entire system between readings
		systemDelta = float64(v.CPUStats.SystemUsage) - float64(previousSystem)
	)

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(len(v.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}
	return cpuPercent
}
