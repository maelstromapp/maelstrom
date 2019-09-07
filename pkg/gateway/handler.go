package gateway

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io"
	"io/ioutil"
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

///////////////////////////////////////////////////////////
// DockerHandlerFactory //
//////////////////////////

func NewDockerHandlerFactory(dockerClient *docker.Client, resolver ComponentResolver, db v1.Db,
	ctx context.Context, privatePort int) (*DockerHandlerFactory, error) {
	containers, err := listContainers(dockerClient)
	if err != nil {
		return nil, err
	}

	maelstromHost, err := resolveMaelstromHost(dockerClient)
	if err != nil {
		return nil, err
	}
	maelstromUrl := fmt.Sprintf("http://%s:%d", maelstromHost, privatePort)
	log.Info("handler: creating DockerHandlerFactory", "maelstromUrl", maelstromUrl)

	byComponentName := map[string][]*Container{}
	reqChanByComponent := map[string]chan *MaelRequest{}
	for _, c := range containers {
		name := c.Labels["maelstrom_component"]
		verStr := c.Labels["maelstrom_version"]
		if name != "" && verStr != "" {
			comp, err := resolver.ByName(name)
			if err == nil {
				var currentImageId string
				image, err := getImageByNameStripRepo(dockerClient, comp.Docker.Image)
				if err == nil {
					if image != nil {
						currentImageId = image.ID
					}
				} else {
					log.Error("handler: cannot list images", "component", name, "err", err)
				}

				if c.ImageID != currentImageId {
					stopContainerLogErr(dockerClient, c.ID, name, verStr, "docker image modified")
				} else if strconv.Itoa(int(comp.Version)) != verStr {
					stopContainerLogErr(dockerClient, c.ID, name, verStr, "component version changed")
				} else {
					// happy path - running container matches docker image ID and component version
					reqCh := reqChanByComponent[name]
					if reqCh == nil {
						reqCh = make(chan *MaelRequest)
						reqChanByComponent[name] = reqCh
					}
					containerWrap, err := StartContainer(reqCh, dockerClient, comp, c.ID, ctx)
					if err == nil {
						list := byComponentName[name]
						byComponentName[name] = append(list, containerWrap)
					} else {
						log.Error("handler: cannot create container wrapper", "component", name, "err", err)
						stopContainerLogErr(dockerClient, c.ID, name, verStr, "container wrapper init failed")
					}
					log.Info("handler: added existing container", "component", name, "containerId", c.ID)
				}
			} else {
				log.Warn("handler: cannot load component", "component", name, "err", err.Error())
			}
		}
	}

	return &DockerHandlerFactory{
		dockerClient:       dockerClient,
		db:                 db,
		byComponentName:    byComponentName,
		reqChanByComponent: reqChanByComponent,
		maelstromUrl:       maelstromUrl,
		ctx:                ctx,
		version:            common.NowMillis(),
		lock:               &sync.Mutex{},
	}, nil
}

type DockerHandlerFactory struct {
	dockerClient *docker.Client
	//router          *Router
	db                 v1.Db
	byComponentName    map[string][]*Container
	reqChanByComponent map[string]chan *MaelRequest
	maelstromUrl       string
	ctx                context.Context
	version            int64
	lock               *sync.Mutex
}

func (f *DockerHandlerFactory) Version() int64 {
	f.lock.Lock()
	ver := f.version
	f.lock.Unlock()
	return ver
}

func (f *DockerHandlerFactory) IncrementVersion() int64 {
	f.lock.Lock()
	oldVer := f.version
	f.version++
	f.lock.Unlock()
	return oldVer
}

func (f *DockerHandlerFactory) ReqChanByComponent(componentName string) chan *MaelRequest {
	f.lock.Lock()
	reqCh := f.reqChanByComponentLocked(componentName)
	f.lock.Unlock()
	return reqCh
}

func (f *DockerHandlerFactory) reqChanByComponentLocked(componentName string) chan *MaelRequest {
	reqCh := f.reqChanByComponent[componentName]
	if reqCh == nil {
		reqCh = make(chan *MaelRequest)
		f.reqChanByComponent[componentName] = reqCh
	}
	return reqCh
}

func (f *DockerHandlerFactory) HandlerComponentInfo() ([]v1.ComponentInfo, int64) {
	f.lock.Lock()
	defer f.lock.Unlock()
	names := make([]v1.ComponentInfo, 0)
	for _, clist := range f.byComponentName {
		for _, cont := range clist {
			names = append(names, cont.componentInfo())
		}
	}
	return names, f.version
}

func (f *DockerHandlerFactory) ConvergeToTarget(target v1.ComponentDelta,
	component v1.Component, async bool) (started int, stopped int, err error) {

	reqCh := f.ReqChanByComponent(component.Name)

	f.lock.Lock()
	defer f.lock.Unlock()

	delta := int(target.Delta)
	containers := f.byComponentName[target.ComponentName]

	// Count non-stopped handlers
	currentCount := len(containers)
	targetCount := currentCount + delta

	// Bump internal version
	f.version++

	if delta < 0 {
		// scale down
		stopCount := delta * -1
		for i := 0; i < stopCount; i++ {
			idx := currentCount - i - 1
			if async {
				go containers[idx].Stop("scale down")
			} else {
				containers[idx].Stop("scale down")
			}
		}
		f.byComponentName[target.ComponentName] = containers[:targetCount]
	} else if delta > 0 {
		// scale up
		for i := 0; i < delta; i++ {
			cont, err := f.startContainer(component, reqCh)
			if err == nil {
				containers = append(containers, cont)
			} else {
				log.Error("handler: unable to start container", "err", err, "component", component.Name)
			}
		}
		f.byComponentName[target.ComponentName] = containers
	}

	return
}

func (f *DockerHandlerFactory) startContainer(component v1.Component, reqCh chan *MaelRequest) (*Container, error) {
	err := pullImage(f.dockerClient, component)
	if err != nil {
		log.Warn("handler: unable to pull image", "err", err.Error(), "component", component.Name)
	}

	containerId, err := startContainer(f.dockerClient, component, f.maelstromUrl)
	if err != nil {
		return nil, err
	}

	c, err := StartContainer(reqCh, f.dockerClient, component, containerId, f.ctx)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (f *DockerHandlerFactory) GetComponentInfo(componentName string, containerId string) v1.ComponentInfo {
	f.lock.Lock()
	defer f.lock.Unlock()

	containers, ok := f.byComponentName[componentName]
	if ok {
		for _, cont := range containers {
			if cont.containerId == containerId {
				return cont.componentInfo()
			}
		}
	}
	return v1.ComponentInfo{}
}

func (f *DockerHandlerFactory) OnDockerEvent(msg common.DockerEvent) {
	if msg.ContainerExited != nil {
		f.OnContainerExited(*msg.ContainerExited)
	}
	if msg.ImageUpdated != nil {
		f.OnImageUpdated(*msg.ImageUpdated)
	}
}

func (f *DockerHandlerFactory) OnContainerExited(msg common.ContainerExitedMessage) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for componentName, containers := range f.byComponentName {
		removeIdx := -1
		for idx, cont := range containers {
			if cont.containerId == msg.ContainerId {
				removeIdx = idx
				break
			}
		}
		if removeIdx >= 0 {
			var keep []*Container
			for i, cont := range containers {
				if i == removeIdx {
					log.Info("handler: OnContainerExited - restarting component", "component", cont.component.Name)
					newContainer, err := f.restartComponent(cont, true, f.reqChanByComponentLocked(cont.component.Name))
					if err == nil {
						keep = append(keep, newContainer)
					} else {
						log.Error("handler: OnContainerExited - unable to restart component", "err", err,
							"component", cont.component.Name)
					}
				} else {
					keep = append(keep, cont)
				}
			}
			f.byComponentName[componentName] = keep
			return
		}
	}
}

func (f *DockerHandlerFactory) OnImageUpdated(msg common.ImageUpdatedMessage) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for componentName, containers := range f.byComponentName {
		var keep []*Container
		for _, cont := range containers {
			if normalizeImageName(cont.component.Docker.Image) == msg.ImageName {
				log.Info("handler: OnImageUpdated - stopping image for component", "component", componentName,
					"imageName", msg.ImageName, "newImageId", msg.ImageId)
				newContainer, err := f.restartComponent(cont, false, f.reqChanByComponentLocked(componentName))
				if err == nil {
					keep = append(keep, newContainer)
				} else {
					log.Error("handler: OnImageUpdated - unable to restart component", "err", err,
						"component", componentName)
				}
			} else {
				keep = append(keep, cont)
			}
		}
		f.byComponentName[componentName] = keep
	}
}

func (f *DockerHandlerFactory) OnComponentNotification(cn v1.ComponentNotification) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if cn.PutComponent != nil {
		containers, ok := f.byComponentName[cn.PutComponent.Name]
		if ok {
			var keep []*Container
			for _, cont := range containers {
				if cont.component.Version < cn.PutComponent.Version {
					newContainer, err := f.restartComponent(cont, false, f.reqChanByComponentLocked(cn.PutComponent.Name))
					if err == nil {
						keep = append(keep, newContainer)
					} else {
						log.Error("handler: OnComponentNotification - unable to restart component", "err", err,
							"component", cn.PutComponent.Name)
					}
				} else {
					keep = append(keep, cont)
				}
			}
			f.byComponentName[cn.PutComponent.Name] = keep
		}
	} else if cn.RemoveComponent != nil {
		f.stopContainersByComponent(cn.RemoveComponent.Name, "component removed")
	}
}

func (f *DockerHandlerFactory) stopContainersByComponent(componentName string, reason string) {
	f.lock.Lock()
	containers := f.byComponentName[componentName]
	f.byComponentName[componentName] = []*Container{}
	f.lock.Unlock()
	for _, cont := range containers {
		cont.Stop(reason)
	}
}

func (f *DockerHandlerFactory) restartComponent(oldContainer *Container, stopAsync bool,
	reqCh chan *MaelRequest) (*Container, error) {
	component, err := f.db.GetComponent(oldContainer.component.Name)
	if err != nil {
		return nil, fmt.Errorf("restartComponent: error loading component: %s - %v", oldContainer.component.Name, err)
	}

	if stopAsync {
		go oldContainer.Stop("restarting component")
	} else {
		oldContainer.Stop("restarting component")
	}
	return f.startContainer(component, reqCh)
}

func listContainers(dockerClient *docker.Client) ([]types.Container, error) {
	filter := filters.NewArgs()
	filter.Add("label", "maelstrom=true")
	return dockerClient.ContainerList(context.Background(), types.ContainerListOptions{
		Filters: filter,
	})
}

func pullImage(dockerClient *docker.Client, c v1.Component) error {
	out, err := dockerClient.ImagePull(context.Background(), c.Docker.Image, types.ImagePullOptions{})
	if err == nil {
		defer common.CheckClose(out, &err)
		_, err = ioutil.ReadAll(out)
	}
	return err
}

func getImageByNameStripRepo(dockerClient *docker.Client, imageName string) (*types.ImageSummary, error) {
	img, err := getImageByName(dockerClient, imageName)
	if err != nil {
		return nil, err
	}
	if img == nil {
		slashes := strings.Count(imageName, "/")
		if slashes > 1 {
			imageName = imageName[strings.Index(imageName, "/")+1:]
		}
		img, err = getImageByName(dockerClient, imageName)
		if err != nil {
			return nil, err
		}
	}
	return img, nil
}

func getImageByName(dockerClient *docker.Client, imageName string) (*types.ImageSummary, error) {
	filter := filters.NewArgs()
	filter.Add("reference", normalizeImageName(imageName))
	images, err := dockerClient.ImageList(context.Background(), types.ImageListOptions{
		Filters: filter,
	})
	if err == nil {
		if len(images) > 0 {
			return &images[0], nil
		}
		return nil, nil
	}
	return nil, err
}

func printContainerLogs(dockerClient *docker.Client, containerId string) error {
	out, err := dockerClient.ContainerLogs(context.Background(), containerId, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err == nil {
		defer common.CheckClose(out, &err)
		fmt.Printf("Container logs for: %s\n", containerId)
		_, err = io.Copy(os.Stdout, out)
	}
	return err
}

func startContainer(dockerClient *docker.Client, c v1.Component, maelstromUrl string) (string, error) {
	ctx := context.Background()
	if c.Docker == nil {
		return "", fmt.Errorf("c.Docker is nil")
	}
	config := toContainerConfig(c, maelstromUrl)
	hostConfig := toContainerHostConfig(c)
	resp, err := dockerClient.ContainerCreate(ctx, config, hostConfig, nil, "")
	if err != nil {
		return "", fmt.Errorf("containerCreate error for: %s - %v", c.Name, err)
	}

	log.Info("handler: starting container", "component", c.Name, "ver", c.Version, "containerId", resp.ID[0:8])

	err = dockerClient.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return "", fmt.Errorf("containerStart error for: %s - %v", c.Name, err)
	}
	return resp.ID, nil
}

func stopContainerLogErr(dockerClient *docker.Client, containerId string, componentName string, version string,
	reason string) {
	err := stopContainer(dockerClient, containerId, componentName, version, reason)
	if err != nil {
		log.Error("handler: unable to stop container", "err", err, "component", componentName, "ver", version,
			"containerId", containerId[0:8])
	}
}

func stopContainer(dockerClient *docker.Client, containerId string, componentName string, version string,
	reason string) error {
	ctx := context.Background()

	log.Info("handler: stopping container", "component", componentName, "ver", version, "reason", reason,
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

func toContainerConfig(c v1.Component, maelstromUrl string) *container.Config {

	env := make([]string, 0)
	for _, e := range c.Docker.Env {
		if !strings.HasPrefix(e, "MAELSTROM_") {
			env = append(env, e)
		}
	}
	env = append(env, fmt.Sprintf("MAELSTROM_PRIVATE_URL=%s", maelstromUrl))
	env = append(env, fmt.Sprintf("MAELSTROM_COMPONENT_NAME=%s", c.Name))
	env = append(env, fmt.Sprintf("MAELSTROM_COMPONENT_VERSION=%d", c.Version))

	return &container.Config{
		Image: c.Docker.Image,
		Env:   env,
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

func toContainerHostConfig(c v1.Component) *container.HostConfig {
	hc := &container.HostConfig{}

	// Set memory (RAM) limits
	resMem := c.Docker.ReserveMemoryMiB
	if resMem <= 0 {
		resMem = 128
	}
	memoryResBytes := resMem * 1024 * 1024
	hc.MemoryReservation = memoryResBytes
	hc.KernelMemory = memoryResBytes
	if c.Docker.LimitMemoryMiB > c.Docker.ReserveMemoryMiB {
		hc.KernelMemory = c.Docker.LimitMemoryMiB * 1024 * 1024
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

	// Set HostConfig so that we can route to host when using docker-in-docker
	// See: https://stackoverflow.com/questions/44830663/docker-container-networking-with-docker-in-docker
	dindHost := os.Getenv("DIND_HOST")
	if dindHost != "" {
		portStr := strconv.Itoa(int(c.Docker.HttpPort))
		port := nat.Port(portStr + "/tcp")
		bindPort := atomic.AddInt64(&hostBindPort, 1)
		hc.PortBindings = nat.PortMap{
			port: []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: strconv.Itoa(int(bindPort)),
				},
			},
		}
	}

	if c.Docker.NetworkName != "" {
		hc.NetworkMode = container.NetworkMode(c.Docker.NetworkName)
	}

	return hc
}

func resolveMaelstromHost(dockerClient *docker.Client) (string, error) {
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

func normalizeImageName(name string) string {
	if !strings.Contains(name, ":") {
		return name + ":latest"
	}
	return name
}
