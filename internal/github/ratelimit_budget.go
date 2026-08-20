package github

import (
	"context"
	"fmt"
	"time"

	gogithub "github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/logging"
)

// RedeliveryBudgetFromRemaining returns how many REST calls the receiver may
// make in this hourly window (50% of remaining, at least 1 if any remain).
func RedeliveryBudgetFromRemaining(remaining int) int {
	if remaining <= 0 {
		return 0
	}
	budget := remaining / 2
	if budget < 1 {
		return 1
	}
	return budget
}

// RedeliveryBudget tracks 50% of the current GitHub primary rate-limit window.
type RedeliveryBudget struct {
	budget  int
	spent   int
	resetAt time.Time
}

func (b *RedeliveryBudget) Budget() int {
	if b == nil {
		return 0
	}
	return b.budget
}

func (b *RedeliveryBudget) ResetAt() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.resetAt
}

func (b *RedeliveryBudget) StartWindow(remaining int, resetAt time.Time) {
	b.budget = RedeliveryBudgetFromRemaining(remaining)
	b.spent = 0
	b.resetAt = resetAt
}

func (b *RedeliveryBudget) CanSpend(n int) bool {
	if b == nil || n <= 0 {
		return true
	}
	return b.spent+n <= b.budget
}

func (b *RedeliveryBudget) Record(n int) {
	if b == nil || n <= 0 {
		return
	}
	b.spent += n
}

func (b *RedeliveryBudget) ApplyRate(rate gogithub.Rate) {
	if b == nil {
		return
	}
	if !rate.Reset.IsZero() {
		b.resetAt = rate.Reset.Time
	}
}

// RefreshFromAPI reads GET /rate_limit (does not count against the primary limit)
// and starts a new 50% window.
func (b *RedeliveryBudget) RefreshFromAPI(ctx context.Context, client *gogithub.Client) error {
	if client == nil {
		return fmt.Errorf("github client is nil")
	}
	limits, _, err := client.RateLimit.Get(ctx)
	if err != nil {
		return fmt.Errorf("getting GitHub rate limit: %w", err)
	}
	remaining := 0
	resetAt := time.Time{}
	if limits != nil && limits.Core != nil {
		remaining = limits.Core.Remaining
		resetAt = limits.Core.Reset.Time
	}
	b.StartWindow(remaining, resetAt)
	return nil
}

// Reserve waits for the next GitHub reset when n calls would exceed the 50%
// budget, then starts a new window. n is the worst-case size of the next unit
// of work (one list page, or one delivery's Get + optional job lookup + POST).
func (b *RedeliveryBudget) Reserve(workerCtx, pluginCtx context.Context, client *gogithub.Client, n int) error {
	if b == nil || n <= 0 {
		return nil
	}
	if b.budget == 0 && b.spent == 0 && b.resetAt.IsZero() {
		if err := b.RefreshFromAPI(pluginCtx, client); err != nil {
			return err
		}
	}
	for {
		if workerCtx.Err() != nil {
			return workerCtx.Err()
		}
		if pluginCtx.Err() != nil {
			return pluginCtx.Err()
		}
		if b.budget == 0 {
			if b.resetAt.IsZero() || !time.Now().Before(b.resetAt) {
				if err := b.RefreshFromAPI(pluginCtx, client); err != nil {
					return err
				}
				if b.budget > 0 && b.CanSpend(n) {
					return nil
				}
			}
			resetAt := b.resetAt
			if resetAt.IsZero() {
				resetAt = time.Now().Add(time.Second)
			}
			logging.Info(pluginCtx, "redelivery budget empty, waiting for GitHub API rate limit reset",
				"resetAt", resetAt.Format(time.RFC3339),
				"needed", n,
			)
			if err := waitForGitHubAPIRateLimitReset(workerCtx, pluginCtx, resetAt); err != nil {
				return err
			}
			if err := b.RefreshFromAPI(pluginCtx, client); err != nil {
				return err
			}
			continue
		}
		if b.CanSpend(n) {
			return nil
		}
		resetAt := b.resetAt
		if resetAt.IsZero() || !time.Now().Before(resetAt) {
			if err := b.RefreshFromAPI(pluginCtx, client); err != nil {
				return err
			}
			if b.CanSpend(n) {
				return nil
			}
			if b.resetAt.IsZero() {
				resetAt = time.Now().Add(time.Second)
			} else {
				resetAt = b.resetAt
			}
		}
		logging.Info(pluginCtx, "redelivery would exceed 50% of remaining GitHub API quota, waiting for reset",
			"resetAt", resetAt.Format(time.RFC3339),
			"budget", b.budget,
			"spent", b.spent,
			"needed", n,
		)
		if err := waitForGitHubAPIRateLimitReset(workerCtx, pluginCtx, resetAt); err != nil {
			return err
		}
		if err := b.RefreshFromAPI(pluginCtx, client); err != nil {
			return err
		}
	}
}
