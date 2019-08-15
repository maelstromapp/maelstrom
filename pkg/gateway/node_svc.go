package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sync"
	"time"
)

func NewNodeServiceImpl(handlerFactory *DockerHandlerFactory, db v1.Db, dockerClient *docker.Client, nodeId string,
	peerUrl string, startTime time.Time, totalMemoryMB int64, numCPUs int64) *NodeServiceImpl {
	return &NodeServiceImpl{
		handlerFactory:     handlerFactory,
		db:                 db,
		dockerClient:       dockerClient,
		nodeId:             nodeId,
		peerUrl:            peerUrl,
		startTimeMillis:    common.TimeToMillis(startTime),
		totalMemoryMB:      totalMemoryMB,
		numCPUs:            numCPUs,
		statsByContainerId: map[string]containerStats{},
		loadStatusLock:     &sync.Mutex{},
	}
}

func NewNodeServiceImplFromDocker(handlerFactory *DockerHandlerFactory, db v1.Db, dockerClient *docker.Client,
	peerUrl string) (*NodeServiceImpl, error) {

	info, err := dockerClient.Info(context.Background())
	if err != nil {
		return nil, err
	}

	return NewNodeServiceImpl(handlerFactory, db, dockerClient, info.ID, peerUrl, time.Now(), info.MemTotal/1024/1024,
		int64(info.NCPU)), nil
}

type NodeServiceImpl struct {
	handlerFactory     *DockerHandlerFactory
	dockerClient       *docker.Client
	db                 v1.Db
	nodeId             string
	peerUrl            string
	startTimeMillis    int64
	totalMemoryMB      int64
	numCPUs            int64
	statsByContainerId map[string]containerStats
	loadStatusLock     *sync.Mutex
}

func (n *NodeServiceImpl) LogPairs() []interface{} {
	return []interface{}{"nodeId", n.nodeId, "peerUrl", n.peerUrl, "totalMemoryMB", n.totalMemoryMB,
		"numCPUs", n.numCPUs}
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

func (n *NodeServiceImpl) StartStopComponents(input v1.StartStopComponentsInput) (v1.StartStopComponentsOutput, error) {
	startedCount := map[string]int64{}
	stoppedCount := map[string]int64{}
	errors := make([]v1.ComponentCountError, 0)

	// stop first - in parallel
	stopWg := &sync.WaitGroup{}
	stoppedComponentNamesCh := make(chan string, len(input.TargetCounts))
	stopHandlerFx := func(componentName string) {
		if n.handlerFactory.StopHandler(componentName, false) {
			stoppedComponentNamesCh <- componentName
		}
		stopWg.Done()
	}
	for _, target := range input.TargetCounts {
		if target.Count <= 0 {
			stopWg.Add(1)
			go stopHandlerFx(target.ComponentName)
		}
	}
	stopWg.Wait()
	close(stoppedComponentNamesCh)
	for componentName := range stoppedComponentNamesCh {
		count := stoppedCount[componentName]
		stoppedCount[componentName] = count + 1
	}

	// then start
	for _, target := range input.TargetCounts {
		if target.Count > 0 {
			comp, err := n.db.GetComponent(target.ComponentName)
			if err != nil {
				msg := "component not found"
				log.Error("nodesvc: unable to get component", "component", target.ComponentName, "err", err)
				errors = append(errors, v1.ComponentCountError{ComponentCount: target, Error: msg})
			} else {
				err = n.handlerFactory.EnsureHandler(comp, true)
				if err != nil {
					msg := "handlerFactory.EnsureHandler error"
					log.Error("nodesvc: handlerFactory.EnsureHandler", "component", target.ComponentName, "err", err)
					errors = append(errors, v1.ComponentCountError{ComponentCount: target, Error: msg})
				} else {
					count := startedCount[target.ComponentName]
					startedCount[target.ComponentName] = count + 1
				}
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

	return v1.StartStopComponentsOutput{
		Started: started,
		Stopped: stopped,
		Errors:  errors,
	}, nil
}

func (n *NodeServiceImpl) RunNodeStatusLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	done := ctx.Done()
	logTicker := time.Tick(interval)
	for {
		select {
		case <-logTicker:
			err := n.logStatus(ctx)
			if err != nil {
				log.Error("nodesvc: error logging status", "err", err)
			}
		case <-done:
			if n.nodeId != "" {
				_, err := n.db.RemoveNodeStatus(n.nodeId)
				if err != nil {
					log.Error("nodesvc: error removing status", "err", err, "nodeId", n.nodeId)
				}
			}
			log.Info("nodesvc: node status loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) logStatus(ctx context.Context) error {
	status, err := n.loadNodeStatus(true, true, ctx)
	if err != nil {
		return err
	}
	return n.db.PutNodeStatus(status)
}

func (n *NodeServiceImpl) loadNodeStatus(includeContainerStatus bool, mutateLocalStats bool,
	ctx context.Context) (v1.NodeStatus, error) {

	nodeStatus := v1.NodeStatus{
		NodeId:            n.nodeId,
		PeerUrl:           n.peerUrl,
		StartedAt:         n.startTimeMillis,
		ObservedAt:        common.NowMillis(),
		TotalMemoryMB:     n.totalMemoryMB,
		NumCPUs:           n.numCPUs,
		TotalCPUPct:       0,
		RunningComponents: n.handlerFactory.HandlerComponentNames(),
		Containers:        nil,
	}

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
				ContainerId:      c.ID,
				Image:            c.Image,
				ImageId:          c.ImageID,
				StartedAt:        c.Created * 1000,
				ComponentName:    c.Labels["maelstrom_component"],
				ComponentVersion: int64(common.ToIntOrDefault(c.Labels["maelstrom_version"], 0)),
				CPUPct:           0,
				MemoryUsedMB:     0,
				MemoryPeakMB:     0,
			}

			stats, err := n.dockerClient.ContainerStats(ctx, c.ID, false)
			if err != nil {
				return v1.NodeStatus{}, fmt.Errorf("docker.ContainerStats error: %v", err)
			}
			var containerStats types.StatsJSON
			err = json.NewDecoder(stats.Body).Decode(&containerStats)
			if err != nil {
				return v1.NodeStatus{}, fmt.Errorf("json decode error: %v", err)
			}
			err = stats.Body.Close()
			if err != nil {
				return v1.NodeStatus{}, fmt.Errorf("json body close error: %v", err)
			}

			if contStats.systemCPU > 0 {
				maelStats.CPUPct = calculateCPUPercent(contStats.containerCPU, contStats.systemCPU, &containerStats)
			}

			maelStats.MemoryUsedMB = int64(containerStats.MemoryStats.Usage / 1024 / 1024)
			if maelStats.MemoryUsedMB > contStats.memoryPeakMB {
				contStats.memoryPeakMB = maelStats.MemoryUsedMB
			}
			maelStats.MemoryPeakMB = contStats.memoryPeakMB
			contStats.containerCPU = containerStats.CPUStats.CPUUsage.TotalUsage
			contStats.systemCPU = containerStats.CPUStats.SystemUsage
			n.statsByContainerId[c.ID] = contStats

			nodeStatus.Containers = append(nodeStatus.Containers, maelStats)
			nodeStatus.TotalCPUPct += maelStats.CPUPct
		}
		nodeStatus.TotalCPUPct = nodeStatus.TotalCPUPct / float64(nodeStatus.NumCPUs)
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
