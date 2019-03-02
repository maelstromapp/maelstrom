package gateway

import (
	"fmt"
	docker "github.com/docker/docker/client"
	. "github.com/franela/goblin"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestLocalHandler(t *testing.T) {
	g := Goblin(t)

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

	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		panic(err)
	}

	g.Describe("Local Handler", func() {

		g.BeforeEach(func() {
			resetDefaults()
		})

		g.AfterEach(func() {
			stopMaelstromContainers(g, dockerClient)
		})

		g.It("Starts container on first request", func() {
			g.Timeout(time.Second * 10)
			GivenNoMaelstromContainers(g, dockerClient).
				WhenHTTPRequestReceived().
				ThenContainerIsStarted()
		})
		g.It("Uses existing container if already started", func() {
			GivenExistingContainer(g, dockerClient).
				WhenHTTPRequestReceived().
				ThenNoNewContainerStarted()
		})
		g.It("Health check stops container on failure", func() {
			GivenExistingContainerWithBadHealthCheckPath(g, dockerClient).
				WhenHealthCheckTimeoutElapses().
				ThenContainerIsStopped()
		})
		g.It("Health check keeps container on success", func() {
			GivenExistingContainer(g, dockerClient).
				WhenHTTPRequestReceived().
				WhenHealthCheckTimeoutElapses().
				ThenContainerIsStarted()
		})
		g.It("Stop drains requests in flight before stopping containers", func() {
			g.Timeout(time.Second * 10)
			GivenExistingContainer(g, dockerClient).
				WhenContainerIsHealthy().
				WhenNLongRunningRequestsMade(5).
				WhenStopRequestReceived().
				ThenContainerIsStopped().
				ThenSuccessfulRequestCountEquals(5)
		})
		g.It("Restarts container if request arrives after stopping", func() {
			g.Timeout(time.Second * 10)
			GivenExistingContainer(g, dockerClient).
				WhenStopRequestReceived().
				ThenContainerIsStopped().
				WhenHTTPRequestReceived().
				ThenContainerIsStarted()
		})
		g.It("Stops container if idle", func() {
			GivenExistingContainerWithIdleTimeout(g, dockerClient, 1).
				WhenIdleTimeoutElapses().
				ThenContainerIsStopped()
		})
		g.It("Stops container when component updated", func() {
			GivenExistingContainer(g, dockerClient).
				WhenComponentIsUpdated().
				ThenContainerIsStopped().
				WhenHTTPRequestReceived().
				ThenContainerIsStartedWithNewVersion()
		})
	})

}
