package converge

import (
	"context"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"sync"
	"time"
)

var ErrConvergeContextCanceled = fmt.Errorf("converger: context canceled")

// side effect functions
type ConvergeNotifyContainersChanged func()
type ConvergePullImage func(c *v1.Component) error
type ConvergeCreateContainer func(ctx context.Context, c *v1.Component) *Container
type ConvergeStopContainer func(c *Container, reason string)
type ConvergeStartLockAcquire func(ctx context.Context, comp *v1.Component) (bool, error)
type ConvergePostStartContainer func(comp *v1.Component, releaseLock bool, success bool) error

const reasonScaleDown = "scale down"
const reasonVersionChanged = "component version changed"
const reasonImageUpdated = "docker image updated"

type convergePlan struct {
	pull  bool
	steps []convergeStep
}

// union of step types
type convergeStep struct {
	start *startStep
	stop  *stopStep
}

func (c convergeStep) String() string {
	if c.start != nil {
		return c.start.String()
	} else if c.stop != nil {
		return c.stop.String()
	} else {
		return "unknown step"
	}
}

type startStep struct {
	component *v1.Component
	lock      bool
}

func (s *startStep) String() string {
	return fmt.Sprintf("startStep: component=%s ver=%d lock=%v", s.component.Name, s.component.Version, s.lock)
}

type stopStep struct {
	containerId maelContainerId
	reason      string
}

func (s *stopStep) String() string {
	return fmt.Sprintf("stopStep: containerId=%d reason=%s", s.containerId, s.reason)
}

type ComponentTarget struct {
	Component *v1.Component
	Count     int
}

type Converger struct {
	currentTarget ComponentTarget
	containers    []*Container
	ctx           context.Context
	ctxCancel     context.CancelFunc
	runCh         chan chan bool
	lock          *sync.Mutex
	wg            *sync.WaitGroup

	// side effect functions
	convergeNotifyContainersChanged ConvergeNotifyContainersChanged
	convergePullImage               ConvergePullImage
	convergeCreateContainer         ConvergeCreateContainer
	convergeStopContainer           ConvergeStopContainer
	convergeStartLockAcquire        ConvergeStartLockAcquire
	convergePostStartContainer      ConvergePostStartContainer
}

func NewConverger(currentTarget ComponentTarget) *Converger {
	ctx, ctxCancel := context.WithCancel(context.Background())
	return &Converger{
		currentTarget: currentTarget,
		ctx:           ctx,
		ctxCancel:     ctxCancel,
		runCh:         make(chan chan bool),
		lock:          &sync.Mutex{},
		wg:            &sync.WaitGroup{},
	}
}

func (c *Converger) WithTarget(target ComponentTarget) *Converger {
	c.SetTarget(target)
	return c
}

func (c *Converger) WithPullImage(fx ConvergePullImage) *Converger {
	c.convergePullImage = fx
	return c
}

func (c *Converger) WithCreateContainer(fx ConvergeCreateContainer) *Converger {
	c.convergeCreateContainer = fx
	return c
}

func (c *Converger) WithStopContainer(fx ConvergeStopContainer) *Converger {
	c.convergeStopContainer = fx
	return c
}

func (c *Converger) WithStartLockAcquire(fx ConvergeStartLockAcquire) *Converger {
	c.convergeStartLockAcquire = fx
	return c
}

func (c *Converger) WithPostStartContainer(fx ConvergePostStartContainer) *Converger {
	c.convergePostStartContainer = fx
	return c
}

func (c *Converger) WithNotifyContainersChanged(fx ConvergeNotifyContainersChanged) *Converger {
	c.convergeNotifyContainersChanged = fx
	return c
}

func (c *Converger) SetComponent(comp *v1.Component) {
	target := c.GetTarget()
	target.Component = comp
	c.SetTarget(target)
}

func (c *Converger) SetTarget(target ComponentTarget) chan bool {
	c.lock.Lock()
	c.currentTarget = target
	c.lock.Unlock()
	doneCh := make(chan bool, 1)
	go func() { c.runCh <- doneCh }()
	return doneCh
}

func (c *Converger) GetTarget() (target ComponentTarget) {
	c.lock.Lock()
	target = c.currentTarget
	c.lock.Unlock()
	return
}

func (c *Converger) GetComponentInfo() []v1.ComponentInfo {
	info := make([]v1.ComponentInfo, 0)
	for _, cn := range c.getContainers() {
		info = append(info, cn.ComponentInfo())
	}
	return info
}

func (c *Converger) OnDockerEvent(event *common.DockerEvent) {
	run := false
	if event.ContainerExited != nil && event.ContainerExited.ContainerId != "" {
		c.stopAndRemoveContainer(0, event.ContainerExited.ContainerId, "container exited")
		run = true
	} else if event.ImageUpdated != nil && common.NormalizeImageName(c.GetTarget().Component.Docker.Image) ==
		common.NormalizeImageName(event.ImageUpdated.ImageName) {
		c.markContainersForTermination(reasonImageUpdated)
		run = true
	}

	if run {
		// alert background goroutine to re-converge
		go func() { c.runCh <- make(chan bool, 1) }()
	}
}

func (c *Converger) Start() {
	c.wg.Add(1)
	go c.run()
}

func (c *Converger) Stop() {
	c.ctxCancel()
	c.wg.Wait()
}

func (c *Converger) getContainers() (containers []*Container) {
	c.lock.Lock()
	containers = c.containers
	c.lock.Unlock()
	return
}

func (c *Converger) getContainerCount() (count int) {
	c.lock.Lock()
	count = len(c.containers)
	c.lock.Unlock()
	return
}

func (c *Converger) markContainersForTermination(reason string) {
	c.lock.Lock()
	for _, cn := range c.containers {
		cn.terminateReason = reason
	}
	c.lock.Unlock()
	go func() { c.runCh <- make(chan bool, 1) }()
}

func (c *Converger) notifyContainersChanged() {
	go c.convergeNotifyContainersChanged()
}

func (c *Converger) stopAndRemoveContainer(id maelContainerId, dockerContainerId string, reason string) {
	// Stop
	var found *Container
	for _, cn := range c.getContainers() {
		if id == cn.id || dockerContainerId == cn.containerId {
			cn.setStatus(v1.ComponentStatusStopping)
			go c.notifyContainersChanged()
			c.convergeStopContainer(cn, reason)
			found = cn
			log.Info("converge: stopAndRemoveContainer found container", "id", cn.id, "containerId", cn.containerId)
		}
	}

	// Remove
	if found != nil {
		c.lock.Lock()
		keep := c.containers[:0]
		for _, cn := range c.containers {
			if found.id != cn.id || found.containerId != cn.containerId {
				keep = append(keep, cn)
			}
		}
		c.containers = keep
		if log.IsDebug() {
			log.Debug("converge: post stopAndRemoveContainer", "containers", c.containers)
		}
		c.lock.Unlock()
		c.notifyContainersChanged()
	} else {
		log.Debug("converge: stopAndRemoveContainer - no container found", "maelContainerId", id,
			"dockerContainerId", dockerContainerId)
	}
}

func (c *Converger) run() {
	defer c.wg.Done()

	running := true
	ticker := time.NewTicker(time.Minute)
	for running {
		select {
		case doneCh := <-c.runCh:
			c.converge()
			doneCh <- true
		case <-ticker.C:
			c.converge()
		case <-c.ctx.Done():
			running = false
		}
	}
	log.Info("converger: exiting gracefully", "component", c.currentTarget.Component.Name)
}

func (c *Converger) converge() {
	startTime := time.Now()
	target := c.GetTarget()
	plan := c.plan(target)

	if plan.pull {
		err := c.convergePullImage(target.Component)
		if err != nil {
			log.Error("converge: unable to pull image - aborting", "component", target.Component.Name,
				"image", target.Component.Docker.Image, "err", err)
			return
		}
	}

	for _, step := range plan.steps {
		var err error
		if step.start != nil {
			err = c.start(step.start)
		} else if step.stop != nil {
			err = c.stop(step.stop)
		}

		if err != nil {
			if err == ErrConvergeContextCanceled {
				log.Warn("converge: context canceled - aborting", "component", target.Component.Name)
			} else {
				log.Error("converge: error applying step - aborting", "step", step, "err", err)
			}
			return
		}
	}

	if len(plan.steps) > 0 {
		elapsed := time.Now().Sub(startTime)
		log.Info("converge: successfully converged to target", "component", target.Component.Name,
			"version", target.Component.Version, "count", target.Count, "image", target.Component.Docker.Image,
			"elapsed", elapsed.String())
	}
}

func (c *Converger) addContainer(container *Container) {
	if container != nil {
		c.lock.Lock()
		c.containers = append(c.containers, container)
		c.lock.Unlock()
	}
}

func (c *Converger) removeContainer(container *Container) bool {
	if container == nil {
		return false
	}

	removed := false
	c.lock.Lock()
	for i, cont := range c.containers {
		if (container.id != 0 && cont.id == container.id) ||
			(container.containerId != "" && cont.containerId == container.containerId) {
			c.containers = append(c.containers[:i], c.containers[i+1:]...)
			removed = true
			break
		}
	}
	c.lock.Unlock()
	return removed
}

func (c *Converger) start(step *startStep) error {
	var releaseLock bool
	var err error
	log.Info("converge: startStep - acquiring lock", "component", step.component.Name, "ver", step.component.Version)
	if step.lock {
		releaseLock, err = c.convergeStartLockAcquire(c.ctx, step.component)
		if err != nil {
			if err != ErrConvergeContextCanceled {
				err = errors.Wrap(err, "unable to acquire component lock")
			}
			return err
		}
	}

	log.Info("converge: startStep - starting container", "component", step.component.Name, "ver", step.component.Version)
	success := true
	container := c.convergeCreateContainer(c.ctx, step.component)
	c.addContainer(container)
	c.notifyContainersChanged()

	err = container.startAndHealthCheck(c.ctx)
	if err != nil {
		c.removeContainer(container)
		c.notifyContainersChanged()
		return errors.Wrap(err, "start container failed")
	}
	go container.run()
	c.notifyContainersChanged()

	log.Info("converge: startStep - post start container", "component", step.component.Name, "ver", step.component.Version)
	err2 := c.convergePostStartContainer(step.component, releaseLock, success)
	if err2 != nil {
		log.Error("converge: error releasing component lock", "component", step.component.Name,
			"err", err2)
	}

	return err
}

func (c *Converger) stop(step *stopStep) error {
	log.Info("converge: stopStep - stopping container", "containerId", step.containerId)
	c.stopAndRemoveContainer(step.containerId, "unused", step.reason)
	return nil
}

func (c *Converger) plan(target ComponentTarget) convergePlan {

	// containers to stop
	toStop := make([]*stopStep, 0)

	// count of running containers that match the target version
	activeCount := 0

	// loop over containers and mark containers to stop if any of these is true:
	//   - running count exceeds the target count
	//   - running container version doesn't match target
	//   - running container has terminateReason set
	for _, cn := range c.containers {
		if activeCount >= target.Count {
			toStop = append(toStop, &stopStep{
				containerId: cn.id,
				reason:      reasonScaleDown,
			})
		} else if cn.terminateReason != "" {
			toStop = append(toStop, &stopStep{
				containerId: cn.id,
				reason:      cn.terminateReason,
			})
		} else if cn.component.Name == target.Component.Name && cn.component.Version == target.Component.Version {
			activeCount++
		} else {
			toStop = append(toStop, &stopStep{
				containerId: cn.id,
				reason:      reasonVersionChanged,
			})
		}
	}

	// compute number of containers to start
	// in the case of a rolling deploy we'd expect:
	//   toStart = target.Count            (start all, up to target)
	//   len(toStop) = len(c.containers)   (stop all)
	toStart := target.Count - activeCount

	plan := convergePlan{
		// pull image if we have any start requests
		pull:  toStart > 0,
		steps: make([]convergeStep, 0),
	}

	// lock on first start unless we're full parallel
	para := getStartParallelism(target.Component)
	restartOrder := getRestartOrder(target.Component)

	lock := para != v1.StartParallelismParallel
	// determine start order - we'll alternate steps between start and stop
	addStartStep := restartOrder == v1.RestartOrderStartstop
	for toStart > 0 || len(toStop) > 0 {
		if addStartStep {
			addStartStep = false
			if toStart > 0 {
				plan.steps = append(plan.steps, convergeStep{
					start: &startStep{
						component: target.Component,
						lock:      lock,
					},
				})

				// if paralellism is "seriesfirst" we don't have to lock after the first start event
				if para == v1.StartParallelismSeriesfirst {
					lock = false
				}
				toStart--
			}
		} else {
			addStartStep = true
			if len(toStop) > 0 {
				// remove first toStop element and add to plan.steps
				var step *stopStep
				step, toStop = toStop[0], toStop[1:]
				plan.steps = append(plan.steps, convergeStep{stop: step})
			}
		}
	}

	if log.IsDebug() {
		log.Debug("converge: prepared plan", "pull", plan.pull, "steps", fmt.Sprintf("%v", plan.steps),
			"containers", c.containers)
	}

	return plan
}

func getStartParallelism(c *v1.Component) (p v1.StartParallelism) {
	p = v1.StartParallelismParallel
	if c.StartParallelism != "" {
		p = c.StartParallelism
	}
	return
}

func getRestartOrder(c *v1.Component) (ro v1.RestartOrder) {
	ro = v1.RestartOrderStopstart
	if c.RestartOrder != "" {
		ro = c.RestartOrder
	}
	return ro
}
