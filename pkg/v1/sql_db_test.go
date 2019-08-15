package v1

import (
	"fmt"
	fuzz "github.com/google/gofuzz"
	"github.com/stretchr/testify/assert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"testing"
	"time"
)

func createTestSqlDb() *SqlDb {
	sqlDb, err := NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=MEMORY&mode=rwc")
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
	listOutEmpty := ListNodeStatusOutput{
		Nodes:     []NodeStatus{},
		NextToken: "",
	}
	listOut, err := db.ListNodeStatus(ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, listOutEmpty, listOut)

	node1 := NodeStatus{}
	f.Fuzz(&node1)
	node1Id := "3322:2322:7TKL:DLUP:5232:C2VA:XYCW:TEJM:FE6L:65PS:FISY:1234"
	node1.NodeId = node1Id
	node1.ObservedAt = nowMillis

	// insert first row and query
	err = db.PutNodeStatus(node1)
	assert.Nil(t, err)
	listOut, err = db.ListNodeStatus(ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, []NodeStatus{node1}, listOut.Nodes)
	assert.Equal(t, "", listOut.NextToken)

	// insert second row and update first, query
	node2 := NodeStatus{}
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
	listOut, err = db.ListNodeStatus(ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, []NodeStatus{node1, node2}, listOut.Nodes)

	// delete first row, query
	deleted, err := db.RemoveNodeStatus(node1.NodeId)
	assert.Nil(t, err)
	assert.True(t, deleted)
	deleted, err = db.RemoveNodeStatus(node1.NodeId)
	assert.Nil(t, err)
	assert.False(t, deleted)
	listOut, err = db.ListNodeStatus(ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, []NodeStatus{node2}, listOut.Nodes)

	// delete rows modified at start of test - should delete nothing
	count, err := db.RemoveNodeStatusOlderThan(start)
	assert.Nil(t, err)
	assert.Equal(t, int64(0), count)

	// delete rows modified before now - should delete second row
	count, err = db.RemoveNodeStatusOlderThan(time.Now().Add(time.Second))
	assert.Nil(t, err)
	assert.Equal(t, int64(1), count)

	// should be empty
	listOut, err = db.ListNodeStatus(ListNodeStatusInput{})
	assert.Nil(t, err)
	assert.Equal(t, listOutEmpty, listOut)
}

func TestListNodeStatusPagination(t *testing.T) {
	nowMillis := common.TimeToMillis(mockTimeNow(time.Now()))
	db := createTestSqlDb()
	f := fuzz.New()
	total := 200
	nodes := make([]NodeStatus, total)
	for i := 0; i < total; i++ {
		var node NodeStatus
		f.Fuzz(&node)
		node.ObservedAt = nowMillis
		node.NodeId = fmt.Sprintf("node-%40d", i)
		nodes[i] = node
		err := db.PutNodeStatus(node)
		assert.Nil(t, err)
	}

	// load all
	listOut, err := db.ListNodeStatus(ListNodeStatusInput{
		Limit:     200,
		NextToken: "",
	})
	assert.Nil(t, err)
	assert.Equal(t, ListNodeStatusOutput{Nodes: nodes, NextToken: ""}, listOut)

	// load 15 at a time
	listOut, err = db.ListNodeStatus(ListNodeStatusInput{
		Limit:     15,
		NextToken: "",
	})
	assert.Nil(t, err)
	assert.Equal(t, ListNodeStatusOutput{Nodes: nodes[0:15], NextToken: "15"}, listOut)

	listOut, err = db.ListNodeStatus(ListNodeStatusInput{
		Limit:     15,
		NextToken: "190",
	})
	assert.Nil(t, err)
	assert.Equal(t, ListNodeStatusOutput{Nodes: nodes[190:], NextToken: ""}, listOut)
}
