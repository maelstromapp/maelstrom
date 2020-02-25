package converge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/revproxy"
	"github.com/coopernurse/maelstrom/pkg/router"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
)

type maelContainerId uint64

func NewContainer(dockerClient *docker.Client, component *v1.Component, maelstromUrl string,
	router *router.Router, id maelContainerId, bufferPool httputil.BufferPool, parentCtx context.Context) *Container {
	ctx, cancelFx := context.WithCancel(parentCtx)
	revProxyCtx, revProxyCancel := context.WithCancel(parentCtx)

	healthCheckMaxFailures := int(component.Docker.HttpHealthCheckMaxFailures)
	if healthCheckMaxFailures <= 0 {
		healthCheckMaxFailures = 1
	}

	statCh := make(chan time.Duration, maxConcurrency(component))

	c := &Container{
		id:                     id,
		status:                 v1.ComponentStatusStarting,
		dockerClient:           dockerClient,
		router:                 router,
		runWg:                  &sync.WaitGroup{},
		revProxyWg:             &sync.WaitGroup{},
		ctx:                    ctx,
		cancel:                 cancelFx,
		revProxyCtx:            revProxyCtx,
		revProxyCancel:         revProxyCancel,
		statCh:                 statCh,
		statLock:               &sync.Mutex{},
		component:              component,
		maelstromUrl:           maelstromUrl,
		startTime:              time.Now(),
		lastReqTime:            time.Time{},
		totalRequests:          0,
		activity:               make([]v1.ComponentActivity, 0),
		healthCheckFailures:    0,
		healthCheckMaxFailures: healthCheckMaxFailures,
		bufferPool:             bufferPool,
	}
	return c
}

type Container struct {
	id     maelContainerId
	status v1.ComponentStatus

	// marker field - if non-empty, container should be terminated by converger
	terminateReason string

	dockerClient *docker.Client

	runWg      *sync.WaitGroup
	revProxyWg *sync.WaitGroup

	// reverse proxy instance used by rev proxy workers
	// hits the container port for the container we're managing
	proxy      *httputil.ReverseProxy
	bufferPool httputil.BufferPool

	// router for this component
	router *router.Router

	// our context.  used to stop run() loop and rev proxy workers if we're asked to stop
	ctx    context.Context
	cancel context.CancelFunc

	revProxyCtx    context.Context
	revProxyCancel context.CancelFunc

	component    *v1.Component
	maelstromUrl string
	containerId  string

	// channel used by rev proxy workers to report request elapsed time
	statCh chan time.Duration

	// lock to guard activity stat access
	statLock *sync.Mutex

	// activity stats
	startTime     time.Time
	lastReqTime   time.Time
	totalRequests int64
	activity      []v1.ComponentActivity

	// health check state
	healthCheckUrl         *url.URL
	healthCheckFailures    int
	healthCheckMaxFailures int
}

func (c *Container) ComponentInfo() v1.ComponentInfo {
	c.statLock.Lock()
	info := v1.ComponentInfo{
		ComponentName:     c.component.Name,
		ComponentVersion:  c.component.Version,
		Status:            c.status,
		MaxConcurrency:    c.component.MaxConcurrency,
		MemoryReservedMiB: c.component.Docker.ReserveMemoryMiB,
		StartTime:         common.TimeToMillis(c.startTime),
		LastRequestTime:   common.TimeToMillis(c.lastReqTime),
		TotalRequests:     c.totalRequests,
		Activity:          c.activity,
	}
	c.statLock.Unlock()
	return info
}

func (c *Container) JoinAndStop(reason string) {
	log.Info("container: shutting down", "reason", reason, "containerId", common.StrTruncate(c.containerId, 8))

	c.setStatus(v1.ComponentStatusStopping)

	// cancel the rev proxies
	c.revProxyCancel()

	// wait for rev proxies to finish
	c.revProxyWg.Wait()

	// cancel context - this will terminate run loop
	c.cancel()

	// wait for run to exit
	c.runWg.Wait()

	// remove container
	c.stopContainerQuietly(reason)
}

func (c *Container) setStatus(newStatus v1.ComponentStatus) {
	c.statLock.Lock()
	if log.IsDebug() {
		log.Debug("container: changing status", "from", c.status, "to", newStatus,
			"containerId", common.StrTruncate(c.containerId, 8))
	}
	c.status = newStatus
	c.statLock.Unlock()
}

func (c *Container) startAndHealthCheck(ctx context.Context) error {
	var stopReason string
	var err error
	err = c.startContainer()
	if err != nil {
		stopReason = "failed to start"
	}
	if err == nil {
		err = c.initReverseProxy(ctx)
		if err != nil {
			stopReason = "failed to init reverse proxy or health check"
		}
	}

	if err == nil {
		c.setStatus(v1.ComponentStatusActive)
	} else {
		c.stopContainerQuietly(stopReason)
	}
	return err
}

func (c *Container) run() {
	c.runWg.Add(1)
	defer c.runWg.Done()

	maxConcurRevProxy := maxConcurrency(c.component)
	if c.component.SoftConcurrencyLimit {
		maxConcurRevProxy = 0
	}
	reqCh := c.router.HandlerStartLocal()
	go func() {
		defer c.router.HandlerStop()
		dispenser := revproxy.NewDispenser(maxConcurRevProxy, reqCh, "",
			"", c.revProxyWg, c.proxy, c.statCh, c.revProxyCtx)
		dispenser.Run(c.ctx)
	}()

	healthCheckSecs := c.component.Docker.HttpHealthCheckSeconds
	if healthCheckSecs <= 0 {
		healthCheckSecs = 10
	}
	healthCheckTicker := time.Tick(time.Duration(healthCheckSecs) * time.Second)
	activityTicker := time.Tick(time.Second * 20)

	var durationSinceRollover time.Duration
	rolloverStartTime := time.Now()
	previousTotalRequests := int64(0)

	running := true
	for running {
		select {
		case dur := <-c.statCh:
			// duration received after request fulfilled - increment counters
			c.bumpReqStats()
			if dur < 0 {
				log.Warn("container: ignoring negative request duration", "component", c.component.Name)
			} else {
				durationSinceRollover += dur
			}
		case <-activityTicker:
			// rotate activity buffer - this is used to report concurrency and req counts every x seconds
			previousTotalRequests = c.appendActivity(previousTotalRequests, rolloverStartTime,
				durationSinceRollover)
			rolloverStartTime = time.Now()
			durationSinceRollover = 0
		case <-healthCheckTicker:
			c.runHealthCheck()
		case <-c.ctx.Done():
			running = false
		}
	}

	log.Info("container: exiting run loop", "containerId", common.StrTruncate(c.containerId, 8))
}

func (c *Container) bumpReqStats() {
	c.statLock.Lock()
	c.lastReqTime = time.Now()
	c.totalRequests++
	c.statLock.Unlock()
}

func (c *Container) appendActivity(previousTotal int64, rolloverStartTime time.Time, totalDuration time.Duration) int64 {
	c.statLock.Lock()
	concurrency := float64(totalDuration) / float64(time.Now().Sub(rolloverStartTime))
	currentTotal := c.totalRequests
	activity := v1.ComponentActivity{
		Requests:    currentTotal - previousTotal,
		Concurrency: concurrency,
	}
	c.activity = append([]v1.ComponentActivity{activity}, c.activity...)
	if len(c.activity) > 10 {
		c.activity = c.activity[0:10]
	}
	c.statLock.Unlock()
	return currentTotal
}

func (c *Container) startContainer() error {
	containerId, err := common.StartContainer(c.dockerClient, c.component, c.maelstromUrl)
	c.containerId = containerId
	return err
}

func (c *Container) stopContainerQuietly(reason string) {
	if c.containerId != "" {
		err := common.RemoveContainer(c.dockerClient, c.containerId, c.component.Name,
			strconv.Itoa(int(c.component.Version)), reason)
		if err != nil && !docker.IsErrContainerNotFound(err) {
			log.Warn("container: unable to stop container", "err", err.Error(), "component", c.component.Name)
		}
	}
}

func (c *Container) runHealthCheck() {
	if getUrlOK(c.healthCheckUrl) {
		c.healthCheckFailures = 0
	} else {
		c.healthCheckFailures++
		if c.healthCheckFailures >= c.healthCheckMaxFailures {
			log.Error("container: health check failed. stopping container",
				"containerId", common.StrTruncate(c.containerId, 8),
				"component", c.component.Name, "failures", c.healthCheckFailures)
			go c.JoinAndStop("health check failed")
			c.healthCheckFailures = 0
		} else {
			log.Warn("container: health check failed", "failures", c.healthCheckFailures,
				"maxFailures", c.healthCheckMaxFailures)
		}
	}
}

func (c *Container) initReverseProxy(ctx context.Context) error {
	cont, err := c.dockerClient.ContainerInspect(ctx, c.containerId)
	if err != nil {
		return fmt.Errorf("container: initReverseProxy ContainerInspect error: %v", err)
	}

	dindHost := os.Getenv("DIND_HOST")

	target := &url.URL{
		Scheme: "http",
		Host:   "",
		Path:   "",
	}

	var ipAddr string

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
			}
			break
		}
	}

	if target.Host == "" && ipAddr != "" {
		target.Host = fmt.Sprintf("%s:%d", ipAddr, c.component.Docker.HttpPort)
	}

	if target.Host == "" {
		return fmt.Errorf("container: initReverseProxy unable to find exposed port for component: " +
			c.component.Name)
	}

	// wait for health check to pass
	healthCheckUrl := toHealthCheckURL(c.component, target)
	healthCheckStartSecs := c.component.Docker.HttpStartHealthCheckSeconds
	if healthCheckStartSecs == 0 {
		healthCheckStartSecs = 60
	}
	if healthCheckStartSecs > 0 {
		if !tryUntilUrlOk(ctx, healthCheckUrl, time.Second*time.Duration(healthCheckStartSecs)) {
			return fmt.Errorf("container: health check never passed for: %s url: %s", c.component.Name,
				healthCheckUrl)
		}
	}

	log.Info("container: active for component", "component", c.component.Name, "ver", c.component.Version,
		"containerId", common.StrTruncate(cont.ID, 8), "url", target.String())
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.BufferPool = c.bufferPool
	proxy.Transport = &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: maxConcurrency(c.component),
		IdleConnTimeout:     20 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 15 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	c.proxy = proxy
	c.healthCheckUrl = healthCheckUrl
	return nil
}

func toHealthCheckURL(c *v1.Component, baseUrl *url.URL) *url.URL {
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

func tryUntilUrlOk(ctx context.Context, u *url.URL, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if getUrlOK(u) {
			return true
		}

		select {
		case <-ctx.Done():
			// context canceled
			return false
		case <-time.After(50 * time.Millisecond):
			// try again
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

func maxConcurrency(c *v1.Component) int {
	maxConcur := int(c.MaxConcurrency)
	if maxConcur <= 0 {
		maxConcur = 1
	}
	return maxConcur
}
