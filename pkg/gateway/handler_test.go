package gateway

import (
	docker "github.com/docker/docker/client"
	. "github.com/franela/goblin"
	"testing"
	"time"
)

func TestLocalHandler(t *testing.T) {
	g := Goblin(t)

	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		panic(err)
	}

	g.Describe("Local Handler", func() {

		g.AfterEach(func() {
			stopMaelstromContainers(g, dockerClient)
		})

		g.It("Starts container on first request", func() {
			GivenLocalHandlerAndNoMaelstromContainers(g, dockerClient).
				WhenHTTPRequestReceived().
				ThenContainerIsStarted()
		})
		g.It("Uses existing container if already started", func() {
			GivenExistingContainer(g, dockerClient).
				WhenLocalHandlerCreated().
				ThenNoNewContainerStarted()
		})
		g.It("Health check stops container on failure", func() {
			GivenExistingContainer(g, dockerClient).
				WhenLocalHandlerCreated().
				WhenHealthCheckFails().
				ThenContainerIsStopped()
		})
		g.It("Health check keeps container on success", func() {
			GivenExistingContainer(g, dockerClient).
				WhenLocalHandlerCreated().
				WhenHealthCheckPasses().
				ThenContainerIsStarted()
		})
		g.It("Stop drains requests in flight before stopping containers", func() {
			g.Timeout(time.Minute)
			GivenExistingContainer(g, dockerClient).
				WhenLocalHandlerCreated().
				WhenContainerIsHealthy().
				WhenNLongRunningRequestsMade(5).
				WhenStopRequestReceived().
				ThenContainerIsStopped().
				ThenSuccessfulRequestCountEquals(5)
		})
		g.It("Restarts container if request arrives after stopping", func() {
			GivenExistingContainer(g, dockerClient).
				WhenLocalHandlerCreated().
				WhenStopRequestReceived().
				ThenContainerIsStopped().
				WhenHTTPRequestReceived().
				ThenContainerIsStarted()
		})
	})

}
