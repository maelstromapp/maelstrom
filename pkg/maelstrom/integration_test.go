package maelstrom

import (
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"testing"
	"time"
)

func TestHandlerStartsContainerOnFirstRequest(t *testing.T) {
	wrapTest(t, func() {
		GivenNoMaelstromContainers(t).
			WhenHTTPRequestReceived().
			ThenContainerIsStarted()
	})
}

func TestRemovesExistingContainersAtStartup(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenSystemIsStarted().
			ThenContainerIsStopped()
	})
}

func TestHealthCheckStopsContainerOnFailure(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainerWithBadHealthCheckPath(t).
			WhenHealthCheckTimeoutElapses().
			ThenContainerIsStopped()
	})
}

func TestHealthCheckKeepsContainerOnSuccess(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenHTTPRequestReceived().
			WhenHealthCheckTimeoutElapses().
			ThenContainerIsStarted()
	})
}

func TestStopDrainsRequestsBeforeStoppingContainers(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenContainerIsHealthy().
			WhenNLongRunningRequestsMade(5).
			WhenStopRequestReceived().
			ThenContainerIsStopped().
			ThenSuccessfulRequestCountEquals(5)
	})
}

func TestRestartsContainerIfRequestArrivesAfterStopping(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenStopRequestReceived().
			ThenContainerIsStopped().
			WhenHTTPRequestReceived().
			ThenContainerIsStarted()
	})
}

func TestStopsContainerIfIdle(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainerWithIdleTimeout(t, 1).
			WhenIdleTimeoutElapses().
			WhenAutoscaleRuns().
			ThenContainerIsStopped()
	})
}

func TestRestartsContainerWhenComponentUpdated(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenComponentIsUpdated().
			WhenHTTPRequestReceived().
			ThenContainerIsStartedWithNewVersion()
	})
}

//func TestOptionallyStartsThenStopsWhenComponentUpdated(t *testing.T) {
//	wrapTest(t, func() {
//		GivenExistingContainerWith(t, func(c *v1.Component) {
//			c.RestartOrder = v1.RestartOrderStartstop
//		}).
//			WhenComponentIsUpdated().
//			WhenHTTPRequestReceived().
//			ThenContainerIsStartedBeforeTheOlderContainerIsStopped()
//	})
//}

func TestOptionallyStopsThenStartsWhenComponentUpdated(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainerWith(t, func(c *v1.Component) {
			c.RestartOrder = v1.RestartOrderStopstart
		}).
			WhenComponentIsUpdated().
			WhenHTTPRequestReceived().
			ThenContainerIsStoppedBeforeTheOlderContainerIsStarted()
	})
}

//func TestOptionallyLockWhenComponentUpdated(t *testing.T) {
//	wrapTest(t, func() {
//		GivenExistingContainerWith(t, func(c *v1.Component) {
//			c.StartParallelism = v1.StartParallelismSeriesfirst
//		}).
//			WhenAnotherInstanceIsStarted().
//			AndDockerEventsAreReset().
//			WhenComponentIsUpdated().
//			WhenHTTPRequestReceived().
//			AndTimePasses(2 * time.Second).
//			ThenContainersAreRestartedInSeries()
//	})
//}

func TestRoutesRequestsToOldComponentDuringUpdates(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainerWith(t, func(c *v1.Component) {
			c.RestartOrder = v1.RestartOrderStartstop
		}).
			WhenHTTPRequestReceived().
			ThenContainerIsStarted().
			WhenHTTPRequestsAreMadeContinuously().
			WhenComponentIsUpdated().
			AndTimePasses(2 * time.Second).
			ThenAllHTTPRequestsCompletedWithoutDelay()
	})
}
