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
	ForComponent(c v1.Component) (Handler, error)
}

type Handler interface {
	http.Handler
	HandleMessage(message []byte) ([]byte, error)
}

func NewHandlerFactory(dockerClient *docker.Client, resolver ComponentResolver) (HandlerFactory, error) {
	containers, err := listContainers(dockerClient)
	if err != nil {
		return nil, err
	}

	byComponentNameVer := map[string]Handler{}
	for _, c := range containers {
		name := c.Labels["maelstrom_component"]
		verStr := c.Labels["maelstrom_version"]
		if name != "" && verStr != "" {
			k := name + ":" + verStr
			comp, err := resolver.ByName(name)
			if err == nil {
				if strconv.Itoa(int(comp.Version)) == verStr {
					handler, err := NewLocalHandler(dockerClient, comp, c.ID)
					if err == nil {
						byComponentNameVer[k] = handler
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
		dockerClient:       dockerClient,
		byComponentNameVer: byComponentNameVer,
	}, nil
}

type DockerHandlerFactory struct {
	dockerClient       *docker.Client
	byComponentNameVer map[string]Handler
}

func (f *DockerHandlerFactory) ForComponent(c v1.Component) (Handler, error) {
	k := c.Name + ":" + strconv.Itoa(int(c.Version))
	handler, ok := f.byComponentNameVer[k]
	if !ok {
		handler, err := NewLocalHandler(f.dockerClient, c, "")
		if err == nil {
			f.byComponentNameVer[k] = handler
		} else {
			log.Printf("ERROR cannot create handler for: %s - %v", c.Name, err)
		}
	}
	return handler, nil
}

func NewLocalHandler(dockerClient *docker.Client, c v1.Component, containerId string) (*LocalHandler, error) {
	handler := &LocalHandler{
		dockerClient: dockerClient,
		component:    c,
		containerId:  containerId,
		requestWG:    &sync.WaitGroup{},
		queue:        make(chan handlerMessage),
	}

	if containerId != "" {
		err := handler.initReverseProxy()
		if err != nil {
			return nil, err
		}
	}

	go handler.run()

	return handler, nil
}

func newHttpHandlerMessage(rw http.ResponseWriter, req *http.Request) handlerMessage {
	return handlerMessage{
		http:       &httpRequest{rw: rw, req: req},
		responseCh: make(chan bool, 1),
	}
}

type handlerMessage struct {
	// union type: only one of these attributes will be non-nil
	http *httpRequest
	stop bool

	// response chan
	responseCh chan bool
}

type httpRequest struct {
	rw  http.ResponseWriter
	req *http.Request
}

type LocalHandler struct {
	proxy        *httputil.ReverseProxy
	targetUrl    *url.URL
	dockerClient *docker.Client
	component    v1.Component
	containerId  string
	requestWG    *sync.WaitGroup
	queue        chan handlerMessage
}

func (h *LocalHandler) run() {
	idleTimeout := h.component.Docker.IdleTimeoutSeconds
	if idleTimeout < 1 {
		idleTimeout = 300
	}
	idleDuration := time.Second * time.Duration(idleTimeout)
	idleTimer := time.NewTimer(idleDuration)
	log.Printf("handler.run starting")
	for {
		select {
		case msg := <-h.queue:
			if msg.http != nil {
				if !idleTimer.Stop() {
					<-idleTimer.C
				}
				idleTimer.Reset(idleDuration)
				go func() {
					h.proxyHttpReq(msg.http.rw, msg.http.req)
					msg.responseCh <- true
				}()
			} else if msg.stop {
				log.Printf("handler.run stopping")
				h.stop()
				msg.responseCh <- true
				return
			} else {
				msg.responseCh <- true
			}
		case <-idleTimer.C:
			h.stop()
		}
	}
}

func (h *LocalHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	h.requestWG.Add(1)
	defer h.requestWG.Done()
	msg := newHttpHandlerMessage(rw, req)
	h.queue <- msg
	<-msg.responseCh
}

func (h *LocalHandler) proxyHttpReq(rw http.ResponseWriter, req *http.Request) {
	err := h.ensureContainer()
	if err != nil {
		log.Printf("ERROR ensureContainer failed for component: %s err: %v\n", h.component.Name, err)
		respondText(rw, http.StatusInternalServerError, "Server Error")
	} else {
		h.proxy.ServeHTTP(rw, req)
	}
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

	log.Printf("Starting handler for component: %s container: %s url: %s\n",
		h.component.Name, cont.ID[0:8], target.String())
	h.proxy = httputil.NewSingleHostReverseProxy(target)
	h.targetUrl = target
	return nil
}

func (h *LocalHandler) runHealthCheck() {
	resp, err := http.Get(h.healthCheckURL().String())
	if resp != nil && resp.Body != nil {
		defer common.CheckClose(resp.Body, &err)
	}
	if err != nil || resp.StatusCode != 200 {
		log.Printf("ERROR health check failed for container: %s\n", h.containerId)
		if h.containerId != "" {
			err = stopContainer(h.dockerClient, h.containerId)
			if err != nil {
				log.Printf("ERROR could not stop container: %v\n", err)
			} else {
				log.Printf("Stopped container: %s\n", h.containerId)
			}
		}
	}
}

func (h *LocalHandler) stop() {
	if h.containerId != "" {
		h.requestWG.Wait()
		t, ok := h.proxy.Transport.(*http.Transport)
		if ok {
			t.CloseIdleConnections()
		}
		err := stopContainer(h.dockerClient, h.containerId)
		if err != nil {
			log.Printf("ERROR stopContainer failed for %s: %v", h.containerId, err)
		}
		h.proxy = nil
		h.containerId = ""
	}
}

func (h *LocalHandler) healthCheckURL() *url.URL {
	path := ""
	if h.component.Docker != nil && h.component.Docker.HttpHealthCheckPath != "" {
		path = h.component.Docker.HttpHealthCheckPath
	}
	return &url.URL{
		Scheme: h.targetUrl.Scheme,
		Opaque: h.targetUrl.Opaque,
		Host:   h.targetUrl.Host,
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

	err = dockerClient.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return "", fmt.Errorf("containerStart error for: %s - %v", c.Name, err)
	}
	return resp.ID, nil
}

func stopContainer(dockerClient *docker.Client, containerId string) error {
	ctx := context.Background()

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
