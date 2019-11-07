package common

import (
	"context"
	"fmt"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Used in CI when running via docker-in-docker
// Currently no provision is made for port overflow since this is
// only intended for use in CI via the DIND_HOST env var
var hostBindPort = int64(33000)

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

func ResolveMaelstromHost(dockerClient *docker.Client) (string, error) {
	// figure out docker network interface
	host := "172.17.0.1"
	nets, err := dockerClient.NetworkList(context.Background(), types.NetworkListOptions{})
	if err != nil {
		return "", err
	}
	networkToGateway := make(map[string]string)
	for _, n := range nets {
		if len(n.IPAM.Config) > 0 {
			networkToGateway[n.Name] = n.IPAM.Config[0].Gateway
		}
	}
	networkPref := []string{"docker_gwbridge", "bridge"}
	for _, n := range networkPref {
		gateway := networkToGateway[n]
		if gateway != "" {
			host = gateway
			break
		}
	}
	return host, nil
}

func ImageExistsLocally(dockerClient *docker.Client, imageName string) (bool, error) {
	filt := filters.NewArgs()
	filt.Add("reference", NormalizeImageName(imageName))
	images, err := dockerClient.ImageList(context.Background(), types.ImageListOptions{
		All:     false,
		Filters: filt,
	})
	if err != nil {
		return false, err
	}
	return len(images) > 0, nil
}

func ListMaelstromContainers(dockerClient *docker.Client) ([]types.Container, error) {
	filter := filters.NewArgs()
	filter.Add("label", "maelstrom=true")
	return dockerClient.ContainerList(context.Background(), types.ContainerListOptions{
		Filters: filter,
	})
}

func RemoveMaelstromContainers(dockerClient *docker.Client, reason string) (int, error) {
	count := 0

	containers, err := ListMaelstromContainers(dockerClient)
	if err != nil {
		return 0, errors.Wrap(err, "docker: ListMaelstromContainers failed")
	}

	for _, c := range containers {
		err = RemoveContainer(dockerClient, c.ID, "", "", reason)
		if err == nil {
			count++
		} else {
			return count, err
		}
	}

	return count, nil
}

func RemoveContainer(dockerClient *docker.Client, containerId string, componentName string, version string,
	reason string) error {
	ctx := context.Background()

	log.Info("common: removing container", "component", componentName, "ver", version, "reason", reason,
		"containerId", containerId[0:8])
	timeout := time.Second * 60
	err := dockerClient.ContainerStop(ctx, containerId, &timeout)
	if err != nil {
		if docker.IsErrContainerNotFound(err) {
			return err
		}
		return fmt.Errorf("containerStop error for: %s - %v", containerId, err)
	}

	_, err = dockerClient.ContainerWait(ctx, containerId)
	if err != nil {
		if docker.IsErrContainerNotFound(err) {
			return err
		}
		return fmt.Errorf("containerWait error for: %s - %v", containerId, err)
	}

	err = dockerClient.ContainerRemove(ctx, containerId, types.ContainerRemoveOptions{})
	if err != nil {
		if docker.IsErrContainerNotFound(err) {
			return err
		}
		return fmt.Errorf("containerRemove error for: %s - %v", containerId, err)
	}
	return nil
}

func StartContainer(dockerClient *docker.Client, c *v1.Component, maelstromUrl string) (string, error) {
	ctx := context.Background()
	if c.Docker == nil {
		return "", fmt.Errorf("c.Docker is nil")
	}
	config := toContainerConfig(c, maelstromUrl)
	hostConfig, err := toContainerHostConfig(c)
	if err != nil {
		return "", err
	}

	// add any manually exposed port binding to the exposed ports map so they're
	// available on the host
	for port, _ := range hostConfig.PortBindings {
		config.ExposedPorts[port] = struct{}{}
	}

	resp, err := dockerClient.ContainerCreate(ctx, config, hostConfig, nil, "")
	if err != nil {
		return "", fmt.Errorf("containerCreate error for: %s - %v", c.Name, err)
	}

	log.Info("common: starting container", "component", c.Name, "ver", c.Version, "containerId", resp.ID[0:8],
		"image", config.Image, "command", config.Cmd, "entrypoint", config.Entrypoint)

	err = dockerClient.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return resp.ID, fmt.Errorf("containerStart error for: %s - %v", c.Name, err)
	}
	return resp.ID, nil
}

func toContainerConfig(c *v1.Component, maelstromUrl string) *container.Config {

	env := make([]string, 0)
	for _, e := range c.Environment {
		if !strings.HasPrefix(e.Name, "MAELSTROM_") {
			env = append(env, e.Name+"="+e.Value)
		}
	}
	env = append(env, fmt.Sprintf("MAELSTROM_PRIVATE_URL=%s", maelstromUrl))
	env = append(env, fmt.Sprintf("MAELSTROM_COMPONENT_NAME=%s", c.Name))
	env = append(env, fmt.Sprintf("MAELSTROM_COMPONENT_VERSION=%d", c.Version))
	env = append(env, fmt.Sprintf("MAELSTROM_PROJECT_NAME=%s", c.ProjectName))

	return &container.Config{
		Image:      c.Docker.Image,
		Cmd:        c.Docker.Command,
		Entrypoint: c.Docker.Entrypoint,
		Env:        env,
		ExposedPorts: nat.PortSet{
			nat.Port(strconv.Itoa(int(c.Docker.HttpPort)) + "/tcp"): struct{}{},
		},
		Labels: map[string]string{
			"maelstrom":           "true",
			"maelstrom_component": c.Name,
			"maelstrom_version":   strconv.Itoa(int(c.Version)),
		},
	}
}

func toContainerHostConfig(c *v1.Component) (*container.HostConfig, error) {
	hc := &container.HostConfig{}

	// Set memory (RAM) limits
	resMem := c.Docker.ReserveMemoryMiB
	if resMem <= 0 {
		resMem = 128
	}
	memoryResBytes := resMem * 1024 * 1024
	hc.MemoryReservation = memoryResBytes
	if c.Docker.LimitMemoryMiB > 0 {
		hc.Memory = c.Docker.LimitMemoryMiB * 1024 * 1024
	}

	// CPU shares
	if c.Docker.CpuShares > 0 {
		hc.CPUShares = c.Docker.CpuShares
	}

	// Container logging
	if c.Docker.LogDriver != "" {
		hc.LogConfig.Type = c.Docker.LogDriver
		hc.LogConfig.Config = map[string]string{}
		for _, nv := range c.Docker.LogDriverOptions {
			hc.LogConfig.Config[nv.Name] = nv.Value
		}
	}

	// Set volume mounts
	if len(c.Docker.Volumes) > 0 {
		hc.Mounts = make([]mount.Mount, 0)
		for _, v := range c.Docker.Volumes {
			if v.Type == "" {
				v.Type = "bind"
			}
			hc.Mounts = append(hc.Mounts, mount.Mount{
				Type:     mount.Type(v.Type),
				Source:   v.Source,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})
		}
	}

	// DNS
	if len(c.Docker.Dns) > 0 {
		hc.DNS = c.Docker.Dns
	}
	if len(c.Docker.DnsSearch) > 0 {
		hc.DNSSearch = c.Docker.DnsSearch
	}
	if len(c.Docker.DnsOptions) > 0 {
		hc.DNSOptions = c.Docker.DnsOptions
	}

	// Port mappings
	if len(c.Docker.Ports) > 0 {
		if hc.PortBindings == nil {
			hc.PortBindings = nat.PortMap{}
		}
		for x, spec := range c.Docker.Ports {
			portMappings, err := nat.ParsePortSpec(spec)
			if err != nil {
				return nil, errors.Wrapf(err, "Unable to parse port: %d - %s", x, spec)
			}
			for _, m := range portMappings {
				hc.PortBindings[m.Port] = []nat.PortBinding{m.Binding}
			}
		}
	}

	// Set HostConfig so that we can route to host when using docker-in-docker
	// See: https://stackoverflow.com/questions/44830663/docker-container-networking-with-docker-in-docker
	dindHost := os.Getenv("DIND_HOST")
	if dindHost != "" {
		portStr := strconv.Itoa(int(c.Docker.HttpPort))
		port := nat.Port(portStr + "/tcp")
		bindPort := atomic.AddInt64(&hostBindPort, 1)
		if hc.PortBindings == nil {
			hc.PortBindings = nat.PortMap{}
		}
		hc.PortBindings[port] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: strconv.Itoa(int(bindPort)),
			},
		}
	}

	if c.Docker.NetworkName != "" {
		hc.NetworkMode = container.NetworkMode(c.Docker.NetworkName)
	}

	return hc, nil
}

func NormalizeImageName(name string) string {
	if !strings.Contains(name, ":") {
		return name + ":latest"
	}
	return name
}
