package gateway

import (
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sort"
)

type PlacementOption struct {
	TargetNode v1.NodeStatus
	Input      v1.StartStopComponentsInput
}

type PlacementOptionByCostDesc struct {
	Options        []*PlacementOption
	InstanceCounts map[string]int
}

func (s PlacementOptionByCostDesc) Len() int { return len(s.Options) }
func (s PlacementOptionByCostDesc) Swap(i, j int) {
	s.Options[i], s.Options[j] = s.Options[j], s.Options[i]
}
func (s PlacementOptionByCostDesc) Less(i, j int) bool {
	// sort by min instances of components remaining - higher is better
	iMinInstCount := s.MinInstCountAfterApplying(i)
	jMinInstCount := s.MinInstCountAfterApplying(j)
	if iMinInstCount == jMinInstCount {
		// next sort by total containers stopped - less is better
		if len(s.Options[i].Input.TargetCounts) == len(s.Options[j].Input.TargetCounts) {
			// finally sort by 1min load average - less is better
			return s.Options[i].TargetNode.LoadAvg1m < s.Options[j].TargetNode.LoadAvg1m
		}
		return len(s.Options[i].Input.TargetCounts) < len(s.Options[j].Input.TargetCounts)
	}
	return iMinInstCount > jMinInstCount

}
func (s PlacementOptionByCostDesc) MinInstCountAfterApplying(x int) int64 {
	minCount := int64(9999999999)
	for _, tc := range s.Options[x].Input.TargetCounts {
		if tc.Count < 0 {
			count := int64(s.InstanceCounts[tc.ComponentName]) + tc.Count
			if count < minCount {
				minCount = count
			}
		}
	}
	return minCount
}

func BestPlacementOption(nodes []v1.NodeStatus, componentName string, requiredMemoryMiB int64) *PlacementOption {
	// key: componentName, value: # of containers running that component
	componentInstanceCounts := map[string]int{}
	for _, node := range nodes {
		for _, comp := range node.RunningComponents {
			count := componentInstanceCounts[comp.ComponentName]
			componentInstanceCounts[comp.ComponentName] = count + 1
		}
	}

	options := make([]*PlacementOption, 0)

	for _, node := range nodes {
		if node.TotalMemoryMiB >= requiredMemoryMiB {

			var targetCounts []v1.ComponentCount

			memoryAvailableMiB := node.TotalMemoryMiB
			for _, c := range node.RunningComponents {
				memoryAvailableMiB -= c.MemoryReservedMiB
			}

			sort.Sort(v1.ComponentInfoByRunningCountAndReqTime{
				Components:     node.RunningComponents,
				InstanceCounts: componentInstanceCounts,
			})
			i := 0
			for memoryAvailableMiB < requiredMemoryMiB && i < len(node.RunningComponents) {
				targetCounts = append(targetCounts, v1.ComponentCount{
					ComponentName: node.RunningComponents[i].ComponentName,
					Count:         0,
				})
				memoryAvailableMiB += node.RunningComponents[i].MemoryReservedMiB
				i++
			}

			if memoryAvailableMiB >= requiredMemoryMiB {
				targetCounts = append(targetCounts, v1.ComponentCount{
					ComponentName: componentName,
					Count:         1,
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
		sort.Sort(PlacementOptionByCostDesc{Options: options, InstanceCounts: componentInstanceCounts})
		return options[0]
	}
	return nil
}
