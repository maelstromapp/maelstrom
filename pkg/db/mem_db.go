package db

import (
	"fmt"
	"sync"
)

type Entity struct {
	Key     string
	Version int64
	Value   []byte
}

func NewMemDb() *MemDb {
	return &MemDb{
		vals: make(map[string]Entity),
		lock: &sync.Mutex{},
	}
}

type MemDb struct {
	vals map[string]Entity
	lock *sync.Mutex
}

func (d *MemDb) Put(input PutInput) (PutOutput, error) {
	k := fmt.Sprintf("%d,%s", input.Type, input.Key)
	d.lock.Lock()
	defer d.lock.Unlock()

	v, ok := d.vals[k]
	ver := int64(0)
	if ok {
		ver = v.Version
		if input.PreviousVersion == 0 {
			return PutOutput{}, AlreadyExists
		}
		if ver != input.PreviousVersion {
			return PutOutput{}, IncorrectPreviousVersion
		}
	} else if input.PreviousVersion != 0 {
		return PutOutput{}, IncorrectPreviousVersion
	}
	ver++
	d.vals[k] = Entity{
		Key:     input.Key,
		Version: ver,
		Value:   input.Value,
	}
	return PutOutput{
		Type:       input.Type,
		Key:        input.Key,
		NewVersion: ver,
	}, nil
}

func (d *MemDb) Get(input GetInput) (GetOutput, error) {
	k := fmt.Sprintf("%d,%s", input.Type, input.Key)
	d.lock.Lock()
	defer d.lock.Unlock()

	v, ok := d.vals[k]
	if ok {
		return GetOutput{
			Type:    input.Type,
			Key:     input.Key,
			Version: v.Version,
			Value:   v.Value,
		}, nil
	}
	return GetOutput{}, NotFound
}
