package clone

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffDelayBounds(t *testing.T) {
	const (
		base    = 100 * time.Millisecond
		ceiling = 400 * time.Millisecond
	)
	want := []time.Duration{base, 2 * base, ceiling, ceiling, ceiling}
	for attempt, floor := range want {
		got := backoffDelay(attempt+1, base, ceiling)
		limit := floor + floor/jitterDivisor
		if got < floor || got >= limit {
			t.Errorf("backoffDelay(%d) = %v, want [%v, %v)", attempt+1, got, floor, limit)
		}
	}
}

func TestBackoffDelayIsJittered(t *testing.T) {
	const (
		base    = 100 * time.Millisecond
		ceiling = time.Second
		samples = 200
	)
	lowest, highest := time.Duration(1<<62), time.Duration(0)
	for range samples {
		delay := backoffDelay(1, base, ceiling)
		if delay < base || delay >= base+base/jitterDivisor {
			t.Fatalf("jittered delay %v outside [%v, %v)", delay, base, base+base/jitterDivisor)
		}
		lowest = min(lowest, delay)
		highest = max(highest, delay)
	}
	if spread := highest - lowest; spread < base/20 {
		t.Errorf("%d samples spanned only %v (%v..%v)", samples, spread, lowest, highest)
	}
}

func TestSleepReturnsOnContextEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleep on a canceled context = %v, want context.Canceled", err)
	}
	if err := sleep(context.Background(), time.Nanosecond); err != nil {
		t.Errorf("sleep for a short delay = %v, want nil", err)
	}
}
