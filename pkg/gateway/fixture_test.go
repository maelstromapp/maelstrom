package gateway

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"github.com/stretchr/testify/assert"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"runtime"
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
const testGatewayPort = 8000

var defaultComponent v1.Component
var cronService *CronService
var contextCancelFx func()
var dockerClient *docker.Client

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
}

func afterTest(t *testing.T) {
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
		Version: 0,
		Name:    "maeltest",
		Docker: &v1.DockerComponent{
			HttpPort:                    8080,
			Image:                       testImageName,
			HttpHealthCheckPath:         "/",
			HttpHealthCheckSeconds:      2,
			HttpStartHealthCheckSeconds: 60,
		},
	}
}

func stopCronService() {
	if cronService != nil {
		contextCancelFx()
		cronService = nil
	}
}

func stopMaelstromContainers(t *testing.T) {
	containers, err := listContainers(dockerClient)
	assert.Nil(t, err, "listContainers err != nil: %v", err)

	for _, c := range containers {
		err = stopContainer(dockerClient, c.ID, "", "", "fixture stopMaelstromContainers")
		if err != nil {
			log.Error("fixture_test: stopContainer failed", "container", c.ID, "err", err)
		}
	}
}

func newDb(t *testing.T) *v1.SqlDb {
	sqlDb, err := v1.NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=MEMORY&mode=rwc")
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

func newFixture(t *testing.T, dockerClient *docker.Client, sqlDb *v1.SqlDb) *Fixture {
	successfulReqs := int64(0)
	daemonWG := &sync.WaitGroup{}
	ctx := context.Background()
	resolver := NewDbResolver(sqlDb, nil)

	router := NewRouter(nil, "", ctx)
	daemonWG.Add(1)
	go router.Run(daemonWG)

	hFactory, err := NewDockerHandlerFactory(dockerClient, resolver, sqlDb, router, ctx, testGatewayPort)
	assert.Nil(t, err, "NewDockerHandlerFactory err != nil: %v", err)

	nodeSvcImpl, err := NewNodeServiceImplFromDocker(hFactory, sqlDb, dockerClient, "")
	assert.Nil(t, err, "NewNodeServiceImplFromDocker err != nil: %v", err)
	router.SetNodeService(nodeSvcImpl, nodeSvcImpl.nodeId)

	gateway := NewGateway(resolver, router, false)
	cancelCtx, cancelFx := context.WithCancel(context.Background())
	contextCancelFx = cancelFx
	cronService = NewCronService(sqlDb, gateway, cancelCtx, time.Second)

	return &Fixture{
		t:              t,
		dockerClient:   dockerClient,
		successfulReqs: &successfulReqs,
		v1Impl:         v1.NewV1(sqlDb, nil, nil),
		nodeSvcImpl:    nodeSvcImpl,
		hFactory:       hFactory,
		router:         router,
		component:      defaultComponent,
		asyncReqWG:     &sync.WaitGroup{},
		daemonWG:       daemonWG,
	}
}

type Fixture struct {
	t                *testing.T
	dockerClient     *docker.Client
	component        v1.Component
	hFactory         *DockerHandlerFactory
	router           *Router
	v1Impl           *v1.V1
	nodeSvcImpl      *NodeServiceImpl
	cronService      *CronService
	nextContainerId  string
	beforeContainers []types.Container
	successfulReqs   *int64
	asyncReqWG       *sync.WaitGroup
	daemonWG         *sync.WaitGroup
}

func GivenNoMaelstromContainers(t *testing.T) *Fixture {
	stopMaelstromContainers(t)
	f := newFixture(t, dockerClient, newDb(t))
	return f
}

func GivenExistingContainer(t *testing.T) *Fixture {
	sqlDb := newDb(t)
	containerId, err := startContainer(dockerClient, defaultComponent, testGatewayUrl)

	fmt.Printf("GivenExistingContainer startContainer: %s\n", containerId)

	f := newFixture(t, dockerClient, sqlDb)
	assert.Nil(t, err, "startContainer err != nil: %v", err)
	f.nextContainerId = containerId

	containers, err := listContainers(dockerClient)
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
	f.router.Route(rw, req, f.component)
	return rw
}

func (f *Fixture) WhenCronServiceStarted() *Fixture {
	f.daemonWG.Add(1)
	go cronService.Run(f.daemonWG)
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
	f.hFactory.stopContainersByComponent(f.component.Name, "fixture WhenStopRequestReceived")
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
	f.hFactory.OnComponentNotification(v1.ComponentNotification{
		PutComponent: &v1.PutComponentOutput{
			Name:    f.component.Name,
			Version: f.component.Version + 1,
		},
	})
	return f
}

func (f *Fixture) AndTimePasses(duration time.Duration) *Fixture {
	time.Sleep(duration)
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
	containers, err := listContainers(f.dockerClient)
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
	containers, err := listContainers(f.dockerClient)
	assert.Nil(f.t, err, "listContainers err != nil: %v", err)

	for _, c := range containers {
		if c.Image == testImageName {
			return true
		}
	}
	return false
}

func (f *Fixture) componentContainerExists() bool {
	containers, err := listContainers(f.dockerClient)
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
