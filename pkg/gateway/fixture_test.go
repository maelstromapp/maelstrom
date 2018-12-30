package gateway

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	docker "github.com/docker/docker/client"
	. "github.com/franela/goblin"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

// Test HTTP docker image. Has endpoints that return 200, 500, and sleep
// See: https://github.com/coopernurse/go-hello-http
const testImageName = "docker.io/coopernurse/go-hello-http:latest"

func stopMaelstromContainers(g *G, dockerClient *docker.Client) {
	containers, err := listContainers(dockerClient)
	g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))

	for _, c := range containers {
		err = dockerClient.ContainerStop(context.Background(), c.ID, nil)
		g.Assert(err == nil).IsTrue(fmt.Sprintf("ContainerStop failed for %s: %v", c.ID, err))

		err = dockerClient.ContainerRemove(context.Background(), c.ID, types.ContainerRemoveOptions{})
		g.Assert(err == nil).IsTrue(fmt.Sprintf("ContainerRemove failed for %s: %v", c.ID, err))
	}
}

func newFixture(g *G, dockerClient *docker.Client) *Fixture {
	successfulReqs := int64(0)
	return &Fixture{
		g:              g,
		dockerClient:   dockerClient,
		successfulReqs: &successfulReqs,
		component: v1.Component{
			Version: 1,
			Name:    "maeltest",
			Docker: &v1.DockerComponent{
				HttpPort:            8080,
				Image:               testImageName,
				HttpHealthCheckPath: "/",
			},
		},
		asyncReqWG: &sync.WaitGroup{},
	}
}

type Fixture struct {
	g                *G
	dockerClient     *docker.Client
	component        v1.Component
	h                *LocalHandler
	nextContainerId  string
	beforeContainers []types.Container
	successfulReqs   *int64
	asyncReqWG       *sync.WaitGroup
}

func GivenLocalHandlerAndNoMaelstromContainers(g *G, dockerClient *docker.Client) *Fixture {
	stopMaelstromContainers(g, dockerClient)
	f := newFixture(g, dockerClient)

	h, err := NewLocalHandler(f.dockerClient, f.component, "")
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("NewLocalHandler err != nil: %v", err))
	f.h = h
	return f
}

func GivenExistingContainer(g *G, dockerClient *docker.Client) *Fixture {
	f := newFixture(g, dockerClient)
	containerId, err := startContainer(dockerClient, f.component)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("startContainer err != nil: %v", err))
	f.nextContainerId = containerId

	containers, err := listContainers(dockerClient)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("listContainers err != nil: %v", err))
	f.beforeContainers = containers

	return f
}

func (f *Fixture) WithIdleTimeoutSeconds(seconds int64) *Fixture {
	f.component.Docker.IdleTimeoutSeconds = seconds
	return f
}

func (f *Fixture) WhenLocalHandlerCreated() *Fixture {
	h, err := NewLocalHandler(f.dockerClient, f.component, f.nextContainerId)
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("NewLocalHandler err != nil: %v", err))
	f.h = h
	f.nextContainerId = ""
	return f
}

func (f *Fixture) WhenHTTPRequestReceived() *Fixture {
	req, err := http.NewRequest("GET", "http://127.0.0.1:12345/", nil)
	rw := httptest.NewRecorder()
	f.g.Assert(err == nil).IsTrue(fmt.Sprintf("http.NewRequest err != nil: %v", err))
	f.h.ServeHTTP(rw, req)
	return f
}

func (f *Fixture) WhenHealthCheckFails() *Fixture {
	f.h.component.Docker.HttpHealthCheckPath = "/fail"
	f.h.runHealthCheck()
	f.h.component.Docker.HttpHealthCheckPath = "/"
	return f
}

func (f *Fixture) WhenHealthCheckPasses() *Fixture {
	f.h.component.Docker.HttpHealthCheckPath = "/"
	f.h.runHealthCheck()
	return f
}

func (f *Fixture) WhenContainerIsHealthy() *Fixture {
	timeout := time.Now().Add(time.Second * 30)
	for {
		if time.Now().After(timeout) {
			f.g.Fail("Container never became healthy")
			break
		} else {
			req, err := http.NewRequest("GET", "http://127.0.0.1:12345/", nil)
			rw := httptest.NewRecorder()
			f.g.Assert(err == nil).IsTrue(fmt.Sprintf("http.NewRequest err != nil: %v", err))
			f.h.ServeHTTP(rw, req)
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
			log.Printf("requesting /sleep")
			req, err := http.NewRequest("GET", "http://127.0.0.1:12345/sleep?seconds=5", nil)
			rw := httptest.NewRecorder()
			f.g.Assert(err == nil).IsTrue(fmt.Sprintf("http.NewRequest err != nil: %v", err))
			start := time.Now()
			f.h.ServeHTTP(rw, req)
			log.Printf("done requesting /sleep - status: %d response: %s", rw.Result().StatusCode, rw.Body.String())
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
	f.h.stop()
	return f
}

func (f *Fixture) WhenIdleTimeoutElapses() *Fixture {
	time.Sleep(time.Second * time.Duration(f.h.component.Docker.IdleTimeoutSeconds+1))
	return f
}

func (f *Fixture) ThenContainerIsStarted() *Fixture {
	f.g.Assert(f.testImageContainerExists()).IsTrue("testImage container is not running!")
	return f
}

func (f *Fixture) ThenContainerIsStopped() *Fixture {
	f.g.Assert(f.testImageContainerExists()).IsFalse("testImage container is running!")
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
