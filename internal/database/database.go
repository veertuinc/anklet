package database

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

type Database struct {
	UniqueRunKey       string
	Client             redis.Cmdable // Interface that both *redis.Client and *redis.ClusterClient implement
	MaxRetries         int
	RetryDelay         time.Duration
	RetryBackoffFactor float64
}

// isRetriableError determines if an error is worth retrying
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// Common retriable errors for Redis connections
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "dial tcp") ||
		strings.Contains(errStr, "i/o timeout")
}

// retryOperation performs an operation with retry logic
func (db *Database) retryOperation(ctx context.Context, operationName string, operation func() error) error {
	var lastErr error

	// Check if context has a deadline (e.g., cleanup/shutdown scenario)
	// If so, reduce retries to fail faster
	maxRetries := db.MaxRetries
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		timeRemaining := time.Until(deadline)
		// If we have less than 10 seconds, don't retry at all - just try once
		if timeRemaining < 10*time.Second {
			maxRetries = 0
		} else if timeRemaining < 30*time.Second {
			// If less than 30 seconds, only retry once
			maxRetries = 1
		}
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check if context is canceled before attempting operation
		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled before %s: %w", operationName, ctx.Err())
		default:
		}

		if attempt > 0 {
			// Calculate delay with exponential backoff
			delay := time.Duration(float64(db.RetryDelay) * float64(attempt) * db.RetryBackoffFactor)

			// If context has a deadline, ensure we don't wait longer than remaining time
			if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
				timeRemaining := time.Until(deadline)
				if delay > timeRemaining {
					// Not enough time left, fail immediately
					return fmt.Errorf("insufficient time remaining for retry of %s: %w", operationName, lastErr)
				}
			}

			// Safely attempt to log, catching any panics from canceled contexts
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Context was canceled during logging, silently continue
						_ = r
					}
				}()
				logging.Warn(ctx, fmt.Sprintf("retrying %s in %v (attempt %d/%d)", operationName, delay, attempt, maxRetries))
			}()

			// Create a timer for the delay
			timer := time.NewTimer(delay)
			defer timer.Stop()

			select {
			case <-ctx.Done():
				return fmt.Errorf("context canceled during retry for %s: %w", operationName, ctx.Err())
			case <-timer.C:
				// Check context again immediately after waking
				if ctx.Err() != nil {
					return fmt.Errorf("context canceled during retry for %s: %w", operationName, ctx.Err())
				}
			}
		}

		lastErr = operation()

		// Check if context was canceled during the operation before any logging
		if ctx.Err() != nil {
			return fmt.Errorf("context canceled after %s operation: %w", operationName, ctx.Err())
		}

		if lastErr == nil {
			if attempt > 0 {
				// Safely attempt to log, catching any panics from canceled contexts
				func() {
					defer func() {
						if r := recover(); r != nil {
							// Context was canceled during logging, silently continue
							_ = r
						}
					}()
					logging.Info(ctx, fmt.Sprintf("%s succeeded after %d retries", operationName, attempt))
				}()
			}
			return nil
		}

		isRetriable := isRetriableError(lastErr)
		if !isRetriable {
			// logging.Debug(ctx, fmt.Sprintf("%s failed with non-retriable error: %v", operationName, lastErr))
			return lastErr
		}

		if attempt < maxRetries {
			// Safely attempt to log, catching any panics from canceled contexts
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Context was canceled during logging, silently continue
						_ = r
					}
				}()
				logging.Debug(ctx, fmt.Sprintf("%s failed (attempt %d/%d): %v", operationName, attempt+1, db.MaxRetries+1, lastErr))
			}()
		}
	}

	return fmt.Errorf("%s failed after %d retries: %w", operationName, db.MaxRetries, lastErr)
}

func NewClient(ctx context.Context, config config.Database) (*Database, error) {
	// Set default retry configuration if not specified
	// Use -1 to explicitly request 0 retries (for cleanup operations)
	// Use 0 or unset to get default of 5 retries (backwards compatible)
	maxRetries := config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0 // -1 means explicitly no retries (fast-fail for cleanup)
	} else if maxRetries == 0 {
		maxRetries = 5 // 0 means use defaults (for backwards compatibility with configs)
	}

	retryDelay := time.Duration(config.RetryDelay) * time.Millisecond
	if retryDelay == 0 {
		retryDelay = 1000 * time.Millisecond // Default to 1 second
	}

	retryBackoffFactor := config.RetryBackoffFactor
	if retryBackoffFactor == 0 {
		retryBackoffFactor = 2.0 // Default to 2x backoff
	}

	var rdb redis.Cmdable

	// Configure TLS if enabled
	var tlsConfig *tls.Config
	if config.TLSEnabled {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: config.TLSInsecure,
		}
	}

	if config.ClusterMode {
		// Use cluster client
		clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:     []string{fmt.Sprintf("%s:%d", config.URL, config.Port)},
			Username:  config.User,
			Password:  config.Password,
			TLSConfig: tlsConfig,
		})
		rdb = clusterClient
		logging.Dev(ctx, fmt.Sprintf("created redis cluster client: %v", clusterClient))
	} else {
		// Use regular client
		regularClient := redis.NewClient(&redis.Options{
			Addr:      fmt.Sprintf("%s:%d", config.URL, config.Port),
			Username:  config.User,
			Password:  config.Password,
			DB:        config.Database,
			TLSConfig: tlsConfig,
		})
		rdb = regularClient
		logging.Dev(ctx, fmt.Sprintf("created redis client: %v", regularClient))
	}

	// Create Database instance with retry configuration
	db := &Database{
		Client:             rdb,
		MaxRetries:         maxRetries,
		RetryDelay:         retryDelay,
		RetryBackoffFactor: retryBackoffFactor,
	}

	// Test connection with retry logic
	err := db.retryOperation(ctx, "initial connection ping", func() error {
		logging.Dev(ctx, "pinging redis client")
		ping := rdb.Ping(ctx)
		if ping.Err() != nil && ping.Err().Error() != "" {
			return fmt.Errorf("error pinging redis client: %s", ping.Err().Error())
		}
		pong, err := ping.Result()
		if err != nil {
			return err
		}

		logging.Dev(ctx, fmt.Sprintf("pinged redis client: %s", pong))

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
	database, ok := ctx.Value(config.ContextKey("database")).(*Database)
	if !ok {
		return nil, errors.New("GetDatabaseFromContext failed (is your database running and enabled in your config.yml?)")
	}
	return database, nil
}

func UpdateUniqueRunKey(ctx context.Context, key string) (context.Context, error) {
	database, ok := ctx.Value(config.ContextKey("database")).(Database)
	if !ok {
		return ctx, fmt.Errorf("database not found in context")
	}
	database.UniqueRunKey = key
	ctx = logging.AppendCtx(ctx, slog.String("uniqueRunKey", key))
	return context.WithValue(ctx, config.ContextKey("database"), database), nil
}

func RemoveUniqueKeyFromDB(ctx context.Context) (context.Context, error) {
	database, ok := ctx.Value(config.ContextKey("database")).(Database)
	if !ok {
		return ctx, fmt.Errorf("database not found in context")
	}
	// we don't use ctx for the database deletion so we avoid getting the cancelled context state, which fails when Del runs
	// but we need to preserve the logger for database operations
	dbCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(ctx); err == nil {
		dbCtx = context.WithValue(dbCtx, config.ContextKey("logger"), logger)
	}
	var deletion int64
	err := database.retryOperation(dbCtx, "delete unique key", func() error {
		result, err := database.RetryDel(dbCtx, database.UniqueRunKey)
		if err != nil {
			return err
		}
		deletion = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	logging.Debug(ctx, fmt.Sprintf("removal of unique key %s from database returned %d (1 is success, 0 failed)", database.UniqueRunKey, deletion))
	return ctx, nil
}

func CheckIfKeyExists(ctx context.Context, key string) (bool, error) {
	if ctx.Err() != nil {
		return false, errors.New("context canceled during CheckIfKeyExists")
	}
	database, err := GetDatabaseFromContext(ctx)
	if err != nil {
		return false, err
	}
	// introduce a random millisecond sleep to prevent concurrent executions from colliding
	src := rand.NewSource(time.Now().UnixNano())
	r := rand.New(src)
	randomSleep := time.Duration(r.Intn(100)) * time.Millisecond
	time.Sleep(randomSleep)

	var exists int64
	err = database.retryOperation(ctx, "check key exists", func() error {
		result, err := database.RetryExists(ctx, key)
		if err != nil {
			return err
		}
		exists = result
		return nil
	})
	if err != nil {
		return false, err
	}
	return exists == 1, nil // 1 is found, 0 is not found
}

func AddUniqueRunKey(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, errors.New("context canceled during AddUniqueRunKey")
	}
	database, err := GetDatabaseFromContext(ctx)
	if err != nil {
		return false, err
	}
	exists, err := CheckIfKeyExists(ctx, database.UniqueRunKey)
	if err != nil {
		return false, err
	}
	if !exists {
		err = database.retryOperation(ctx, "set unique key", func() error {
			return database.RetrySet(ctx, database.UniqueRunKey, "true", 0)
		})
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return true, errors.New("unique run key already exists")
}

// Retry wrapper methods for common Redis operations

// RetryGet performs a GET operation with retry logic
func (db *Database) RetryGet(ctx context.Context, key string) (string, error) {
	var result string
	err := db.retryOperation(ctx, "get key", func() error {
		val, err := db.Client.Get(ctx, key).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetrySet performs a SET operation with retry logic
func (db *Database) RetrySet(ctx context.Context, key string, value any, expiration time.Duration) error {
	return db.retryOperation(ctx, "set key", func() error {
		return db.Client.Set(ctx, key, value, expiration).Err()
	})
}

// RetryDel performs a DEL operation with retry logic
func (db *Database) RetryDel(ctx context.Context, keys ...string) (int64, error) {
	var result int64
	err := db.retryOperation(ctx, "delete keys", func() error {
		val, err := db.Client.Del(ctx, keys...).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryExists performs an EXISTS operation with retry logic
func (db *Database) RetryExists(ctx context.Context, keys ...string) (int64, error) {
	var result int64
	err := db.retryOperation(ctx, "check exists", func() error {
		val, err := db.Client.Exists(ctx, keys...).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryLPush performs an LPUSH operation with retry logic
func (db *Database) RetryLPush(ctx context.Context, key string, values ...any) (int64, error) {
	var result int64
	err := db.retryOperation(ctx, "list push", func() error {
		val, err := db.Client.LPush(ctx, key, values...).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryRPush performs an RPUSH operation with retry logic
func (db *Database) RetryRPush(ctx context.Context, key string, values ...any) (int64, error) {
	var result int64
	err := db.retryOperation(ctx, "list rpush", func() error {
		val, err := db.Client.RPush(ctx, key, values...).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryLRange performs an LRANGE operation with retry logic
func (db *Database) RetryLRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	var result []string
	err := db.retryOperation(ctx, "list range", func() error {
		val, err := db.Client.LRange(ctx, key, start, stop).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryLRem performs an LREM operation with retry logic
func (db *Database) RetryLRem(ctx context.Context, key string, count int64, value any) (int64, error) {
	var result int64
	err := db.retryOperation(ctx, "list remove", func() error {
		val, err := db.Client.LRem(ctx, key, count, value).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryLIndex performs an LINDEX operation with retry logic
func (db *Database) RetryLIndex(ctx context.Context, key string, index int64) (string, error) {
	var result string
	err := db.retryOperation(ctx, "list index", func() error {
		val, err := db.Client.LIndex(ctx, key, index).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryLLen performs an LLEN operation with retry logic
func (db *Database) RetryLLen(ctx context.Context, key string) (int64, error) {
	var result int64
	err := db.retryOperation(ctx, "list length", func() error {
		val, err := db.Client.LLen(ctx, key).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryRPopLPush performs an RPOPLPUSH operation with retry logic
func (db *Database) RetryRPopLPush(ctx context.Context, source, destination string) (string, error) {
	var result string
	err := db.retryOperation(ctx, "rpop lpush", func() error {
		val, err := db.Client.RPopLPush(ctx, source, destination).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryRPop performs an RPOP operation with retry logic
func (db *Database) RetryRPop(ctx context.Context, key string) (string, error) {
	var result string
	err := db.retryOperation(ctx, "rpop", func() error {
		val, err := db.Client.RPop(ctx, key).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryKeys performs a KEYS operation with retry logic
func (db *Database) RetryKeys(ctx context.Context, pattern string) ([]string, error) {
	var result []string
	err := db.retryOperation(ctx, "keys search", func() error {
		val, err := db.Client.Keys(ctx, pattern).Result()
		if err != nil {
			return err
		}
		result = val
		return nil
	})
	return result, err
}

// RetryScan performs a SCAN operation with retry logic
func (db *Database) RetryScan(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	var keys []string
	var nextCursor uint64
	err := db.retryOperation(ctx, "scan", func() error {
		resultKeys, resultCursor, err := db.Client.Scan(ctx, cursor, match, count).Result()
		if err != nil {
			return err
		}
		keys = resultKeys
		nextCursor = resultCursor
		return nil
	})
	return keys, nextCursor, err
}

// RetryLSet performs an LSET operation with retry logic
func (db *Database) RetryLSet(ctx context.Context, key string, index int64, value any) error {
	return db.retryOperation(ctx, "list set", func() error {
		return db.Client.LSet(ctx, key, index, value).Err()
	})
}

func Unwrap[T any](payload string) (T, error, error) {
	var wrappedPayload map[string]any
	var t T
	err := json.Unmarshal([]byte(payload), &wrappedPayload)
	if err != nil {
		return t, err, nil
	}
	payloadBytes, err := json.Marshal(wrappedPayload)
	if err != nil {
		return t, err, nil
	}
	if err := json.Unmarshal(payloadBytes, &t); err != nil {
		return t, err, nil
	}
	return t, nil, nil
}
