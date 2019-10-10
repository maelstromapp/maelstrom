package component

import v1 "github.com/coopernurse/maelstrom/pkg/v1"

type ScaleInput struct {
	TargetVersion int64
	TargetCounts  []ScaleTarget
}

type ScaleTarget struct {
	Component         *v1.Component
	Delta             int64
	RequiredMemoryMiB int64
}

func (t ScaleTarget) ToV1ComponentDelta() v1.ComponentDelta {
	return v1.ComponentDelta{
		ComponentName:     t.Component.Name,
		Delta:             t.Delta,
		RequiredMemoryMiB: t.RequiredMemoryMiB,
	}
}

type scaleInputInternal struct {
	*ScaleInput
	Output chan ScaleOutput
}

type scaleComponentInput struct {
	targetCount int
}

type ScaleOutput struct {
	TargetVersionMismatch bool
	Started               []v1.ComponentDelta
	Stopped               []v1.ComponentDelta
	Errors                []v1.ComponentDeltaError
}
