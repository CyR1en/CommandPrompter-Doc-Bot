package safenet

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"
)

func RetryDelay(headers map[string]string, retryIndex int, now time.Time) (time.Duration, error) {
	var delay *float64
	if raw := headers["retry-after-ms"]; raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			seconds := parsed / 1_000
			delay = &seconds
		}
	}
	if delay == nil {
		if raw := headers["retry-after"]; raw != "" {
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
				delay = &parsed
			} else if requestedAt, dateErr := http.ParseTime(raw); dateErr == nil {
				seconds := requestedAt.Sub(now).Seconds()
				delay = &seconds
			}
		}
	}
	if delay == nil {
		backoff := 500 * time.Millisecond * time.Duration(1<<min(retryIndex, 30))
		return min(backoff, 8*time.Second), nil
	}
	if math.IsNaN(*delay) || math.IsInf(*delay, 0) || *delay > time.Minute.Seconds() {
		return 0, errors.New("provider retry delay exceeds the safe limit")
	}
	return time.Duration(max(0, *delay) * float64(time.Second)), nil
}
