package component

import v1 "github.com/coopernurse/maelstrom/pkg/v1"

type ScaleInput struct {
	TargetVersion int64
	TargetCounts  []ScaleTarget
}

type ScaleTarget struct {
	Component         *v1.Component
	TargetCount       int64
	RequiredMemoryMiB int64
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
