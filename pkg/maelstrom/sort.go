package maelstrom

import "gitlab.com/coopernurse/maelstrom/pkg/v1"

type nameValueByName []v1.NameValue

func (s nameValueByName) Len() int           { return len(s) }
func (s nameValueByName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s nameValueByName) Less(i, j int) bool { return s[i].Name < s[j].Name }

type componentWithEventSourcesByName []v1.ComponentWithEventSources

func (s componentWithEventSourcesByName) Len() int      { return len(s) }
func (s componentWithEventSourcesByName) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s componentWithEventSourcesByName) Less(i, j int) bool {
	return s[i].Component.Name < s[j].Component.Name
}

type NodeStatusByEmptyThenLoadAvg []v1.NodeStatus

func (s NodeStatusByEmptyThenLoadAvg) Len() int      { return len(s) }
func (s NodeStatusByEmptyThenLoadAvg) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s NodeStatusByEmptyThenLoadAvg) Less(i, j int) bool {
	if len(s[i].RunningComponents) == 0 && len(s[j].RunningComponents) > 0 {
		return true
	}
	if len(s[j].RunningComponents) == 0 && len(s[i].RunningComponents) > 0 {
		return false
	}
	return s[i].LoadAvg1m < s[j].LoadAvg1m
}

type ComponentInfoByRunningCountAndReqTime struct {
	Components     []v1.ComponentInfo
	InstanceCounts map[string]int
}

func (s ComponentInfoByRunningCountAndReqTime) Len() int { return len(s.Components) }
func (s ComponentInfoByRunningCountAndReqTime) Swap(i, j int) {
	s.Components[i], s.Components[j] = s.Components[j], s.Components[i]
}
func (s ComponentInfoByRunningCountAndReqTime) Less(i, j int) bool {
	iCount := s.InstanceCounts[s.Components[i].ComponentName]
	jCount := s.InstanceCounts[s.Components[j].ComponentName]

	if iCount > jCount {
		return true
	}
	if jCount > iCount {
		return false
	}
	return s.Components[i].LastRequestTime > s.Components[j].LastRequestTime
}

type ComponentDeltaByCompName []v1.ComponentDelta

func (s ComponentDeltaByCompName) Len() int           { return len(s) }
func (s ComponentDeltaByCompName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s ComponentDeltaByCompName) Less(i, j int) bool { return s[i].ComponentName < s[j].ComponentName }

type StringPtr []*string

func (s StringPtr) Len() int           { return len(s) }
func (s StringPtr) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s StringPtr) Less(i, j int) bool { return *s[i] < *s[j] }
