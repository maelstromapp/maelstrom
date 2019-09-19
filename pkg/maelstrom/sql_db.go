package maelstrom

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/GuiaBolso/darwin"
	"github.com/Masterminds/squirrel"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/mattn/go-sqlite3"
	"github.com/mgutz/logxi/v1"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

func NewSqlDb(driver string, dsn string) (*SqlDb, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if strings.Contains(driver, "postgres") {
		squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar)
	}

	return &SqlDb{db: db, driver: driver}, nil
}

type SqlDb struct {
	db       *sql.DB
	driver   string
	DebugLog bool
}

func (d *SqlDb) Close() {
	err := d.db.Close()
	if err != nil {
		log.Error("sql_db: err closing db", "err", err)
	}
}

func (d *SqlDb) DeleteAll() error {
	tables := []string{"component", "eventsource", "nodestatus", "rolelock"}
	for _, t := range tables {
		_, err := d.db.Exec(fmt.Sprintf("delete from %s", t))
		if err != nil {
			return fmt.Errorf("sql_db: unable to delete from: %s - %v", t, err)
		}
	}
	return nil
}

func (d *SqlDb) AcquireOrRenewRole(roleId string, nodeId string, lockDur time.Duration) (bool, string, error) {
	for i := 0; i < 5; i++ {
		acquired, lockNodeId, err := d.acquireOrRenewRoleOnce(roleId, nodeId, lockDur)
		if lockNodeId == "" && err == nil {
			// retry terminal case where insert failed. this ensures we resolve the current nodeId
			time.Sleep(time.Duration(rand.Intn(50)+1) * time.Millisecond)
		} else {
			return acquired, lockNodeId, err
		}
	}
	return false, "", fmt.Errorf("sql_db: rolelock failed to acquire lock for: %s", roleId)
}

func (d *SqlDb) acquireOrRenewRoleOnce(roleId string, nodeId string, lockDur time.Duration) (bool, string, error) {
	var curNodeId string
	var curExpires int64
	rows, err := squirrel.Select("nodeId", "expiresAt").
		From("rolelock").
		Where(squirrel.Eq{"roleId": roleId}).
		RunWith(d.db).Query()
	if err != nil {
		return false, "", fmt.Errorf("sql_db: rolelock Select failed for: %s - %v", roleId, err)
	}
	if rows.Next() {
		err = rows.Scan(&curNodeId, &curExpires)
		if err != nil {
			_ = rows.Close()
			return false, "", fmt.Errorf("sql_db: rolelock Scan failed for: %s - %v", roleId, err)
		}
	}
	err = rows.Close()
	if err != nil {
		return false, "", fmt.Errorf("sql_db: rolelock Select close failed for: %s - %v", roleId, err)
	}

	now := time.Now()
	nowMillis := common.TimeToMillis(now)
	expiresAt := common.TimeToMillis(now.Add(lockDur))

	if curNodeId != "" {

		if curExpires > nowMillis && curNodeId != nodeId {
			// lock is not expired and is held by a different node
			return false, curNodeId, nil
		}

		q := squirrel.Update("rolelock").
			SetMap(map[string]interface{}{
				"nodeId":    nodeId,
				"expiresAt": expiresAt,
			}).Where(squirrel.And{
			squirrel.Eq{"roleId": roleId},
			squirrel.Or{
				squirrel.Lt{"expiresAt": nowMillis},
				squirrel.And{squirrel.Eq{"nodeId": nodeId}, squirrel.Eq{"expiresAt": curExpires}},
			},
		})

		result, err := q.RunWith(d.db).Exec()
		if err != nil {
			if sqliteErr(err, sqlite3.ErrLocked) {
				// hack to workaround "table is locked" error when using sqlite3 - outer call will retry
				return false, "", nil
			}
			return false, "", fmt.Errorf("sql_db: update failed for roleId: %s err: %v", roleId, err)
		}
		rows, err := result.RowsAffected()

		if err != nil {
			return false, "", fmt.Errorf("sql_db: RowsAffected failed for roleId: %s err: %v", roleId, err)
		}
		if rows == 1 {
			return true, nodeId, nil
		}
	}

	insertQ := squirrel.Insert("rolelock").
		Columns("roleId", "nodeId", "expiresAt").Values(roleId, nodeId, expiresAt)
	_, err = insertQ.RunWith(d.db).Exec()
	if err == nil {
		return true, nodeId, nil
	} else {
		// retry: nodeId and err are nil/empty
		return false, "", nil
	}
}

func (d *SqlDb) ReleaseAllRoles(nodeId string) error {
	_, err := squirrel.Delete("rolelock").Where(squirrel.Eq{"nodeId": nodeId}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete rolelock by nodeId failed: err: %v", err)
	}
	return err
}

func (d *SqlDb) PutComponent(component v1.Component) (int64, error) {
	now := common.NowMillis()
	previousVersion := component.Version
	component.Version += 1
	component.ModifiedAt = now
	jsonVal, err := json.Marshal(component)
	if err != nil {
		return 0, fmt.Errorf("PutComponent unable to marshal JSON: %v", err)
	}

	if previousVersion == 0 {
		err = d.insertRow("component", component.Name, component,
			[]string{"name", "projectName", "version", "createdAt", "modifiedAt", "json"},
			[]interface{}{component.Name, component.ProjectName, 1, now, now, jsonVal})
	} else {
		err = d.updateRow("component", component.Name, previousVersion, map[string]interface{}{
			"modifiedAt": now,
			"json":       jsonVal,
			"version":    component.Version,
		})
	}

	if err != nil {
		return 0, err
	}
	return component.Version, nil
}

func (d *SqlDb) GetComponent(componentName string) (component v1.Component, err error) {
	err = d.getRow("component", componentName, &component)
	return
}

func (d *SqlDb) ListComponents(input v1.ListComponentsInput) (output v1.ListComponentsOutput, err error) {
	q := squirrel.Select("json").From("component").OrderBy("name")
	if input.NamePrefix != "" {
		q = q.Where(squirrel.Like{"name": input.NamePrefix + "%"})
	}
	if input.ProjectName != "" {
		q = q.Where(squirrel.Eq{"projectName": input.ProjectName})
	}

	components := make([]v1.Component, 0)
	nextToken, err := d.selectPaginated(q, input.NextToken, input.Limit, func(rows *sql.Rows) error {
		var comp v1.Component
		err := d.scanJSON(rows, &comp)
		if err != nil {
			return fmt.Errorf("ListComponents: %v", err)
		}
		components = append(components, comp)
		return nil
	})
	if err != nil {
		return v1.ListComponentsOutput{}, err
	}
	return v1.ListComponentsOutput{NextToken: nextToken, Components: components}, nil
}

func (d *SqlDb) scanJSON(rows *sql.Rows, target interface{}) error {
	var jsonVal []byte
	err := rows.Scan(&jsonVal)
	if err != nil {
		return fmt.Errorf("scanJSON err in scan: %v", err)
	}
	err = json.Unmarshal(jsonVal, target)
	if err != nil {
		return fmt.Errorf("scanJSON err in unmarshal: %v", err)
	}
	return nil
}

func (d *SqlDb) selectPaginated(q squirrel.SelectBuilder, nextToken string, limit int64,
	onRow func(rows *sql.Rows) error) (string, error) {
	offset := 0
	if nextToken != "" {
		o, err := strconv.Atoi(nextToken)
		if err == nil {
			offset = o
		}
	}

	if limit < 1 || limit > 1000 {
		limit = 1000
	}

	rows, err := q.Offset(uint64(offset)).Limit(uint64(limit + 1)).RunWith(d.db).Query()
	if err != nil {
		return "", fmt.Errorf("err in query: %v", err)
	}

	defer common.CheckClose(rows, &err)
	x := int64(0)
	for x < limit && rows.Next() {
		err = onRow(rows)
		if err != nil {
			return "", err
		}
		x++
	}

	nextToken = ""
	if rows.Next() {
		nextToken = strconv.Itoa(offset + int(x))
	}
	return nextToken, nil
}

func (d *SqlDb) RemoveComponent(componentName string) (found bool, err error) {
	_, err = squirrel.Delete("eventsource").Where(squirrel.Eq{"componentName": componentName}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete eventsource by componentName failed: %s err: %v", componentName, err)
		return
	}
	return d.removeRow("component", componentName)
}

func (d *SqlDb) PutEventSource(eventSource v1.EventSource) (int64, error) {
	now := common.NowMillis()
	previousVersion := eventSource.Version
	eventSource.Version += 1
	eventSource.ModifiedAt = now
	jsonVal, err := json.Marshal(eventSource)
	if err != nil {
		return 0, fmt.Errorf("PutEventSource unable to marshal JSON: %v", err)
	}

	eventType := string(getEventSourceType(eventSource))

	if previousVersion == 0 {
		err = d.insertRow("eventsource", eventSource.Name, eventSource,
			[]string{"name", "componentName", "projectName", "type", "version", "createdAt", "modifiedAt", "json"},
			[]interface{}{eventSource.Name, eventSource.ComponentName, eventSource.ProjectName,
				eventType, 1, now, now, jsonVal})
	} else {
		err = d.updateRow("eventsource", eventSource.Name, previousVersion, map[string]interface{}{
			"modifiedAt":    now,
			"componentName": eventSource.ComponentName,
			"type":          eventType,
			"json":          jsonVal,
			"version":       eventSource.Version,
		})
	}

	if err != nil {
		return 0, err
	}
	return eventSource.Version, nil
}

func (d *SqlDb) GetEventSource(eventSourceName string) (es v1.EventSource, err error) {
	err = d.getRow("eventsource", eventSourceName, &es)
	return
}

func (d *SqlDb) RemoveEventSource(eventSourceName string) (bool, error) {
	return d.removeRow("eventsource", eventSourceName)
}

func (d *SqlDb) ListEventSources(input v1.ListEventSourcesInput) (v1.ListEventSourcesOutput, error) {
	q := squirrel.Select("json").From("eventsource").OrderBy("name")
	if input.NamePrefix != "" {
		q = q.Where(squirrel.Like{"name": input.NamePrefix + "%"})
	}
	if input.ComponentName != "" {
		q = q.Where(squirrel.Eq{"componentName": input.ComponentName})
	}
	if input.ProjectName != "" {
		q = q.Where(squirrel.Eq{"projectName": input.ProjectName})
	}
	if input.EventSourceType != "" {
		q = q.Where(squirrel.Eq{"type": string(input.EventSourceType)})
	}

	eventSources := make([]v1.EventSource, 0)
	nextToken, err := d.selectPaginated(q, input.NextToken, input.Limit, func(rows *sql.Rows) error {
		var es v1.EventSource
		err := d.scanJSON(rows, &es)
		if err != nil {
			return fmt.Errorf("ListEventSources: %v", err)
		}
		eventSources = append(eventSources, es)
		return nil
	})
	if err != nil {
		return v1.ListEventSourcesOutput{}, err
	}
	return v1.ListEventSourcesOutput{NextToken: nextToken, EventSources: eventSources}, nil
}

func (d *SqlDb) PutNodeStatus(status v1.NodeStatus) error {
	jsonVal, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("PutNodeStatus unable to marshal JSON: %v", err)
	}
	q := squirrel.Update("nodestatus").
		SetMap(map[string]interface{}{
			"observedAt": status.ObservedAt,
			"json":       jsonVal,
		}).Where(squirrel.Eq{"nodeId": status.NodeId})

	result, err := q.RunWith(d.db).Exec()
	if err != nil {
		return fmt.Errorf("PutNodeStatus update failed for nodeId: %s err: %v", status.NodeId, err)
	}
	rows, err2 := result.RowsAffected()
	if err2 != nil {
		return fmt.Errorf("PutNodeStatus RowsAffected failed for nodeId: %s err: %v", status.NodeId, err)
	}
	if rows != 1 {
		q := squirrel.Insert("nodestatus").
			Columns("nodeId", "observedAt", "json").Values(status.NodeId, status.ObservedAt, jsonVal)
		_, err := q.RunWith(d.db).Exec()
		if err != nil {
			return fmt.Errorf("PutNodeStatus insert failed for nodeId: %s err: %v", status.NodeId, err)
		}
	}
	return nil
}

func (d *SqlDb) ListNodeStatus(input v1.ListNodeStatusInput) (v1.ListNodeStatusOutput, error) {
	q := squirrel.Select("json").From("nodestatus").OrderBy("nodeId")
	nodes := make([]v1.NodeStatus, 0)
	nextToken, err := d.selectPaginated(q, input.NextToken, input.Limit, func(rows *sql.Rows) error {
		var node v1.NodeStatus
		err := d.scanJSON(rows, &node)
		if err != nil {
			return fmt.Errorf("ListNodeStatus: %v", err)
		}
		nodes = append(nodes, node)
		return nil
	})
	if err != nil {
		return v1.ListNodeStatusOutput{}, err
	}
	return v1.ListNodeStatusOutput{NextToken: nextToken, Nodes: nodes}, nil
}

func (d *SqlDb) RemoveNodeStatusOlderThan(observedAt time.Time) (int64, error) {
	del := squirrel.Delete("nodestatus").Where(squirrel.Lt{"observedAt": common.TimeToMillis(observedAt)})
	return d.removeRows(del)
}

func (d *SqlDb) RemoveNodeStatus(nodeId string) (bool, error) {
	return d.removeRowByColumn("nodestatus", "nodeId", nodeId)
}

func (d *SqlDb) insertRow(table string, name string, val interface{}, columns []string, bindVals []interface{}) error {
	q := squirrel.Insert(table).Columns(columns...).Values(bindVals...)
	if d.DebugLog {
		log.Debug("sql_db: insertRow", "sql", squirrel.DebugSqlizer(q))
	}

	_, err := q.RunWith(d.db).Exec()
	if err != nil {
		var count int64
		err2 := squirrel.Select("count(*)").From(table).Where(squirrel.Eq{"name": name}).
			RunWith(d.db).QueryRow().Scan(&count)
		if err2 == nil && count > 0 {
			return AlreadyExists
		}
		return fmt.Errorf("insertRow failed for table: %s name: %s err: %v", table, name, err)
	}
	return nil
}

func (d *SqlDb) updateRow(table string, name string, previousVersion int64, updates map[string]interface{}) error {
	q := squirrel.Update(table).
		SetMap(updates).
		Where(squirrel.Eq{"name": name, "version": previousVersion})
	if d.DebugLog {
		log.Debug("sql_db: updateRow", "sql", squirrel.DebugSqlizer(q))
	}
	result, err := q.RunWith(d.db).Exec()
	if err != nil {
		return fmt.Errorf("updateRow failed for table: %s name: %s err: %v", table, name, err)
	}
	rows, err2 := result.RowsAffected()
	if err2 != nil {
		log.Warn("sql_db: updateRow RowsAffected", "err", err)
	}
	if rows != 1 {
		return IncorrectPreviousVersion
	}
	return nil
}

func (d *SqlDb) getRow(table string, name string, target interface{}) error {
	rows, err := squirrel.Select("json").
		From(table).
		Where(squirrel.Eq{"name": name}).
		RunWith(d.db).Query()
	if err != nil {
		return fmt.Errorf("getRow %s query failed for: %s - %v", table, name, err)
	}
	defer common.CheckClose(rows, &err)
	if rows.Next() {
		var jsonVal []byte
		err = rows.Scan(&jsonVal)
		if err == nil {
			err = json.Unmarshal(jsonVal, target)
			if err != nil {
				return fmt.Errorf("getRow %s json unmarshal failed for: %s - %v", table, name, err)
			}
			return nil
		} else {
			return fmt.Errorf("getRow %s scan failed for: %s - %v", table, name, err)
		}
	} else {
		return NotFound
	}
}

func (d *SqlDb) removeRow(table string, name string) (found bool, err error) {
	return d.removeRowByColumn(table, "name", name)
}

func (d *SqlDb) removeRowByColumn(table string, colName string, colValue string) (found bool, err error) {
	var result sql.Result
	var rows int64
	result, err = squirrel.Delete(table).Where(squirrel.Eq{colName: colValue}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete %s failed: %s err: %v", table, colValue, err)
		return
	}
	rows, err = result.RowsAffected()
	if err != nil {
		err = fmt.Errorf("delete %s rowsaffected failed: %s err: %v", table, colValue, err)
		return
	}
	found = rows > 0
	return
}

func (d *SqlDb) removeRows(del squirrel.DeleteBuilder) (rows int64, err error) {
	var result sql.Result
	result, err = del.RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete failed - err: %v", err)
		return
	}
	rows, err = result.RowsAffected()
	if err != nil {
		err = fmt.Errorf("delete rowsaffected failed - err: %v", err)
		return
	}
	return
}

func (d *SqlDb) Migrate() error {
	migrations := []darwin.Migration{
		{
			Version:     1,
			Description: "Create component table",
			Script: `create table component (
                        name          varchar(60) primary key,
                        projectName   varchar(60) not null,
                        version       int not null,
                        createdAt     bigint not null,
                        modifiedAt    bigint not null,
                        json          mediumblob not null
                     )`,
		},
		{
			Version:     2,
			Description: "Create eventsource table",
			Script: `create table eventsource (
                        name           varchar(60) primary key,
                        componentName  varchar(60) not null,
                        projectName    varchar(60) not null,
                        version        int not null,
                        createdAt      bigint not null,
                        modifiedAt     bigint not null,
                        type           varchar(30) not null,
                        json           mediumblob not null
                     )`,
		},
		{
			Version:     3,
			Description: "Create nodestatus table",
			Script: `create table nodestatus (
                        nodeId         varchar(60) primary key,
                        observedAt     bigint not null,
                        json           mediumblob not null
                     )`,
		},
		{
			Version:     4,
			Description: "Create rolelock table",
			Script: `create table rolelock (
                        roleId         varchar(80) primary key,
                        nodeId         varchar(60) not null,
                        expiresAt      bigint not null
                     )`,
		},
	}
	darwinDriver := darwin.NewGenericDriver(d.db, migrationDialect(d.driver))
	m := darwin.New(darwinDriver, migrations, nil)
	err := m.Migrate()
	if err != nil {
		return fmt.Errorf("GorpDb.Migrate failed: %v", err)
	}
	return nil
}

func migrationDialect(driver string) darwin.Dialect {
	if strings.Contains(driver, "sqlite") {
		return darwin.SqliteDialect{}
	} else if strings.Contains(driver, "postgres") {
		return darwin.PostgresDialect{}
	}
	return darwin.MySQLDialect{}
}

func sqliteErr(err error, num sqlite3.ErrNo) bool {
	if err != nil {
		serr, ok := err.(sqlite3.Error)
		if ok && serr.Code == num {
			return true
		}
	}
	return false
}
