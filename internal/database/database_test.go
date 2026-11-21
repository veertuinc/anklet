package database

import (
	"context"
	"crypto/tls"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
)

type fakeRedisCmdable struct {
	redis.Cmdable
	kv    map[string]string
	lists map[string][]string
	pages map[uint64]struct {
		keys       []string
		nextCursor uint64
	}
}

func newFakeRedisCmdable() *fakeRedisCmdable {
	return &fakeRedisCmdable{
		kv:    make(map[string]string),
		lists: make(map[string][]string),
		pages: make(map[uint64]struct {
			keys       []string
			nextCursor uint64
		}),
	}
}

func (f *fakeRedisCmdable) Get(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	if v, ok := f.kv[key]; ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func (f *fakeRedisCmdable) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	switch v := value.(type) {
	case string:
		f.kv[key] = v
	case []byte:
		f.kv[key] = string(v)
	}
	cmd.SetVal("OK")
	return cmd
}

func (f *fakeRedisCmdable) Del(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	var n int64
	for _, k := range keys {
		if _, ok := f.kv[k]; ok {
			delete(f.kv, k)
			n++
		}
		if _, ok := f.lists[k]; ok {
			delete(f.lists, k)
			n++
		}
	}
	cmd.SetVal(n)
	return cmd
}

func (f *fakeRedisCmdable) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	var n int64
	for _, k := range keys {
		if _, ok := f.kv[k]; ok {
			n++
		} else if _, ok := f.lists[k]; ok {
			n++
		}
	}
	cmd.SetVal(n)
	return cmd
}

func (f *fakeRedisCmdable) LPush(_ context.Context, key string, values ...any) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	for _, v := range values {
		f.lists[key] = append([]string{v.(string)}, f.lists[key]...)
	}
	cmd.SetVal(int64(len(f.lists[key])))
	return cmd
}

func (f *fakeRedisCmdable) RPush(_ context.Context, key string, values ...any) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	for _, v := range values {
		f.lists[key] = append(f.lists[key], v.(string))
	}
	cmd.SetVal(int64(len(f.lists[key])))
	return cmd
}

func (f *fakeRedisCmdable) LRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(append([]string(nil), f.lists[key]...))
	return cmd
}

func (f *fakeRedisCmdable) LRem(_ context.Context, key string, _ int64, value any) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	target := value.(string)
	for i, item := range f.lists[key] {
		if item == target {
			f.lists[key] = append(f.lists[key][:i], f.lists[key][i+1:]...)
			cmd.SetVal(1)
			return cmd
		}
	}
	cmd.SetVal(0)
	return cmd
}

func (f *fakeRedisCmdable) LIndex(_ context.Context, key string, index int64) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[key]
	if index < 0 {
		index = int64(len(list)) + index
	}
	if index < 0 || index >= int64(len(list)) {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(list[index])
	return cmd
}

func (f *fakeRedisCmdable) LLen(_ context.Context, key string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	cmd.SetVal(int64(len(f.lists[key])))
	return cmd
}

func (f *fakeRedisCmdable) RPop(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[key]
	if len(list) == 0 {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	val := list[len(list)-1]
	f.lists[key] = list[:len(list)-1]
	cmd.SetVal(val)
	return cmd
}

func (f *fakeRedisCmdable) RPopLPush(_ context.Context, source, destination string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[source]
	if len(list) == 0 {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	val := list[len(list)-1]
	f.lists[source] = list[:len(list)-1]
	f.lists[destination] = append([]string{val}, f.lists[destination]...)
	cmd.SetVal(val)
	return cmd
}

func (f *fakeRedisCmdable) LSet(_ context.Context, key string, index int64, value any) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	f.lists[key][index] = value.(string)
	cmd.SetVal("OK")
	return cmd
}

func (f *fakeRedisCmdable) Scan(_ context.Context, cursor uint64, _ string, _ int64) *redis.ScanCmd {
	cmd := redis.NewScanCmd(context.Background(), nil)
	page := f.pages[cursor]
	cmd.SetVal(page.keys, page.nextCursor)
	return cmd
}

func TestUnwrapPreservesInt64Precision(t *testing.T) {
	t.Parallel()

	type sample struct {
		ID int64 `json:"id"`
	}
	const want int64 = 9007199254740993
	got, err := Unwrap[sample](`{"id":9007199254740993}`)
	if err != nil {
		t.Fatalf("Unwrap() error = %v", err)
	}
	if got.ID != want {
		t.Fatalf("Unwrap() ID = %d, want %d", got.ID, want)
	}
	if _, err := Unwrap[sample](`{invalid`); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBuildTLSConfig(t *testing.T) {
	t.Parallel()

	if cfg := buildTLSConfig(config.Database{TLSEnabled: false}); cfg != nil {
		t.Fatalf("expected nil TLS config when disabled, got %+v", cfg)
	}

	tlsCfg := buildTLSConfig(config.Database{TLSEnabled: true, TLSInsecure: true})
	if tlsCfg == nil {
		t.Fatal("expected non-nil TLS config when enabled")
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want TLS 1.2 (%#x)", tlsCfg.MinVersion, tls.VersionTLS12)
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Errorf("InsecureSkipVerify = false, want true")
	}
}

func TestRetryOperationBehaviors(t *testing.T) {
	t.Parallel()

	t.Run("non-retriable error fails immediately", func(t *testing.T) {
		db := &Database{MaxRetries: 5, RetryDelay: time.Millisecond, RetryBackoffFactor: 1}
		attempts := 0
		err := db.retryOperation(context.Background(), "op", func() error {
			attempts++
			return redis.Nil
		})
		if !errors.Is(err, redis.Nil) || attempts != 1 {
			t.Fatalf("got (attempts=%d, err=%v), want (1, redis.Nil)", attempts, err)
		}
	})

	t.Run("transient error succeeds on retry", func(t *testing.T) {
		db := &Database{MaxRetries: 3, RetryDelay: time.Millisecond, RetryBackoffFactor: 1}
		var attempts atomic.Int32
		err := db.retryOperation(context.Background(), "op", func() error {
			if attempts.Add(1) < 3 {
				return errors.New("i/o timeout")
			}
			return nil
		})
		if err != nil || attempts.Load() != 3 {
			t.Fatalf("got (attempts=%d, err=%v), want (3, nil)", attempts.Load(), err)
		}
	})

	t.Run("short deadline under 10s disables retries", func(t *testing.T) {
		db := &Database{MaxRetries: 5, RetryDelay: time.Millisecond, RetryBackoffFactor: 1}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		attempts := 0
		err := db.retryOperation(ctx, "op", func() error {
			attempts++
			return errors.New("connection reset")
		})
		if err == nil || attempts != 1 {
			t.Fatalf("got (attempts=%d, err=%v), want (1, non-nil)", attempts, err)
		}
	})

	t.Run("medium deadline under 30s limits to 1 retry", func(t *testing.T) {
		db := &Database{MaxRetries: 5, RetryDelay: time.Millisecond, RetryBackoffFactor: 1}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		attempts := 0
		err := db.retryOperation(ctx, "op", func() error {
			attempts++
			return errors.New("broken pipe")
		})
		if err == nil || attempts != 2 {
			t.Fatalf("got (attempts=%d, err=%v), want (2, non-nil)", attempts, err)
		}
	})

	t.Run("stops on context cancel during retry wait", func(t *testing.T) {
		db := &Database{MaxRetries: 5, RetryDelay: 5 * time.Second, RetryBackoffFactor: 2.0}
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		go func() {
			time.Sleep(25 * time.Millisecond)
			cancel()
		}()
		err := db.retryOperation(ctx, "test-op", func() error {
			attempts++
			return errors.New("connection refused")
		})
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Fatalf("got (attempts=%d, err=%v), want (1, context.Canceled)", attempts, err)
		}
	})
}

func TestRetryWrappersAndScanAllKeys(t *testing.T) {
	t.Parallel()

	fake := newFakeRedisCmdable()
	fake.pages[0] = struct {
		keys       []string
		nextCursor uint64
	}{keys: []string{"k1", "k2"}, nextCursor: 10}
	fake.pages[10] = struct {
		keys       []string
		nextCursor uint64
	}{keys: []string{"k2", "k3"}, nextCursor: 0}

	db := &Database{Client: fake, MaxRetries: 0}
	ctx := context.Background()

	if err := db.RetrySet(ctx, "greeting", "hi", 0); err != nil {
		t.Fatal(err)
	}
	if v, err := db.RetryGet(ctx, "greeting"); err != nil || v != "hi" {
		t.Fatalf("RetryGet = (%q, %v), want hi", v, err)
	}
	if ex, err := db.RetryExists(ctx, "greeting"); err != nil || ex != 1 {
		t.Fatalf("RetryExists = (%d, %v), want 1", ex, err)
	}
	if _, err := db.RetryRPush(ctx, "list", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RetryLPush(ctx, "list", "z"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RetryLLen(ctx, "list"); err != nil || n != 3 {
		t.Fatalf("RetryLLen = (%d, %v), want 3", n, err)
	}
	if err := db.RetryLSet(ctx, "list", 0, "first"); err != nil {
		t.Fatal(err)
	}
	if v, err := db.RetryLIndex(ctx, "list", 0); err != nil || v != "first" {
		t.Fatalf("RetryLIndex = (%q, %v), want first", v, err)
	}
	if items, err := db.RetryLRange(ctx, "list", 0, -1); err != nil || len(items) != 3 {
		t.Fatalf("RetryLRange = (%v, %v), want len 3", items, err)
	}
	if moved, err := db.RetryRPopLPush(ctx, "list", "dest"); err != nil || moved != "b" {
		t.Fatalf("RetryRPopLPush = (%q, %v), want b", moved, err)
	}
	if popped, err := db.RetryRPop(ctx, "dest"); err != nil || popped != "b" {
		t.Fatalf("RetryRPop = (%q, %v), want b", popped, err)
	}
	if rem, err := db.RetryLRem(ctx, "list", 1, "first"); err != nil || rem != 1 {
		t.Fatalf("RetryLRem = (%d, %v), want 1", rem, err)
	}
	if del, err := db.RetryDel(ctx, "greeting"); err != nil || del != 1 {
		t.Fatalf("RetryDel = (%d, %v), want 1", del, err)
	}
	keys, err := db.ScanAllKeys(ctx, "k*")
	if err != nil || len(keys) != 3 {
		t.Fatalf("ScanAllKeys = (%v, %v), want 3 deduplicated keys", keys, err)
	}

	// Context lookup helper
	ctxWithDB := context.WithValue(ctx, config.ContextKey("database"), db)
	if got, err := GetDatabaseFromContext(ctxWithDB); err != nil || got != db {
		t.Fatalf("GetDatabaseFromContext = (%v, %v)", got, err)
	}
	if _, err := GetDatabaseFromContext(ctx); err == nil {
		t.Fatal("expected error when database missing from context")
	}
}
