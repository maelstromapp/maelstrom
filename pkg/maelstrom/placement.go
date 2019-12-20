package maelstrom

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"sort"
)

type PlacementOption struct {
	TargetNode *v1.NodeStatus
	Input      *v1.StartStopComponentsInput
}

func (p *PlacementOption) cloneWithTargetDelta(componentName string, delta int64, requiredRam int64) *PlacementOption {
	currentCount := int64(-1)
	targetCounts := make([]v1.ComponentTarget, 0)
	for _, tc := range p.Input.TargetCounts {
		if tc.ComponentName == componentName {
			currentCount = tc.TargetCount
		} else {
			targetCounts = append(targetCounts, tc)
		}
	}
	if currentCount < 0 {
		byComp, _ := p.ContainerCountByComponent()
		currentCount = int64(byComp[componentName])
	}

	targetCounts = append(targetCounts, v1.ComponentTarget{
		ComponentName:     componentName,
		RequiredMemoryMiB: requiredRam,
		TargetCount:       currentCount + delta,
	})

	return &PlacementOption{
		TargetNode: p.TargetNode,
		Input: &v1.StartStopComponentsInput{
			ClientNodeId:  p.Input.ClientNodeId,
			TargetVersion: p.TargetNode.Version,
			ReturnStatus:  p.Input.ReturnStatus,
			TargetCounts:  targetCounts,
		},
	}
}

func (p *PlacementOption) RamUsed() int64 {
	byComp := map[string]int{}
	ramUsed := int64(0)
	for _, tc := range p.Input.TargetCounts {
		byComp[tc.ComponentName] = int(tc.TargetCount)
		ramUsed += tc.TargetCount * tc.RequiredMemoryMiB
	}
	for _, ci := range p.TargetNode.RunningComponents {
		_, ok := byComp[ci.ComponentName]
		if !ok {
			byComp[ci.ComponentName] += 1
			ramUsed += ci.MemoryReservedMiB
		}
	}
	return ramUsed
}

func (p *PlacementOption) ContainerCountByComponent() (byComp map[string]int, total int) {
	byComp = map[string]int{}
	total = 0
	for _, tc := range p.Input.TargetCounts {
		byComp[tc.ComponentName] = int(tc.TargetCount)
		total += int(tc.TargetCount)
	}
	for _, ci := range p.TargetNode.RunningComponents {
		_, ok := byComp[ci.ComponentName]
		if !ok {
			byComp[ci.ComponentName] += 1
			total += 1
		}
	}
	return
}

func (p *PlacementOption) scaleDownCount() int {
	scaleDown := 0
	byComp := map[string]int{}
	for _, ci := range p.TargetNode.RunningComponents {
		byComp[ci.ComponentName] += 1
	}
	for _, tc := range p.Input.TargetCounts {
		if tc.TargetCount < int64(byComp[tc.ComponentName]) {
			scaleDown++
		}
	}
	return scaleDown
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
	Options       []*PlacementOption
	ComponentName string
}

func (s PlacementOptionByCostDesc) Len() int { return len(s.Options) }
func (s PlacementOptionByCostDesc) Swap(i, j int) {
	s.Options[i], s.Options[j] = s.Options[j], s.Options[i]
}
func (s PlacementOptionByCostDesc) Less(i, j int) bool {
	iContainersByComp, iContainers := s.Options[i].ContainerCountByComponent()
	jContainersByComp, jContainers := s.Options[j].ContainerCountByComponent()

	// sort by # of instances of this component we're already running - lower is better (anti-affinity)
	iCompCount := iContainersByComp[s.ComponentName]
	jCompCount := jContainersByComp[s.ComponentName]
	if iCompCount != jCompCount {
		return iCompCount < jCompCount
	}

	// prefer options with minimal scale down (displacement)
	iScaleDown := s.Options[i].scaleDownCount()
	jScaleDown := s.Options[j].scaleDownCount()
	if iScaleDown != jScaleDown {
		return iScaleDown < jScaleDown
	}

	// if one node is empty, prefer it
	if iContainers <= 1 && jContainers > 1 {
		return true
	}
	if jContainers <= 1 && iContainers > 1 {
		return false
	}

	// finally sort by 1min load average - less is better
	return s.Options[i].TargetNode.LoadAvg1m < s.Options[j].TargetNode.LoadAvg1m
}

func BestStartComponentOption(placementByNode map[string]*PlacementOption, componentName string,
	requiredMemoryMiB int64, displaceOK bool) *PlacementOption {

	options := make([]*PlacementOption, 0)

	// key: componentName, value: # of containers for that component
	componentInstanceCounts := map[string]int{}

	for _, placementOption := range placementByNode {

		// Calc memory available
		memoryAvailableMiB := placementOption.TargetNode.TotalMemoryMiB - placementOption.RamUsed()

		// Only consider node if its total ram >= required ram for component
		if placementOption.TargetNode.TotalMemoryMiB >= requiredMemoryMiB {

			// If insufficient memory available and displacement is allowed, displace other
			// components on that node to free ram
			if (memoryAvailableMiB < requiredMemoryMiB) && displaceOK {

				// build list of components running - ignoring components already marked to scale down
				runningComps := make([]v1.ComponentInfo, 0)

				countByComp, _ := placementOption.ContainerCountByComponent()
				for _, ci := range placementOption.TargetNode.RunningComponents {
					if countByComp[ci.ComponentName] > 0 && ci.ComponentName != componentName {
						runningComps = append(runningComps, ci)
						countByComp[ci.ComponentName] -= 1
					}
				}

				// sort runningComps so that ones with largest cluster-side instance counts are first
				sort.Sort(ComponentInfoByRunningCountAndReqTime{
					Components:     runningComps,
					InstanceCounts: componentInstanceCounts,
				})

				// stop components in order until sufficient memory is available
				for i := 0; memoryAvailableMiB < requiredMemoryMiB && i < len(runningComps); i++ {
					memoryAvailableMiB += runningComps[i].MemoryReservedMiB
					placementOption = placementOption.cloneWithTargetDelta(runningComps[i].ComponentName, -1,
						runningComps[i].MemoryReservedMiB)
				}
			}

			// if sufficient memory available, add as option
			if memoryAvailableMiB >= requiredMemoryMiB {
				modifiedOption := placementOption.cloneWithTargetDelta(componentName, 1, requiredMemoryMiB)
				options = append(options, modifiedOption)
			}
		}
	}

	if len(options) > 0 {
		sort.Sort(PlacementOptionByCostDesc{Options: options, ComponentName: componentName})
		sort.Sort(ComponentTargetByCompName(options[0].Input.TargetCounts))
		return options[0]
	}
	return nil
}

func BestStopComponentOption(placementByNode map[string]*PlacementOption, componentName string) *PlacementOption {

	options := make([]*PlacementOption, 0)

	for _, placementOption := range placementByNode {
		countByComp, _ := placementOption.ContainerCountByComponent()
		if countByComp[componentName] > 0 {
			modifiedOption := placementOption.cloneWithTargetDelta(componentName, -1, 0)
			options = append(options, modifiedOption)
		}
	}

	if len(options) > 0 {
		sort.Sort(PlacementOptionByCostDesc{Options: options, ComponentName: componentName})
		return options[len(options)-1]
	}
	return nil
}
