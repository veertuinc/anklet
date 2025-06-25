package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

type Database struct {
	UniqueRunKey string
	Client       *redis.Client
}

func NewClient(ctx context.Context, config config.Database) (*Database, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", config.URL, config.Port),
		Username: config.User,
		Password: config.Password, // no password set
		DB:       config.Database, // use default DB,
	})
	logging.DevContext(ctx, fmt.Sprintf("created redis client: %v", rdb))

	logging.DevContext(ctx, "pinging redis client")
	ping := rdb.Ping(ctx)
	if ping.Err() != nil && ping.Err().Error() != "" {
		return nil, errors.New("error pinging redis client: " + ping.Err().Error())
	}
	pong, err := ping.Result()
	if err != nil {
		return nil, err
	}

	logging.DevContext(ctx, fmt.Sprintf("pinged redis client: %s", pong))

	if pong != "PONG" {
		return nil, fmt.Errorf("unable to connect to Redis, received: %s", pong)
	}

	return &Database{
		Client: rdb,
	}, nil
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
	logging, err := logging.GetLoggerFromContext(ctx)
	if err != nil {
		return ctx, err
	}
	// we don't use ctx for the database deletion so we avoid getting the cancelled context state, which fails when Del runs
	deletion, err := database.Client.Del(context.Background(), database.UniqueRunKey).Result()
	if err != nil {
		return nil, err
	}
	logging.DebugContext(ctx, fmt.Sprintf("removal of unique key %s from database returned %d (1 is success, 0 failed)", database.UniqueRunKey, deletion))
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
	exists, err := database.Client.Exists(ctx, key).Result()
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
		setting := database.Client.Set(ctx, database.UniqueRunKey, "true", 0)
		if setting.Err() != nil {
			return false, setting.Err()
		}
		return true, nil
	}
	return true, errors.New("unique run key already exists")
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
