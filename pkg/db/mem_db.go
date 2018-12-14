package db

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

func NewMemDb() *MemDb {
	return &MemDb{
		vals: make(map[string]EntityVal),
		lock: &sync.Mutex{},
	}
}

type MemDb struct {
	vals map[string]EntityVal
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
	d.vals[k] = EntityVal{
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
			Type: input.Type,
			Value: EntityVal{
				Key:     input.Key,
				Version: v.Version,
				Value:   v.Value,
			},
		}, nil
	}
	return GetOutput{}, NotFound
}

func (d *MemDb) List(input ListInput) (ListOutput, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	var prefix string
	if input.NamePrefix == "" {
		prefix = fmt.Sprintf("%d,", input.Type)
	} else {
		prefix = fmt.Sprintf("%d,%s", input.Type, input.NamePrefix)
	}

	keys := make([]string, 0)
	for k := range d.vals {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}

	sort.Strings(keys)
	start := 0
	if input.NextToken != "" {
		s, err := strconv.Atoi(input.NextToken)
		if err == nil {
			start = s
		}
	}

	limit := 1000
	if input.Limit > 0 && input.Limit < 1000 {
		limit = int(input.Limit)
	}

	out := ListOutput{Values: make([]EntityVal, 0)}

	cur := start
	for ; cur < len(keys) && len(out.Values) < limit; cur++ {
		k := keys[cur]
		out.Values = append(out.Values, d.vals[k])
	}

	if cur < len(keys) {
		out.NextToken = strconv.Itoa(cur)
	}

	return out, nil
}
