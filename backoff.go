package clone

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	backoffFactor = 2
	jitterDivisor = 4
)

func backoffDelay(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	delay := baseDelay
	for range attempt - 1 {
		delay *= backoffFactor
		if delay >= maxDelay {
			return jitter(maxDelay)
		}
	}
	return jitter(delay)
}

func jitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	spread := delay / jitterDivisor
	if spread <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int64N(int64(spread)))
}

func sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
