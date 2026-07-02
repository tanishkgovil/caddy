package caddy

import (
	"context"
	"time"

	"github.com/caddyserver/certmagic"
)

// newMeteredStorage wraps s so each operation records metrics
// TryLocker is optional, so if s implements it, the wrapper will too.
func newMeteredStorage(s certmagic.Storage) certmagic.Storage {
	m := meteredStorage{s}
	if _, ok := s.(certmagic.TryLocker); ok {
		return meteredTryLockerStorage{m}
	}
	return m
}

// meteredStorage wraps certmagic.Storage to record metrics for each operation.
type meteredStorage struct {
	certmagic.Storage
}

// observeStorage records metrics for a storage operation.
func observeStorage(op string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	storageMetrics.ops.WithLabelValues(op, result).Inc()
	storageMetrics.opDuration.WithLabelValues(op, result).Observe(time.Since(start).Seconds())
}

func (s meteredStorage) Store(ctx context.Context, key string, value []byte) error {
	start := time.Now()
	err := s.Storage.Store(ctx, key, value)
	observeStorage("store", start, err)
	return err
}

func (s meteredStorage) Load(ctx context.Context, key string) ([]byte, error) {
	start := time.Now()
	b, err := s.Storage.Load(ctx, key)
	observeStorage("load", start, err)
	return b, err
}

func (s meteredStorage) Delete(ctx context.Context, key string) error {
	start := time.Now()
	err := s.Storage.Delete(ctx, key)
	observeStorage("delete", start, err)
	return err
}

func (s meteredStorage) List(ctx context.Context, path string, recursive bool) ([]string, error) {
	start := time.Now()
	keys, err := s.Storage.List(ctx, path, recursive)
	observeStorage("list", start, err)
	return keys, err
}

func (s meteredStorage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	start := time.Now()
	info, err := s.Storage.Stat(ctx, key)
	observeStorage("stat", start, err)
	return info, err
}

func (s meteredStorage) Lock(ctx context.Context, name string) error {
	start := time.Now()
	err := s.Storage.Lock(ctx, name)
	observeStorage("lock", start, err)
	return err
}

func (s meteredStorage) Unlock(ctx context.Context, name string) error {
	start := time.Now()
	err := s.Storage.Unlock(ctx, name)
	observeStorage("unlock", start, err)
	return err
}

// meteredTryLockerStorage wraps meteredStorage with the TryLocker interface if needed.
type meteredTryLockerStorage struct {
	meteredStorage
}

func (s meteredTryLockerStorage) TryLock(ctx context.Context, name string) (bool, error) {
	return s.Storage.(certmagic.TryLocker).TryLock(ctx, name)
}
