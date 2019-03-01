package gateway

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
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

type HandlerFactory interface {
	GetHandlerAndRegisterRequest(c v1.Component) (Handler, error)
}

type Handler interface {
	http.Handler
	HandleMessage(message []byte) ([]byte, error)
}

///////////////////////////////////////////////////////////
// DockerHandlerFactory //
//////////////////////////

func NewDockerHandlerFactory(dockerClient *docker.Client, resolver ComponentResolver) (*DockerHandlerFactory, error) {
	containers, err := listContainers(dockerClient)
	if err != nil {
		return nil, err
	}

	byComponentName := map[string]*LocalHandler{}
	for _, c := range containers {
		name := c.Labels["maelstrom_component"]
		verStr := c.Labels["maelstrom_version"]
		if name != "" && verStr != "" {
			comp, err := resolver.ByName(name)
			if err == nil {
				if strconv.Itoa(int(comp.Version)) == verStr {
					handler, err := NewLocalHandler(dockerClient, comp, c.ID)
					if err == nil {
						byComponentName[name] = handler
					} else {
						log.Printf("ERROR cannot create handler for: %s - %v", name, err)
					}
				} else {
					log.Printf("TODO: invalid version of component: %s containerId: %s", name, c.ID)
				}
			} else {
				log.Printf("WARN cannot load component: %s - %v", name, err)
			}
		}
	}

	return &DockerHandlerFactory{
		dockerClient:    dockerClient,
		byComponentName: byComponentName,
		lock:            &sync.Mutex{},
	}, nil
}

type DockerHandlerFactory struct {
	dockerClient    *docker.Client
	byComponentName map[string]*LocalHandler
	lock            *sync.Mutex
}

func (f *DockerHandlerFactory) GetHandlerAndRegisterRequest(c v1.Component) (Handler, error) {
	f.lock.Lock()
	defer f.lock.Unlock()

	handler, ok := f.byComponentName[c.Name]

	// if container is for an older version of component, stop it
	if ok && handler.component.Version < c.Version {
		ok = false
		log.Printf("ForComponent stopping old handler for: %s.%d", handler.component.Name, handler.component.Version)
		f.stopHandler(handler)
	}

	// this case could happen if handler health check failed and handler stopped
	if ok && handler.Stopped() {
		ok = false
		log.Printf("ForComponent removing stale handler for: %s.%d", handler.component.Name, handler.component.Version)
		delete(f.byComponentName, c.Name)
	}

	if !ok {
		// start new container for component
		var err error
		log.Printf("ForComponent creating handler for: %s.%d", c.Name, c.Version)
		handler, err = NewLocalHandler(f.dockerClient, c, "")
		if err == nil {
			// container started ok - store in map so we can re-use on future requests
			f.byComponentName[c.Name] = handler
		} else {
			log.Printf("ERROR cannot create handler for: %s - %v", c.Name, err)
			return nil, err
		}
	}

	err := handler.RegisterRequest()
	if err != nil {
		return nil, fmt.Errorf("ForComponent unable to register request: %v", err)
	}
	return handler, nil
}

func (f *DockerHandlerFactory) OnComponentNotification(cn v1.ComponentNotification) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if cn.PutComponent != nil {
		handler, ok := f.byComponentName[cn.PutComponent.Name]
		if ok && handler.component.Version < cn.PutComponent.Version {
			f.stopHandler(handler)
		}
	} else if cn.RemoveComponent != nil {
		handler, ok := f.byComponentName[cn.RemoveComponent.Name]
		if ok {
			f.stopHandler(handler)
		}
	}
}

func (f *DockerHandlerFactory) stopHandlerByComponent(c v1.Component) {
	f.lock.Lock()
	handler, ok := f.byComponentName[c.Name]
	f.lock.Unlock()
	if ok {
		f.stopHandler(handler)
	}
}

func (f *DockerHandlerFactory) stopHandler(h *LocalHandler) {
	log.Printf("Stopping handler for component: %s.%d", h.component.Name, h.component.Version)
	delete(f.byComponentName, h.component.Name)
	go func() { h.DrainAndStop() }()
}

///////////////////////////////////////////////////////////
// LocalHandler //
//////////////////

func NewLocalHandler(dockerClient *docker.Client, c v1.Component, containerId string) (*LocalHandler, error) {
	handler := &LocalHandler{
		dockerClient: dockerClient,
		component:    c,
		containerId:  containerId,
		requestWG:    &sync.WaitGroup{},
		lock:         &sync.Mutex{},
		stopped:      false,
		lastReqTime:  time.Now(),
	}

	if containerId != "" {
		err := handler.initReverseProxy()
		if err != nil {
			return nil, err
		}
	}

	go handler.monitorIdle()

	return handler, nil
}

type LocalHandler struct {
	proxy        *httputil.ReverseProxy
	targetUrl    *url.URL
	dockerClient *docker.Client
	component    v1.Component
	containerId  string
	requestWG    *sync.WaitGroup
	lock         *sync.Mutex
	lastReqTime  time.Time
	stopped      bool
}

func (h *LocalHandler) monitorHealthCheck(containerId string, healthCheckUrl *url.URL) {
	seconds := h.component.Docker.HttpHealthCheckSeconds
	if seconds < 1 {
		seconds = 10
	}
	interval := time.Second * time.Duration(seconds)
	log.Printf("handler.monitorHealthCheck starting. interval=%v", interval)
	for {
		time.Sleep(interval)
		h.lock.Lock()
		stopped := h.stopped
		currentContainerId := h.containerId
		h.lock.Unlock()
		if stopped || currentContainerId != containerId {
			return
		} else if !getUrlOK(healthCheckUrl) {
			log.Printf("ERROR health check failed for url: %s container: %s\n", healthCheckUrl, containerId[0:8])
			h.DrainAndStop()
			return
		}
	}
}

func (h *LocalHandler) monitorIdle() {
	idleTimeout := h.component.Docker.IdleTimeoutSeconds
	if idleTimeout < 1 {
		idleTimeout = 300
	}
	idleDuration := time.Second * time.Duration(idleTimeout)
	sleepDuration := idleDuration
	log.Printf("handler.monitorIdle starting. idleDuration=%v", idleDuration)
	for {
		time.Sleep(sleepDuration)

		h.lock.Lock()
		if h.stopped {
			h.lock.Unlock()
			return
		} else {
			sinceLastReq := time.Now().Sub(h.lastReqTime)
			sleepDuration := idleDuration - sinceLastReq
			if sleepDuration <= 0 {
				log.Printf("handler.monitorIdle stopping idle container: %s last req: %v ago",
					h.component.Name, sinceLastReq)
				h.stopped = true
				h.lock.Unlock()
				h.DrainAndStop()
				return
			} else {
				h.lock.Unlock()
			}
		}
	}
}

func (h *LocalHandler) Stopped() bool {
	h.lock.Lock()
	defer h.lock.Unlock()
	return h.stopped
}

func (h *LocalHandler) RegisterRequest() error {
	h.lock.Lock()
	defer h.lock.Unlock()

	if h.stopped {
		return fmt.Errorf("cannot proxy request to stopped handler: %s.%d", h.component.Name, h.component.Version)
	}

	err := h.ensureContainer()
	if err != nil {
		return err
	}
	h.requestWG.Add(1)
	h.lastReqTime = time.Now()
	return nil
}

func (h *LocalHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	defer h.requestWG.Done()
	h.proxy.ServeHTTP(rw, req)
}

func (h *LocalHandler) HandleMessage(message []byte) ([]byte, error) {
	return nil, fmt.Errorf("HandleMessage - Not implemented")
}

func (h *LocalHandler) ensureContainer() error {
	if h.containerId == "" || h.proxy == nil {
		err := pullImage(h.dockerClient, h.component)
		if err != nil {
			log.Printf("WARN unable to pull image: %s err: %v\n", h.component.Docker.Image, err)
		}

		containerId, err := startContainer(h.dockerClient, h.component)
		if err != nil {
			return err
		}

		h.containerId = containerId
		err = h.initReverseProxy()
		if err != nil {
			err2 := stopContainer(h.dockerClient, containerId)
			if err2 != nil {
				log.Printf("ERROR ensureContainer stopContainer: %s err: %v", h.component.Name, err)
			}
			return err
		}
	}
	return nil
}

func (h *LocalHandler) initReverseProxy() error {
	cont, err := h.dockerClient.ContainerInspect(context.Background(), h.containerId)
	if err != nil {
		return fmt.Errorf("initReverseProxy ContainerInspect error: %v", err)
	}

	dindHost := os.Getenv("DIND_HOST")

	target := &url.URL{
		Scheme: "http",
		Host:   "",
		Path:   "",
	}
	for p, portMap := range cont.NetworkSettings.Ports {
		k := string(p)
		if strings.HasSuffix(k, "/tcp") {
			if len(portMap) > 0 && dindHost != "" && portMap[0].HostIP == "0.0.0.0" {
				// CI docker-in-docker mode
				target.Host = dindHost + ":" + portMap[0].HostPort
			} else {
				// Normal mode where we can route directly to the container's IP addr
				target.Host = cont.NetworkSettings.IPAddress + ":" + k[0:strings.Index(k, "/")]
			}
			break
		}
	}

	if target.Host == "" {
		return fmt.Errorf("initReverseProxy unable to find exposed port for component: " + h.component.Name)
	}

	// wait for health check to pass
	healthCheckUrl := toHealthCheckURL(h.component, target)
	healthCheckStartSecs := h.component.Docker.HttpStartHealthCheckSeconds
	if healthCheckStartSecs == 0 {
		healthCheckStartSecs = 60
	}
	if healthCheckStartSecs > 0 {
		if !tryUntilUrlOk(healthCheckUrl, time.Second*time.Duration(healthCheckStartSecs)) {
			return fmt.Errorf("health check never passed for: %s url: %s", h.component.Name, healthCheckUrl)
		}
	}

	// start background goroutine to monitor health check
	go h.monitorHealthCheck(cont.ID, healthCheckUrl)

	log.Printf("Handler active for component: %s.%d container: %s url: %s\n",
		h.component.Name, h.component.Version, cont.ID[0:8], target.String())
	h.proxy = httputil.NewSingleHostReverseProxy(target)
	h.targetUrl = target
	return nil
}

func (h *LocalHandler) DrainAndStop() {
	h.lock.Lock()
	defer h.lock.Unlock()

	h.stopped = true
	if h.containerId != "" {
		h.requestWG.Wait()
		t, ok := h.proxy.Transport.(*http.Transport)
		if ok {
			t.CloseIdleConnections()
		}
		log.Printf("Stopping container for component: %s.%d container: %s\n",
			h.component.Name, h.component.Version, h.containerId[0:8])
		err := stopContainer(h.dockerClient, h.containerId)
		if err != nil {
			log.Printf("ERROR stopContainer failed for %s: %v", h.containerId, err)
		}
		h.proxy = nil
		h.containerId = ""
	}
}

func tryUntilUrlOk(u *url.URL, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if getUrlOK(u) {
			return true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return false
}

func getUrlOK(u *url.URL) bool {
	resp, err := http.Get(u.String())
	if resp != nil && resp.Body != nil {
		defer common.CheckClose(resp.Body, &err)
	}
	return (err == nil) && (resp.StatusCode == 200)
}

func toHealthCheckURL(c v1.Component, baseUrl *url.URL) *url.URL {
	path := "/"
	if c.Docker != nil && c.Docker.HttpHealthCheckPath != "" {
		path = c.Docker.HttpHealthCheckPath
	}
	return &url.URL{
		Scheme: baseUrl.Scheme,
		Opaque: baseUrl.Opaque,
		Host:   baseUrl.Host,
		Path:   path,
	}
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

func startContainer(dockerClient *docker.Client, c v1.Component) (string, error) {
	ctx := context.Background()
	if c.Docker == nil {
		return "", fmt.Errorf("c.Docker is nil")
	}
	config := toContainerConfig(c)
	hostConfig := toContainerHostConfig(c)
	resp, err := dockerClient.ContainerCreate(ctx, config, hostConfig, nil, "")
	if err != nil {
		return "", fmt.Errorf("containerCreate error for: %s - %v", c.Name, err)
	}

	log.Printf("Starting container for component: %s.%d container: %s\n", c.Name, c.Version, resp.ID[0:8])

	err = dockerClient.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return "", fmt.Errorf("containerStart error for: %s - %v", c.Name, err)
	}
	return resp.ID, nil
}

func stopContainer(dockerClient *docker.Client, containerId string) error {
	ctx := context.Background()

	log.Printf("Stopping container: %s\n", containerId[0:8])
	timeout := time.Duration(time.Second * 60)
	err := dockerClient.ContainerStop(ctx, containerId, &timeout)
	if err != nil {
		return fmt.Errorf("containerStop error for: %s - %v", containerId, err)
	}

	_, err = dockerClient.ContainerWait(ctx, containerId)
	if err != nil {
		return fmt.Errorf("containerWait error for: %s - %v", containerId, err)
	}

	err = dockerClient.ContainerRemove(ctx, containerId, types.ContainerRemoveOptions{})
	if err != nil {
		return fmt.Errorf("containerRemove error for: %s - %v", containerId, err)
	}
	return nil
}

func toContainerConfig(c v1.Component) *container.Config {
	return &container.Config{
		Image: c.Docker.Image,
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
	dindHost := os.Getenv("DIND_HOST")
	if dindHost == "" {
		return nil
	}

	// Set HostConfig so that we can route to host when using docker-in-docker
	// See: https://stackoverflow.com/questions/44830663/docker-container-networking-with-docker-in-docker

	portStr := strconv.Itoa(int(c.Docker.HttpPort))
	port := nat.Port(portStr + "/tcp")
	bindPort := atomic.AddInt64(&hostBindPort, 1)
	return &container.HostConfig{
		PortBindings: nat.PortMap{
			port: []nat.PortBinding{
				{
					HostIP:   "0.0.0.0",
					HostPort: strconv.Itoa(int(bindPort)),
				},
			},
		},
	}
}
