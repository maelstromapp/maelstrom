package maelstrom

import (
	"github.com/coopernurse/maelstrom/pkg/v1"
	"time"
)

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

type NodeStatusByStartedAt []v1.NodeStatus

func (s NodeStatusByStartedAt) Len() int           { return len(s) }
func (s NodeStatusByStartedAt) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s NodeStatusByStartedAt) Less(i, j int) bool { return s[i].StartedAt < s[j].StartedAt }

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

type ComponentTargetByCompName []v1.ComponentTarget

func (s ComponentTargetByCompName) Len() int           { return len(s) }
func (s ComponentTargetByCompName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s ComponentTargetByCompName) Less(i, j int) bool { return s[i].ComponentName < s[j].ComponentName }

type projectInfoByName []v1.ProjectInfo

func (s projectInfoByName) Len() int           { return len(s) }
func (s projectInfoByName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s projectInfoByName) Less(i, j int) bool { return s[i].ProjectName < s[j].ProjectName }

type httpEventSourcesForResolver []v1.EventSource

func (s httpEventSourcesForResolver) Len() int      { return len(s) }
func (s httpEventSourcesForResolver) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s httpEventSourcesForResolver) Less(i, j int) bool {
	// want most specific to least specific
	if s[i].Http != nil && s[j].Http != nil {
		// if hostname empty, sort to bottom
		if s[i].Http.Hostname == "" && s[j].Http.Hostname != "" {
			return false
		}
		if s[j].Http.Hostname == "" && s[i].Http.Hostname != "" {
			return true
		}

		// if paths are same length, sort by hostname desc
		if len(s[i].Http.PathPrefix) == len(s[j].Http.PathPrefix) {
			return s[i].Http.Hostname > s[j].Http.Hostname
		}
		// sort by path length descending so most specific paths are considered first
		return len(s[i].Http.PathPrefix) > len(s[j].Http.PathPrefix)
	}
	return s[i].Name < s[j].Name
}

type DurationAscend []time.Duration

func (s DurationAscend) Len() int           { return len(s) }
func (s DurationAscend) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s DurationAscend) Less(i, j int) bool { return s[i] < s[j] }
