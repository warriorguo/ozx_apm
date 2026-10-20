package api

import (
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/objstore"
	"github.com/warriorguo/ozx_apm/server/internal/sessionfiles"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// RouterOption adjusts a router.
type RouterOption func(*routerOptions)

type routerOptions struct {
	sessionFiles    *sessionfiles.Service
	sessionFilesSet bool
}

// WithSessionFiles uses this session file service (nil: the feature is off)
// instead of one built from the configuration.
func WithSessionFiles(svc *sessionfiles.Service) RouterOption {
	return func(o *routerOptions) {
		o.sessionFiles = svc
		o.sessionFilesSet = true
	}
}

func applyOptions(cfg *config.Config, repo *storage.Repository, logger *zap.Logger, opts []RouterOption) routerOptions {
	var o routerOptions
	for _, opt := range opts {
		opt(&o)
	}
	if !o.sessionFilesSet {
		o.sessionFiles = NewSessionFileService(cfg, repo, logger)
	}
	return o
}

// NewSessionFileService builds the session file service (APM-13) from the
// configuration: nil (feature off) when the object store is not configured
// or there is no repository. Never logs a key.
func NewSessionFileService(cfg *config.Config, repo *storage.Repository, logger *zap.Logger) *sessionfiles.Service {
	if cfg == nil || repo == nil || !cfg.S3.Configured() {
		return nil
	}
	client, err := objstore.New(objstore.Config{
		Endpoint: cfg.S3.Endpoint, Bucket: cfg.S3.Bucket, Region: cfg.S3.Region,
		AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey,
	}, nil)
	if err != nil {
		logger.Error("session files disabled: invalid object store configuration", zap.Error(err))
		return nil
	}
	sf := cfg.SessionFiles
	return sessionfiles.New(repo, client, sessionfiles.Config{
		Prefix:           cfg.S3.Prefix,
		Retention:        time.Duration(sf.RetentionDays) * 24 * time.Hour,
		Incomplete:       time.Duration(sf.IncompleteDays) * 24 * time.Hour,
		MaxArchiveBytes:  sf.MaxArchiveBytes,
		MaxUploadsPerDay: sf.MaxUploadsPerDevicePerDay,
		MaxBytesPerDay:   sf.MaxBytesPerDevicePerDay,
		MaxOpenUploads:   sf.MaxOpenUploadsPerDevice,
	}, logger)
}
