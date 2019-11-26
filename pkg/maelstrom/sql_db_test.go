package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/v1"
	fuzz "github.com/google/gofuzz"
	"github.com/stretchr/testify/assert"
	"sort"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

func createTestSqlDb() *SqlDb {
	sqlDb, err := NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=WAL&mode=memory&_busy_timeout=5000")
	//sqlDb, err := NewSqlDb("mysql", "root:test@(127.0.0.1:3306)/maeltest")
	//sqlDb, err := NewSqlDb("postgres", "postgres://postgres:test@127.0.0.1:5432/maeltest?sslmode=disable")
	panicOnErr(err)

	// run sql migrations and delete any existing data
	panicOnErr(sqlDb.Migrate())
	panicOnErr(sqlDb.DeleteAll())

	return sqlDb
}

func mockTimeNow(t time.Time) time.Time {
	timeNow = func() time.Time { return t }
	return t
}

func TestNodeStatusCRUD(t *testing.T) {
	start := time.Now()
	nowMillis := common.TimeToMillis(mockTimeNow(time.Now().Add(time.Millisecond)))
	f := fuzz.New()
	db := createTestSqlDb()

	// empty list by default
	dbNodes, err := db.ListNodeStatus()
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{}, dbNodes)

	node1 := v1.NodeStatus{}
	f.Fuzz(&node1)
	node1Id := "3322:2322:7TKL:DLUP:5232:C2VA:XYCW:TEJM:FE6L:65PS:FISY:1234"
	node1.NodeId = node1Id
	node1.ObservedAt = nowMillis

	// insert first row and query
	err = db.PutNodeStatus(node1)
	assert.Nil(t, err)
	dbNodes, err = db.ListNodeStatus()
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{node1}, dbNodes)

	// insert second row and update first, query
	node2 := v1.NodeStatus{}
	f.Fuzz(&node2)
	node2.ObservedAt = nowMillis
	node2.NodeId = "node2"
	node2.ObservedAt = common.TimeToMillis(start.Add(time.Millisecond))
	assert.Nil(t, db.PutNodeStatus(node2))
	f.Fuzz(&node1)
	node1.ObservedAt = nowMillis
	node1.NodeId = node1Id
	node1.ObservedAt = node2.ObservedAt
	assert.Nil(t, db.PutNodeStatus(node1))
	dbNodes, err = db.ListNodeStatus()
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{node1, node2}, dbNodes)

	// delete first row, query
	deleted, err := db.RemoveNodeStatus(node1.NodeId)
	assert.Nil(t, err)
	assert.True(t, deleted)
	deleted, err = db.RemoveNodeStatus(node1.NodeId)
	assert.Nil(t, err)
	assert.False(t, deleted)
	dbNodes, err = db.ListNodeStatus()
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{node2}, dbNodes)

	// delete rows modified at start of test - should delete nothing
	count, err := db.RemoveNodeStatusOlderThan(start)
	assert.Nil(t, err)
	assert.Equal(t, int64(0), count)

	// delete rows modified before now - should delete second row
	count, err = db.RemoveNodeStatusOlderThan(time.Now().Add(time.Second))
	assert.Nil(t, err)
	assert.Equal(t, int64(1), count)

	// should be empty
	dbNodes, err = db.ListNodeStatus()
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{}, dbNodes)
}

func TestListNodeStatus(t *testing.T) {
	nowMillis := common.TimeToMillis(mockTimeNow(time.Now()))
	db := createTestSqlDb()
	f := fuzz.New()
	total := 200
	nodes := make([]v1.NodeStatus, total)
	for i := 0; i < total; i++ {
		var node v1.NodeStatus
		f.Fuzz(&node)
		node.ObservedAt = nowMillis
		node.NodeId = fmt.Sprintf("node-%04d", i)
		nodes[i] = node
		err := db.PutNodeStatus(node)
		assert.Nil(t, err)
	}

	// load all
	dbNodes, err := db.ListNodeStatus()
	assert.Nil(t, err)
	assert.Equal(t, nodes, dbNodes)
}

func TestAcquireReleaseRole(t *testing.T) {
	db := createTestSqlDb()
	node1 := "node1"
	node2 := "node2"
	r1 := "role1"
	r2 := "role2"

	// acquire role1 as node1 - OK
	acquired, lockNode, err := db.AcquireOrRenewRole(r1, node1, time.Minute)
	assert.Nil(t, err)
	assert.True(t, acquired)
	assert.Equal(t, node1, lockNode)

	// acquire same role as node2 - DENY
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node2, time.Minute)
	assert.Nil(t, err)
	assert.False(t, acquired)
	assert.Equal(t, node1, lockNode)

	// release lock with wrong node id and try to acquire - DENY
	err = db.ReleaseRole(r1, node2)
	assert.Nil(t, err)
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node2, time.Minute)
	assert.Nil(t, err)
	assert.False(t, acquired)

	// release lock and acquire as node2 - OK
	err = db.ReleaseRole(r1, node1)
	assert.Nil(t, err)
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node2, time.Minute)
	assert.Nil(t, err)
	assert.True(t, acquired)
	assert.Equal(t, node2, lockNode)

	// acquire role2 as node2 - OK
	acquired, lockNode, err = db.AcquireOrRenewRole(r2, node2, time.Minute)
	assert.Nil(t, err)
	assert.True(t, acquired)
	assert.Equal(t, node2, lockNode)

	// release all roles as node1
	err = db.ReleaseAllRoles(node1)
	assert.Nil(t, err)

	// acquire role1 as node2 - OK
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node2, time.Millisecond)
	assert.Nil(t, err)
	assert.True(t, acquired)
	assert.Equal(t, node2, lockNode)

	// let lock expire
	time.Sleep(time.Millisecond * 2)

	// acquire role1 as node1 - OK
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node1, time.Second)
	assert.Nil(t, err)
	assert.True(t, acquired)
	assert.Equal(t, node1, lockNode)

	// extend lock with same node id
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node1, time.Minute)
	assert.Nil(t, err)
	assert.True(t, acquired)
	assert.Equal(t, node1, lockNode)

	// wait for 1sec orig lock to expire
	time.Sleep(time.Millisecond * 1100)

	// node2 should not be able to get lock because lock was extended by 1 minute
	acquired, lockNode, err = db.AcquireOrRenewRole(r1, node2, time.Minute)
	assert.Nil(t, err)
	assert.False(t, acquired)
	assert.Equal(t, node1, lockNode)
}

func TestAcquireReleaseRoleConcurrency(t *testing.T) {
	db := createTestSqlDb()
	roles := make([]string, 300)
	for i := 0; i < len(roles); i++ {
		roles[i] = fmt.Sprintf("role%d", i)
	}
	wg := &sync.WaitGroup{}
	lock := &sync.Mutex{}
	acquiredRoles := make([]string, 0)
	wg.Add(5)
	for i := 0; i < 5; i++ {
		nodeId := fmt.Sprintf("n%d", i)
		go func() {
			defer wg.Done()
			for x := 0; x < len(roles); x++ {
				acquired, _, err := db.AcquireOrRenewRole(roles[x], nodeId, time.Minute)
				assert.Nil(t, err)
				if acquired {
					lock.Lock()
					acquiredRoles = append(acquiredRoles, roles[x])
					lock.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	sort.Strings(roles)
	sort.Strings(acquiredRoles)
	assert.Equal(t, len(roles), len(acquiredRoles))
	for i := 0; i < len(roles); i++ {
		assert.Equal(t, roles[i], acquiredRoles[i])
	}
}

func TestToggleEventSourceEnabled(t *testing.T) {
	db := createTestSqlDb()
	out, err := db.ListEventSources(v1.ListEventSourcesInput{})
	assert.Nil(t, err)
	assert.Equal(t, []v1.EventSourceWithStatus{}, out.EventSources)

	es := validPutEventSourceInput("es1", "c1").EventSource
	_, err = db.PutEventSource(es)
	assert.Nil(t, err)

	es2 := validPutEventSourceInput("es2", "c1").EventSource
	_, err = db.PutEventSource(es2)
	assert.Nil(t, err)

	out, err = db.ListEventSources(v1.ListEventSourcesInput{})
	assert.Nil(t, err)
	sanitizeEventSources(out.EventSources)
	assert.Equal(t, []v1.EventSourceWithStatus{
		{
			EventSource: es,
			Enabled:     true,
		},
		{
			EventSource: es2,
			Enabled:     true,
		},
	}, out.EventSources)

	num, err := db.SetEventSourcesEnabled([]string{"es1"}, false)
	assert.Nil(t, err)
	assert.Equal(t, int64(1), num)

	out, err = db.ListEventSources(v1.ListEventSourcesInput{})
	assert.Nil(t, err)
	sanitizeEventSources(out.EventSources)
	assert.Equal(t, []v1.EventSourceWithStatus{
		{
			EventSource: es,
			Enabled:     false,
		},
		{
			EventSource: es2,
			Enabled:     true,
		},
	}, out.EventSources)
}

func TestComponentCount(t *testing.T) {
	db := createTestSqlDb()

	// count is zero initially
	getComponentDeployCount(t, db, "c1", 1, 0)

	// increment and check
	assert.Nil(t, db.IncrementComponentDeployCount("c1", 1))
	getComponentDeployCount(t, db, "c1", 1, 1)

	assert.Nil(t, db.IncrementComponentDeployCount("c1", 1))
	getComponentDeployCount(t, db, "c1", 1, 2)

	// check other key - should be zero
	getComponentDeployCount(t, db, "c2", 1, 0)
	getComponentDeployCount(t, db, "c1", 2, 0)

	// increment others
	assert.Nil(t, db.IncrementComponentDeployCount("c2", 1))
	assert.Nil(t, db.IncrementComponentDeployCount("c1", 2))
	getComponentDeployCount(t, db, "c2", 1, 1)
	getComponentDeployCount(t, db, "c1", 2, 1)
	getComponentDeployCount(t, db, "c1", 1, 2)
}

func getComponentDeployCount(t *testing.T, db Db, componentName string, ver int64, expectedCount int) {
	count, err := db.GetComponentDeployCount(componentName, ver)
	assert.Nil(t, err)
	assert.Equal(t, expectedCount, count)
}
