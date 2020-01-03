package converge

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestConvergePlanNoContainers(t *testing.T) {
	conv := NewConverger(ComponentTarget{})

	target := newTarget(0, 1, v1.StartParallelismParallel, v1.RestartOrderStartstop)
	expected := newConvergePlan(false)
	assert.Equal(t, expected, conv.plan(target))

	target.Count = 1
	expected = newConvergePlan(true, newStartStep(target, false))
	assert.Equal(t, expected, conv.plan(target))
}

func TestConvergePlanScaleDown(t *testing.T) {
	conv := NewConverger(ComponentTarget{})
	target := newTarget(0, 1, v1.StartParallelismParallel, v1.RestartOrderStartstop)

	// 2 running containers
	conv.containers = newContainers(target, 2)

	// target=0 -> stop both
	expected := newConvergePlan(false, newStopStep(1, reasonScaleDown), newStopStep(2, reasonScaleDown))
	assert.Equal(t, expected, conv.plan(target))

	// target=1 -> stop 1
	target = newTarget(1, 1, v1.StartParallelismParallel, v1.RestartOrderStartstop)
	expected = newConvergePlan(false, newStopStep(2, reasonScaleDown))
	assert.Equal(t, expected, conv.plan(target))
}

func TestConvergePlanRollingDeploy(t *testing.T) {
	conv := NewConverger(ComponentTarget{})

	// v1 - 2 running containers
	target := newTarget(2, 1, v1.StartParallelismParallel, v1.RestartOrderStartstop)
	conv.containers = newContainers(target, 2)

	// v2 - parallel - startstop
	target = newTarget(2, 2, v1.StartParallelismParallel, v1.RestartOrderStartstop)
	expected := newConvergePlan(true,
		newStartStep(target, false), newStopStep(1, reasonVersionChanged),
		newStartStep(target, false), newStopStep(2, reasonVersionChanged))
	assert.Equal(t, expected, conv.plan(target))

	// v3 - series - stopstart
	target = newTarget(2, 3, v1.StartParallelismSeries, v1.RestartOrderStopstart)
	expected = newConvergePlan(true,
		newStopStep(1, reasonVersionChanged), newStartStep(target, true),
		newStopStep(2, reasonVersionChanged), newStartStep(target, true))
	assert.Equal(t, expected, conv.plan(target))

	// v4 - seriesfirst - stopstart
	target = newTarget(2, 4, v1.StartParallelismSeriesfirst, v1.RestartOrderStopstart)
	expected = newConvergePlan(true,
		newStopStep(1, reasonVersionChanged), newStartStep(target, true),
		newStopStep(2, reasonVersionChanged), newStartStep(target, false))
	assert.Equal(t, expected, conv.plan(target))

	// v5 - hybrid - seriesfirst - startstop - with a scale up to 4
	target = newTarget(4, 4, v1.StartParallelismSeriesfirst, v1.RestartOrderStartstop)
	expected = newConvergePlan(true,
		newStartStep(target, true), newStopStep(1, reasonVersionChanged),
		newStartStep(target, false), newStopStep(2, reasonVersionChanged),
		newStartStep(target, false), newStartStep(target, false))
	assert.Equal(t, expected, conv.plan(target))

	// v5 - hybrid - seriesfirst - startstop - with a scale down to 1
	//
	// note: this is a little odd - we'd probably expect the 2nd stop to have a reason: "scale down"
	//       but due to how the loop is written we apply the "did the component change" test first
	//       in theory, both reasons are valid.
	target = newTarget(1, 4, v1.StartParallelismSeriesfirst, v1.RestartOrderStartstop)
	expected = newConvergePlan(true,
		newStartStep(target, true), newStopStep(1, reasonVersionChanged),
		newStopStep(2, reasonVersionChanged))
	assert.Equal(t, expected, conv.plan(target))
}

/////////////////////////////////////////////////

func newTarget(count int, version int64, parallelism v1.StartParallelism,
	restartOrder v1.RestartOrder) ComponentTarget {
	return ComponentTarget{
		Component: &v1.Component{Name: "foo", Version: version, StartParallelism: parallelism,
			RestartOrder: restartOrder, Docker: &v1.DockerComponent{Image: "image/foo"}},
		Count: count,
	}
}

func newConvergePlan(pull bool, steps ...convergeStep) convergePlan {
	if steps == nil {
		steps = []convergeStep{}
	}
	return convergePlan{
		pull:  pull,
		steps: steps,
	}
}

func newStartStep(target ComponentTarget, lock bool) convergeStep {
	return convergeStep{
		start: &startStep{
			component: target.Component,
			lock:      lock,
		},
	}
}

func newStopStep(id maelContainerId, reason string) convergeStep {
	return convergeStep{
		stop: &stopStep{
			containerId: id,
			reason:      reason,
		},
	}
}

func newContainers(target ComponentTarget, count int) []*Container {
	containers := make([]*Container, count)
	for i := 0; i < count; i++ {
		containers[i] = &Container{id: maelContainerId(i + 1), component: target.Component}
	}
	return containers
}
