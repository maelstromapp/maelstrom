package common

func MaxInt64(vals ...int64) int64 {
	if len(vals) == 0 {
		return 0
	}

	max := vals[0]
	for i := 1; i < len(vals); i++ {
		if vals[i] > max {
			max = vals[i]
		}
	}
	return max
}

func DefaultInt64(v int64, defaultVal int64) int64 {
	if v == 0 {
		return defaultVal
	}
	return v
}
