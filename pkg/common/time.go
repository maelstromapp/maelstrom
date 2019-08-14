package common

import "time"

func NowMillis() int64 {
	return TimeToMillis(time.Now())
}

func TimeToMillis(t time.Time) int64 {
	return t.UnixNano() / 1e6
}

func MillisToTime(millis int64) time.Time {
	return time.Unix(0, millis*1e6)
}
