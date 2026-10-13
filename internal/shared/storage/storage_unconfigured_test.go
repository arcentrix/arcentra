package storage

import (
	"context"
	"errors"
	"fmt"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"gorm.io/gorm"
	"testing"
	"time"
)

type emptyStorageRepository struct {
	repo.IStorageRepository
	err error
}

func (r emptyStorageRepository) GetDefault(context.Context) (*model.StorageConfig, error) {
	return nil, r.err
}
func TestFreshInstallStorageFailsOperationsNotStartup(t *testing.T) {
	st, err := ProvideStorageFromDB(&repo.Repositories{Storage: emptyStorageRepository{err: fmt.Errorf("wrapped: %w", gorm.ErrRecordNotFound)}})
	if err != nil || st == nil {
		t.Fatalf("fresh installation blocked: %v", err)
	}
	ctx := context.Background()
	_, a := st.PutObject(ctx, "name", nil, "")
	_, b := st.GetObject(ctx, "name")
	_, c := st.Upload(ctx, "name", nil, "")
	_, d := st.Download(ctx, "name")
	e := st.Delete(ctx, "name")
	_, f := st.GetPresignedURL(ctx, "name", time.Minute)
	for _, err := range []error{a, b, c, d, e, f} {
		if !errors.Is(err, ErrStorageUnconfigured) {
			t.Fatalf("unconfigured storage must fail: %v", err)
		}
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if !errors.Is(st.Delete(cancelCtx, "name"), context.Canceled) {
		t.Fatal("cancellation lost")
	}
}
func TestDatabaseFailureMustNotBecomeUnconfiguredStorage(t *testing.T) {
	failure := errors.New("database unavailable")
	st, err := ProvideStorageFromDB(&repo.Repositories{Storage: emptyStorageRepository{err: failure}})
	if st != nil || !errors.Is(err, failure) {
		t.Fatal("database failure bypassed")
	}
}
