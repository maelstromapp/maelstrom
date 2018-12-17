package db

import (
	"database/sql"
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
	db     *sql.DB
	driver string
}

func (d *SqlDb) Close() {
	err := d.db.Close()
	if err != nil {
		log.Printf("ERROR closing db: %v", err)
	}
}

func (d *SqlDb) DeleteAll() error {
	tables := []string{"component"}
	for _, t := range tables {
		_, err := d.db.Exec(fmt.Sprintf("delete from %s", t))
		if err != nil {
			return fmt.Errorf("Unable to delete from: %s - %v", t, err)
		}
	}
	return nil
}

func (d *SqlDb) Put(input PutInput) (output PutOutput, err error) {
	var result sql.Result
	now := common.NowMillis()
	newVersion := int64(1)
	if input.PreviousVersion == 0 {
		result, err = squirrel.Insert("component").
			Columns("name", "version", "createdAt", "modifiedAt", "json").
			Values(input.Key, 1, now, now, input.Value).
			RunWith(d.db).Exec()
		if err != nil {
			_, getErr := d.Get(GetInput{Type: input.Type, Key: input.Key})
			if getErr == nil {
				err = AlreadyExists
			}
		}
	} else {
		newVersion = input.PreviousVersion + 1
		result, err = squirrel.Update("component").
			Set("modifiedAt", now).
			Set("json", input.Value).
			Set("version", newVersion).
			Where(squirrel.Eq{"name": input.Key, "version": input.PreviousVersion}).
			RunWith(d.db).Exec()
		if err == nil {
			rows, err2 := result.RowsAffected()
			if err2 != nil {
				log.Printf("WARNING: Put update RowsAffected returned err: %v", err)
			}
			if rows != 1 {
				err = IncorrectPreviousVersion
			}
		}
	}

	if err == nil {
		output.Key = input.Key
		output.Type = input.Type
		output.NewVersion = newVersion
	}
	return
}

func (d *SqlDb) Get(input GetInput) (out GetOutput, err error) {
	var rows *sql.Rows
	rows, err = squirrel.Select("name", "version", "json").
		From("component").
		Where(squirrel.Eq{"name": input.Key}).
		RunWith(d.db).Query()
	if err != nil {
		return
	}
	defer common.CheckClose(rows, &err)
	if rows.Next() {
		err = rows.Scan(&out.Value.Key, &out.Value.Version, &out.Value.Value)
	} else {
		err = NotFound
	}
	return
}

func (d *SqlDb) List(input ListInput) (out ListOutput, err error) {
	q := squirrel.Select("name", "version", "json").From("component").OrderBy("name")
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
	out.Values = []EntityVal{}
	if err != nil {
		return
	}
	defer common.CheckClose(rows, &err)
	for len(out.Values) < limit && rows.Next() {
		var val EntityVal
		err = rows.Scan(&val.Key, &val.Version, &val.Value)
		if err == nil {
			out.Values = append(out.Values, val)
		}
	}
	if rows.Next() {
		out.NextToken = strconv.Itoa(offset + len(out.Values))
	}
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
