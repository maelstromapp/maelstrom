package common

import (
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"strings"
	"sync"
)

type DockerImageObserver interface {
	OnDockerEvent(msg DockerEvent)
}

type DockerEvent struct {
	ImageUpdated    *ImageUpdatedMessage
	ContainerExited *ContainerExitedMessage
}

type ImageUpdatedMessage struct {
	ImageName string
	ImageId   string
}

type ContainerExitedMessage struct {
	ContainerId string
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
	filterArgs.Add("event", "die")
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
					log.Debug("docker_monitor: event", "from", m.From, "actor", m.Actor, "type", m.Type,
						"status", m.Status)
				}
				if m.Type == "container" && m.Status == "die" {
					log.Info("docker_monitor: container exited", "containerId", m.Actor.ID[0:8])
					d.observer.OnDockerEvent(DockerEvent{
						ContainerExited: &ContainerExitedMessage{
							ContainerId: m.Actor.ID,
						}})
				} else if m.Type == "image" && m.Status == "tag" {
					imageName := m.Actor.Attributes["name"]
					imageId := m.Actor.ID
					log.Info("docker_monitor: updated image", "image", imageName, "imageId", imageId)
					d.observer.OnDockerEvent(DockerEvent{
						ImageUpdated: &ImageUpdatedMessage{
							ImageName: imageName,
							ImageId:   imageId,
						}})
				}
			case <-d.ctx.Done():
				log.Info("docker_monitor: shutdown gracefully")
				return
			}
		}
	}()

	go func() {
		for m := range errCh {
			if m != context.Canceled {
				log.Error("docker_monitor: docker error", "err", m.Error())
			}
		}
	}()
}

func IsErrRemovalInProgress(err error) bool {
	if err == nil {
		return false
	}
	// TODO: find a more robust way to detect this error
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "removal of container") && strings.Contains(s, "already in progress")
}
