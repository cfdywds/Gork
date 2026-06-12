package products

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	controlaccount "github.com/dslzl/gork/app/control/account"
	"github.com/dslzl/gork/app/control/model"
	dataaccount "github.com/dslzl/gork/app/dataplane/account"
	platformconfig "github.com/dslzl/gork/app/platform/config"
)

type fakeDirectory struct {
	leases  []any
	queries []ReserveAccountQuery
	err     error
}

func (d *fakeDirectory) Reserve(_ context.Context, query ReserveAccountQuery) (any, error) {
	d.queries = append(d.queries, query)
	if d.err != nil {
		return nil, d.err
	}
	if len(d.leases) == 0 {
		return nil, nil
	}
	lease := d.leases[0]
	d.leases = d.leases[1:]
	return lease, nil
}

type fakeRefreshService struct {
	calls int
	err   error
}

func (s *fakeRefreshService) RefreshOnDemand(context.Context) error {
	s.calls++
	return s.err
}

type fakeRuntimeRefreshService struct {
	calls int
}

func (s *fakeRuntimeRefreshService) RefreshScheduled(context.Context, *string) (controlaccount.RefreshResult, error) {
	return controlaccount.RefreshResult{}, nil
}

func (s *fakeRuntimeRefreshService) RefreshOnDemand(context.Context) (controlaccount.RefreshResult, error) {
	s.calls++
	return controlaccount.RefreshResult{Refreshed: 1}, nil
}

func TestSelectionMaxRetriesFollowsStrategy(t *testing.T) {
	resetAccountSelectionForTest(t)
	accountSelectionStrategy = func() string { return "random" }
	if got := SelectionMaxRetries(); got != 5 {
		t.Fatalf("random retries = %d", got)
	}
	accountSelectionStrategy = func() string { return "quota" }
	accountSelectionGetInt = func(key string, defaultValue int) int {
		if key != "retry.max_retries" {
			t.Fatalf("unexpected config key %q", key)
		}
		return 3
	}
	if got := SelectionMaxRetries(); got != 3 {
		t.Fatalf("quota retries = %d", got)
	}
}

func TestAccountSelectionProductionDefaultsUseRuntimeSources(t *testing.T) {
	oldStrategyFunc := accountSelectionStrategy
	oldGetInt := accountSelectionGetInt
	oldGetBool := accountSelectionGetBool
	oldRefresh := accountRefreshService
	oldConfig := platformconfig.GlobalConfig
	oldSelectorStrategy := dataaccount.CurrentStrategy()
	oldRefreshService := controlaccount.GetRefreshService()
	t.Cleanup(func() {
		accountSelectionStrategy = oldStrategyFunc
		accountSelectionGetInt = oldGetInt
		accountSelectionGetBool = oldGetBool
		accountRefreshService = oldRefresh
		platformconfig.GlobalConfig = oldConfig
		_ = dataaccount.SetStrategy(oldSelectorStrategy)
		controlaccount.SetRefreshService(oldRefreshService)
	})

	defaults := filepath.Join(t.TempDir(), "config.defaults.toml")
	if err := os.WriteFile(defaults, []byte("[retry]\nmax_retries = 2\n\n[features]\nauto_chat_mode_fallback = true\n"), 0o600); err != nil {
		t.Fatalf("write defaults: %v", err)
	}
	platformconfig.GlobalConfig = platformconfig.NewConfigSnapshot(accountSelectionConfigBackend{
		data: map[string]any{
			"retry":    map[string]any{"max_retries": 7},
			"features": map[string]any{"auto_chat_mode_fallback": false},
		},
	}, platformconfig.ConfigSnapshotOptions{})
	if err := platformconfig.GlobalConfig.Load(context.Background(), defaults); err != nil {
		t.Fatalf("load config: %v", err)
	}

	accountSelectionStrategy = defaultAccountSelectionStrategy
	accountSelectionGetInt = defaultAccountSelectionGetInt
	accountSelectionGetBool = defaultAccountSelectionGetBool
	accountRefreshService = defaultAccountRefreshService

	if err := dataaccount.SetStrategy("random"); err != nil {
		t.Fatalf("set random strategy: %v", err)
	}
	if got := SelectionMaxRetries(); got != 5 {
		t.Fatalf("random retries from selector = %d", got)
	}
	if err := dataaccount.SetStrategy("quota"); err != nil {
		t.Fatalf("set quota strategy: %v", err)
	}
	if got := SelectionMaxRetries(); got != 7 {
		t.Fatalf("quota retries from GlobalConfig = %d", got)
	}
	spec := model.ModelSpec{ModeID: model.ModeAuto, Capability: model.CapabilityChat}
	if got, want := ModeCandidates(spec), []model.ModeID{model.ModeAuto}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModeCandidates from GlobalConfig = %#v, want %#v", got, want)
	}

	refresh := &fakeRuntimeRefreshService{}
	controlaccount.SetRefreshService(refresh)
	adapter := accountRefreshService()
	if adapter == nil {
		t.Fatalf("default refresh service adapter is nil")
	}
	if err := adapter.RefreshOnDemand(context.Background()); err != nil {
		t.Fatalf("RefreshOnDemand returned error: %v", err)
	}
	if refresh.calls != 1 {
		t.Fatalf("RefreshOnDemand calls = %d", refresh.calls)
	}
}

func TestModeCandidatesAutoChatFallback(t *testing.T) {
	resetAccountSelectionForTest(t)
	spec := model.ModelSpec{ModeID: model.ModeAuto, Capability: model.CapabilityChat}
	if got, want := ModeCandidates(spec), []model.ModeID{model.ModeAuto, model.ModeFast, model.ModeExpert}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModeCandidates(auto chat) = %#v, want %#v", got, want)
	}
	accountSelectionGetBool = func(string, bool) bool { return false }
	if got, want := ModeCandidates(spec), []model.ModeID{model.ModeAuto}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModeCandidates(disabled fallback) = %#v, want %#v", got, want)
	}
	image := model.ModelSpec{ModeID: model.ModeAuto, Capability: model.CapabilityImage}
	if got, want := ModeCandidates(image), []model.ModeID{model.ModeAuto}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModeCandidates(image) = %#v, want %#v", got, want)
	}
}

func TestReserveAccountTriesCandidatesAndReturnsSelectedMode(t *testing.T) {
	resetAccountSelectionForTest(t)
	dir := &fakeDirectory{leases: []any{nil, "lease-fast"}}
	spec := model.ModelSpec{
		ModeID:     model.ModeAuto,
		Tier:       model.TierSuper,
		Capability: model.CapabilityChat,
	}
	now := int64(123)
	lease, selected, ok, err := ReserveAccount(context.Background(), dir, spec, ReserveAccountOptions{
		ExcludeTokens: []string{"tok"},
		NowSOverride:  &now,
	})
	if err != nil || !ok || lease != "lease-fast" || selected != model.ModeFast {
		t.Fatalf("lease=%#v selected=%v ok=%t err=%v", lease, selected, ok, err)
	}
	if len(dir.queries) != 2 || dir.queries[0].ModeID != model.ModeAuto || dir.queries[1].ModeID != model.ModeFast {
		t.Fatalf("queries=%#v", dir.queries)
	}
	if !reflect.DeepEqual(dir.queries[0].ExcludeTokens, []string{"tok"}) || dir.queries[0].NowSOverride == nil || *dir.queries[0].NowSOverride != now {
		t.Fatalf("query options=%#v", dir.queries[0])
	}
	if !reflect.DeepEqual(dir.queries[0].PoolCandidates, []int{1, 2}) {
		t.Fatalf("pool candidates=%#v", dir.queries[0].PoolCandidates)
	}
}

func TestReserveAccountRefreshesOnlyForNonRandomStrategy(t *testing.T) {
	resetAccountSelectionForTest(t)
	spec := model.ModelSpec{ModeID: model.ModeAuto, Capability: model.CapabilityChat}
	refresh := &fakeRefreshService{}
	accountRefreshService = func() AccountRefreshService { return refresh }

	accountSelectionStrategy = func() string { return "random" }
	dir := &fakeDirectory{}
	_, selected, ok, err := ReserveAccount(context.Background(), dir, spec, ReserveAccountOptions{})
	if err != nil || ok || selected != model.ModeAuto || refresh.calls != 0 {
		t.Fatalf("random lease ok=%t selected=%v refresh=%d err=%v", ok, selected, refresh.calls, err)
	}

	accountSelectionStrategy = func() string { return "quota" }
	dir = &fakeDirectory{leases: []any{nil, nil, nil, "lease-after-refresh"}}
	lease, selected, ok, err := ReserveAccount(context.Background(), dir, spec, ReserveAccountOptions{})
	if err != nil || !ok || lease != "lease-after-refresh" || selected != model.ModeAuto || refresh.calls != 1 {
		t.Fatalf("quota lease=%#v ok=%t selected=%v refresh=%d err=%v", lease, ok, selected, refresh.calls, err)
	}
}

func TestReserveAccountPropagatesDirectoryAndRefreshErrors(t *testing.T) {
	resetAccountSelectionForTest(t)
	spec := model.ModelSpec{ModeID: model.ModeAuto, Capability: model.CapabilityChat}

	reserveErr := errors.New("reserve failed")
	_, selected, ok, err := ReserveAccount(context.Background(), &fakeDirectory{err: reserveErr}, spec, ReserveAccountOptions{})
	if !errors.Is(err, reserveErr) || ok || selected != model.ModeAuto {
		t.Fatalf("reserve error selected=%v ok=%t err=%v", selected, ok, err)
	}

	refreshErr := errors.New("refresh failed")
	refresh := &fakeRefreshService{err: refreshErr}
	accountRefreshService = func() AccountRefreshService { return refresh }
	_, selected, ok, err = ReserveAccount(context.Background(), &fakeDirectory{}, spec, ReserveAccountOptions{})
	if !errors.Is(err, refreshErr) || ok || selected != model.ModeAuto || refresh.calls != 1 {
		t.Fatalf("refresh error selected=%v ok=%t calls=%d err=%v", selected, ok, refresh.calls, err)
	}
}

func resetAccountSelectionForTest(t *testing.T) {
	t.Helper()
	oldStrategy := accountSelectionStrategy
	oldGetInt := accountSelectionGetInt
	oldGetBool := accountSelectionGetBool
	oldRefresh := accountRefreshService
	accountSelectionStrategy = func() string { return "quota" }
	accountSelectionGetInt = func(string, int) int { return 1 }
	accountSelectionGetBool = func(string, bool) bool { return true }
	accountRefreshService = func() AccountRefreshService { return nil }
	t.Cleanup(func() {
		accountSelectionStrategy = oldStrategy
		accountSelectionGetInt = oldGetInt
		accountSelectionGetBool = oldGetBool
		accountRefreshService = oldRefresh
	})
}

type accountSelectionConfigBackend struct {
	data map[string]any
}

func (b accountSelectionConfigBackend) Load(context.Context) (map[string]any, error) {
	return b.data, nil
}

func (b accountSelectionConfigBackend) ApplyPatch(context.Context, map[string]any) error {
	return nil
}

func (b accountSelectionConfigBackend) Clear(context.Context) error {
	return nil
}

func (b accountSelectionConfigBackend) Version(context.Context) (any, error) {
	return "test", nil
}

func (b accountSelectionConfigBackend) Close(context.Context) error {
	return nil
}
