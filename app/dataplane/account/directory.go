package account

import (
	"context"
	"errors"
	"sync"

	controlaccount "github.com/dslzl/gork/app/control/account"
	"github.com/dslzl/gork/app/dataplane/shared"
	"github.com/dslzl/gork/app/platform/config"
	appruntime "github.com/dslzl/gork/app/platform/runtime"
)

type AccountDirectory struct {
	repository controlaccount.AccountRepository
	table      *AccountRuntimeTable
	mu         sync.RWMutex
	syncMu     sync.Mutex
}

type ReserveOptions struct {
	ExcludeTokens []string
	PreferTags    []string
	NowS          *int
}

type FeedbackOptions struct {
	Remaining *int
	ResetAtMS *int
	NowS      *int
}

type directoryIntConfig interface {
	GetInt(key string, defaultValue int) int
}

type globalDirectoryConfig struct{}

func (globalDirectoryConfig) GetInt(key string, defaultValue int) int {
	return config.GlobalConfig.GetInt(key, defaultValue)
}

type poolIntervalConfig struct {
	key        string
	defaultSec int
}

var directoryConfigSource directoryIntConfig = globalDirectoryConfig{}

var directoryPoolIntervalConfig = map[string]poolIntervalConfig{
	"basic": {key: "account.refresh.basic_interval_sec", defaultSec: 86400},
	"super": {key: "account.refresh.super_interval_sec", defaultSec: 7200},
	"heavy": {key: "account.refresh.heavy_interval_sec", defaultSec: 7200},
}

var (
	accountDirectorySingleton *AccountDirectory
	accountDirectoryMu        sync.Mutex
)

func NewAccountDirectory(repository controlaccount.AccountRepository) *AccountDirectory {
	return &AccountDirectory{repository: repository}
}

func BindAccountDirectory(directory *AccountDirectory) func() {
	accountDirectoryMu.Lock()
	previous := accountDirectorySingleton
	accountDirectorySingleton = directory
	accountDirectoryMu.Unlock()
	return func() {
		accountDirectoryMu.Lock()
		if accountDirectorySingleton == directory {
			accountDirectorySingleton = previous
		}
		accountDirectoryMu.Unlock()
	}
}

func (d *AccountDirectory) Bootstrap(ctx context.Context) error {
	table, err := Bootstrap(ctx, d.repository)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.table = table
	d.mu.Unlock()
	return nil
}

func (d *AccountDirectory) SyncIfChanged(ctx context.Context) (bool, error) {
	d.mu.RLock()
	hasTable := d.table != nil
	d.mu.RUnlock()
	if !hasTable {
		return false, nil
	}

	d.syncMu.Lock()
	defer d.syncMu.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.table == nil {
		return false, nil
	}
	return ApplyChanges(ctx, d.table, d.repository)
}

func (d *AccountDirectory) Reserve(poolCandidates any, modeID int, options ReserveOptions) (AccountLease, bool) {
	return d.reserve(poolCandidates, modeID, options, false)
}

func (d *AccountDirectory) ReserveAny(poolCandidates any, options ReserveOptions) (AccountLease, bool) {
	return d.reserve(poolCandidates, -1, options, true)
}

func (d *AccountDirectory) reserve(poolCandidates any, modeID int, options ReserveOptions, anyMode bool) (AccountLease, bool) {
	pools := normalizePoolCandidates(poolCandidates)
	if len(pools) == 0 {
		return AccountLease{}, false
	}
	ts := optionNowS(options.NowS)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.table == nil {
		return AccountLease{}, false
	}

	selectOptions := SelectOptions{
		ExcludeIdxs:   excludeIdxs(d.table, options.ExcludeTokens),
		PreferTagIdxs: preferTagIdxs(d.table, options.PreferTags),
		NowS:          ts,
	}
	selectedIdx := 0
	ok := false
	for _, poolID := range pools {
		if anyMode {
			selectedIdx, ok = SelectAny(d.table, poolID, selectOptions)
		} else {
			selectedIdx, ok = Select(d.table, poolID, modeID, selectOptions)
		}
		if ok {
			break
		}
	}
	if !ok {
		return AccountLease{}, false
	}

	IncrementInflight(d.table, selectedIdx)
	UpdateLastUse(d.table, selectedIdx, ts)
	return NewLease(
		selectedIdx,
		d.table.GetToken(selectedIdx),
		d.table.GetPoolID(selectedIdx),
		modeID,
		ts,
	), true
}

func (d *AccountDirectory) Release(lease AccountLease) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.table == nil || lease.Idx < 0 || lease.Idx >= len(d.table.InflightByIdx) {
		return
	}
	DecrementInflight(d.table, lease.Idx)
}

func (d *AccountDirectory) Feedback(token string, kind controlaccount.FeedbackKind, modeID int, options FeedbackOptions) {
	ts := optionNowS(options.NowS)
	strategy := CurrentStrategy()

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.table == nil {
		return
	}
	idx, ok := d.table.IdxByToken[token]
	if !ok {
		return
	}

	switch kind {
	case controlaccount.FeedbackKindSuccess:
		if strategy == strategyRandom {
			ApplySuccessRandom(d.table, idx)
		} else {
			ApplySuccessQuota(d.table, idx, modeID)
		}
	case controlaccount.FeedbackKindRateLimited:
		if strategy == strategyRandom {
			ApplyRateLimitedRandom(d.table, idx, poolCoolingSec(d.table.PoolByIdx[idx]))
		} else {
			ApplyRateLimitedQuota(d.table, idx, modeID)
		}
		UpdateLastFail(d.table, idx, ts)
	case controlaccount.FeedbackKindUnauthorized:
		ApplyAuthFailure(d.table, idx)
		UpdateLastFail(d.table, idx, ts)
		ApplyStatusChange(d.table, idx, StatusExpired)
	case controlaccount.FeedbackKindForbidden:
		ApplyForbidden(d.table, idx)
		UpdateLastFail(d.table, idx, ts)
	case controlaccount.FeedbackKindServerError:
		ApplyServerError(d.table, idx)
		UpdateLastFail(d.table, idx, ts)
	}

	if strategy == strategyQuota && options.Remaining != nil && options.ResetAtMS != nil {
		ApplyQuotaUpdate(d.table, idx, modeID, *options.Remaining, *options.ResetAtMS/1000)
	}
}

func (d *AccountDirectory) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.table == nil {
		return 0
	}
	return d.table.Size
}

func (d *AccountDirectory) Revision() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.table == nil {
		return 0
	}
	return d.table.Revision
}

func poolCoolingSec(poolID int) int {
	pool := shared.PoolIDToString[poolID]
	if pool == "" {
		pool = "basic"
	}
	interval, ok := directoryPoolIntervalConfig[pool]
	if !ok {
		interval = directoryPoolIntervalConfig["basic"]
	}
	seconds := directoryConfigSource.GetInt(interval.key, interval.defaultSec)
	if seconds < 0 {
		return 0
	}
	return seconds
}

func GetAccountDirectory(ctx context.Context, repository controlaccount.AccountRepository) (*AccountDirectory, error) {
	accountDirectoryMu.Lock()
	defer accountDirectoryMu.Unlock()
	if accountDirectorySingleton != nil {
		return accountDirectorySingleton, nil
	}
	if repository == nil {
		return nil, errors.New("AccountDirectory not bootstrapped - repository required on first call")
	}
	directory := NewAccountDirectory(repository)
	if err := directory.Bootstrap(ctx); err != nil {
		return nil, err
	}
	accountDirectorySingleton = directory
	return directory, nil
}

func normalizePoolCandidates(poolCandidates any) []int {
	switch value := poolCandidates.(type) {
	case int:
		return []int{value}
	case []int:
		return append([]int(nil), value...)
	case []shared.PoolID:
		pools := make([]int, 0, len(value))
		for _, poolID := range value {
			pools = append(pools, int(poolID))
		}
		return pools
	default:
		return nil
	}
}

func excludeIdxs(table *AccountRuntimeTable, tokens []string) map[int]bool {
	if len(tokens) == 0 {
		return nil
	}
	out := map[int]bool{}
	for _, token := range tokens {
		if idx, ok := table.IdxByToken[token]; ok {
			out[idx] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func preferTagIdxs(table *AccountRuntimeTable, tags []string) map[int]bool {
	if len(tags) == 0 {
		return nil
	}
	out := map[int]bool{}
	for _, tag := range tags {
		for idx := range table.TagIdx[tag] {
			out[idx] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func optionNowS(value *int) int {
	if value != nil {
		return *value
	}
	return int(appruntime.NowS())
}
