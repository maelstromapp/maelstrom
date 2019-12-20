package maelstrom

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/sqs"
	linuxproc "github.com/c9s/goprocinfo/linux"
	"github.com/coopernurse/barrister-go"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	"github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"math/rand"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const rolePlacement = "placement"
const roleAutoScale = "autoscale"
const roleCron = "cron"

type ShutdownFunc func()

type placeComponentResult struct {
	output *v1.PlaceComponentOutput
	err    error
}

func NewNodeServiceImplFromDocker(db db.Db, dockerClient *docker.Client, privatePort int,
	peerUrl string, totalMemAllowed int64, instanceId string, shutdownCh chan ShutdownFunc,
	awsSession *session.Session, terminateCommand string, pullState *component.PullState) (*NodeServiceImpl, error) {

	maelstromHost, err := common.ResolveMaelstromHost(dockerClient)
	if err != nil {
		return nil, err
	}
	maelstromUrl := fmt.Sprintf("http://%s:%d", maelstromHost, privatePort)

	info, err := dockerClient.Info(context.Background())
	if err != nil {
		return nil, err
	}
	nodeId := info.ID

	nodeSvc := &NodeServiceImpl{
		dispatcher:       nil,
		db:               db,
		nodeId:           nodeId,
		peerUrl:          peerUrl,
		instanceId:       instanceId,
		totalMemAllowed:  totalMemAllowed,
		startTimeMillis:  common.TimeToMillis(time.Now()),
		numCPUs:          int64(info.NCPU),
		loadStatusLock:   &sync.Mutex{},
		placeCompLock:    &sync.Mutex{},
		placeCompWaiters: make(map[string][]chan placeComponentResult),
		shutdownCh:       shutdownCh,
		awsSession:       awsSession,
		terminateCommand: terminateCommand,
	}

	compLock := NewCompLocker(db, nodeId)

	log.Info("maelstromd: creating dispatcher", "maelstromUrl", maelstromUrl)
	dispatcher, err := component.NewDispatcher(nodeSvc, dockerClient, maelstromUrl, nodeId, pullState,
		compLock.startLockAcquire, compLock.postStartContainer, nodeSvc.OnContainersChanged)
	if err != nil {
		return nil, err
	}
	nodeSvc.dispatcher = dispatcher

	nodeSvc.cluster = NewCluster(nodeId, nodeSvc)
	nodeSvc.cluster.AddObserver(nodeSvc.dispatcher)
	_, err = nodeSvc.resolveAndBroadcastNodeStatus(context.Background())
	if err != nil {
		return nil, err
	}
	return nodeSvc, nil
}

type NodeServiceImpl struct {
	dispatcher *component.Dispatcher
	db         db.Db
	cluster    *Cluster
	// nodeId is the maelstrom node id used to uniquely identify this node in the cluster
	// it is currently the docker node id and is derived from the docker daemon at startup
	nodeId string
	// instanceId is an optional string used to identify the host machine
	// this is typically an identifier set by the cloud provider (e.g. AWS EC2 Instance ID)
	instanceId       string
	peerUrl          string
	totalMemAllowed  int64
	startTimeMillis  int64
	numCPUs          int64
	loadStatusLock   *sync.Mutex
	placeCompLock    *sync.Mutex
	placeCompWaiters map[string][]chan placeComponentResult
	shutdownCh       chan ShutdownFunc
	awsSession       *session.Session
	terminateCommand string

	// if node.observedAt is older than this duration we'll consider it stale and remove it
	NodeLiveness time.Duration
}

func (n *NodeServiceImpl) Dispatcher() *component.Dispatcher {
	return n.dispatcher
}

func (n *NodeServiceImpl) NodeId() string {
	return n.nodeId
}

func (n *NodeServiceImpl) Cluster() *Cluster {
	return n.cluster
}

func (n *NodeServiceImpl) LogPairs() []interface{} {
	return []interface{}{"nodeId", n.nodeId, "peerUrl", n.peerUrl, "numCPUs", n.numCPUs}
}

func (n *NodeServiceImpl) ListNodeStatus(input v1.ListNodeStatusInput) (v1.ListNodeStatusOutput, error) {
	var nodes []v1.NodeStatus
	var err error
	if input.ForceRefresh {
		nodes, err = n.refreshNodes()
		if err != nil {
			code := MiscError
			msg := "nodesvc: ListNodeStatus - error refreshing node status"
			log.Error(msg, "code", code, "err", err)
			return v1.ListNodeStatusOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
		}
	} else {
		nodes = n.cluster.GetNodes()
	}
	sort.Sort(NodeStatusByStartedAt(nodes))
	return v1.ListNodeStatusOutput{RespondingNodeId: n.nodeId, Nodes: nodes}, nil
}

func (n *NodeServiceImpl) refreshNodes() ([]v1.NodeStatus, error) {
	type nodeStatusOrError struct {
		Node  v1.NodeStatus
		Error error
	}
	type nodeStatusListOrError struct {
		Nodes []v1.NodeStatus
		Error error
	}
	singleNodeChan := make(chan nodeStatusOrError)
	finalResultChan := make(chan nodeStatusListOrError)

	go func() {
		nodes := make([]v1.NodeStatus, 0)
		var err error
		for res := range singleNodeChan {
			if res.Error == nil {
				nodes = append(nodes, res.Node)
				n.cluster.SetNode(res.Node)
			} else {
				err = res.Error
			}
		}
		if err == nil {
			finalResultChan <- nodeStatusListOrError{Nodes: nodes}
		} else {
			finalResultChan <- nodeStatusListOrError{Error: err}
		}
	}()

	wg := &sync.WaitGroup{}
	for _, node := range n.cluster.GetNodes() {
		wg.Add(1)
		go func(node v1.NodeStatus) {
			nodeSvc := n.cluster.GetNodeServiceWithTimeout(node, 30*time.Second)
			out, err := nodeSvc.GetStatus(v1.GetNodeStatusInput{})
			if err == nil {
				singleNodeChan <- nodeStatusOrError{Node: out.Status}
			} else {
				singleNodeChan <- nodeStatusOrError{Error: err}
			}
			wg.Done()
		}(node)
	}
	wg.Wait()
	close(singleNodeChan)

	res := <-finalResultChan
	return res.Nodes, res.Error
}

func (n *NodeServiceImpl) GetStatus(input v1.GetNodeStatusInput) (v1.GetNodeStatusOutput, error) {
	status, err := n.resolveNodeStatus(context.Background())
	if err != nil {
		code := MiscError
		msg := "nodesvc: GetStatus - error loading node status"
		log.Error(msg, "code", code, "err", err)
		return v1.GetNodeStatusOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
	}
	return v1.GetNodeStatusOutput{Status: status}, nil
}

func (n *NodeServiceImpl) StatusChanged(input v1.StatusChangedInput) (v1.StatusChangedOutput, error) {
	if input.Exiting {
		n.cluster.RemoveNode(input.NodeId)
	} else {
		n.cluster.SetNode(*input.Status)
	}
	return v1.StatusChangedOutput{NodeId: input.NodeId}, nil
}

func (n *NodeServiceImpl) PlaceComponent(input v1.PlaceComponentInput) (v1.PlaceComponentOutput, error) {
	// determine if we're the placement node
	input.ComponentName = strings.ToLower(input.ComponentName)
	acquired := false
	deadline := time.Now().Add(70 * time.Second)
	for !acquired && time.Now().Before(deadline) {
		roleOk, roleNode, err := n.db.AcquireOrRenewRole(rolePlacement, n.nodeId, time.Minute)
		if err != nil {
			log.Warn("nodesvc: db.AcquireOrRenewRole error", "component", input.ComponentName, "role", rolePlacement,
				"err", err)
		} else {
			acquired = roleOk
			if !roleOk {
				if roleNode == n.nodeId {
					log.Warn("nodesvc: db.AcquireOrRenewRole returned false, but also returned our nodeId - will retry",
						"component", input.ComponentName, "role", rolePlacement, "node", n.nodeId)
				} else {
					peerSvc := n.cluster.GetNodeServiceById(roleNode)
					if peerSvc == nil {
						log.Warn("nodesvc: PlaceComponent can't find peer node - will retry",
							"component", input.ComponentName, "peerNode", roleNode)
					} else {
						// delegate request to peer
						return peerSvc.PlaceComponent(input)
					}
				}
			}
		}
		if !acquired {
			time.Sleep(time.Duration(rand.Intn(3000)+2000) * time.Millisecond)
		}
	}

	if !acquired {
		msg := "nodesvc: timeout trying to acquire or delegate placement"
		log.Error(msg, "component", input.ComponentName, "role", rolePlacement)
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(DbError), Message: msg}
	}
	defer logErr(n.db.ReleaseRole(rolePlacement, n.nodeId), "release "+rolePlacement+" for nodeId: "+n.nodeId)

	var waitCh chan placeComponentResult

	n.placeCompLock.Lock()
	waiters := n.placeCompWaiters[input.ComponentName]
	if waiters == nil {
		// no waiters yet - create slice so future callers will queue here
		n.placeCompWaiters[input.ComponentName] = make([]chan placeComponentResult, 0)
	} else {
		// waiter queue exists - add to slice and wait
		waitCh = make(chan placeComponentResult, 1)
		n.placeCompWaiters[input.ComponentName] = append(waiters, waitCh)
	}
	n.placeCompLock.Unlock()

	if waitCh == nil {
		out, err := n.placeComponentInternal(input)
		res := placeComponentResult{err: err}
		if err == nil {
			res.output = &out
		}
		n.placeCompLock.Lock()
		for _, waitCh := range n.placeCompWaiters[input.ComponentName] {
			waitCh <- res
		}
		delete(n.placeCompWaiters, input.ComponentName)
		n.placeCompLock.Unlock()
		return out, err
	} else {
		// placement is in progress - wait for completion
		return waitForPlacement(waitCh)
	}
}

func waitForPlacement(waitCh chan placeComponentResult) (v1.PlaceComponentOutput, error) {
	select {
	case <-time.After(time.Minute * 3):
		// timeout
		return v1.PlaceComponentOutput{}, fmt.Errorf("nodesvc: timeout waiting for placement")
	case res := <-waitCh:
		if res.err == nil {
			return *res.output, nil
		}
		return v1.PlaceComponentOutput{}, res.err
	}
}

func (n *NodeServiceImpl) placeComponentInternal(input v1.PlaceComponentInput) (v1.PlaceComponentOutput, error) {
	// get component
	comp, err := n.db.GetComponent(input.ComponentName)
	if err == db.NotFound {
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{
			Code:    1003,
			Message: "No Component found with name: " + input.ComponentName}
	} else if err != nil {
		code := MiscError
		msg := "nodesvc: PlaceComponent:GetComponent error"
		log.Error(msg, "component", input.ComponentName, "code", code, "err", err)
		return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
	}

	requiredRAM := comp.Docker.ReserveMemoryMiB
	if requiredRAM <= 0 {
		requiredRAM = 128
	}

	startTime := time.Now()
	deadline := startTime.Add(time.Minute * 3)
	for time.Now().Before(deadline) {
		placedNode, retry := n.placeComponentTryOnce(input.ComponentName, requiredRAM)
		if placedNode != nil {
			log.Info("nodesvc: PlaceComponent successful", "elapsed", time.Now().Sub(startTime).String(),
				"component", input.ComponentName, "clientNode", common.TruncNodeId(n.nodeId),
				"placedNode", common.TruncNodeId(placedNode.NodeId))
			return v1.PlaceComponentOutput{
				ComponentName: input.ComponentName,
				Node:          *placedNode,
			}, nil
		}
		if !retry {
			code := MiscError
			msg := "nodesvc: PlaceComponent:placeComponent error"
			log.Error(msg, "component", input.ComponentName, "code", code)
			return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
		}
		sleepDur := time.Millisecond * time.Duration(rand.Intn(3000))
		log.Warn("nodesvc: PlaceComponent:placeComponent - will retry",
			"component", input.ComponentName, "nodeId", n.nodeId, "sleep", sleepDur)
		time.Sleep(sleepDur)
	}
	code := MiscError
	msg := "nodesvc: PlaceComponent deadline reached - component not started"
	log.Error(msg, "component", input.ComponentName, "code", code)
	return v1.PlaceComponentOutput{}, &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func (n *NodeServiceImpl) placeComponentTryOnce(componentName string, requiredRAM int64) (*v1.NodeStatus, bool) {
	// filter nodes to subset whose total ram is > required
	nodes := make([]v1.NodeStatus, 0)

	// also look for components that may already be running this component
	nodesWithComponent := make([]v1.NodeStatus, 0)

	// track largest RAM found
	maxNodeRAM := int64(0)
	for _, n := range n.cluster.GetNodes() {
		if n.TotalMemoryMiB > requiredRAM {
			nodes = append(nodes, n)
		}
		for _, c := range n.RunningComponents {
			if c.ComponentName == componentName {
				nodesWithComponent = append(nodesWithComponent, n)
			}
		}
		if n.TotalMemoryMiB > maxNodeRAM {
			maxNodeRAM = n.TotalMemoryMiB
		}
	}

	// if we found any candidates, contact them and verify
	for _, node := range nodesWithComponent {
		output, err := n.cluster.GetNodeService(node).GetStatus(v1.GetNodeStatusInput{})
		if err == nil {
			n.cluster.SetNode(output.Status)
			for _, c := range output.Status.RunningComponents {
				if c.ComponentName == componentName {
					return &output.Status, false
				}
			}
		} else {
			log.Error("nodesvc: PlaceComponent:GetStatus failed", "component", componentName,
				"clientNode", n.nodeId, "remoteNode", node.NodeId, "peerUrl", node.PeerUrl, "err", err)
		}
	}

	// fail if component too large to place on any node
	if len(nodes) == 0 {
		log.Error("nodesvc: PlaceComponent failed - component RAM larger than max node RAM",
			"component", componentName, "requiredRAM", requiredRAM, "nodeMaxRAM", maxNodeRAM)
		return nil, false
	}

	option := BestStartComponentOption(newPlacementOptionsByNodeId(nodes), componentName, requiredRAM, true)
	if option == nil {
		log.Error("nodesvc: PlaceComponent failed - BestPlacementOption returned nil",
			"component", componentName, "requiredRAM", requiredRAM, "nodeMaxRAM", maxNodeRAM, "nodeCount", len(nodes))
		return nil, false
	}

	// try first option
	node := option.TargetNode
	option.Input.ClientNodeId = n.nodeId

	output, err := n.cluster.GetNodeService(*node).StartStopComponents(*option.Input)

	if output.TargetStatus != nil {
		n.cluster.SetNode(*output.TargetStatus)
	}

	if err == nil {
		if output.TargetVersionMismatch && output.TargetStatus != nil {
			log.Warn("nodesvc: target version mismatch. component not started.", "req", option.Input,
				"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId)
		} else if len(output.Errors) == 0 {
			comp, err := n.db.GetComponent(componentName)
			if err != nil {
				log.Error("nodesvc: placeComponentTryOnce GetComponent error", "component", componentName, "err", err)
				return nil, false
			}
			updatedStatus, running := n.waitUntilComponentRunning(*node, output.TargetStatus, comp)
			if running {
				// Success
				n.cluster.SetNode(*updatedStatus)
				return output.TargetStatus, false
			}
			log.Warn("nodesvc: started component, but node doesn't report it running",
				"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId,
				"status", output.TargetStatus)
		} else {
			// some aspect of placement failed. we'll retry with any updated cluster state
			log.Error("nodesvc: error in StartStopComponents", "errors", output.Errors,
				"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId)
		}
	} else {
		log.Error("nodesvc: error in StartStopComponents", "err", err,
			"component", componentName, "clientNode", n.nodeId, "remoteNode", node.NodeId)
	}

	// retry
	return nil, true
}

func (n *NodeServiceImpl) waitUntilComponentRunning(node v1.NodeStatus, status *v1.NodeStatus,
	comp v1.Component) (*v1.NodeStatus, bool) {
	seconds := healthCheckSeconds(comp.Docker)
	deadline := time.Now().Add(time.Second * time.Duration(seconds))
	for time.Now().Before(deadline) {
		for _, c := range status.RunningComponents {
			if c.ComponentName == comp.Name {
				return status, true
			}
		}
		time.Sleep(time.Second)
		output, err := n.cluster.GetNodeService(node).GetStatus(v1.GetNodeStatusInput{})
		if err != nil {
			log.Error("nodesvc: error getting status", "remoteNode", common.TruncNodeId(node.NodeId), "err", err)
			return status, false
		} else {
			status = &output.Status
		}
	}
	return status, false
}

func (n *NodeServiceImpl) autoscale() {
	roleOk, _, err := n.db.AcquireOrRenewRole(roleAutoScale, n.nodeId, time.Minute)
	if err != nil {
		log.Error("nodesvc: autoscale AcquireOrRenewRole error", "role", roleAutoScale, "err", err)
		return
	}
	if !roleOk {
		return
	}

	nodes := n.cluster.GetNodes()
	componentsByName, err := loadActiveComponents(nodes, n.db)
	if err != nil {
		log.Error("nodesvc: autoscale loadActiveComponents error", "err", err)
		return
	}

	inputs := CalcAutoscalePlacement(nodes, componentsByName)

	for _, input := range inputs {
		output, err := n.cluster.GetNodeService(*input.TargetNode).StartStopComponents(*input.Input)
		if err == nil {
			log.Info("autoscale: StartStopComponents success",
				"targetNode", common.TruncNodeId(input.TargetNode.NodeId), "targetCounts", input.Input.TargetCounts)
			if output.TargetStatus != nil {
				n.cluster.SetNode(*output.TargetStatus)
			}
		} else {
			log.Error("autoscale: StartStopComponents failed", "err", err,
				"targetNode", common.TruncNodeId(input.TargetNode.NodeId), "input", input)
		}
	}
}

func (n *NodeServiceImpl) StartStopComponents(input v1.StartStopComponentsInput) (v1.StartStopComponentsOutput, error) {
	scaleTargets := make([]component.ScaleTarget, len(input.TargetCounts))
	for i, tc := range input.TargetCounts {
		comp, err := n.db.GetComponent(tc.ComponentName)
		if err != nil {
			return v1.StartStopComponentsOutput{},
				rpcErr(err, MiscError, "nodesvc: GetComponent failed for: "+tc.ComponentName)
		}
		scaleTargets[i] = component.ScaleTarget{
			Component:         &comp,
			TargetCount:       tc.TargetCount,
			RequiredMemoryMiB: tc.RequiredMemoryMiB,
		}
	}

	scaleOut := n.dispatcher.Scale(&component.ScaleInput{
		TargetVersion: input.TargetVersion,
		TargetCounts:  scaleTargets,
	})

	status, err := n.resolveNodeStatus(context.Background())
	if err != nil {
		return v1.StartStopComponentsOutput{},
			rpcErr(err, MiscError, "nodesvc: StartStopComponents:resolveNodeStatus failed")
	}

	return v1.StartStopComponentsOutput{
		TargetVersionMismatch: scaleOut.TargetVersionMismatch,
		TargetStatus:          &status,
		Started:               scaleOut.Started,
		Stopped:               scaleOut.Stopped,
		Errors:                scaleOut.Errors,
	}, nil
}

func (n *NodeServiceImpl) OnContainersChanged() {
	_, err := n.resolveAndBroadcastNodeStatus(context.Background())
	if err != nil {
		log.Error("nodesvc: OnContainersChanged error", "err", err)
	}
}

func (n NodeServiceImpl) TerminateNode(input v1.TerminateNodeInput) (v1.TerminateNodeOutput, error) {
	out := v1.TerminateNodeOutput{
		AcceptedMessage: false,
		NodeId:          n.nodeId,
		InstanceId:      n.instanceId,
	}
	if input.AwsLifecycleHook != nil && input.AwsLifecycleHook.InstanceId == n.instanceId {
		n.terminateSelfViaAwsHook(*input.AwsLifecycleHook)
	}
	return out, nil
}

func (n NodeServiceImpl) terminateSelfViaAwsHook(hook v1.AwsLifecycleHook) {
	log.Info("nodesvc: TerminateNode received - shutting down", "instanceId", n.instanceId, "nodeId", n.nodeId)

	// Delete the hook message from SQS
	sqsSvc := sqs.New(n.awsSession)
	_, err := sqsSvc.DeleteMessage(&sqs.DeleteMessageInput{
		QueueUrl:      aws.String(hook.QueueUrl),
		ReceiptHandle: aws.String(hook.MessageReceiptHandle),
	})
	if err != nil {
		log.Error("nodesvc: TerminateNode sqs delete message error", "err", err)
	}

	// Trigger a shutdown, passing in a callback that marks the autoscale hook complete
	n.shutdownCh <- func() {
		autoscaleSvc := autoscaling.New(n.awsSession)
		_, err := autoscaleSvc.CompleteLifecycleAction(&autoscaling.CompleteLifecycleActionInput{
			AutoScalingGroupName:  aws.String(hook.AutoScalingGroupName),
			InstanceId:            aws.String(hook.InstanceId),
			LifecycleActionToken:  aws.String(hook.LifecycleActionToken),
			LifecycleHookName:     aws.String(hook.LifecycleHookName),
			LifecycleActionResult: aws.String("CONTINUE"),
		})
		if err != nil {
			log.Error("nodesvc: CompleteAutoscalingLifecycleAction error", "err", err)
		}
		// continue running post-terminate command regardless of error since we're at point of no return
		n.runPostTerminateCommand()
	}
}

func (n NodeServiceImpl) runPostTerminateCommand() {
	// Run post-terminate command (typically "systemctl disable maelstromd")
	// This is useful to prevent systemd from re-spawning maelstrom after we exit
	if n.terminateCommand != "" {
		parts := strings.Split(n.terminateCommand, " ")
		cmd := exec.Command(parts[0], parts[1:]...)
		log.Info("nodesvc: running post-terminate command", "command", n.terminateCommand)
		err := cmd.Run()
		if err != nil {
			log.Error("nodesvc: post-terminate error", "err", err)
		}
	}
}

func (n *NodeServiceImpl) RunAwsAutoScaleTerminatePollerLoop(queueUrl string, maxAgeSeconds int,
	ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.Tick(30 * time.Second)
	roleId := "aws-terminate-poller"
	maxAgeDur := time.Second * time.Duration(maxAgeSeconds)
	for {
		select {
		case <-ticker:
			var hookMsgs []*AwsLifecycleHookMessage
			acquired, _, err := n.db.AcquireOrRenewRole(roleId, n.nodeId, 95*time.Second)
			if err == nil {
				if acquired {
					for {
						hookMsgs, err = n.pollAwsTerminateQueue(queueUrl, maxAgeDur)
						if err == nil {
							err = n.sendAwsTerminateMessages(hookMsgs)
						}
						if len(hookMsgs) == 0 {
							break
						}
					}
				}
			}
			if err != nil {
				log.Error("nodesvc: aws autoscale terminate poller failed", "err", err, "nodeId", n.nodeId, "queueUrl",
					queueUrl)
			}
		case <-ctx.Done():
			log.Info("nodesvc: aws autoscale terminate poller loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) RunAwsSpotTerminatePollerLoop(interval time.Duration, ctx context.Context,
	wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.Tick(interval)
	running := true
	for running {
		select {
		case <-ticker:
			if awsSpotInstanceTerminate() {
				log.Info("nodesvc: spot terminate received - shutting down", "instanceId", n.instanceId,
					"nodeId", n.nodeId)
				n.shutdownCh <- n.runPostTerminateCommand
				running = false
			}
		case <-ctx.Done():
			running = false
		}
	}
	log.Info("nodesvc: aws spot terminate poller loop shutdown gracefully")
}

func (n *NodeServiceImpl) sendAwsTerminateMessages(hookMsgs []*AwsLifecycleHookMessage) error {
	for _, msg := range hookMsgs {
		input := v1.TerminateNodeInput{AwsLifecycleHook: msg.ToAwsLifecycleHook()}
		out, err := n.TerminateNode(input)
		localAccepted := false
		if err == nil {
			localAccepted = out.AcceptedMessage
		} else {
			return errors.Wrap(err, "local TerminateNode failed")
		}
		if !localAccepted {
			n.cluster.BroadcastTerminationEvent(input)
		}
	}
	return nil
}

func (n *NodeServiceImpl) pollAwsTerminateQueue(queueUrl string,
	maxAge time.Duration) ([]*AwsLifecycleHookMessage, error) {
	sqsSvc := sqs.New(n.awsSession)
	out, err := sqsSvc.ReceiveMessage(&sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueUrl),
		MaxNumberOfMessages: aws.Int64(10),
		VisibilityTimeout:   aws.Int64(60),
		WaitTimeSeconds:     aws.Int64(2),
	})
	if err == nil {
		hookMsgs := []*AwsLifecycleHookMessage{}
		for _, msg := range out.Messages {
			var hook AwsLifecycleHookMessage
			err = json.Unmarshal([]byte(*msg.Body), &hook)
			if err != nil {
				return nil, errors.Wrap(err, "json.Unmarshal failed for AwsLifecycleHookMessage")
			}
			msgAge := hook.TryParseAge()
			if msgAge == nil || *msgAge > maxAge {
				log.Info("nodesvc: deleting stale lifecycle hook message", "time", hook.Time,
					"instance", hook.EC2InstanceId)
				_, err = sqsSvc.DeleteMessage(&sqs.DeleteMessageInput{
					QueueUrl:      aws.String(queueUrl),
					ReceiptHandle: msg.ReceiptHandle,
				})
				if err != nil {
					log.Error("nodesvc: error deleting stale lifecycle hook message", "err", err)
				}
			} else if hook.EC2InstanceId != "" {
				hook.QueueUrl = queueUrl
				hook.MessageReceiptHandle = *msg.ReceiptHandle
				hookMsgs = append(hookMsgs, &hook)
			}
		}
		return hookMsgs, nil
	} else {
		return nil, errors.Wrap(err, "aws terminate sqs ReceiveMessage failed")
	}
}

func (n *NodeServiceImpl) RunAutoscaleLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.Tick(interval)
	for {
		select {
		case <-ticker:
			n.autoscale()
		case <-ctx.Done():
			// release role (will no-op if we don't hold the role lock)
			logErr(n.db.ReleaseRole(roleAutoScale, n.nodeId), "release "+roleAutoScale+" for nodeId: "+n.nodeId)

			log.Info("nodesvc: autoscale loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) RunNodeStatusLoop(interval time.Duration, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	logTicker := time.Tick(interval)
	n.logStatusAndRefreshClusterNodeList(ctx)
	for {
		select {
		case <-logTicker:
			n.logStatusAndRefreshClusterNodeList(ctx)
		case <-ctx.Done():
			if n.nodeId != "" {
				_, err := n.db.RemoveNodeStatus(n.nodeId)
				if err != nil {
					log.Error("nodesvc: error removing status", "err", err, "nodeId", n.nodeId)
				}
				n.cluster.RemoveAndBroadcast()
			}
			log.Info("nodesvc: status loop shutdown gracefully")
			return
		}
	}
}

func (n *NodeServiceImpl) logStatusAndRefreshClusterNodeList(ctx context.Context) {
	err := n.logStatus(ctx)
	if err != nil && !docker.IsErrContainerNotFound(err) {
		log.Error("nodesvc: error logging status", "err", err)
	}

	// Load all node status rows from the db
	allNodes, err := n.db.ListNodeStatus()
	if err != nil {
		// don't update cluster
		log.Error("nodesvc: error listing nodes", "err", err)
		return
	}

	minObservedAt := int64(0)
	if n.NodeLiveness > 0 {
		minObservedAt = common.TimeToMillis(time.Now().Add(-1 * n.NodeLiveness))
	}

	// Remove rows that collide with our peerUrl (which could happen if for some reason the
	// docker daemon id changed on the host) or if the row is stale

	okNodeChan := make(chan v1.NodeStatus, len(allNodes))
	checkerWG := &sync.WaitGroup{}

	for _, node := range allNodes {
		remove := false
		if node.PeerUrl == n.peerUrl && node.NodeId != n.nodeId {
			remove = true
			log.Warn("nodesvc: deleting invalid node status row", "nodeId", node.NodeId, "peerUrl", node.PeerUrl,
				"observedAt", common.MillisToTime(node.ObservedAt))
		} else if minObservedAt > 0 && node.ObservedAt < minObservedAt {
			remove = true
			log.Warn("nodesvc: deleting stale node status row", "nodeId", node.NodeId,
				"observedAt", common.MillisToTime(node.ObservedAt), "nodeLivenessDur", n.NodeLiveness.String())
		}

		if remove {
			_, err = n.db.RemoveNodeStatus(node.NodeId)
			if err != nil {
				log.Error("nodesvc: error deleting node status", "err", err, "nodeId", node.NodeId)
			}
		} else {
			if node.NodeId == n.nodeId {
				okNodeChan <- node
			} else {
				// verify node connectivity
				checkerWG.Add(1)
				go healthCheckPeer(node, okNodeChan, checkerWG)
			}
		}
	}

	// collect results
	checkerWG.Wait()
	close(okNodeChan)
	keep := allNodes[:0]
	for node := range okNodeChan {
		keep = append(keep, node)
	}

	n.cluster.SetAllNodes(keep)
}

func (n *NodeServiceImpl) logStatus(ctx context.Context) error {
	status, err := n.resolveNodeStatus(ctx)
	if err != nil {
		return err
	}
	return n.db.PutNodeStatus(status)
}

func (n *NodeServiceImpl) resolveAndBroadcastNodeStatus(ctx context.Context) (v1.NodeStatus, error) {
	n.loadStatusLock.Lock()
	defer n.loadStatusLock.Unlock()

	status, err := n.resolveNodeStatus(ctx)
	if err != nil {
		return status, err
	}

	err = n.db.PutNodeStatus(status)
	if err != nil {
		return status, err
	}

	n.cluster.SetAndBroadcastStatus(status)
	return status, nil
}

func (n *NodeServiceImpl) resolveNodeStatus(ctx context.Context) (v1.NodeStatus, error) {
	infoResp := n.dispatcher.ComponentInfo()
	nodeStatus := v1.NodeStatus{
		NodeId:            n.nodeId,
		PeerUrl:           n.peerUrl,
		StartedAt:         n.startTimeMillis,
		ObservedAt:        common.NowMillis(),
		NumCPUs:           n.numCPUs,
		Version:           infoResp.Version,
		RunningComponents: infoResp.Info,
	}

	meminfo, err := linuxproc.ReadMemInfo("/proc/meminfo")
	if err != nil {
		return v1.NodeStatus{}, fmt.Errorf("ReadMemInfo error: %v", err)
	}
	nodeStatus.TotalMemoryMiB = int64(meminfo.MemTotal / 1024)
	nodeStatus.FreeMemoryMiB = int64(meminfo.MemAvailable / 1024)

	if n.totalMemAllowed >= 0 {
		delta := nodeStatus.TotalMemoryMiB - n.totalMemAllowed
		if delta > 0 {
			nodeStatus.TotalMemoryMiB -= delta
			nodeStatus.FreeMemoryMiB -= delta
			if nodeStatus.FreeMemoryMiB < 0 {
				nodeStatus.FreeMemoryMiB = 0
			}
		}
	}

	loadavg, err := linuxproc.ReadLoadAvg("/proc/loadavg")
	if err != nil {
		return v1.NodeStatus{}, fmt.Errorf("ReadLoadAvg error: %v", err)
	}
	nodeStatus.LoadAvg1m = loadavg.Last1Min
	nodeStatus.LoadAvg5m = loadavg.Last5Min
	nodeStatus.LoadAvg15m = loadavg.Last15Min

	return nodeStatus, nil
}

func rpcErr(err error, code ErrorCode, msg string) error {
	log.Error(msg, "code", code, "err", err)
	return &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func healthCheckPeer(node v1.NodeStatus, okNodeChan chan<- v1.NodeStatus, wg *sync.WaitGroup) {
	defer wg.Done()
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(node.PeerUrl + "/_mael_health_check")
	if err == nil && resp.StatusCode == http.StatusOK {
		okNodeChan <- node
	} else {
		var errStr string
		if err != nil {
			errStr = err.Error()
		}
		log.Warn("nodesvc: unable to health check peer", "nodeId", node.NodeId,
			"peerUrl", node.PeerUrl, "err", errStr)
	}
}

func healthCheckSeconds(d *v1.DockerComponent) int64 {
	seconds := d.HttpStartHealthCheckSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return seconds
}
