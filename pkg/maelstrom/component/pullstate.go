package component

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"sync"
)

func NewPullState(dockerClient *docker.Client) *PullState {
	return &PullState{
		dockerClient:         dockerClient,
		pulledVerByComponent: make(map[string]int64),
		lock:                 &sync.Mutex{},
	}
}

type PullState struct {
	dockerClient         *docker.Client
	pulledVerByComponent map[string]int64
	lock                 *sync.Mutex
}

func (p *PullState) Pull(c v1.Component, forcePull bool) {
	p.lock.Lock()
	ver := p.pulledVerByComponent[c.Name]
	p.lock.Unlock()

	if ver != c.Version || forcePull {
		err := pullImage(p.dockerClient, c)
		if err != nil {
			log.Warn("component: unable to pull image", "err", err.Error(), "component", c.Name,
				"image", c.Docker.Image)
		} else {
			p.lock.Lock()
			p.pulledVerByComponent[c.Name] = c.Version
			p.lock.Unlock()
			log.Info("component: successfully pulled image", "component", c.Name, "image", c.Docker.Image)
		}
	}
}
