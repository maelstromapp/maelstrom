package gateway

import (
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/assert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io/ioutil"
	"sort"
	"testing"
	"testing/quick"
	"time"
)

func TestPropertyNeverExceedsTotalMemory(t *testing.T) {
	f := func(nodeComps NodesAndComponents) bool {
		input := nodeComps.Input
		beforeRamByNode := map[string]int64{}
		for _, node := range input.Nodes {
			totalRam, _ := totalRamUsed(node, nil, input.ComponentsByName)
			beforeRamByNode[node.NodeId] = totalRam
			if totalRam > node.TotalMemoryMiB {
				panic(fmt.Sprintf("%s - totalRam %d > %d", node.NodeId, totalRam, node.TotalMemoryMiB))
			}
		}

		data, err := json.Marshal(input)
		if err != nil {
			panic(err)
		}

		options := CalcAutoscalePlacement(input.Nodes, input.ComponentsByName)
		for _, opt := range options {
			optRam := OptionNetRam(opt, input.ComponentsByName)
			ramUsed := beforeRamByNode[opt.TargetNode.NodeId] + optRam
			if ramUsed < 0 || ramUsed > opt.TargetNode.TotalMemoryMiB {
				fmt.Printf("fail: %s TotalMemory=%d beforeUsed=%d afterUsed=%d\n", opt.TargetNode.NodeId,
					opt.TargetNode.TotalMemoryMiB, beforeRamByNode[opt.TargetNode.NodeId], ramUsed)
				err = ioutil.WriteFile("/tmp/fail.json", data, 0660)
				if err != nil {
					panic(err)
				}
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
		t.Error(err)
	}
}

// two nodes, one running comp, one empty. Max concurrency exceeded, so we scale up. Empty comp gets new comp.
func TestScaleUpPlacesOnEmptyNode(t *testing.T) {
	for i := 0; i < 30; i++ {
		testScaleUpPlacesOnEmptyNode(t)
	}
}

func testScaleUpPlacesOnEmptyNode(t *testing.T) {
	minConcurPct := 0.2
	maxConcurPct := 1.0

	comps := []v1.Component{
		makeComponentWithConcur("a", 100, 0, 10, 1, minConcurPct, maxConcurPct),
		makeComponentWithConcur("b", 200, 0, 10, 1, minConcurPct, maxConcurPct),
	}

	nodes := randNodes(2, defaultRand)
	nodes[0].RunningComponents = []v1.ComponentInfo{
		{
			ComponentName:     comps[0].Name,
			MaxConcurrency:    comps[0].MaxConcurrency,
			MemoryReservedMiB: comps[0].Docker.ReserveMemoryMiB,
			LastRequestTime:   common.TimeToMillis(time.Now()),
			TotalRequests:     50,
			Activity:          []v1.ComponentActivity{{Requests: 10, Concurrency: 1.1}},
		},
	}
	nodes[0].TotalMemoryMiB = 400
	nodes[1].TotalMemoryMiB = 400
	nodes[1].RunningComponents = []v1.ComponentInfo{}

	expected := []*PlacementOption{
		{
			TargetNode: nodes[1],
			Input: v1.StartStopComponentsInput{
				ClientNodeId:  "",
				TargetVersion: nodes[1].Version,
				TargetCounts: []v1.ComponentDelta{
					{
						ComponentName:     comps[0].Name,
						Delta:             1,
						RequiredMemoryMiB: comps[0].Docker.ReserveMemoryMiB,
					},
				},
				ReturnStatus: true,
			},
		},
	}

	actual := CalcAutoscalePlacement(nodes, componentsByName(comps))
	assert.Equal(t, expected, actual)
}

func TestPlaceHighRAMComponent(t *testing.T) {
	// - test: 3 nodes, 2 w/o enough ram to run component, 1 with enough, but running other components.
	//   should rebalance components from n3 to n1/2 and start big component on n3.

	nodes := randNodes(3, defaultRand)
	comps := []v1.Component{
		makeComponentWithConcur("a", 100, 0, 10, 1, 0.1, 0.3),
		makeComponentWithConcur("b", 200, 0, 10, 1, 0.1, 0.3),
		makeComponentWithConcur("big", 2000, 1, 10, 1, 0.1, 0.3),
	}
	nodes[0].TotalMemoryMiB = 1000
	nodes[0].RunningComponents = []v1.ComponentInfo{}
	nodes[1].TotalMemoryMiB = 1000
	nodes[1].RunningComponents = []v1.ComponentInfo{}
	nodes[2].TotalMemoryMiB = 2500
	nodes[2].RunningComponents = []v1.ComponentInfo{
		toComponentInfo(comps[0], v1.ComponentActivity{Requests: 10, Concurrency: .2}),
		toComponentInfo(comps[1], v1.ComponentActivity{Requests: 10, Concurrency: .2}),
	}

	expected := []*PlacementOption{
		makePlacementOption(nodes[0], makeTargetCount(comps[0], 1)),
		makePlacementOption(nodes[1], makeTargetCount(comps[1], 1)),
		makePlacementOption(nodes[2], makeTargetCount(comps[0], -1), makeTargetCount(comps[1], -1),
			makeTargetCount(comps[2], 1)),
	}

	actual := CalcAutoscalePlacement(nodes, componentsByName(comps))
	assert.Equal(t, expected, sortPlacementOptions(actual))
}

func TestPlacementAntiAffinity(t *testing.T) {
	// property: anti-affinity - always spreads components onto different nodes
	f := func(nodeComps NodesAndComponents) bool {

		type minMaxNode struct {
			minCount  int
			minNodeId string
			maxCount  int
			maxNodeId string
		}

		input := nodeComps.Input

		data, err := json.Marshal(input)
		if err != nil {
			panic(err)
		}

		minMaxCountForComponent := map[string]minMaxNode{}
		compCountByNodeId := map[string]map[string]int{}
		options := CalcAutoscalePlacement(input.Nodes, input.ComponentsByName)
		for _, opt := range options {
			nodeId := opt.TargetNode.NodeId
			runningComp := RunningComponents(opt.TargetNode, opt)
			compCount := map[string]int{}
			ramAvail := opt.TargetNode.TotalMemoryMiB
			for _, rc := range runningComp {
				compCount[rc.ComponentName] += 1
				ramAvail -= rc.MemoryReservedMiB
			}
			for comp, count := range compCount {
				c := input.ComponentsByName[comp]
				if ramAvail >= c.Docker.ReserveMemoryMiB {
					minMax, ok := minMaxCountForComponent[comp]
					if !ok {
						minMaxCountForComponent[comp] = minMaxNode{minCount: count, minNodeId: nodeId,
							maxCount: count, maxNodeId: nodeId}
					} else {
						if count < minMax.minCount {
							minMax.minCount = count
							minMax.minNodeId = nodeId
						}
						if count > minMax.maxCount {
							minMax.maxCount = count
							minMax.maxNodeId = nodeId
						}
						minMaxCountForComponent[comp] = minMax
					}
				}
			}
			compCountByNodeId[nodeId] = compCount
		}
		for comp, minMax := range minMaxCountForComponent {
			if minMax.maxCount-minMax.minCount > 1 {
				fmt.Printf("fail: minmax comp=%s min=%d max=%d\n", comp, minMax.minCount, minMax.maxCount)
				fmt.Printf("fail: minmax minNode: %v\n", compCountByNodeId[minMax.minNodeId])
				fmt.Printf("fail: minmax maxNode: %v\n", compCountByNodeId[minMax.maxNodeId])
				err = ioutil.WriteFile("/tmp/fail.json", data, 0660)
				if err != nil {
					panic(err)
				}
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
		t.Error(err)
	}
}

func TestOnlyScaleToZeroIfIdle(t *testing.T) {
	// property: never scales to 0 if component last req time within idleTimeoutSeconds
	f := func(nodeComps NodesAndComponents) bool {
		input := nodeComps.Input
		compCountBefore := map[string]int{}
		compMaxReqTime := map[string]int64{}
		compCountAfter := map[string]int{}
		placementOptionByNode := map[string]*PlacementOption{}
		for _, node := range input.Nodes {
			for _, rc := range node.RunningComponents {
				compCountBefore[rc.ComponentName] += 1
				if rc.LastRequestTime > compMaxReqTime[rc.ComponentName] {
					compMaxReqTime[rc.ComponentName] = rc.LastRequestTime
				}
			}
		}

		options := CalcAutoscalePlacement(input.Nodes, input.ComponentsByName)
		for _, opt := range options {
			placementOptionByNode[opt.TargetNode.NodeId] = opt
		}
		for _, node := range input.Nodes {
			for _, rc := range RunningComponents(node, placementOptionByNode[node.NodeId]) {
				compCountAfter[rc.ComponentName] += 1
			}
		}

		for comp, before := range compCountBefore {
			if before > 0 && compCountAfter[comp] < 1 {
				c := input.ComponentsByName[comp]
				idleSec := c.Docker.IdleTimeoutSeconds
				secsSinceReq := (common.TimeToMillis(time.Now()) - compMaxReqTime[comp]) / 1000
				if secsSinceReq < idleSec {
					fmt.Println("fail: ", comp, "min", c.MinInstances, "idle", idleSec, "lastReq", secsSinceReq,
						"before", before, "after", compCountAfter[comp])
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
		t.Error(err)
	}
}

func TestNoIdleNodes(t *testing.T) {
	// property: never leaves node idle if # components >= # nodes
	f := func(nodeComps NodesAndComponents) bool {
		input := nodeComps.Input
		options := CalcAutoscalePlacement(input.Nodes, input.ComponentsByName)
		placementOptionByNode := map[string]*PlacementOption{}
		smallestMoveableComponentRam := int64(9999999999999)
		var emptyNodeWithMostRam v1.NodeStatus
		for _, opt := range options {
			placementOptionByNode[opt.TargetNode.NodeId] = opt
		}
		for _, node := range input.Nodes {
			runningComps := RunningComponents(node, placementOptionByNode[node.NodeId])
			if len(runningComps) == 0 {
				if node.TotalMemoryMiB > emptyNodeWithMostRam.TotalMemoryMiB {
					emptyNodeWithMostRam = node
				}
			} else if len(runningComps) > 1 {
				for _, rc := range runningComps {
					if rc.MemoryReservedMiB < smallestMoveableComponentRam {
						smallestMoveableComponentRam = rc.MemoryReservedMiB
					}
				}
			}
		}
		if emptyNodeWithMostRam.TotalMemoryMiB > smallestMoveableComponentRam {
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
		t.Error(err)
	}
}

/////////////////////////////////////////////////////////////////////////

func sortPlacementOptions(options []*PlacementOption) []*PlacementOption {
	sort.Sort(PlacementOptionByNode(options))
	for i, opt := range options {
		sort.Sort(v1.ComponentDeltaByCompName(opt.Input.TargetCounts))
		options[i] = opt
	}
	return options
}

func makePlacementOption(node v1.NodeStatus, targetCounts ...v1.ComponentDelta) *PlacementOption {
	return &PlacementOption{
		TargetNode: node,
		Input: v1.StartStopComponentsInput{
			ClientNodeId:  "",
			TargetVersion: node.Version,
			TargetCounts:  targetCounts,
			ReturnStatus:  true,
		},
	}
}

func makeTargetCount(c v1.Component, delta int) v1.ComponentDelta {
	requiredMemory := int64(0)
	if delta > 0 {
		requiredMemory = c.Docker.ReserveMemoryMiB
	}
	return v1.ComponentDelta{
		ComponentName:     c.Name,
		Delta:             int64(delta),
		RequiredMemoryMiB: requiredMemory,
	}
}

func toComponentInfo(c v1.Component, activity ...v1.ComponentActivity) v1.ComponentInfo {
	return v1.ComponentInfo{
		ComponentName:     c.Name,
		MaxConcurrency:    c.MaxConcurrency,
		MemoryReservedMiB: c.Docker.ReserveMemoryMiB,
		LastRequestTime:   0,
		TotalRequests:     0,
		Activity:          activity,
	}
}

func makeComponent(name string, requiredRamMiB int64, minInst int64, maxInst int64, maxConcur int64) v1.Component {
	return makeComponentWithConcur(name, requiredRamMiB, minInst, maxInst, maxConcur, 0, 0)
}

func makeComponentWithConcur(name string, requiredRamMiB int64, minInst int64, maxInst int64, maxConcur int64,
	scaleDownPct float64, scaleUpPct float64) v1.Component {
	return v1.Component{
		Name:                    name,
		ProjectName:             "",
		Environment:             nil,
		MinInstances:            minInst,
		MaxInstances:            maxInst,
		ScaleDownConcurrencyPct: scaleDownPct,
		ScaleUpConcurrencyPct:   scaleUpPct,
		MaxConcurrency:          maxConcur,
		MaxDurationSeconds:      10,
		Version:                 0,
		ModifiedAt:              0,
		Docker: &v1.DockerComponent{
			Image:                       "foo",
			Command:                     []string{"/bin/false"},
			HttpPort:                    8080,
			HttpHealthCheckPath:         "/",
			HttpStartHealthCheckSeconds: 0,
			HttpHealthCheckSeconds:      0,
			IdleTimeoutSeconds:          10,
			Volumes:                     nil,
			NetworkName:                 "",
			LogDriver:                   "",
			LogDriverOptions:            nil,
			LimitCpu:                    0,
			ReserveMemoryMiB:            requiredRamMiB,
			LimitMemoryMiB:              0,
		},
	}
}
