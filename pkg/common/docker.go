package common

import (
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"sync"
)

type DockerImageObserver interface {
	OnImageUpdated(msg ImageUpdatedMessage)
}

type ImageUpdatedMessage struct {
	ImageName string
	ImageId   string
}

func NewDockerImageMonitor(dockerClient *docker.Client, observer DockerImageObserver,
	ctx context.Context) *DockerImageMonitor {
	return &DockerImageMonitor{
		dockerClient: dockerClient,
		observer:     observer,
		ctx:          ctx,
	}
}

type DockerImageMonitor struct {
	dockerClient *docker.Client
	observer     DockerImageObserver
	ctx          context.Context
}

func (d *DockerImageMonitor) RunAsync(wg *sync.WaitGroup) {
	filterArgs := filters.NewArgs()
	filterArgs.Add("event", "tag")
	msgCh, errCh := d.dockerClient.Events(d.ctx, types.EventsOptions{
		Filters: filterArgs,
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case m := <-msgCh:
				if log.IsDebug() {
					log.Debug("docker: DockerImageMonitor event", "from", m.From, "actor", m.Actor, "type", m.Type,
						"status", m.Status)
				}
				if m.Type == "image" && m.Status == "tag" {
					imageName := m.Actor.Attributes["name"]
					imageId := m.Actor.ID
					log.Info("docker: updated image", "image", imageName, "imageId", imageId)
					d.observer.OnImageUpdated(ImageUpdatedMessage{
						ImageName: imageName,
						ImageId:   imageId,
					})
				}
			case <-d.ctx.Done():
				log.Info("docker: DockerImageMonitor shutdown gracefully")
				return
			}
		}
	}()

	go func() {
		for m := range errCh {
			if m != context.Canceled {
				log.Error("docker: DockerImageMonitor docker error", "err", m.Error())
			}
		}
	}()
}
