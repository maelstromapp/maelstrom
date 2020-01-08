package converge

import (
	"context"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/revproxy"
	"github.com/coopernurse/maelstrom/pkg/router"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"sync"
)

func NewRegistry(dockerClient *docker.Client, routerReg *router.Registry, maelstromUrl string,
	pullImage ConvergePullImage,
	startLockAcquire ConvergeStartLockAcquire,
	postStartContainer ConvergePostStartContainer,
	notifyContainersChanged ConvergeNotifyContainersChanged) *Registry {
	bufferPool := revproxy.NewProxyBufferPool()
	return &Registry{
		dockerClient:            dockerClient,
		routerReg:               routerReg,
		maelstromUrl:            maelstromUrl,
		pullImage:               pullImage,
		startLockAcquire:        startLockAcquire,
		postStartContainer:      postStartContainer,
		notifyContainersChanged: notifyContainersChanged,
		version:                 common.NowMillis(),
		byCompName:              make(map[string]*Converger),
		containerCounterId:      0,
		bufferPool:              bufferPool,
		lock:                    &sync.Mutex{},
	}
}

type Registry struct {
	dockerClient            *docker.Client
	routerReg               *router.Registry
	maelstromUrl            string
	pullImage               ConvergePullImage
	startLockAcquire        ConvergeStartLockAcquire
	postStartContainer      ConvergePostStartContainer
	notifyContainersChanged ConvergeNotifyContainersChanged
	version                 int64
	byCompName              map[string]*Converger
	containerCounterId      maelContainerId
	bufferPool              *revproxy.ProxyBufferPool
	lock                    *sync.Mutex
}

func (r *Registry) RemoveStaleContainers() error {
	rmCount, err := common.RemoveMaelstromContainers(r.dockerClient, "removing stale containers")
	if err != nil {
		return errors.Wrap(err, "converge: remove containers failed")
	}
	if rmCount > 0 {
		log.Info("converge: removed stale containers", "count", rmCount)
	}
	return nil
}

func (r *Registry) GetRouterRegistry() *router.Registry {
	return r.routerReg
}

func (r *Registry) Shutdown() {
	r.lock.Lock()
	defer r.lock.Unlock()

	log.Info("converge: shutdown starting")
	wg := &sync.WaitGroup{}
	for _, c := range r.byCompName {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Stop()
		}()
	}
	wg.Wait()
	r.byCompName = make(map[string]*Converger)
}

func (r *Registry) OnDockerEvent(msg common.DockerEvent) {
	r.lock.Lock()
	convergers := r.byCompName
	r.lock.Unlock()

	for _, c := range convergers {
		c.OnDockerEvent(&msg)
	}
}

func (r *Registry) OnComponentNotification(change v1.DataChangedUnion) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if change.RemoveComponent != nil {
		cn, ok := r.byCompName[change.RemoveComponent.Name]
		if ok {
			log.Info("converge: shutting down component - component removed from db",
				"component", change.RemoveComponent.Name)
			delete(r.byCompName, change.RemoveComponent.Name)
			go cn.Stop()
		}
	}
	if change.PutComponent != nil {
		cn, ok := r.byCompName[change.PutComponent.Name]
		if ok {
			cn.SetComponent(change.PutComponent)
		}
	}
}

func (r *Registry) ByComponent(comp *v1.Component) (c *Converger) {
	r.lock.Lock()
	defer r.lock.Unlock()

	c = r.byCompName[comp.Name]
	if c == nil {
		c = NewConverger(ComponentTarget{
			Component: comp,
			Count:     0,
		}).
			WithPullImage(r.pullImage).
			WithStartContainer(r.startContainerAndHealthCheck).
			WithStopContainer(r.stopContainer).
			WithStartLockAcquire(r.startLockAcquire).
			WithPostStartContainer(r.postStartContainer).
			WithNotifyContainersChanged(r.onContainersChanged)
		c.Start()
		r.byCompName[comp.Name] = c
	}
	return
}

func (r *Registry) GetState() (version int64, compInfo []v1.ComponentInfo) {
	r.lock.Lock()
	defer r.lock.Unlock()

	version = r.version
	compInfo = make([]v1.ComponentInfo, 0)
	for _, conv := range r.byCompName {
		compInfo = append(compInfo, conv.GetComponentInfo()...)
	}

	return
}

func (r *Registry) SetTargets(version int64, targets []ComponentTarget) bool {
	r.lock.Lock()
	versionMatch := version == r.version
	if versionMatch {
		r.version++
	}
	r.lock.Unlock()

	if versionMatch {
		for _, t := range targets {
			conv := r.ByComponent(t.Component)
			conv.SetTarget(t)
		}
	}
	return versionMatch
}

func (r *Registry) incrContainerIdCounter() (c maelContainerId) {
	r.lock.Lock()
	r.containerCounterId++
	c = r.containerCounterId
	r.lock.Unlock()
	return
}

func (r *Registry) onContainersChanged(count int) {
	r.incrContainerIdCounter()
	r.notifyContainersChanged(count)
}

func (r *Registry) startContainerAndHealthCheck(ctx context.Context, comp *v1.Component) (*Container, error) {
	containerId := r.incrContainerIdCounter()
	router := r.routerReg.ByComponent(comp.Name)
	cn := NewContainer(r.dockerClient, comp, r.maelstromUrl, router, containerId,
		r.bufferPool, ctx)
	err := cn.startAndHealthCheck(ctx)
	if err != nil {
		return nil, err
	}
	go cn.run()
	return cn, nil
}

func (r *Registry) stopContainer(cn *Container, reason string) {
	cn.CancelAndStop(reason)
}
