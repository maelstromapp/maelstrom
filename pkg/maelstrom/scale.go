package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
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
	componentName     string
	minInstances      int
	maxInstances      int
	currentInstances  int
	targetInstances   int
	reserveMemoryMiB  int64
	pctMaxConcurrency float64
}

type componentDelta struct {
	componentName    string
	reserveMemoryMiB int64
	delta            int
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

func componentsByName(comps []v1.Component) map[string]v1.Component {
	byName := map[string]v1.Component{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	return byName
}

func loadActiveComponents(nodes []v1.NodeStatus, db Db) (map[string]v1.Component, error) {
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
			componentName:     compName,
			minInstances:      int(comp.MinInstances),
			maxInstances:      int(comp.MaxInstances),
			reserveMemoryMiB:  comp.Docker.ReserveMemoryMiB,
			currentInstances:  len(infos),
			targetInstances:   scaleOutput.targetInstances,
			pctMaxConcurrency: scaleOutput.pctMaxConcurrency,
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
					totalConcur += val.Concurrency
				}
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
	if target > input.maxInst && input.maxInst > input.minInst {
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
				componentName:    c.componentName,
				reserveMemoryMiB: c.reserveMemoryMiB,
				delta:            c.targetInstances - c.currentInstances,
			})
		}
	}
	return deltas
}

func mergeTargetCounts(a []v1.ComponentDelta, b []v1.ComponentDelta) []v1.ComponentDelta {
	byComponentName := map[string]v1.ComponentDelta{}
	for _, count := range a {
		byComponentName[count.ComponentName] = count
	}
	for _, count := range b {
		oldCount, ok := byComponentName[count.ComponentName]
		if ok {
			count.Delta += oldCount.Delta
			if count.RequiredMemoryMiB <= 0 {
				count.RequiredMemoryMiB = oldCount.RequiredMemoryMiB
			}
		}
		byComponentName[count.ComponentName] = count
	}

	merged := make([]v1.ComponentDelta, 0)
	for _, count := range byComponentName {
		if count.Delta != 0 {
			if count.RequiredMemoryMiB <= 0 && count.Delta > 0 {
				panic(fmt.Sprintf("Invalid ComponentDelta: %+v", count))
			}
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

func countByComponent(componentName string, rcs []v1.ComponentInfo) int {
	count := 0
	for _, rc := range rcs {
		if rc.ComponentName == componentName {
			count++
		}
	}
	return count
}

func nodeRamUsedMiB(runningComps []v1.ComponentInfo) int64 {
	ramUsed := int64(0)
	for _, rc := range runningComps {
		ramUsed += rc.MemoryReservedMiB
	}
	return ramUsed
}

func computeScaleStartStopInputs(nodes []v1.NodeStatus, deltas []componentDelta) []*PlacementOption {

	optionByNode := map[string]*PlacementOption{}
	beforeRamByNode := map[string]int64{}

	for _, node := range nodes {
		totalRam := int64(0)
		for _, rc := range node.RunningComponents {
			totalRam += rc.MemoryReservedMiB
		}
		beforeRamByNode[node.NodeId] = totalRam
	}

	sort.Sort(componentDeltaByDelta(deltas))

	// scale down components
	for _, d := range deltas {
		if d.delta < 0 {
			stopTotal := d.delta * -1
			for i := 0; i < stopTotal; i++ {
				option := BestStopComponentOption(nodes, optionByNode, d.componentName)
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
				option := BestStartComponentOption(nodes, optionByNode, d.componentName, d.reserveMemoryMiB, false)
				if option == nil {
					log.Warn("scale: unable to scale up component", "component", d.componentName, "delta", d.delta)
					break
				} else {
					optionByNode[option.TargetNode.NodeId] = mergeOption(optionByNode, option)
				}
			}
		}
	}

	// rebalance - migrate components to empty nodes
	for _, node := range nodes {
		runningComps := RunningComponents(node, optionByNode[node.NodeId])
		ramUsed := nodeRamUsedMiB(runningComps)
		if ramUsed == 0 {
			fromNode, comp := findCompToMove(nodes, optionByNode, node.NodeId, node.TotalMemoryMiB)
			if fromNode != nil {
				fromOpt := getOrCreate(optionByNode, *fromNode)
				fromOpt.Input.TargetCounts = mergeTargetCounts(fromOpt.Input.TargetCounts, []v1.ComponentDelta{
					{
						ComponentName: comp.ComponentName,
						Delta:         -1,
					},
				})
				optionByNode[fromNode.NodeId] = fromOpt

				toOpt := getOrCreate(optionByNode, node)
				toOpt.Input.TargetCounts = mergeTargetCounts(toOpt.Input.TargetCounts, []v1.ComponentDelta{
					{
						ComponentName:     comp.ComponentName,
						Delta:             1,
						RequiredMemoryMiB: comp.MemoryReservedMiB,
					},
				})
				optionByNode[node.NodeId] = toOpt
			}
		}
	}

	options := make([]*PlacementOption, 0)
	for _, opt := range optionByNode {
		if len(opt.Input.TargetCounts) > 0 {
			options = append(options, opt)
		}
	}
	sort.Sort(PlacementOptionByNode(options))
	return options
}

func getOrCreate(optionByNode map[string]*PlacementOption, node v1.NodeStatus) *PlacementOption {
	opt := optionByNode[node.NodeId]
	if opt == nil {
		opt = &PlacementOption{
			TargetNode: node,
			Input: v1.StartStopComponentsInput{
				ClientNodeId:  "",
				TargetVersion: node.Version,
				TargetCounts:  []v1.ComponentDelta{},
				ReturnStatus:  true,
			},
		}
	}
	return opt
}

func findCompToMove(nodes []v1.NodeStatus, placementByNode map[string]*PlacementOption, otherNodeId string,
	freeMemoryMiB int64) (*v1.NodeStatus, *v1.ComponentInfo) {
	for _, n := range nodes {
		runningComps := RunningComponents(n, placementByNode[n.NodeId])
		if n.NodeId != otherNodeId && len(runningComps) > 1 {
			for _, rc := range runningComps {
				if rc.MemoryReservedMiB <= freeMemoryMiB {
					return &n, &rc
				}
			}
		}
	}
	return nil, nil
}

func CalcAutoscalePlacement(nodes []v1.NodeStatus, componentsByName map[string]v1.Component) []*PlacementOption {
	concurrency := toComponentConcurrency(nodes, componentsByName)
	deltas := toComponentDeltas(concurrency)
	return computeScaleStartStopInputs(nodes, deltas)
}

func OptionNetRam(option *PlacementOption, componentsByName map[string]v1.Component) int64 {
	ram := int64(0)
	for _, tc := range option.Input.TargetCounts {
		ram += tc.Delta * componentsByName[tc.ComponentName].Docker.ReserveMemoryMiB
	}
	return ram
}

func totalRamUsed(node v1.NodeStatus, options []PlacementOption,
	componentsByName map[string]v1.Component) (int64, *PlacementOption) {
	totalRam := int64(0)
	optIdx := -1
	for _, rc := range node.RunningComponents {
		totalRam += rc.MemoryReservedMiB
	}
	for idx, opt := range options {
		if opt.TargetNode.NodeId == node.NodeId {
			optIdx = idx
			fmt.Printf("found opt: %v\n", opt)
			for _, tc := range opt.Input.TargetCounts {
				comp, ok := componentsByName[tc.ComponentName]
				if ok {
					totalRam += comp.Docker.ReserveMemoryMiB * tc.Delta
				} else {
					fmt.Printf("comp not found: %s - %+v\n", tc.ComponentName, tc)
				}
			}
		}
	}
	if optIdx >= 0 {
		return totalRam, &options[optIdx]
	}
	return totalRam, nil
}
