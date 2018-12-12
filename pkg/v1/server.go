package v1

import (
	"encoding/json"
	"github.com/coopernurse/barrister-go"
	"gitlab.com/coopernurse/maelstrom/pkg/db"
	"go.uber.org/zap"
	"regexp"
	"strings"
)

var _ MaelstromService = (*V1)(nil)
var componentNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type ErrorCode int

const (
	MiscError ErrorCode = -32000
	DbError             = -32001
)

func NewV1(db db.Db) *V1 {
	log, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	return &V1{
		db:  db,
		log: log,
	}
}

type V1 struct {
	log *zap.Logger
	db  db.Db
}

func (v *V1) onError(code ErrorCode, msg string, err error) error {
	v.log.Error(msg, zap.Error(err))
	return &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func (v *V1) PutComponent(input PutComponentInput) (PutComponentOutput, error) {
	// * 1001 - input.name is invalid
	name := strings.TrimSpace(input.Name)
	invalidNameMsg := ""
	if name == "" {
		invalidNameMsg = "PutComponentInput.name is required"
	} else if !componentNameRE.MatchString(name) {
		invalidNameMsg = "PutComponentInput.name is invalid (only alpha-numeric chars, _ - are valid)"
	}
	if invalidNameMsg != "" {
		return PutComponentOutput{}, &barrister.JsonRpcError{Code: 1001, Message: invalidNameMsg}
	}

	// Convert to Component JSON
	val, err := json.Marshal(PutInputToComponent(input))
	if err != nil {
		return PutComponentOutput{}, v.onError(MiscError, "Error serializing Component as JSON", err)
	}

	// Save component to db
	dbOut, err := v.db.Put(db.PutInput{
		Type:            db.Component,
		Key:             name,
		PreviousVersion: input.PreviousVersion,
		Value:           val,
	})
	if err != nil {
		if err == db.IncorrectPreviousVersion {
			return PutComponentOutput{},
				&barrister.JsonRpcError{Code: 1004, Message: "Component previousVersion is incorrect"}
		} else if err == db.AlreadyExists {
			return PutComponentOutput{},
				&barrister.JsonRpcError{Code: 1002, Message: "Component already exists with name: " + input.Name}
		}
		return PutComponentOutput{}, v.onError(DbError, "Error in db.Put", err)
	}

	return PutComponentOutput{
		Name:    name,
		Version: dbOut.NewVersion,
	}, nil
}

func (v *V1) GetComponent(input GetComponentInput) (GetComponentOutput, error) {
	dbOut, err := v.db.Get(db.GetInput{
		Type: db.Component,
		Key:  input.Name,
	})
	if err != nil {
		if err == db.NotFound {
			return GetComponentOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No Component found with name: " + input.Name}
		}
		return GetComponentOutput{}, v.onError(DbError, "Error in db.Get", err)
	}

	var c Component
	err = json.Unmarshal(dbOut.Value, &c)
	if err != nil {
		return GetComponentOutput{}, v.onError(MiscError, "Error deserializing Component JSON", err)
	}

	return GetComponentOutput{
		Name:    c.Name,
		Version: dbOut.Version,
		Docker:  c.DockerComponent,
	}, nil
}
