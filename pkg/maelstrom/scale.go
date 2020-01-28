package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/db"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"math"
	"sort"
)

type CalcAutoscaleInput struct {
	Nodes            []v1.NodeStatus
	ComponentsByName map[string]v1.Component
}

type componentConcurrency struct {
	componentName       string
	minInstances        int
	maxInstances        int
	maxInstancesPerNode int64
	currentInstances    int
	targetInstances     int
	reserveMemoryMiB    int64
	pctMaxConcurrency   float64
}

type componentDelta struct {
	componentName       string
	reserveMemoryMiB    int64
	maxInstancesPerNode int64
	delta               int
}

type scaleTargetInput struct {
	componentName         string
	infos                 []v1.ComponentInfo
	maxConcurrencyPerInst int64
	minInst               int64
	maxInst               int64
	scaleDownConcurPct    float64
	scaleUpConcurPct      float64
}

type scaleTargetOutput struct {
	componentName     string
	currentInstances  int
	targetInstances   int
	pctMaxConcurrency float64
	sumConcur         sumConcurrencyOutput
}

type sumConcurrencyOutput struct {
	currentInstances     int
	sumMaxConcurrency    float64
	sumLatestConcurrency float64
	sumAvgConcurrency    float64
}

type componentDeltaByDelta []componentDelta

func (s componentDeltaByDelta) Len() int           { return len(s) }
func (s componentDeltaByDelta) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s componentDeltaByDelta) Less(i, j int) bool { return s[i].delta < s[j].delta }

/////////////////////////////////////////////////////

func CalcAutoscalePlacement(nodes []v1.NodeStatus, componentsByName map[string]v1.Component) []*PlacementOption {
	nodes = removeStopping(nodes)
	for _, n := range nodes {
		log.Info("scale: CalcAutoscalePlacement", "peerUrl", n.PeerUrl, "totalRam", n.TotalMemoryMiB,
			"version", n.Version, "running", n.RunningComponents)
	}
	concurrency := toComponentConcurrency(nodes, componentsByName)
	deltas := toComponentDeltas(concurrency)
	return computeScaleStartStopInputs(nodes, deltas)
}

func removeStopping(input []v1.NodeStatus) []v1.NodeStatus {
	output := make([]v1.NodeStatus, len(input))
	for i, node := range input {
		output[i] = removeStoppingFromNode(node)
	}
	return output
}

func removeStoppingFromNode(node v1.NodeStatus) v1.NodeStatus {
	components := make([]v1.ComponentInfo, 0)
	for _, comp := range node.RunningComponents {
		if comp.Status != "stopping" {
			components = append(components, comp)
		}
	}
	node.RunningComponents = components
	return node
}

func groupOptionsByType(options []*PlacementOption) [][]*PlacementOption {
	startOnly := make([]*PlacementOption, 0)
	startStop := make([]*PlacementOption, 0)
	stopOnly := make([]*PlacementOption, 0)
	for _, opt := range options {
		start, stop := opt.scaleUpDownCounts()
		if start > 0 && stop > 0 {
			startStop = append(startStop, opt)
		} else if start > 0 {
			startOnly = append(startOnly, opt)
		} else {
			stopOnly = append(stopOnly, opt)
		}
	}
	return [][]*PlacementOption{startOnly, startStop, stopOnly}
}

func componentsByName(comps []v1.Component) map[string]v1.Component {
	byName := map[string]v1.Component{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	return byName
}

func loadActiveComponents(nodes []v1.NodeStatus, db db.Db) (map[string]v1.Component, error) {
	activeComponents := map[string]bool{}
	for _, node := range nodes {
		for _, comp := range node.RunningComponents {
			activeComponents[comp.ComponentName] = true
		}
	}

	componentsByName := map[string]v1.Component{}
	listInput := v1.ListComponentsInput{
		Limit:     1000,
		NextToken: "",
	}
	for {
		output, err := db.ListComponents(listInput)
		if err != nil {
			return nil, errors.Wrap(err, "scale: ListComponents failed")
		}
		for _, comp := range output.Components {
			if comp.MinInstances > 0 || activeComponents[comp.Name] {
				componentsByName[comp.Name] = comp
			}
		}
		listInput.NextToken = output.NextToken
		if listInput.NextToken == "" {
			break
		}
	}
	return componentsByName, nil
}

func toComponentConcurrency(nodes []v1.NodeStatus, componentsByName map[string]v1.Component) []componentConcurrency {
	compInfoByComponent := map[string][]v1.ComponentInfo{}
	lastReqTimeByComponent := map[string]int64{}
	for _, node := range nodes {
		for _, compInfo := range node.RunningComponents {
			arr := compInfoByComponent[compInfo.ComponentName]
			compInfoByComponent[compInfo.ComponentName] = append(arr, compInfo)
			lastReqTimeByComponent[compInfo.ComponentName] = common.MaxInt64(compInfo.LastRequestTime,
				compInfo.StartTime, lastReqTimeByComponent[compInfo.ComponentName])
		}
	}

	var concur []componentConcurrency
	for compName, comp := range componentsByName {
		infos := compInfoByComponent[compName]
		secsSinceLastReq := (common.NowMillis() - lastReqTimeByComponent[compName]) / 1000
		minInstances := comp.MinInstances
		idleTimeoutSec := comp.Docker.IdleTimeoutSeconds
		if idleTimeoutSec <= 0 {
			idleTimeoutSec = 300
		}
		if minInstances < 1 && secsSinceLastReq <= idleTimeoutSec {
			minInstances = 1
		}
		scaleOutput := calcScaleTarget(toScaleTargetInput(comp, minInstances, infos))

		concur = append(concur, componentConcurrency{
			componentName:       compName,
			minInstances:        int(comp.MinInstances),
			maxInstances:        int(comp.MaxInstances),
			maxInstancesPerNode: comp.MaxInstancesPerNode,
			reserveMemoryMiB:    comp.Docker.ReserveMemoryMiB,
			currentInstances:    len(infos),
			targetInstances:     scaleOutput.targetInstances,
			pctMaxConcurrency:   scaleOutput.pctMaxConcurrency,
		})
	}

	return concur
}

func toScaleTargetInput(c v1.Component, minInstances int64, infos []v1.ComponentInfo) scaleTargetInput {
	maxConcurPerInst := c.MaxConcurrency
	if maxConcurPerInst <= 0 {
		maxConcurPerInst = 1
	}
	scaleDownPct := c.ScaleDownConcurrencyPct
	if scaleDownPct <= 0 {
		scaleDownPct = 0.25
	}
	scaleUpPct := c.ScaleUpConcurrencyPct
	if scaleUpPct <= 0 {
		scaleUpPct = 0.75
	}
	return scaleTargetInput{
		componentName:         c.Name,
		infos:                 infos,
		maxConcurrencyPerInst: maxConcurPerInst,
		minInst:               minInstances,
		maxInst:               c.MaxInstances,
		scaleDownConcurPct:    scaleDownPct,
		scaleUpConcurPct:      scaleUpPct,
	}
}

func sumMaxConcurrency(infos []v1.ComponentInfo, maxConcurrencyPerInst int64) sumConcurrencyOutput {
	if len(infos) == 0 {
		return sumConcurrencyOutput{}
	}

	if maxConcurrencyPerInst <= 0 {
		maxConcurrencyPerInst = 1
	}

	var sumMaxConcur, sumLatestConcur, sumAvgConcur float64
	for _, compInfo := range infos {
		nodeActivity := compInfo.Activity
		if len(nodeActivity) > 0 {
			maxVal := float64(0)
			totalConcur := float64(0)
			sumLatestConcur += nodeActivity[0].Concurrency
			for _, val := range nodeActivity {
				if val.Concurrency > maxVal {
					maxVal = val.Concurrency
				}
				totalConcur += val.Concurrency
			}
			sumMaxConcur += maxVal
			sumAvgConcur += totalConcur / float64(len(nodeActivity))
		}
	}
	return sumConcurrencyOutput{
		currentInstances:     len(infos),
		sumMaxConcurrency:    sumMaxConcur,
		sumLatestConcurrency: sumLatestConcur,
		sumAvgConcurrency:    sumAvgConcur,
	}
}

func calcScaleTarget(input scaleTargetInput) scaleTargetOutput {
	var output scaleTargetOutput

	sumConcurOut := sumMaxConcurrency(input.infos, input.maxConcurrencyPerInst)

	output.componentName = input.componentName
	output.sumConcur = sumConcurOut

	target := int64(len(input.infos))
	output.currentInstances = int(target)
	if sumConcurOut.currentInstances > 0 {
		denom := float64(int64(sumConcurOut.currentInstances) * input.maxConcurrencyPerInst)
		scaleDenom := float64(input.maxConcurrencyPerInst) * input.scaleUpConcurPct
		pctAvgConcurrency := sumConcurOut.sumAvgConcurrency / denom
		pctMaxConcurrency := sumConcurOut.sumMaxConcurrency / denom
		if pctAvgConcurrency > input.scaleUpConcurPct {
			target = int64(math.Ceil(sumConcurOut.sumAvgConcurrency / scaleDenom))
		} else if pctMaxConcurrency < input.scaleDownConcurPct {
			target = int64(math.Ceil(sumConcurOut.sumMaxConcurrency / scaleDenom))
		}
		output.pctMaxConcurrency = pctMaxConcurrency
	}

	if target < input.minInst {
		target = input.minInst
	}
	if target > input.maxInst && input.maxInst > 0 && input.maxInst >= input.minInst {
		target = input.maxInst
	}
	output.targetInstances = int(target)
	log.Info("scale: output", "scaleOutput", fmt.Sprintf("%+v", output))
	return output
}

func toComponentDeltas(concurrency []componentConcurrency) []componentDelta {
	var deltas []componentDelta
	for _, c := range concurrency {
		if c.targetInstances != c.currentInstances {
			deltas = append(deltas, componentDelta{
				componentName:       c.componentName,
				reserveMemoryMiB:    c.reserveMemoryMiB,
				maxInstancesPerNode: c.maxInstancesPerNode,
				delta:               c.targetInstances - c.currentInstances,
			})
		}
	}
	return deltas
}

func mergeTargetCounts(a []v1.ComponentTarget, b []v1.ComponentTarget) []v1.ComponentTarget {
	merged := make([]v1.ComponentTarget, 0)

	byComponentName := map[string]bool{}

	// add "b" first
	for _, count := range b {
		byComponentName[count.ComponentName] = true
		merged = append(merged, count)
	}

	// add "a" if not in "b"
	for _, count := range a {
		if !byComponentName[count.ComponentName] {
			merged = append(merged, count)
		}
	}

	return merged
}

func mergeOption(optionByNode map[string]*PlacementOption, toMerge *PlacementOption) *PlacementOption {
	prevOption := optionByNode[toMerge.TargetNode.NodeId]
	if prevOption != nil {
		toMerge.Input.TargetCounts = mergeTargetCounts(toMerge.Input.TargetCounts,
			prevOption.Input.TargetCounts)
	}
	return toMerge
}

func computeScaleStartStopInputs(nodes []v1.NodeStatus, deltas []componentDelta) []*PlacementOption {

	optionByNode := map[string]*PlacementOption{}
	beforeRamByNode := map[string]int64{}

	for _, node := range nodes {
		totalRam := int64(0)
		comps := make([]string, 0)
		for _, rc := range node.RunningComponents {
			totalRam += rc.MemoryReservedMiB
			comps = append(comps, rc.ComponentName)
		}
		beforeRamByNode[node.NodeId] = totalRam

		optionByNode[node.NodeId] = newPlacementOptionForNode(node)
	}

	sort.Sort(componentDeltaByDelta(deltas))

	// scale down components
	for _, d := range deltas {
		if d.delta < 0 {
			stopTotal := d.delta * -1
			for i := 0; i < stopTotal; i++ {
				option := BestStopComponentOption(optionByNode, d.componentName)
				if option == nil {
					log.Warn("scale: unable to scale down component", "component", d.componentName, "delta", d.delta)
					break
				} else {
					optionByNode[option.TargetNode.NodeId] = mergeOption(optionByNode, option)
				}
			}
		}
	}

	// scale up
	for _, d := range deltas {
		if d.delta > 0 {
			for i := 0; i < d.delta; i++ {
				option := BestStartComponentOption(optionByNode, d.componentName, d.reserveMemoryMiB,
					d.maxInstancesPerNode, false)
				if option == nil {
					log.Warn("scale: unable to scale up component", "component", d.componentName,
						"maxInstPerNode", d.maxInstancesPerNode, "targetDelta", d.delta, "added", i)
					break
				} else {
					log.Info("scale: BestStart", "component", d.componentName,
						"componentMemory", d.reserveMemoryMiB, "peerUrl", option.TargetNode.PeerUrl,
						"totalNodeRam", option.TargetNode.TotalMemoryMiB, "ramUsedAfter", option.RamUsed())
					optionByNode[option.TargetNode.NodeId] = mergeOption(optionByNode, option)
				}
			}
		}
	}

	// rebalance - migrate components to empty nodes
	for _, node := range nodes {
		placementOption := optionByNode[node.NodeId]
		ramUsed := placementOption.RamUsed()

		if ramUsed == 0 {
			fromOption, compName, requiredRam := findCompToMove(optionByNode, node.NodeId, node.TotalMemoryMiB)
			if fromOption != nil {
				log.Info("scale: rebalancing component", "component", compName, "requiredRam", requiredRam,
					"toNode", common.TruncNodeId(node.NodeId), "freeRam", node.TotalMemoryMiB)
				cloned := fromOption.cloneWithTargetDelta(compName, -1, requiredRam)
				fromOption.Input.TargetCounts = cloned.Input.TargetCounts

				cloned = placementOption.cloneWithTargetDelta(compName, 1, requiredRam)
				placementOption.Input.TargetCounts = cloned.Input.TargetCounts
			}
		}
	}

	options := make([]*PlacementOption, 0)
	for _, opt := range optionByNode {
		if len(opt.Input.TargetCounts) > 0 {
			sort.Sort(ComponentTargetByCompName(opt.Input.TargetCounts))
			options = append(options, opt)
		}
	}
	sort.Sort(PlacementOptionByNode(options))

	return options
}

func findCompToMove(placementByNode map[string]*PlacementOption, otherNodeId string,
	freeMemoryMiB int64) (*PlacementOption, string, int64) {
	for _, placementOption := range placementByNode {
		runningComps, totalContainers := placementOption.ContainerCountByComponent()
		if log.IsDebug() {
			log.Debug("scale: findCompToMove", "totalContainers", totalContainers, "running", runningComps,
				"nodeId", common.TruncNodeId(placementOption.TargetNode.NodeId))
		}
		if placementOption.TargetNode.NodeId != otherNodeId && totalContainers > 1 {
			for _, rc := range placementOption.TargetNode.RunningComponents {
				ramRequired := placementOption.ramForComponent(rc.ComponentName)
				if runningComps[rc.ComponentName] > 0 && ramRequired <= freeMemoryMiB {
					return placementOption, rc.ComponentName, ramRequired
				}
			}
			for _, tc := range placementOption.Input.TargetCounts {
				ramRequired := placementOption.ramForComponent(tc.ComponentName)
				if runningComps[tc.ComponentName] > 0 && ramRequired <= freeMemoryMiB {
					return placementOption, tc.ComponentName, ramRequired
				}
			}
		}
	}
	return nil, "", 0
}

func newPlacementOptionsByNodeId(nodes []v1.NodeStatus) map[string]*PlacementOption {
	byNodeId := map[string]*PlacementOption{}
	for _, n := range nodes {
		byNodeId[n.NodeId] = newPlacementOptionForNode(n)
	}
	return byNodeId
}

func newPlacementOptionForNode(node v1.NodeStatus) *PlacementOption {
	return &PlacementOption{
		TargetNode: &node,
		Input: &v1.StartStopComponentsInput{
			ClientNodeId:  "",
			TargetVersion: node.Version,
			TargetCounts:  []v1.ComponentTarget{},
			ReturnStatus:  true,
		},
	}
}
