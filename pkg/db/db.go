package db

import "fmt"

type EntityType int

const (
	Component EntityType = iota
)

var NotFound = fmt.Errorf("Not Found")
var AlreadyExists = fmt.Errorf("Entity already exists")
var IncorrectPreviousVersion = fmt.Errorf("Incorrect PreviousVersion")

type Db interface {
	Put(input PutInput) (PutOutput, error)
	Get(input GetInput) (GetOutput, error)
}

type PutInput struct {
	Type            EntityType
	Key             string
	PreviousVersion int64
	Value           []byte
}

type PutOutput struct {
	Type       EntityType
	Key        string
	NewVersion int64
}

type GetInput struct {
	Type EntityType
	Key  string
}

type GetOutput struct {
	Type    EntityType
	Key     string
	Version int64
	Value   []byte
}
