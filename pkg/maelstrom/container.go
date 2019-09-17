package maelstrom

import (
	"context"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func StartContainer(reqCh chan *MaelRequest, dockerClient *docker.Client, component v1.Component,
	containerId string, parentCtx context.Context) (*Container, error) {

	proxy, healthCheckUrl, err := initReverseProxy(dockerClient, component, containerId)
	if err != nil {
		return nil, err
	}

	ctx, cancelFx := context.WithCancel(parentCtx)
	handlerWg := &sync.WaitGroup{}
	runWg := &sync.WaitGroup{}

	maxConcur := int(component.MaxConcurrency)
	if maxConcur <= 0 {
		maxConcur = 5
	}

	statCh := make(chan time.Duration, maxConcur)

	for i := 0; i < maxConcur; i++ {
		handlerWg.Add(1)
		go localRevProxy(reqCh, statCh, proxy, ctx, handlerWg)
	}

	c := &Container{
		reqCh:          reqCh,
		statCh:         statCh,
		containerId:    containerId,
		component:      component,
		healthCheckUrl: healthCheckUrl,
		handlerWg:      handlerWg,
		runWg:          runWg,
		ctx:            ctx,
		cancel:         cancelFx,
		lastReqTime:    time.Now(),
		lock:           &sync.Mutex{},
		dockerClient:   dockerClient,
		activity:       []v1.ComponentActivity{},
	}
	c.runWg.Add(1)
	go c.Run()

	return c, nil
}

type Container struct {
	reqCh          chan *MaelRequest
	statCh         chan time.Duration
	containerId    string
	component      v1.Component
	healthCheckUrl *url.URL
	runWg          *sync.WaitGroup
	handlerWg      *sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	lastReqTime    time.Time
	totalRequests  int64
	activity       []v1.ComponentActivity
	lock           *sync.Mutex
	dockerClient   *docker.Client
}

func (c *Container) Stop(reason string) {
	c.cancel()
	c.runWg.Wait()
	err := stopContainer(c.dockerClient, c.containerId, c.component.Name, strconv.Itoa(int(c.component.Version)), reason)
	if err != nil && !docker.IsErrContainerNotFound(err) && !common.IsErrRemovalInProgress(err) {
		log.Error("container: error stopping container", "err", err, "containerId", c.containerId[0:8],
			"component", c.component.Name)
	}
}

func (c *Container) HealthCheck() bool {
	return getUrlOK(c.healthCheckUrl)
}

func (c *Container) componentInfo() v1.ComponentInfo {
	c.lock.Lock()
	info := v1.ComponentInfo{
		ComponentName:     c.component.Name,
		MaxConcurrency:    c.component.MaxConcurrency,
		MemoryReservedMiB: c.component.Docker.ReserveMemoryMiB,
		LastRequestTime:   common.TimeToMillis(c.lastReqTime),
		TotalRequests:     c.totalRequests,
		Activity:          c.activity,
	}
	c.lock.Unlock()
	return info
}

func (c *Container) bumpReqStats() {
	c.lock.Lock()
	c.lastReqTime = time.Now()
	c.totalRequests++
	c.lock.Unlock()
}

func (c *Container) appendActivity(previousTotal int64, rolloverStartTime time.Time, totalDuration time.Duration) int64 {
	concurrency := float64(totalDuration) / float64(time.Now().Sub(rolloverStartTime))
	c.lock.Lock()
	currentTotal := c.totalRequests
	activity := v1.ComponentActivity{
		Requests:    currentTotal - previousTotal,
		Concurrency: concurrency,
	}
	c.activity = append([]v1.ComponentActivity{activity}, c.activity...)
	if len(c.activity) > 10 {
		c.activity = c.activity[0:10]
	}
	c.lock.Unlock()
	return currentTotal
}

func (c *Container) Run() {

	healthCheckSecs := c.component.Docker.HttpHealthCheckSeconds
	if healthCheckSecs <= 0 {
		healthCheckSecs = 10
	}

	healthCheckTicker := time.Tick(time.Duration(healthCheckSecs) * time.Second)
	concurrencyTicker := time.Tick(time.Second * 20)

	var durationSinceRollover time.Duration
	rolloverStartTime := time.Now()
	previousTotalRequests := int64(0)

	done := make(chan bool, 1)
	go func() {
		for {
			select {
			case dur := <-c.statCh:
				c.bumpReqStats()
				durationSinceRollover += dur
			case <-concurrencyTicker:
				previousTotalRequests = c.appendActivity(previousTotalRequests, rolloverStartTime, durationSinceRollover)
				rolloverStartTime = time.Now()
				durationSinceRollover = 0
			case <-healthCheckTicker:
				if !getUrlOK(c.healthCheckUrl) {
					log.Error("container: health check failed. stopping container", "containerId", c.containerId[0:8],
						"component", c.component.Name)
					go c.Stop("health check failed")
				}
			case <-done:
				log.Info("container: run loop exiting", "containerId", c.containerId[0:8], "component", c.component.Name)
				return
			}
		}
	}()

	<-c.ctx.Done()
	log.Info("container: waiting for handlers to finish", "containerId", c.containerId[0:8],
		"component", c.component.Name)
	c.handlerWg.Wait()
	done <- true
	c.runWg.Done()
}

/////////////////////////////////////////////////////////////

func initReverseProxy(dockerClient *docker.Client, component v1.Component,
	containerId string) (*httputil.ReverseProxy, *url.URL, error) {
	cont, err := dockerClient.ContainerInspect(context.Background(), containerId)
	if err != nil {
		return nil, nil, fmt.Errorf("container: initReverseProxy ContainerInspect error: %v", err)
	}

	dindHost := os.Getenv("DIND_HOST")

	target := &url.URL{
		Scheme: "http",
		Host:   "",
		Path:   "",
	}

	var ipAddr string
	var port string

	for _, endpoint := range cont.NetworkSettings.Networks {
		if endpoint.IPAddress != "" {
			ipAddr = endpoint.IPAddress
			break
		}
	}
	for p, portMap := range cont.NetworkSettings.Ports {
		k := string(p)
		if strings.HasSuffix(k, "/tcp") {
			if len(portMap) > 0 && dindHost != "" && portMap[0].HostIP == "0.0.0.0" {
				// CI docker-in-docker mode
				target.Host = dindHost + ":" + portMap[0].HostPort
			} else {
				// Normal mode where we can route directly to the container's IP addr
				port = k[0:strings.Index(k, "/")]
			}
			break
		}
	}

	if target.Host == "" && ipAddr != "" && port != "" {
		target.Host = ipAddr + ":" + port
	}

	if target.Host == "" {
		return nil, nil, fmt.Errorf("container: initReverseProxy unable to find exposed port for component: " +
			component.Name)
	}

	// wait for health check to pass
	healthCheckUrl := toHealthCheckURL(component, target)
	healthCheckStartSecs := component.Docker.HttpStartHealthCheckSeconds
	if healthCheckStartSecs == 0 {
		healthCheckStartSecs = 60
	}
	if healthCheckStartSecs > 0 {
		if !tryUntilUrlOk(healthCheckUrl, time.Second*time.Duration(healthCheckStartSecs)) {
			return nil, nil, fmt.Errorf("container: health check never passed for: %s url: %s", component.Name,
				healthCheckUrl)
		}
	}

	log.Info("container: active for component", "component", component.Name, "ver", component.Version,
		"containerId", cont.ID[0:8], "url", target.String())
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return proxy, healthCheckUrl, nil
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
