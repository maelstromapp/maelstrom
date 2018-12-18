package v1

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/GuiaBolso/darwin"
	"github.com/Masterminds/squirrel"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"log"
	"strconv"
	"strings"
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
		log.Printf("ERROR closing db: %v", err)
	}
}

func (d *SqlDb) DeleteAll() error {
	tables := []string{"component", "eventsource"}
	for _, t := range tables {
		_, err := d.db.Exec(fmt.Sprintf("delete from %s", t))
		if err != nil {
			return fmt.Errorf("Unable to delete from: %s - %v", t, err)
		}
	}
	return nil
}

func (d *SqlDb) PutComponent(component Component) (int64, error) {
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
			[]string{"name", "version", "createdAt", "modifiedAt", "json"},
			[]interface{}{component.Name, 1, now, now, jsonVal})
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

func (d *SqlDb) GetComponent(componentName string) (component Component, err error) {
	err = d.getRow("component", componentName, &component)
	return
}

func (d *SqlDb) ListComponents(input ListComponentsInput) (components []Component, nextToken string, err error) {
	q := squirrel.Select("version", "json").From("component").OrderBy("name")
	if input.NamePrefix != "" {
		q = q.Where(squirrel.Like{"name": input.NamePrefix + "%"})
	}

	offset := 0
	if input.NextToken != "" {
		o, err := strconv.Atoi(input.NextToken)
		if err == nil {
			offset = o
		}
	}

	limit := 1000
	if input.Limit > 0 && input.Limit < 1000 {
		limit = int(input.Limit)
	}

	var rows *sql.Rows
	rows, err = q.Offset(uint64(offset)).Limit(uint64(limit + 1)).RunWith(d.db).Query()
	if err != nil {
		err = fmt.Errorf("ListComponents err in query: %v", err)
		return
	}

	components = []Component{}
	defer common.CheckClose(rows, &err)
	for len(components) < limit && rows.Next() {
		var version int64
		var jsonVal []byte
		err = rows.Scan(&version, &jsonVal)
		if err != nil {
			err = fmt.Errorf("ListComponents err in scan: %v", err)
			return
		}
		var comp Component
		err = json.Unmarshal(jsonVal, &comp)
		if err != nil {
			err = fmt.Errorf("ListComponents err in json unmarshal: %v", err)
			return
		}
		components = append(components, comp)
	}
	if rows.Next() {
		nextToken = strconv.Itoa(offset + len(components))
	}
	return
}

func (d *SqlDb) RemoveComponent(componentName string) (found bool, err error) {
	_, err = squirrel.Delete("eventsource").Where(squirrel.Eq{"componentName": componentName}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete eventsource by componentName failed: %s err: %v", componentName, err)
		return
	}
	return d.removeRow("component", componentName)
}

func (d *SqlDb) PutEventSource(eventSource EventSource) (int64, error) {
	now := common.NowMillis()
	previousVersion := eventSource.Version
	eventSource.Version += 1
	eventSource.ModifiedAt = now
	jsonVal, err := json.Marshal(eventSource)
	if err != nil {
		return 0, fmt.Errorf("PutEventSource unable to marshal JSON: %v", err)
	}

	if previousVersion == 0 {
		err = d.insertRow("eventsource", eventSource.Name, eventSource,
			[]string{"name", "componentName", "version", "createdAt", "modifiedAt", "json"},
			[]interface{}{eventSource.Name, eventSource.ComponentName, 1, now, now, jsonVal})
	} else {
		err = d.updateRow("eventsource", eventSource.Name, previousVersion, map[string]interface{}{
			"modifiedAt":    now,
			"componentName": eventSource.ComponentName,
			"json":          jsonVal,
			"version":       eventSource.Version,
		})
	}

	if err != nil {
		return 0, err
	}
	return eventSource.Version, nil
}

func (d *SqlDb) GetEventSource(eventSourceName string) (es EventSource, err error) {
	err = d.getRow("eventsource", eventSourceName, &es)
	return
}

func (d *SqlDb) RemoveEventSource(eventSourceName string) (bool, error) {
	return d.removeRow("eventsource", eventSourceName)
}

func (d *SqlDb) insertRow(table string, name string, val interface{}, columns []string, bindVals []interface{}) error {
	q := squirrel.Insert(table).Columns(columns...).Values(bindVals...)
	if d.DebugLog {
		log.Printf("SQL: %v", squirrel.DebugSqlizer(q))
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
		log.Printf("SQL: %v", squirrel.DebugSqlizer(q))
	}
	result, err := q.RunWith(d.db).Exec()
	if err != nil {
		return fmt.Errorf("updateRow failed for table: %s name: %s err: %v", table, name, err)
	}
	rows, err2 := result.RowsAffected()
	if err2 != nil {
		log.Printf("WARNING: Put update RowsAffected returned err: %v", err)
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
	var result sql.Result
	var rows int64
	result, err = squirrel.Delete(table).Where(squirrel.Eq{"name": name}).RunWith(d.db).Exec()
	if err != nil {
		err = fmt.Errorf("delete %s failed: %s err: %v", table, name, err)
		return
	}
	rows, err = result.RowsAffected()
	if err != nil {
		err = fmt.Errorf("delete %s rowsaffected failed: %s err: %v", table, name, err)
		return
	}
	found = rows > 0
	return
}

func (d *SqlDb) Migrate() error {
	migrations := []darwin.Migration{
		{
			Version:     1,
			Description: "Create component table",
			Script: `create table component (
                        name        varchar(60) primary key,
                        version     int not null,
                        createdAt   bigint not null,
                        modifiedAt  bigint not null,
                        json        mediumblob not null
                     )`,
		},
		{
			Version:     2,
			Description: "Create eventsource table",
			Script: `create table eventsource (
                        name           varchar(60) primary key,
                        componentName  varchar(60) not null,
                        version        int not null,
                        createdAt      bigint not null,
                        modifiedAt     bigint not null,
                        json           mediumblob not null
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
