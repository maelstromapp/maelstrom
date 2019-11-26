package maelstrom

import (
	"context"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Test HTTP docker image. Has endpoints that return 200, 500, and sleep
// See: https://github.com/coopernurse/go-hello-http
const testImageName = "docker.io/coopernurse/go-hello-http:latest"
const testGatewayUrl = "http://127.0.0.1:8000"

var defaultComponent v1.Component
var cronService *CronService
var contextCancelFx func()
var dockerClient *docker.Client
var pinger *httpPinger
var dMonitor *dockerMonitor
var httpResultAgg httpResultAggregate

type httpResult struct {
	elapsed time.Duration
	err     error
}

type httpResultAggregate struct {
	successCount int
	errorCount   int
	lastError    error
	meanElapsed  time.Duration
	p50Elapsed   time.Duration
	p90Elapsed   time.Duration
	p99Elapsed   time.Duration
}

func newHttpPinger(url string, reqDelay time.Duration, fixture *Fixture) *httpPinger {
	ctx, cancelFx := context.WithCancel(context.Background())
	return &httpPinger{
		url:         url,
		fixture:     fixture,
		ctx:         ctx,
		ctxCancel:   cancelFx,
		wg:          &sync.WaitGroup{},
		reqDelay:    reqDelay,
		resultCh:    make(chan *httpResult),
		aggResultCh: make(chan httpResultAggregate),
	}
}

type httpPinger struct {
	url         string
	fixture     *Fixture
	ctx         context.Context
	ctxCancel   context.CancelFunc
	wg          *sync.WaitGroup
	reqDelay    time.Duration
	resultCh    chan *httpResult
	aggResultCh chan httpResultAggregate
}

func (p *httpPinger) start(n int) {
	p.wg.Add(n)
	for i := 0; i < n; i++ {
		go p.pingerLoop()
	}
	go p.collectorLoop()
}

func (p *httpPinger) stop() httpResultAggregate {
	p.ctxCancel()
	p.wg.Wait()
	close(p.resultCh)
	return <-p.aggResultCh
}

func (p *httpPinger) pingerLoop() {
	defer p.wg.Done()
	ticker := time.Tick(p.reqDelay)
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker:
			var err error
			start := time.Now()
			rw := p.fixture.makeHttpRequest(p.url)
			elapsed := time.Now().Sub(start)
			if rw.Result().StatusCode != 200 {
				err = fmt.Errorf("non 200 response: %d", rw.Result().StatusCode)
			}
			p.resultCh <- &httpResult{
				elapsed: elapsed,
				err:     err,
			}
		}
	}
}

func (p *httpPinger) collectorLoop() {
	var agg httpResultAggregate
	var totalElapsed time.Duration
	timings := make([]time.Duration, 0)
	for r := range p.resultCh {
		if r.err == nil {
			agg.successCount++
		} else {
			agg.errorCount++
			agg.lastError = r.err
		}
		totalElapsed += r.elapsed
		timings = append(timings, r.elapsed)
	}
	totalReq := len(timings)
	if totalReq > 0 {
		agg.meanElapsed = totalElapsed / time.Duration(totalReq)
		sort.Sort(DurationAscend(timings))
		agg.p50Elapsed = timings[totalReq/2]
		agg.p90Elapsed = timings[int(float64(totalReq)*.9)]
		agg.p99Elapsed = timings[int(float64(totalReq)*.99)]
	}
	p.aggResultCh <- agg
}

type dockerEvent struct {
	image       string
	timeNano    int64
	action      string
	containerId string
}

func newDockerMonitor(dockerClient *docker.Client) *dockerMonitor {
	ctx, cancelFx := context.WithCancel(context.Background())
	return &dockerMonitor{
		dockerClient: dockerClient,
		ctx:          ctx,
		ctxCancel:    cancelFx,
		wg:           &sync.WaitGroup{},
		events:       make([]*dockerEvent, 0),
	}
}

type dockerMonitor struct {
	dockerClient *docker.Client
	ctx          context.Context
	ctxCancel    context.CancelFunc
	wg           *sync.WaitGroup
	events       []*dockerEvent
}

func (d *dockerMonitor) reset() {
	d.events = make([]*dockerEvent, 0)
}

func (d *dockerMonitor) start() {
	d.wg.Add(1)
	go d.monitorEventLoop()
}

func (d *dockerMonitor) stop() {
	d.ctxCancel()
	d.wg.Wait()
}

func (d *dockerMonitor) monitorEventLoop() {
	defer d.wg.Done()
	msgCh, _ := d.dockerClient.Events(d.ctx, types.EventsOptions{})
	for {
		select {
		case <-d.ctx.Done():
			return
		case m := <-msgCh:
			if m.Status != "" {
				d.events = append(d.events, &dockerEvent{
					image:       m.From,
					timeNano:    m.TimeNano,
					action:      m.Action,
					containerId: m.ID,
				})
			}
			fmt.Printf("%+v\n", m)
		}
	}
}

func (d *dockerMonitor) actionsByImage(image string) []string {
	actions := make([]string, 0)
	for _, ev := range d.events {
		if ev.image == image && ev.action != "" {
			actions = append(actions, ev.action)
		}
	}
	return actions
}

func init() {
	dc, err := docker.NewEnvClient()
	if err != nil {
		panic(err)
	}
	dockerClient = dc

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGQUIT)
		buf := make([]byte, 1<<20)
		for {
			<-sigs
			stacklen := runtime.Stack(buf, true)
			fmt.Printf("=== received SIGQUIT ===\n*** goroutine dump...\n%s\n*** end\n", buf[:stacklen])
		}
	}()
}

func beforeTest() {
	resetDefaults()
	startDockerMonitor()
}

func afterTest(t *testing.T) {
	stopPinger()
	stopDockerMonitor()
	stopCronService()
	stopMaelstromContainers(t)
}

func wrapTest(t *testing.T, test func()) {
	beforeTest()
	defer afterTest(t)
	test()
}

func resetDefaults() {
	defaultComponent = v1.Component{
		Version:        0,
		Name:           "maeltest",
		MaxConcurrency: 5,
		Docker: &v1.DockerComponent{
			HttpPort:                    8080,
			Image:                       testImageName,
			HttpHealthCheckPath:         "/",
			HttpHealthCheckSeconds:      2,
			HttpStartHealthCheckSeconds: 60,
		},
	}
}

func startDockerMonitor() {
	stopDockerMonitor()
	dMonitor = newDockerMonitor(dockerClient)
	dMonitor.start()
}

func stopDockerMonitor() {
	if dMonitor != nil {
		dMonitor.stop()
		dMonitor = nil
	}
}

func stopPinger() {
	if pinger != nil {
		httpResultAgg = pinger.stop()
		pinger = nil
	}
}

func stopCronService() {
	if cronService != nil {
		contextCancelFx()
		cronService = nil
	}
}

func stopMaelstromContainers(t *testing.T) {
	_, err := common.RemoveMaelstromContainers(dockerClient, "stopping all maelstrom containers")
	assert.Nil(t, err, "RemoveMaelstromContainers err != nil: %v", err)
}

func newDb(t *testing.T) *SqlDb {
	sqlDb, err := NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=MEMORY&mode=rwc")
	assert.Nil(t, err, "NewSqlDb err != nil: %v", err)
	err = sqlDb.Migrate()
	assert.Nil(t, err, "sqlDb.Migrate err != nil: %v", err)
	err = sqlDb.DeleteAll()
	assert.Nil(t, err, "sqlDb.DeleteAll err != nil: %v", err)
	defaultComponent.Version = 0
	_, err = sqlDb.PutComponent(defaultComponent)
	assert.Nil(t, err, "sqlDb.PutComponent err != nil: %v", err)
	defaultComponent.Version = 1
	return sqlDb
}

func newFixture(t *testing.T, dockerClient *docker.Client, sqlDb *SqlDb) *Fixture {
	successfulReqs := int64(0)
	daemonWG := &sync.WaitGroup{}
	resolver := NewDbResolver(sqlDb, nil, 0)
	shutdownCh := make(chan ShutdownFunc)

	outboundIp, err := common.GetOutboundIP()
	if err != nil {
		log.Error("maelstromd: cannot resolve outbound IP address", "err", err)
		os.Exit(2)
	}

	nodeSvcImpl, err := NewNodeServiceImplFromDocker(sqlDb, dockerClient, 8374, "", -1, "", shutdownCh, nil,
		"", component.NewPullState(dockerClient))
	assert.Nil(t, err, "NewNodeServiceImplFromDocker err != nil: %v", err)

	gateway := NewGateway(resolver, nodeSvcImpl.Dispatcher(), false, outboundIp.String())
	cancelCtx, cancelFx := context.WithCancel(context.Background())
	contextCancelFx = cancelFx
	cronService = NewCronService(sqlDb, gateway, cancelCtx, "testnode", time.Second)

	return &Fixture{
		t:              t,
		dockerClient:   dockerClient,
		successfulReqs: &successfulReqs,
		v1Impl:         NewMaelServiceImpl(sqlDb, nil, nil, nodeSvcImpl.nodeId, nodeSvcImpl.Cluster()),
		nodeSvcImpl:    nodeSvcImpl,
		component:      defaultComponent,
		asyncReqWG:     &sync.WaitGroup{},
		daemonWG:       daemonWG,
		shutdownCh:     shutdownCh,
	}
}

type Fixture struct {
	t                *testing.T
	dockerClient     *docker.Client
	component        v1.Component
	v1Impl           *MaelServiceImpl
	nodeSvcImpl      *NodeServiceImpl
	cronService      *CronService
	nextContainerId  string
	beforeContainers []types.Container
	successfulReqs   *int64
	asyncReqWG       *sync.WaitGroup
	daemonWG         *sync.WaitGroup
	shutdownCh       chan ShutdownFunc
}

func GivenNoMaelstromContainers(t *testing.T) *Fixture {
	stopMaelstromContainers(t)
	f := newFixture(t, dockerClient, newDb(t))
	return f
}

func GivenExistingContainer(t *testing.T) *Fixture {
	return GivenExistingContainerWith(t, func(c *v1.Component) {})
}

func GivenExistingContainerWith(t *testing.T, mutateFx func(c *v1.Component)) *Fixture {
	sqlDb := newDb(t)
	mutateFx(&defaultComponent)
	containerId, err := common.StartContainer(dockerClient, &defaultComponent, testGatewayUrl)

	fmt.Printf("GivenExistingContainer startContainer: %s\n", containerId)

	f := newFixture(t, dockerClient, sqlDb)
	assert.Nil(t, err, "startContainer err != nil: %v", err)
	f.nextContainerId = containerId

	containers, err := common.ListMaelstromContainers(dockerClient)
	assert.Nil(t, err, "listContainers err != nil: %v", err)
	f.beforeContainers = containers

	return f
}

func GivenExistingContainerWithIdleTimeout(t *testing.T, seconds int64) *Fixture {
	defaultComponent.Docker.IdleTimeoutSeconds = seconds
	return GivenExistingContainer(t)
}

func GivenExistingContainerWithBadHealthCheckPath(t *testing.T) *Fixture {
	defaultComponent.Docker.HttpHealthCheckPath = "/fail"
	defaultComponent.Docker.HttpStartHealthCheckSeconds = -1
	return GivenExistingContainer(t)
}

func (f *Fixture) makeHttpRequest(url string) *httptest.ResponseRecorder {
	req, err := http.NewRequest("GET", url, nil)
	rw := httptest.NewRecorder()
	assert.Nil(f.t, err, "http.NewRequest err != nil: %v", err)
	f.nodeSvcImpl.Dispatcher().Route(rw, req, &f.component, false)
	return rw
}

func (f *Fixture) WhenSystemIsStarted() *Fixture {
	// no-op
	return f
}

func (f *Fixture) WhenAnotherInstanceIsStarted() *Fixture {
	containerId, err := common.StartContainer(dockerClient, &defaultComponent, testGatewayUrl)
	assert.Nil(f.t, err, "StartContainer err != nil: %v", err)

	fmt.Printf("WhenAnotherInstanceIsStarted startContainer: %s\n", containerId)
	return f
}

func (f *Fixture) WhenCronServiceStartedWithSeconds() *Fixture {
	f.daemonWG.Add(1)
	go cronService.Run(f.daemonWG, true)
	return f
}

func (f *Fixture) WhenCronEventSourceRegistered(schedule string) *Fixture {
	_, err := f.v1Impl.PutEventSource(v1.PutEventSourceInput{
		EventSource: v1.EventSource{
			Name:          "cron-" + defaultComponent.Name,
			ComponentName: defaultComponent.Name,
			Version:       0,
			ModifiedAt:    0,
			Cron: &v1.CronEventSource{
				Schedule: schedule,
				Http: v1.CronHttpRequest{
					Method: "GET",
					Path:   "/count",
				},
			},
		},
	})
	assert.Nil(f.t, err, "cron PutEventSource err != nil: %v", err)
	return f
}

func (f *Fixture) WhenHTTPRequestReceived() *Fixture {
	f.makeHttpRequest("http://127.0.0.1:12345/")
	return f
}

func (f *Fixture) WhenContainerIsHealthy() *Fixture {
	timeout := time.Now().Add(time.Second * 30)
	for {
		if time.Now().After(timeout) {
			f.t.Errorf("Container never became healthy")
			break
		} else {
			rw := f.makeHttpRequest("http://127.0.0.1:12345/")
			if rw.Result().StatusCode == 200 && rw.Body.String() != "" {
				fmt.Printf("container healthy: %s\n", rw.Body.String())
				break
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	return f
}

func (f *Fixture) WhenNLongRunningRequestsMade(n int) *Fixture {
	for i := 0; i < n; i++ {
		f.asyncReqWG.Add(1)
		go func() {
			defer f.asyncReqWG.Done()
			log.Info("fixture_test: requesting /sleep")
			start := time.Now()
			rw := f.makeHttpRequest("http://127.0.0.1:12345/sleep?seconds=5")
			log.Info("fixture_test: done requesting /sleep", "status", rw.Result().StatusCode,
				"body", rw.Body.String())
			if rw.Result().StatusCode == 200 && rw.Body.String() != "" {
				elapsedMillis := time.Now().Sub(start) / 1e6
				atomic.AddInt64(f.successfulReqs, 1)
				fmt.Printf("elapsedMS: %d  response: %s\n", elapsedMillis, rw.Body.String())
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	return f
}

func (f *Fixture) WhenStopRequestReceived() *Fixture {
	info := f.nodeSvcImpl.Dispatcher().ComponentInfo()
	f.nodeSvcImpl.Dispatcher().Scale(&component.ScaleInput{
		TargetVersion: info.Version,
		TargetCounts: []component.ScaleTarget{
			{
				Component:         &f.component,
				Delta:             -1,
				RequiredMemoryMiB: 0,
			},
		},
	})
	return f
}

func (f *Fixture) WhenIdleTimeoutElapses() *Fixture {
	time.Sleep(time.Second * time.Duration(f.component.Docker.IdleTimeoutSeconds+1))
	return f
}

func (f *Fixture) WhenHealthCheckTimeoutElapses() *Fixture {
	time.Sleep(time.Second * time.Duration(f.component.Docker.HttpHealthCheckSeconds+1))
	return f
}

func (f *Fixture) WhenAutoscaleRuns() *Fixture {
	f.nodeSvcImpl.autoscale()
	return f
}

func (f *Fixture) WhenComponentIsUpdated() *Fixture {
	f.component.Version++
	f.nodeSvcImpl.Dispatcher().OnComponentNotification(v1.DataChangedUnion{
		PutComponent: &f.component,
	})
	return f
}

func (f *Fixture) WhenHTTPRequestsAreMadeContinuously() *Fixture {
	stopPinger()
	pinger = newHttpPinger("http://127.0.0.1:12345/", time.Millisecond, f)
	pinger.start(2)
	return f
}

func (f *Fixture) AndTimePasses(duration time.Duration) *Fixture {
	time.Sleep(duration)
	return f
}

func (f *Fixture) AndDockerEventsAreReset() *Fixture {
	dMonitor.reset()
	return f
}

func (f *Fixture) ThenContainerStartsWithin(timeout time.Duration) *Fixture {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.testImageContainerExists() {
			return f
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Errorf("testImage container did not start before deadline elapsed")
	return f
}

func (f *Fixture) ThenContainerIsStarted() *Fixture {
	assert.True(f.t, f.testImageContainerExists(), "testImage container is not running!")
	return f
}

func (f *Fixture) ThenContainerIsStartedWithNewVersion() *Fixture {
	assert.True(f.t, f.componentContainerExists(), "component container is not running!")
	return f
}

func (f *Fixture) ThenContainerIsStartedBeforeTheOlderContainerIsStopped() *Fixture {
	expected := []string{"create", "start", "create", "start", "kill", "die", "stop", "destroy"}
	assert.Equal(f.t, expected, dMonitor.actionsByImage(defaultComponent.Docker.Image))
	return f
}

func (f *Fixture) ThenContainerIsStoppedBeforeTheOlderContainerIsStarted() *Fixture {
	expected := []string{"create", "start", "kill", "die", "stop", "destroy", "create", "start"}
	assert.Equal(f.t, expected, dMonitor.actionsByImage(defaultComponent.Docker.Image))
	return f
}

func (f *Fixture) ThenContainersAreRestartedInSeries() *Fixture {
	expected := []string{"create", "start", "create", "start"}
	assert.Equal(f.t, expected, dMonitor.actionsByImage(defaultComponent.Docker.Image))
	return f
}

func (f *Fixture) ThenContainersAreRestartedInParallel() *Fixture {
	expected := []string{"create", "create", "start", "start"}
	assert.Equal(f.t, expected, dMonitor.actionsByImage(defaultComponent.Docker.Image))
	return f
}

func (f *Fixture) ThenAllHTTPRequestsCompletedWithoutDelay() *Fixture {
	stopPinger()
	assert.True(f.t, httpResultAgg.successCount > 0)
	assert.Equal(f.t, 0, httpResultAgg.errorCount, "got errors: %v", httpResultAgg.lastError)
	assert.True(f.t, httpResultAgg.p99Elapsed < 20*time.Millisecond)
	return f
}

func (f *Fixture) ThenContainerIsStopped() *Fixture {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f.testImageContainerExists() {
			time.Sleep(100 * time.Millisecond)
		} else {
			return f
		}
	}
	f.t.Errorf("testImage container is running!")
	return f
}

func (f *Fixture) ThenNoNewContainerStarted() *Fixture {
	containers, err := common.ListMaelstromContainers(f.dockerClient)
	assert.Nil(f.t, err, "listContainers err != nil: %v", err)
	f.scrubContainers(f.beforeContainers)
	f.scrubContainers(containers)
	assert.Equal(f.t, f.beforeContainers, containers)
	return f
}

func (f *Fixture) ThenSuccessfulRequestCountEquals(x int) *Fixture {
	f.asyncReqWG.Wait()
	assert.Equal(f.t, *f.successfulReqs, int64(x))
	return f
}

// strip status from Container structs, as this encodes relative time info
// that may change during the test run and cause equality tests to fail
func (f *Fixture) scrubContainers(containers []types.Container) {
	for i, _ := range containers {
		containers[i].Status = ""
	}
}

func (f *Fixture) testImageContainerExists() bool {
	containers, err := common.ListMaelstromContainers(f.dockerClient)
	assert.Nil(f.t, err, "listContainers err != nil: %v", err)

	for _, c := range containers {
		if c.Image == testImageName {
			return true
		}
	}
	return false
}

func (f *Fixture) componentContainerExists() bool {
	containers, err := common.ListMaelstromContainers(f.dockerClient)
	assert.Nil(f.t, err, "listContainers err != nil: %v", err)

	compVer := strconv.Itoa(int(f.component.Version))

	for _, c := range containers {
		name := c.Labels["maelstrom_component"]
		verStr := c.Labels["maelstrom_version"]
		if c.Image == f.component.Docker.Image && name == f.component.Name && verStr == compVer {
			return true
		}
	}
	return false
}
