package database

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

type Database struct {
	Client             redis.Cmdable // Interface that both *redis.Client and *redis.ClusterClient implement
	MaxRetries         int
	RetryDelay         time.Duration
	RetryBackoffFactor float64
}

// isRetriableError determines if an error is worth retrying.
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "dial tcp") ||
		strings.Contains(errStr, "i/o timeout")
}

func safeLogWarn(ctx context.Context, msg string) {
	if _, err := logging.GetLoggerFromContext(ctx); err == nil {
		logging.Warn(ctx, msg)
	}
}

func safeLogInfo(ctx context.Context, msg string) {
	if _, err := logging.GetLoggerFromContext(ctx); err == nil {
		logging.Info(ctx, msg)
	}
}

func safeLogDebug(ctx context.Context, msg string) {
	if _, err := logging.GetLoggerFromContext(ctx); err == nil {
		logging.Debug(ctx, msg)
	}
}

// retryOperation performs an operation with retry logic.
func (db *Database) retryOperation(ctx context.Context, operationName string, operation func() error) error {
	var lastErr error

	maxRetries := db.MaxRetries
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		timeRemaining := time.Until(deadline)
		if timeRemaining < 10*time.Second {
			maxRetries = 0
		} else if timeRemaining < 30*time.Second {
			maxRetries = 1
		}
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled before %s: %w", operationName, ctx.Err())
		default:
		}

		if attempt > 0 {
			delay := time.Duration(float64(db.RetryDelay) * float64(attempt) * db.RetryBackoffFactor)
			if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
				if delay > time.Until(deadline) {
					return fmt.Errorf("insufficient time remaining for retry of %s: %w", operationName, lastErr)
				}
			}

			safeLogWarn(ctx, fmt.Sprintf("retrying %s in %v (attempt %d/%d)", operationName, delay, attempt, maxRetries))

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return fmt.Errorf("context canceled during retry for %s: %w", operationName, ctx.Err())
			case <-timer.C:
				if ctx.Err() != nil {
					return fmt.Errorf("context canceled during retry for %s: %w", operationName, ctx.Err())
				}
			}
		}

		lastErr = operation()

		if ctx.Err() != nil {
			return fmt.Errorf("context canceled after %s operation: %w", operationName, ctx.Err())
		}

		if lastErr == nil {
			if attempt > 0 {
				safeLogInfo(ctx, fmt.Sprintf("%s succeeded after %d retries", operationName, attempt))
			}
			return nil
		}

		if !isRetriableError(lastErr) {
			return lastErr
		}

		if attempt < maxRetries {
			safeLogDebug(ctx, fmt.Sprintf("%s failed (attempt %d/%d): %v", operationName, attempt+1, maxRetries+1, lastErr))
		}
	}

	return fmt.Errorf("%s failed after %d retries: %w", operationName, maxRetries, lastErr)
}

func retryResult[T any](db *Database, ctx context.Context, operationName string, op func() (T, error)) (T, error) {
	var result T
	err := db.retryOperation(ctx, operationName, func() error {
		val, opErr := op()
		if opErr != nil {
			return opErr
		}
		result = val
		return nil
	})
	return result, err
}

func buildTLSConfig(cfg config.Database) *tls.Config {
	if !cfg.TLSEnabled {
		return nil
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLSInsecure, //nolint:gosec // Explicitly opt-in via config tls_insecure
	}
}

func NewClient(ctx context.Context, dbConfig config.Database) (*Database, error) {
	// Use -1 to explicitly request 0 retries (for cleanup operations).
	// Use 0 or unset to get default of 5 retries.
	maxRetries := dbConfig.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	} else if maxRetries == 0 {
		maxRetries = 5
	}

	retryDelay := time.Duration(dbConfig.RetryDelay) * time.Millisecond
	if retryDelay <= 0 {
		retryDelay = 1000 * time.Millisecond
	}

	retryBackoffFactor := dbConfig.RetryBackoffFactor
	if retryBackoffFactor <= 0 {
		retryBackoffFactor = 2.0
	}

	tlsConfig := buildTLSConfig(dbConfig)
	addr := fmt.Sprintf("%s:%d", dbConfig.URL, dbConfig.Port)

	var rdb redis.Cmdable
	if dbConfig.ClusterMode {
		clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:     []string{addr},
			Username:  dbConfig.User,
			Password:  dbConfig.Password,
			TLSConfig: tlsConfig,
		})
		rdb = clusterClient
		logging.Dev(ctx, "created redis cluster client", "addr", addr)
	} else {
		regularClient := redis.NewClient(&redis.Options{
			Addr:      addr,
			Username:  dbConfig.User,
			Password:  dbConfig.Password,
			DB:        dbConfig.Database,
			TLSConfig: tlsConfig,
		})
		rdb = regularClient
		logging.Dev(ctx, "created redis client", "addr", addr, "db", dbConfig.Database)
	}

	db := &Database{
		Client:             rdb,
		MaxRetries:         maxRetries,
		RetryDelay:         retryDelay,
		RetryBackoffFactor: retryBackoffFactor,
	}

	err := db.retryOperation(ctx, "initial connection ping", func() error {
		logging.Dev(ctx, "pinging redis client")
		pong, pingErr := rdb.Ping(ctx).Result()
		if pingErr != nil {
			return fmt.Errorf("error pinging redis client: %w", pingErr)
		}
		logging.Dev(ctx, "pinged redis client", "response", pong)
		if pong != "PONG" {
			return fmt.Errorf("unable to connect to Redis, received: %s", pong)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return db, nil
}

func GetDatabaseFromContext(ctx context.Context) (*Database, error) {
	db, ok := ctx.Value(config.ContextKey("database")).(*Database)
	if !ok || db == nil {
		return nil, errors.New("GetDatabaseFromContext failed (is your database running and enabled in your config.yml?)")
	}
	return db, nil
}

// Retry wrapper methods for common Redis operations.

func (db *Database) RetryGet(ctx context.Context, key string) (string, error) {
	return retryResult(db, ctx, "get key", func() (string, error) {
		return db.Client.Get(ctx, key).Result()
	})
}

func (db *Database) RetrySet(ctx context.Context, key string, value any, expiration time.Duration) error {
	return db.retryOperation(ctx, "set key", func() error {
		return db.Client.Set(ctx, key, value, expiration).Err()
	})
}

func (db *Database) RetryDel(ctx context.Context, keys ...string) (int64, error) {
	return retryResult(db, ctx, "delete keys", func() (int64, error) {
		return db.Client.Del(ctx, keys...).Result()
	})
}

func (db *Database) RetryExists(ctx context.Context, keys ...string) (int64, error) {
	return retryResult(db, ctx, "check exists", func() (int64, error) {
		return db.Client.Exists(ctx, keys...).Result()
	})
}

func (db *Database) RetryLPush(ctx context.Context, key string, values ...any) (int64, error) {
	return retryResult(db, ctx, "list push", func() (int64, error) {
		return db.Client.LPush(ctx, key, values...).Result()
	})
}

func (db *Database) RetryRPush(ctx context.Context, key string, values ...any) (int64, error) {
	return retryResult(db, ctx, "list rpush", func() (int64, error) {
		return db.Client.RPush(ctx, key, values...).Result()
	})
}

func (db *Database) RetryLRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return retryResult(db, ctx, "list range", func() ([]string, error) {
		return db.Client.LRange(ctx, key, start, stop).Result()
	})
}

func (db *Database) RetryLRem(ctx context.Context, key string, count int64, value any) (int64, error) {
	return retryResult(db, ctx, "list remove", func() (int64, error) {
		return db.Client.LRem(ctx, key, count, value).Result()
	})
}

func (db *Database) RetryLIndex(ctx context.Context, key string, index int64) (string, error) {
	return retryResult(db, ctx, "list index", func() (string, error) {
		return db.Client.LIndex(ctx, key, index).Result()
	})
}

func (db *Database) RetryLLen(ctx context.Context, key string) (int64, error) {
	return retryResult(db, ctx, "list length", func() (int64, error) {
		return db.Client.LLen(ctx, key).Result()
	})
}

func (db *Database) RetryRPopLPush(ctx context.Context, source, destination string) (string, error) {
	return retryResult(db, ctx, "rpop lpush", func() (string, error) {
		return db.Client.RPopLPush(ctx, source, destination).Result()
	})
}

func (db *Database) RetryRPop(ctx context.Context, key string) (string, error) {
	return retryResult(db, ctx, "rpop", func() (string, error) {
		return db.Client.RPop(ctx, key).Result()
	})
}

func (db *Database) RetryScan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	type scanOut struct {
		keys   []string
		cursor uint64
	}
	res, err := retryResult(db, ctx, "scan", func() (scanOut, error) {
		keys, nextCursor, scanErr := db.Client.Scan(ctx, cursor, match, count).Result()
		if scanErr != nil {
			return scanOut{}, scanErr
		}
		return scanOut{keys: keys, cursor: nextCursor}, nil
	})
	return res.keys, res.cursor, err
}

// ScanAllKeys iterates through Redis SCAN until cursor returns 0 and deduplicates keys.
func (db *Database) ScanAllKeys(ctx context.Context, pattern string) ([]string, error) {
	seen := make(map[string]struct{})
	var allKeys []string
	var cursor uint64
	for {
		keys, nextCursor, err := db.RetryScan(ctx, cursor, pattern, 100)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if _, exists := seen[k]; !exists {
				seen[k] = struct{}{}
				allKeys = append(allKeys, k)
			}
		}
		if nextCursor == 0 {
			break
		}
		cursor = nextCursor
	}
	return allKeys, nil
}

func (db *Database) RetryLSet(ctx context.Context, key string, index int64, value any) error {
	return db.retryOperation(ctx, "list set", func() error {
		return db.Client.LSet(ctx, key, index, value).Err()
	})
}

// Unwrap decodes a JSON payload directly into T without intermediate float64 map conversion.
func Unwrap[T any](payload string) (T, error) {
	var t T
	if err := json.Unmarshal([]byte(payload), &t); err != nil {
		return t, err
	}
	return t, nil
}
