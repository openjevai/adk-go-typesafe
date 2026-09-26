package typesafe

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultAttempts   = 3
	defaultBackoff    = 250 * time.Millisecond
	defaultMaxBackoff = 5 * time.Second
	overloadedStatus  = 529
	// OpenJEV returns 503 for temporary unavailability, retryable alongside 429 and 529.
	serviceUnavailableStatus = 503
)

// RetryPolicy bounds retries for explicit rate-limit, overload, and temporary
// unavailability responses. A zero policy enables three total attempts with
// 250ms initial and 5s maximum backoff. Backoff doubles with jitter. A valid
// Retry-After is a minimum delay; if it exceeds MaxBackoff the original
// APIError is returned without retrying. HTTP 429, 503, and 529 are retried;
// transport errors and other statuses are not. Use a context deadline to bound
// the entire evaluation, including a custom HTTP client's requests.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func retryPolicy(policy *RetryPolicy) (RetryPolicy, error) {
	if policy == nil {
		return RetryPolicy{MaxAttempts: 1}, nil
	}
	p := *policy
	if p.MaxAttempts < 0 || p.InitialBackoff < 0 || p.MaxBackoff < 0 {
		return RetryPolicy{}, errors.New("typesafe: retry limits must not be negative")
	}
	if p.MaxAttempts == 0 {
		p.MaxAttempts = defaultAttempts
	}
	if p.InitialBackoff == 0 {
		p.InitialBackoff = defaultBackoff
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = defaultMaxBackoff
	}
	if p.InitialBackoff > p.MaxBackoff {
		return RetryPolicy{}, errors.New("typesafe: initial retry backoff exceeds maximum")
	}
	return p, nil
}

func (c *Client) sendWithRetry(ctx context.Context, body []byte) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		data, err := c.send(ctx, body)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || attempt >= c.retry.MaxAttempts ||
			(apiErr.StatusCode != http.StatusTooManyRequests &&
				apiErr.StatusCode != overloadedStatus &&
				apiErr.StatusCode != serviceUnavailableStatus) {
			return data, err
		}
		delay, retry := c.retry.delay(attempt, apiErr.RetryAfter)
		if !retry {
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p RetryPolicy) delay(attempt int, retryAfter string) (time.Duration, bool) {
	limit := p.InitialBackoff
	for i := 1; i < attempt && limit < p.MaxBackoff; i++ {
		if limit > p.MaxBackoff/2 {
			limit = p.MaxBackoff
		} else {
			limit *= 2
		}
	}
	//nolint:gosec // Backoff jitter does not require cryptographic randomness.
	delay := limit/2 + time.Duration(rand.Int64N(int64(limit-limit/2)+1))
	minimum, valid := retryAfterDelay(retryAfter)
	if valid && minimum > p.MaxBackoff {
		return 0, false
	}
	if valid && minimum > delay {
		delay = minimum
	}
	return delay, true
}

func retryAfterDelay(value string) (time.Duration, bool) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err == nil && seconds >= 0 {
		const maxSeconds = int64((1<<63 - 1) / time.Second)
		if seconds > maxSeconds {
			return time.Duration(1<<63 - 1), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if errors.Is(err, strconv.ErrRange) && len(value) > 0 && value[0] != '-' {
		return time.Duration(1<<63 - 1), true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(0, time.Until(when)), true
}
