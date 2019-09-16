package maelstrom

import (
	"fmt"
	fuzz "github.com/google/gofuzz"
	"github.com/stretchr/testify/assert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sort"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/mattn/go-sqlite3"
)

func createTestSqlDb() *SqlDb {
	sqlDb, err := NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=WAL&mode=memory&_busy_timeout=5000")
	//sqlDb, err := NewSqlDb("mysql", "root:test@(127.0.0.1:3306)/foo")
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
	listOutEmpty := v1.ListNodeStatusOutput{
		Nodes:     []v1.NodeStatus{},
		NextToken: "",
	}
	listOut, err := db.ListNodeStatus(v1.ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, listOutEmpty, listOut)

	node1 := v1.NodeStatus{}
	f.Fuzz(&node1)
	node1Id := "3322:2322:7TKL:DLUP:5232:C2VA:XYCW:TEJM:FE6L:65PS:FISY:1234"
	node1.NodeId = node1Id
	node1.ObservedAt = nowMillis

	// insert first row and query
	err = db.PutNodeStatus(node1)
	assert.Nil(t, err)
	listOut, err = db.ListNodeStatus(v1.ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{node1}, listOut.Nodes)
	assert.Equal(t, "", listOut.NextToken)

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
	listOut, err = db.ListNodeStatus(v1.ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{node1, node2}, listOut.Nodes)

	// delete first row, query
	deleted, err := db.RemoveNodeStatus(node1.NodeId)
	assert.Nil(t, err)
	assert.True(t, deleted)
	deleted, err = db.RemoveNodeStatus(node1.NodeId)
	assert.Nil(t, err)
	assert.False(t, deleted)
	listOut, err = db.ListNodeStatus(v1.ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, []v1.NodeStatus{node2}, listOut.Nodes)

	// delete rows modified at start of test - should delete nothing
	count, err := db.RemoveNodeStatusOlderThan(start)
	assert.Nil(t, err)
	assert.Equal(t, int64(0), count)

	// delete rows modified before now - should delete second row
	count, err = db.RemoveNodeStatusOlderThan(time.Now().Add(time.Second))
	assert.Nil(t, err)
	assert.Equal(t, int64(1), count)

	// should be empty
	listOut, err = db.ListNodeStatus(v1.ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, listOutEmpty, listOut)
}

func TestListNodeStatusPagination(t *testing.T) {
	nowMillis := common.TimeToMillis(mockTimeNow(time.Now()))
	db := createTestSqlDb()
	f := fuzz.New()
	total := 200
	nodes := make([]v1.NodeStatus, total)
	for i := 0; i < total; i++ {
		var node v1.NodeStatus
		f.Fuzz(&node)
		node.ObservedAt = nowMillis
		node.NodeId = fmt.Sprintf("node-%40d", i)
		nodes[i] = node
		err := db.PutNodeStatus(node)
		assert.Nil(t, err)
	}

	// load all
	listOut, err := db.ListNodeStatus(v1.ListNodeStatusInput{
		Limit:     200,
		NextToken: "",
	})
	assert.Nil(t, err)
	assert.Equal(t, v1.ListNodeStatusOutput{Nodes: nodes, NextToken: ""}, listOut)

	// load 15 at a time
	listOut, err = db.ListNodeStatus(v1.ListNodeStatusInput{
		Limit:     15,
		NextToken: "",
	})
	assert.Nil(t, err)
	assert.Equal(t, v1.ListNodeStatusOutput{Nodes: nodes[0:15], NextToken: "15"}, listOut)

	listOut, err = db.ListNodeStatus(v1.ListNodeStatusInput{
		Limit:     15,
		NextToken: "190",
	})
	assert.Nil(t, err)
	assert.Equal(t, v1.ListNodeStatusOutput{Nodes: nodes[190:], NextToken: ""}, listOut)
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
