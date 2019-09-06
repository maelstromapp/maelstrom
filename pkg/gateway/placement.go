package gateway

import (
	"fmt"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sort"
)

type PlacementOption struct {
	TargetNode v1.NodeStatus
	Input      v1.StartStopComponentsInput
}

type PlacementOptionByNode []*PlacementOption

func (s PlacementOptionByNode) Len() int { return len(s) }
func (s PlacementOptionByNode) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s PlacementOptionByNode) Less(i, j int) bool {
	return s[i].TargetNode.NodeId < s[j].TargetNode.NodeId
}

type PlacementOptionByCostDesc struct {
	Options             []*PlacementOption
	InstanceCounts      map[string]int
	NodeComponentCounts map[string]int
}

func (s PlacementOptionByCostDesc) Len() int { return len(s.Options) }
func (s PlacementOptionByCostDesc) Swap(i, j int) {
	s.Options[i], s.Options[j] = s.Options[j], s.Options[i]
}
func (s PlacementOptionByCostDesc) Less(i, j int) bool {
	// sort by # of instances of this component we're already running - lower is better (anti-affinity)
	iCompCount := s.NodeComponentCounts[s.Options[i].TargetNode.NodeId]
	jCompCount := s.NodeComponentCounts[s.Options[j].TargetNode.NodeId]
	if iCompCount != jCompCount {
		return iCompCount < jCompCount
	}

	// sort by min instances of components remaining - higher is better
	iMinInstCount := s.MinInstCountAfterApplying(i)
	jMinInstCount := s.MinInstCountAfterApplying(j)
	if iMinInstCount != jMinInstCount {
		return iMinInstCount > jMinInstCount
	}

	// next sort by total containers stopped - less is better
	if len(s.Options[i].Input.TargetCounts) != len(s.Options[j].Input.TargetCounts) {
		return len(s.Options[i].Input.TargetCounts) < len(s.Options[j].Input.TargetCounts)
	}

	// finally sort by 1min load average - less is better
	return s.Options[i].TargetNode.LoadAvg1m < s.Options[j].TargetNode.LoadAvg1m
}

func (s PlacementOptionByCostDesc) MinInstCountAfterApplying(x int) int64 {
	minCount := int64(9999999999)
	for _, tc := range s.Options[x].Input.TargetCounts {
		if tc.Delta < 0 {
			count := int64(s.InstanceCounts[tc.ComponentName]) + tc.Delta
			if count < minCount {
				minCount = count
			}
		}
	}
	return minCount
}

func RunningComponents(node v1.NodeStatus, placement *PlacementOption) []v1.ComponentInfo {
	comps := make([]v1.ComponentInfo, 0)

	stopCountByComponent := map[string]int{}
	if placement != nil {
		for _, tc := range placement.Input.TargetCounts {
			if tc.Delta < 0 {
				stopCountByComponent[tc.ComponentName] += int(tc.Delta) * -1
			} else {
				if tc.RequiredMemoryMiB <= 0 {
					panic(fmt.Sprintf("Invalid ComponentDelta: %+v", tc))
				}
				for i := 0; i < int(tc.Delta); i++ {
					comps = append(comps, v1.ComponentInfo{
						ComponentName:     tc.ComponentName,
						MemoryReservedMiB: tc.RequiredMemoryMiB,
					})
				}
			}
		}
	}

	for _, rc := range node.RunningComponents {
		stopCount := stopCountByComponent[rc.ComponentName]
		if stopCount == 0 {
			comps = append(comps, rc)
		} else {
			stopCountByComponent[rc.ComponentName] -= 1
		}
	}
	return comps
}

func BestStartComponentOption(nodes []v1.NodeStatus, placementByNode map[string]*PlacementOption,
	componentName string, requiredMemoryMiB int64, displaceOK bool) *PlacementOption {
	// key: componentName, value: # of containers running that component
	componentInstanceCounts := map[string]int{}
	// key: nodeId, value: # of containers for THIS component running
	nodeComponentCounts := map[string]int{}
	for _, node := range nodes {
		runningComps := RunningComponents(node, placementByNode[node.NodeId])
		for _, comp := range runningComps {
			componentInstanceCounts[comp.ComponentName] += 1
			if comp.ComponentName == componentName {
				nodeComponentCounts[node.NodeId] += 1
			}
		}
	}

	options := make([]*PlacementOption, 0)

	for _, node := range nodes {
		if node.TotalMemoryMiB >= requiredMemoryMiB {

			var targetCounts []v1.ComponentDelta
			placement := placementByNode[node.NodeId]
			runningComps := RunningComponents(node, placement)
			memoryAvailableMiB := node.TotalMemoryMiB - nodeRamUsedMiB(runningComps)

			if displaceOK {

				sort.Sort(v1.ComponentInfoByRunningCountAndReqTime{
					Components:     runningComps,
					InstanceCounts: componentInstanceCounts,
				})
				i := 0
				for memoryAvailableMiB < requiredMemoryMiB && i < len(runningComps) {
					if runningComps[i].ComponentName != componentName {
						targetCounts = append(targetCounts, v1.ComponentDelta{
							ComponentName: runningComps[i].ComponentName,
							Delta:         -1,
						})
						memoryAvailableMiB += runningComps[i].MemoryReservedMiB
					}
					i++
				}
			}

			if memoryAvailableMiB >= requiredMemoryMiB {
				targetCounts = append(targetCounts, v1.ComponentDelta{
					ComponentName:     componentName,
					Delta:             1,
					RequiredMemoryMiB: requiredMemoryMiB,
				})
				options = append(options, &PlacementOption{
					TargetNode: node,
					Input: v1.StartStopComponentsInput{
						ClientNodeId:  "",
						TargetVersion: node.Version,
						TargetCounts:  targetCounts,
						ReturnStatus:  true,
					},
				})
			}
		}
	}

	if len(options) > 0 {
		sort.Sort(PlacementOptionByCostDesc{Options: options, InstanceCounts: componentInstanceCounts,
			NodeComponentCounts: nodeComponentCounts})
		return options[0]
	}
	return nil
}

type StopPlacementOptionByCost struct {
	Options              []*PlacementOption
	InstanceCountsByNode map[string]int
}

func (s StopPlacementOptionByCost) Len() int { return len(s.Options) }
func (s StopPlacementOptionByCost) Swap(i, j int) {
	s.Options[i], s.Options[j] = s.Options[j], s.Options[i]
}
func (s StopPlacementOptionByCost) Less(i, j int) bool {
	// sort by instances of components - higher is better (shutdown component on node with most copies running)
	iInstCount := s.InstanceCountsByNode[s.Options[i].TargetNode.NodeId]
	jInstCount := s.InstanceCountsByNode[s.Options[j].TargetNode.NodeId]
	if iInstCount == jInstCount {
		// next sort by 1min load average - higher is better
		return s.Options[i].TargetNode.LoadAvg1m > s.Options[j].TargetNode.LoadAvg1m
	}
	return iInstCount > jInstCount
}

func BestStopComponentOption(nodes []v1.NodeStatus, placementByNode map[string]*PlacementOption,
	componentName string) *PlacementOption {

	instanceCountsByNode := map[string]int{}
	options := make([]*PlacementOption, 0)

	for _, node := range nodes {
		count := 0
		placement := placementByNode[node.NodeId]
		var requiredMemoryMiB int64
		for _, comp := range node.RunningComponents {
			if comp.ComponentName == componentName {
				count++
				if comp.MemoryReservedMiB > requiredMemoryMiB {
					requiredMemoryMiB = comp.MemoryReservedMiB
				}
			}
		}
		if placement != nil {
			for _, tc := range placement.Input.TargetCounts {
				if tc.ComponentName == componentName {
					count += int(tc.Delta)
				}
			}
		}

		if count > 0 {
			instanceCountsByNode[node.NodeId] = count
			options = append(options, &PlacementOption{
				TargetNode: node,
				Input: v1.StartStopComponentsInput{
					ClientNodeId:  "",
					TargetVersion: node.Version,
					TargetCounts: []v1.ComponentDelta{
						{
							ComponentName: componentName,
							Delta:         -1,
						},
					},
					ReturnStatus: true,
				},
			})
		}
	}

	if len(options) > 0 {
		sort.Sort(StopPlacementOptionByCost{Options: options, InstanceCountsByNode: instanceCountsByNode})
		return options[0]
	}
	return nil
}
