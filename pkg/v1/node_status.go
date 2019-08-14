package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"time"
)

func NewNodeStatusLogger(dockerClient *docker.Client, db Db, logInterval time.Duration,
	ctx context.Context) *NodeStatusLogger {
	return &NodeStatusLogger{
		dockerClient:       dockerClient,
		db:                 db,
		logInterval:        logInterval,
		ctx:                ctx,
		statsByContainerId: map[string]containerStats{},
	}
}

type NodeStatusLogger struct {
	dockerClient       *docker.Client
	db                 Db
	logInterval        time.Duration
	ctx                context.Context
	statsByContainerId map[string]containerStats
	nodeId             string
}

func (n *NodeStatusLogger) Run() {
	done := n.ctx.Done()
	logTicker := time.Tick(n.logInterval)
	for {
		select {
		case <-logTicker:
			err := n.logStatus()
			if err != nil {
				log.Error("nodestatus: error logging status", "err", err)
			}
		case <-done:
			if n.nodeId != "" {
				_, err := n.db.RemoveNodeStatus(n.nodeId)
				if err != nil {
					log.Error("nodestatus: error removing status", "err", err, "nodeId", n.nodeId)
				}
			}
			log.Info("nodestatus: logger exiting gracefully")
			return
		}
	}
}

func (n *NodeStatusLogger) logStatus() error {
	status, err := n.loadNodeStatus()
	if err != nil {
		return err
	}
	return n.db.PutNodeStatus(status)
}

func (n *NodeStatusLogger) loadNodeStatus() (NodeStatus, error) {
	info, err := n.dockerClient.Info(n.ctx)
	if err != nil {
		return NodeStatus{}, fmt.Errorf("docker.Info error: %v", err)
	}

	n.nodeId = info.ID

	nodeStatus := NodeStatus{
		NodeId:        info.ID,
		ModifiedAt:    common.NowMillis(),
		TotalMemoryMB: info.MemTotal / 1024 / 1024,
		NumCPUs:       int64(info.NCPU),
		TotalCPUPct:   0,
		Containers:    make([]ContainerStatus, 0),
	}

	containers, err := n.dockerClient.ContainerList(n.ctx, types.ContainerListOptions{})
	if err != nil {
		return NodeStatus{}, fmt.Errorf("docker.ContainerList error: %v", err)
	}

	for _, c := range containers {
		contStats := n.statsByContainerId[c.ID]
		maelStats := ContainerStatus{
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

		stats, err := n.dockerClient.ContainerStats(n.ctx, c.ID, false)
		if err != nil {
			return NodeStatus{}, fmt.Errorf("docker.ContainerStats error: %v", err)
		}
		var containerStats types.StatsJSON
		err = json.NewDecoder(stats.Body).Decode(&containerStats)
		if err != nil {
			return NodeStatus{}, fmt.Errorf("json decode error: %v", err)
		}
		err = stats.Body.Close()
		if err != nil {
			return NodeStatus{}, fmt.Errorf("json body close error: %v", err)
		}

		if contStats.systemCPU > 0 {
			maelStats.CPUPct = calculateCPUPercent(contStats.containerCPU, contStats.systemCPU, &containerStats)
		}

		contStats.containerCPU = containerStats.CPUStats.CPUUsage.TotalUsage
		contStats.systemCPU = containerStats.CPUStats.SystemUsage
		n.statsByContainerId[c.ID] = contStats

		maelStats.MemoryUsedMB = int64(containerStats.MemoryStats.Usage / 1024 / 1024)
		if maelStats.MemoryUsedMB > maelStats.MemoryPeakMB {
			maelStats.MemoryPeakMB = maelStats.MemoryUsedMB
		}
		nodeStatus.Containers = append(nodeStatus.Containers, maelStats)
		nodeStatus.TotalCPUPct += maelStats.CPUPct
	}
	nodeStatus.TotalCPUPct = nodeStatus.TotalCPUPct / float64(nodeStatus.NumCPUs)
	return nodeStatus, nil
}

/////////////////////////////////////////////////

type containerStats struct {
	containerCPU uint64
	systemCPU    uint64
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
