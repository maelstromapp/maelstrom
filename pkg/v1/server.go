package v1

import (
	"github.com/coopernurse/barrister-go"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"go.uber.org/zap"
	"regexp"
	"strings"
)

var _ MaelstromService = (*V1)(nil)
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type ErrorCode int

const (
	MiscError ErrorCode = -32000
	DbError             = -32001
)

func newRpcErr(code int, msg string) error {
	return &barrister.JsonRpcError{Code: code, Message: msg}
}

func nameValid(errPrefix string, name string) (string, error) {
	name = strings.TrimSpace(name)
	invalidNameMsg := ""
	if name == "" {
		invalidNameMsg = errPrefix + " is required"
	} else if len(name) > 60 {
		invalidNameMsg = errPrefix + " exceeds max length of 60 bytes"
	} else if !nameRE.MatchString(name) {
		invalidNameMsg = errPrefix + " is invalid (only alpha-numeric chars, _ - are valid)"
	}

	if invalidNameMsg != "" {
		return "", newRpcErr(1001, invalidNameMsg)
	}
	return name, nil
}

func NewV1(db Db) *V1 {
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
	db  Db
}

func (v *V1) onError(code ErrorCode, msg string, err error) error {
	v.log.Error(msg, zap.Error(err))
	return &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func (v *V1) transformPutError(prefix string, err error) error {
	if err != nil {
		if err == IncorrectPreviousVersion {
			return &barrister.JsonRpcError{Code: 1004, Message: prefix + ": previousVersion is incorrect"}
		} else if err == AlreadyExists {
			return &barrister.JsonRpcError{Code: 1002, Message: prefix + ": already exists with key"}
		}
		return v.onError(DbError, "Error in "+prefix, err)
	}
	return nil
}

func (v *V1) PutComponent(input PutComponentInput) (PutComponentOutput, error) {
	// * 1001 - input.name is invalid
	name, err := nameValid("input.name", input.Component.Name)
	if err != nil {
		return PutComponentOutput{}, err
	}

	// Save component to db
	newVersion, err := v.db.PutComponent(input.Component)
	if err != nil {
		return PutComponentOutput{}, v.transformPutError("PutComponent", err)
	}

	return PutComponentOutput{
		Name:    name,
		Version: newVersion,
	}, nil
}

func (v *V1) GetComponent(input GetComponentInput) (GetComponentOutput, error) {
	c, err := v.db.GetComponent(input.Name)
	if err != nil {
		if err == NotFound {
			return GetComponentOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No Component found with name: " + input.Name}
		}
		return GetComponentOutput{}, v.onError(DbError, "Error in db.GetComponent", err)
	}

	return GetComponentOutput{Component: c}, nil
}

func (v *V1) ListComponents(input ListComponentsInput) (ListComponentsOutput, error) {
	components, nextToken, err := v.db.ListComponents(input)
	if err != nil {
		return ListComponentsOutput{}, v.onError(DbError, "Error in db.ListComponents", err)
	}
	return ListComponentsOutput{Components: components, NextToken: nextToken}, nil
}

func (v *V1) RemoveComponent(input RemoveComponentInput) (RemoveComponentOutput, error) {
	// * 1001 - input.name is invalid
	name, err := nameValid("input.name", input.Name)
	if err != nil {
		return RemoveComponentOutput{}, err
	}

	found, err := v.db.RemoveComponent(name)
	return RemoveComponentOutput{
		Name:  name,
		Found: found,
	}, err
}

func (v *V1) PutEventSource(input PutEventSourceInput) (PutEventSourceOutput, error) {
	// * 1001 - input.name is invalid
	name, err := nameValid("input.name", input.EventSource.Name)
	if err != nil {
		return PutEventSourceOutput{}, err
	}
	// * 1001 - input.componentName is invalid
	_, err = nameValid("input.componentName", input.EventSource.ComponentName)
	if err != nil {
		return PutEventSourceOutput{}, err
	}

	// * 1001 - No event source sub-type is provided
	if input.EventSource.Http == nil {
		return PutEventSourceOutput{}, newRpcErr(1001, "No event source sub-type provided (e.g. http)")
	}

	// * 1001 - input.http is provided but has no hostname or pathPrefix
	if input.EventSource.Http != nil {
		if input.EventSource.Http.Hostname == "" && input.EventSource.Http.PathPrefix == "" {
			return PutEventSourceOutput{}, newRpcErr(1001, "http.hostname or http.pathPrefix must be provided")
		}
	}

	// * 1003 - input.componentName does not reference a component defined in the system
	_, err = v.db.GetComponent(input.EventSource.ComponentName)
	if err == NotFound {
		return PutEventSourceOutput{},
			newRpcErr(1003, "eventSource.component not found: "+input.EventSource.ComponentName)
	}

	input.EventSource.Name = name
	input.EventSource.ModifiedAt = common.NowMillis()
	newVersion, err := v.db.PutEventSource(input.EventSource)
	if err != nil {
		return PutEventSourceOutput{}, v.transformPutError("PutEventSource", err)
	}

	return PutEventSourceOutput{Name: name, Version: newVersion}, nil
}

func (v *V1) GetEventSource(input GetEventSourceInput) (GetEventSourceOutput, error) {
	es, err := v.db.GetEventSource(input.Name)
	if err != nil {
		if err == NotFound {
			return GetEventSourceOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No event source found with name: " + input.Name}
		}
		return GetEventSourceOutput{}, v.onError(DbError, "Error in db.GetEventSource", err)
	}
	return GetEventSourceOutput{EventSource: es}, nil
}

func (v *V1) RemoveEventSource(input RemoveEventSourceInput) (RemoveEventSourceOutput, error) {
	// * 1001 - input.name is invalid
	name, err := nameValid("input.name", input.Name)
	if err != nil {
		return RemoveEventSourceOutput{}, err
	}

	found, err := v.db.RemoveEventSource(name)
	return RemoveEventSourceOutput{
		Name:  name,
		Found: found,
	}, err
}
