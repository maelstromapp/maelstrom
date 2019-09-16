package maelstrom

import (
	"github.com/coopernurse/barrister-go"
	log "github.com/mgutz/logxi/v1"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sync"
)

type ClusterObserver interface {
	OnClusterUpdated(nodes map[string]v1.NodeStatus)
}

func NewCluster(myNodeId string, localNodeService v1.NodeService) *Cluster {
	return &Cluster{
		myNodeId:         myNodeId,
		localNodeService: localNodeService,
		observers:        []ClusterObserver{},
		nodesById:        map[string]v1.NodeStatus{},
		lock:             &sync.Mutex{},
	}
}

type Cluster struct {
	myNodeId         string
	localNodeService v1.NodeService
	observers        []ClusterObserver
	nodesById        map[string]v1.NodeStatus
	lock             *sync.Mutex
}

func (c *Cluster) AddObserver(observer ClusterObserver) {
	c.lock.Lock()
	c.observers = append(c.observers, observer)
	c.lock.Unlock()
}

func (c *Cluster) SetNode(node v1.NodeStatus) bool {
	modified := false
	c.lock.Lock()
	oldNode, ok := c.nodesById[node.NodeId]
	if !ok || node.Version > oldNode.Version || node.ObservedAt > oldNode.ObservedAt {
		c.nodesById[node.NodeId] = node
		modified = true
	}
	c.lock.Unlock()
	if !ok {
		log.Info("cluster: added node", "nodeId", c.myNodeId, "remoteNode", node.NodeId)
	}
	if modified {
		if log.IsDebug() {
			log.Debug("cluster: SetNode modified", "myNode", c.myNodeId, "peerNode", node.NodeId,
				"version", node.Version)
		}
		c.notifyAll()
	}
	return modified
}

func (c *Cluster) SetAllNodes(nodes []v1.NodeStatus) {
	newNodesById := map[string]v1.NodeStatus{}
	for _, node := range nodes {
		newNodesById[node.NodeId] = node
	}
	c.lock.Lock()
	c.nodesById = newNodesById
	c.lock.Unlock()
	c.notifyAll()
}

func (c *Cluster) SetAndBroadcastStatus(node v1.NodeStatus) error {
	c.SetNode(node)
	input := v1.StatusChangedInput{
		NodeId:  c.myNodeId,
		Exiting: false,
		Status:  &node,
	}
	var lastErr error
	for _, svc := range c.GetRemoteNodeServices() {
		_, err := svc.StatusChanged(input)
		if err != nil {
			log.Error("cluster: SetAndBroadcastStatus error calling StatusChanged", "err", err)
			lastErr = err
		}
	}
	return lastErr
}

func (c *Cluster) RemoveAndBroadcast() {
	c.RemoveNode(c.myNodeId)
	input := v1.StatusChangedInput{
		NodeId:  c.myNodeId,
		Exiting: true,
	}
	for _, svc := range c.GetRemoteNodeServices() {
		_, err := svc.StatusChanged(input)
		if err != nil {
			log.Error("cluster: RemoveAndBroadcast error calling StatusChanged", "err", err)
		}
	}
}

func (c *Cluster) RemoveNode(nodeId string) bool {
	modified := false
	c.lock.Lock()
	_, ok := c.nodesById[nodeId]
	if ok {
		modified = true
		delete(c.nodesById, nodeId)
	}
	c.lock.Unlock()
	if modified {
		log.Info("cluster: removed node", "nodeId", c.myNodeId, "remoteNode", nodeId)
		c.notifyAll()
	}
	return modified
}

func (c *Cluster) GetNodes() []v1.NodeStatus {
	c.lock.Lock()
	nodes := make([]v1.NodeStatus, len(c.nodesById))
	i := 0
	for _, n := range c.nodesById {
		nodes[i] = n
		i++
	}
	c.lock.Unlock()
	return nodes
}

func (c *Cluster) GetNodeById(nodeId string) *v1.NodeStatus {
	var node *v1.NodeStatus
	c.lock.Lock()
	n, ok := c.nodesById[nodeId]
	c.lock.Unlock()
	if ok {
		node = &n
	}
	return node
}

func (c *Cluster) GetNodeServiceById(nodeId string) v1.NodeService {
	node := c.GetNodeById(nodeId)
	if node == nil {
		return nil
	} else {
		return c.GetNodeService(*node)
	}
}

func (c *Cluster) GetNodeService(node v1.NodeStatus) v1.NodeService {
	if node.NodeId == c.myNodeId {
		return c.localNodeService
	}
	client := barrister.NewRemoteClient(&barrister.HttpTransport{Url: node.PeerUrl + "/_mael/v1"}, false)
	return v1.NewNodeServiceProxy(client)
}

func (c *Cluster) GetRemoteNodeServices() []v1.NodeService {
	svcs := make([]v1.NodeService, 0)
	c.lock.Lock()
	for nodeId, nodeStatus := range c.nodesById {
		if nodeId != c.myNodeId {
			svcs = append(svcs, c.GetNodeService(nodeStatus))
		}
	}
	c.lock.Unlock()
	return svcs
}

func (c *Cluster) notifyAll() {
	for _, o := range c.observers {
		go o.OnClusterUpdated(c.nodesById)
	}
}
