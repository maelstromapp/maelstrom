package gateway

import (
	"github.com/coopernurse/barrister-go"
	log "github.com/mgutz/logxi/v1"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sync"
)

type ClusterObserver interface {
	OnClusterUpdated(nodes map[string]v1.NodeStatus)
}

func NewCluster(nodeId string, localNodeService v1.NodeService) *Cluster {
	return &Cluster{
		nodeId:           nodeId,
		localNodeService: localNodeService,
		observers:        []ClusterObserver{},
		nodesById:        map[string]v1.NodeStatus{},
		lock:             &sync.Mutex{},
	}
}

type Cluster struct {
	nodeId           string
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
		log.Info("cluster: added node", "nodeId", c.nodeId, "remoteNode", node.NodeId)
	}
	if modified {
		c.notifyAll()
	}
	return modified
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
		log.Info("cluster: removed node", "nodeId", c.nodeId, "remoteNode", nodeId)
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

func (c *Cluster) GetNodeService(node v1.NodeStatus) v1.NodeService {
	if node.NodeId == c.nodeId {
		return c.localNodeService
	}
	client := barrister.NewRemoteClient(&barrister.HttpTransport{Url: node.PeerUrl}, false)
	return v1.NewNodeServiceProxy(client)
}

func (c *Cluster) notifyAll() {
	for _, o := range c.observers {
		go o.OnClusterUpdated(c.nodesById)
	}
}