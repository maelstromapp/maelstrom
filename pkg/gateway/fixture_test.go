package gateway

import (
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	. "github.com/franela/goblin"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Test HTTP docker image. Has endpoints that return 200, 500, and sleep
// See: https://github.com/coopernurse/go-hello-http
const testImageName = "docker.io/coopernurse/go-hello-http:latest"

var defaultComponent v1.Component

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

func stopMaelstromContainers(g *G, dockerClient *docker.Client) {
	containers, err := listContainers(dockerClient)
	g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))

	for _, c := range containers {
		err = stopContainer(dockerClient, c.ID, "", 0)
		if err != nil {
			log.Error("fixture_test: stopContainer failed", "container", c.ID, "err", err)
		}
	}
}

func newDb(g *G) *v1.SqlDb {
	sqlDb, err := v1.NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=MEMORY&mode=rwc")
	g.Assert(err == nil).IsTrue(fmt.Sprintf("NewSqlDb err != nil: %v", err))
	err = sqlDb.Migrate()
	g.Assert(err == nil).IsTrue(fmt.Sprintf("sqlDb.Migrate err != nil: %v", err))
	err = sqlDb.DeleteAll()
	g.Assert(err == nil).IsTrue(fmt.Sprintf("sqlDb.DeleteAll err != nil: %v", err))
	defaultComponent.Version = 0
	_, err = sqlDb.PutComponent(defaultComponent)
	g.Assert(err == nil).IsTrue(fmt.Sprintf("sqlDb.PutComponent err != nil: %v", err))
	defaultComponent.Version = 1
	return sqlDb
}

func newFixture(g *G, dockerClient *docker.Client, sqlDb *v1.SqlDb) *Fixture {
	successfulReqs := int64(0)
	resolver := NewDbResolver(sqlDb)
	hFactory, err := NewDockerHandlerFactory(dockerClient, resolver)
	g.Assert(err == nil).IsTrue(fmt.Sprintf("NewDockerHandlerFactory err != nil: %v", err))

	return &Fixture{
		g:              g,
		dockerClient:   dockerClient,
		successfulReqs: &successfulReqs,
		v1Impl:         v1.NewV1(sqlDb, nil),
		hFactory:       hFactory,
		component:      defaultComponent,
		asyncReqWG:     &sync.WaitGroup{},
	}
}

type Fixture struct {
	g                *G
	dockerClient     *docker.Client
	component        v1.Component
	hFactory         *DockerHandlerFactory
	v1Impl           *v1.V1
	nextContainerId  string
	beforeContainers []types.Container
	successfulReqs   *int64
	asyncReqWG       *sync.WaitGroup
}

func GivenNoMaelstromContainers(g *G, dockerClient *docker.Client) *Fixture {
	stopMaelstromContainers(g, dockerClient)
	f := newFixture(g, dockerClient, newDb(g))
	return f
}

func GivenExistingContainer(g *G, dockerClient *docker.Client) *Fixture {
	sqlDb := newDb(g)
	containerId, err := startContainer(dockerClient, defaultComponent)

	f := newFixture(g, dockerClient, sqlDb)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("startContainer err != nil: %v", err))
	f.nextContainerId = containerId

	containers, err := listContainers(dockerClient)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))
	f.beforeContainers = containers

	return f
}

func GivenExistingContainerWithIdleTimeout(g *G, dockerClient *docker.Client, seconds int64) *Fixture {
	defaultComponent.Docker.IdleTimeoutSeconds = seconds
	return GivenExistingContainer(g, dockerClient)
}

func GivenExistingContainerWithBadHealthCheckPath(g *G, dockerClient *docker.Client) *Fixture {
	defaultComponent.Docker.HttpHealthCheckPath = "/fail"
	defaultComponent.Docker.HttpStartHealthCheckSeconds = -1
	return GivenExistingContainer(g, dockerClient)
}

func (f *Fixture) makeHttpRequest(url string) *httptest.ResponseRecorder {
	h, err := f.hFactory.GetHandlerAndRegisterRequest(f.component)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("hFactory.ForComponent err != nil: %v", err))
	if err == nil {
		lh, ok := h.(*LocalHandler)
		if ok && lh != nil {
			cid := lh.containerId
			if len(cid) > 8 {
				cid = lh.containerId[0:8]
			}
			req, err := http.NewRequest("GET", url, nil)
			rw := httptest.NewRecorder()
			f.g.Assert(err == nil).IsTrue(fmt.Sprintf("http.NewRequest err != nil: %v", err))
			lh.ServeHTTP(rw, req)
			return rw
		} else {
			f.g.Fail(fmt.Sprintf("GetHandler type assert failed for *LocalHandler: %+v", h))
		}
		return nil
	}
	return nil
}

func (f *Fixture) WhenHTTPRequestReceived() *Fixture {
	f.makeHttpRequest("http://127.0.0.1:12345/")
	return f
}

func (f *Fixture) WhenContainerIsHealthy() *Fixture {
	timeout := time.Now().Add(time.Second * 30)
	for {
		if time.Now().After(timeout) {
			f.g.Fail("Container never became healthy")
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
	f.hFactory.stopHandlerByComponent(f.component)
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

func (f *Fixture) WhenComponentIsUpdated() *Fixture {
	f.hFactory.OnComponentNotification(v1.ComponentNotification{
		PutComponent: &v1.PutComponentOutput{
			Name:    f.component.Name,
			Version: f.component.Version + 1,
		},
	})
	return f
}

func (f *Fixture) ThenContainerIsStarted() *Fixture {
	f.g.Assert(f.testImageContainerExists()).IsTrue("testImage container is not running!")
	return f
}

func (f *Fixture) ThenContainerIsStartedWithNewVersion() *Fixture {
	f.g.Assert(f.componentContainerExists()).IsTrue("component container is not running!")
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
	f.g.Fail("testImage container is running!")
	return f
}

func (f *Fixture) ThenNoNewContainerStarted() *Fixture {
	containers, err := listContainers(f.dockerClient)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))
	f.g.Assert(containers).Eql(f.beforeContainers)
	return f
}

func (f *Fixture) ThenSuccessfulRequestCountEquals(x int) *Fixture {
	f.asyncReqWG.Wait()
	f.g.Assert(int64(x)).Eql(*f.successfulReqs)
	return f
}

func (f *Fixture) testImageContainerExists() bool {
	containers, err := listContainers(f.dockerClient)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))

	for _, c := range containers {
		if c.Image == testImageName {
			return true
		}
	}
	return false
}

func (f *Fixture) componentContainerExists() bool {
	containers, err := listContainers(f.dockerClient)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))

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
