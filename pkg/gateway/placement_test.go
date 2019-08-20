package gateway

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"testing/quick"
	"time"
)

var defaultRand = rand.New(rand.NewSource(time.Now().UnixNano()))

const ramRequiredMedium = 1000
const componentA = "a"

func randComponent(componentNum int, maxRam int64, rand *rand.Rand) v1.ComponentInfo {
	reserveMiB := (128 * rand.Int63n(16)) + 128
	if reserveMiB > maxRam {
		reserveMiB = maxRam
	}
	return v1.ComponentInfo{
		ComponentName:     strconv.Itoa(componentNum),
		MemoryReservedMiB: reserveMiB,
		LastRequestTime:   common.TimeToMillis(time.Now().Add(-1 * time.Second * time.Duration(rand.Intn(3600)))),
	}
}

func randComponents(maxComponents int, totalMemoryMiB int64, rand *rand.Rand) []v1.ComponentInfo {
	comps := make([]v1.ComponentInfo, 0)
	memoryAvail := totalMemoryMiB
	for i := 0; i < maxComponents && memoryAvail > 0; i++ {
		comp := randComponent(i, memoryAvail, rand)
		memoryAvail -= comp.MemoryReservedMiB
		comps = append(comps, comp)
	}
	return comps
}

func randNodeWithComponents(nodeNum int, maxComponents int, maxRam int64, rand *rand.Rand) v1.NodeStatus {
	totalMemoryMiB := rand.Int63n(16000) + 128
	if totalMemoryMiB > maxRam {
		totalMemoryMiB = maxRam
	}
	return v1.NodeStatus{
		NodeId:            fmt.Sprintf("node-%d", nodeNum),
		StartedAt:         common.TimeToMillis(time.Now()) - rand.Int63n(100000),
		ObservedAt:        common.TimeToMillis(time.Now()),
		Version:           rand.Int63n(1000000) + 1,
		PeerUrl:           fmt.Sprintf("http://%d.example.org/", nodeNum),
		TotalMemoryMiB:    totalMemoryMiB,
		FreeMemoryMiB:     0,
		NumCPUs:           0,
		LoadAvg1m:         float64(rand.Intn(99999999)),
		LoadAvg5m:         0,
		LoadAvg15m:        0,
		RunningComponents: randComponents(maxComponents, totalMemoryMiB, rand),
		Containers:        nil,
	}
}

func randNode(nodeNum int, rand *rand.Rand) v1.NodeStatus {
	return randNodeWithComponents(nodeNum, rand.Intn(25), 16000, rand)
}

type NodeList []v1.NodeStatus

func (n NodeList) Generate(rand *rand.Rand, size int) reflect.Value {
	nodes := make([]v1.NodeStatus, size+1)
	nodes[0] = randNode(0, rand)
	nodes[0].TotalMemoryMiB = ramRequiredMedium * 2
	for i := 1; i <= size; i++ {
		nodes[i] = randNode(i, rand)
	}
	return reflect.ValueOf(nodes)
}

type FullNodeList []v1.NodeStatus

func (n FullNodeList) Generate(rand *rand.Rand, size int) reflect.Value {
	nodes := make([]v1.NodeStatus, size)
	for i := 0; i < size; i++ {
		nodes[i] = randNodeWithComponents(i, 30, ramRequiredMedium, rand)
	}
	return reflect.ValueOf(nodes)
}

func scaleDownCount(counts []v1.ComponentCount) int {
	count := 0
	for _, c := range counts {
		if c.Count == 0 {
			count++
		}
	}
	return count
}

/////////////////////////////////////////////////////////////////////////////

func TestBestPlacementOptionNeverReturnsNilIfNodeHasEnoughRAM(t *testing.T) {
	f := func(nl NodeList) bool {
		return BestPlacementOption(nl, componentA, ramRequiredMedium) != nil
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func TestBestPlacementOptionNeverStopsContainerIfNodeHasEnoughUnreservedRAM(t *testing.T) {
	f := func(nl FullNodeList) bool {
		// make sure one node has enough free ram to avoid stopping a container
		if len(nl[0].RunningComponents) > 0 {
			nl[0].TotalMemoryMiB = ramRequiredMedium + 50
			nl[0].RunningComponents = nl[0].RunningComponents[0:1]
			nl[0].RunningComponents[0].MemoryReservedMiB = 10
		}
		option := BestPlacementOption(nl, componentA, ramRequiredMedium)
		return option != nil && scaleDownCount(option.Input.TargetCounts) == 0
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func TestBestPlacementOptionNoNodeWithEnoughRam(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand)}
	nodes[0].TotalMemoryMiB = ramRequiredMedium - 1
	assert.Nil(t, BestPlacementOption(nodes, componentA, ramRequiredMedium))
}

func TestBestPlacementOptionSingleNode(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand)}
	nodes[0].TotalMemoryMiB += ramRequiredMedium
	option := BestPlacementOption(nodes, componentA, ramRequiredMedium)
	assert.Equal(t, nodes[0], option.TargetNode)
}

func TestBestPlacementOptionStopsExisting(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand)}
	nodes[0].TotalMemoryMiB = ramRequiredMedium + 100
	nodes[0].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     "0",
			MemoryReservedMiB: 500,
			LastRequestTime:   0,
		},
		{
			ComponentName:     "1",
			MemoryReservedMiB: 400,
			LastRequestTime:   0,
		},
	}
	expected := &PlacementOption{
		TargetNode: nodes[0],
		Input: v1.StartStopComponentsInput{
			ClientNodeId:  "",
			TargetVersion: nodes[0].Version,
			TargetCounts: []v1.ComponentCount{
				{ComponentName: "0", Count: 0},
				{ComponentName: "1", Count: 0},
				{ComponentName: componentA, Count: 1},
			},
			ReturnStatus: true,
		},
	}
	assert.Equal(t, expected, BestPlacementOption(nodes, componentA, ramRequiredMedium))
}

func TestBestPlacementOptionStopsComponentWithMostInstances(t *testing.T) {
	nodes := []v1.NodeStatus{randNode(0, defaultRand), randNode(1, defaultRand)}
	nodes[0].TotalMemoryMiB = ramRequiredMedium + 100
	nodes[0].LoadAvg1m = 1
	nodes[0].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     "0",
			MemoryReservedMiB: 500,
			LastRequestTime:   0,
		},
		{
			ComponentName:     "1",
			MemoryReservedMiB: 400,
			LastRequestTime:   0,
		},
	}
	nodes[1].TotalMemoryMiB = ramRequiredMedium + 100
	nodes[1].LoadAvg1m = .5
	nodes[1].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     "1",
			MemoryReservedMiB: 900,
			LastRequestTime:   0,
		},
	}
	expected := &PlacementOption{
		TargetNode: nodes[1],
		Input: v1.StartStopComponentsInput{
			ClientNodeId:  "",
			TargetVersion: nodes[1].Version,
			TargetCounts: []v1.ComponentCount{
				{ComponentName: "1", Count: 0},
				{ComponentName: componentA, Count: 1},
			},
			ReturnStatus: true,
		},
	}
	assert.Equal(t, expected, BestPlacementOption(nodes, componentA, 300))
}
