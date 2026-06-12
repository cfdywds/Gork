package app

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	accountcontrol "github.com/dslzl/gork/app/control/account"
	accountbackends "github.com/dslzl/gork/app/control/account/backends"
	proxycontrol "github.com/dslzl/gork/app/control/proxy"
	accountdataplane "github.com/dslzl/gork/app/dataplane/account"
	platformruntime "github.com/dslzl/gork/app/platform/runtime"
	platformstorage "github.com/dslzl/gork/app/platform/storage"
	adminproduct "github.com/dslzl/gork/app/products/web/admin"
)

type appMainLifecycleState struct {
	runtimeStore *platformruntime.RedisRuntimeStore
	repository   accountcontrol.AccountRepository
	directory    *accountdataplane.AccountDirectory
	schedulerKey *platformruntime.RedisRuntimeLease
	adminCleanup func()
}

type appMainLifecycleStep func(context.Context, *appMainLifecycleState) (Hook, error)

var (
	appMainRuntimeClientFactory       platformruntime.RedisRuntimeClientFactory
	appMainStartRuntimeStore          appMainLifecycleStep                = defaultAppMainStartRuntimeStore
	appMainConfigureTaskSnapshotStore appMainLifecycleStep                = defaultAppMainConfigureTaskSnapshotStore
	appMainInitializeRepository       appMainLifecycleStep                = defaultAppMainInitializeRepository
	appMainRunStartupMigrations       appMainLifecycleStep                = defaultAppMainRunStartupMigrations
	appMainReconcileLocalMediaCache   appMainLifecycleStep                = defaultAppMainReconcileLocalMediaCache
	appMainStartAccountDirectory      appMainLifecycleStep                = defaultAppMainStartAccountDirectory
	appMainStartRefreshRuntime        appMainLifecycleStep                = defaultAppMainStartRefreshRuntime
	appMainStartProxyScheduler        appMainLifecycleStep                = defaultAppMainStartProxyScheduler
	appMainAcquireSchedulerFileLock   func(context.Context) (Hook, error) = acquireAppMainSchedulerFileLock
	appMainConsoleResetInterval                                           = 30 * time.Second
)

func defaultLifecycleHooks() ([]Hook, []Hook) {
	state := &appMainLifecycleState{}
	cleanups := []Hook{}
	stepHook := func(step appMainLifecycleStep) Hook {
		return func(ctx context.Context) error {
			cleanup, err := step(ctx, state)
			if err != nil {
				return err
			}
			if cleanup != nil {
				cleanups = append(cleanups, cleanup)
			}
			return nil
		}
	}
	startupHooks := []Hook{
		func(ctx context.Context) error { return appMainEnsureConfig(ctx) },
		func(context.Context) error { return appMainSetupLogging() },
		stepHook(appMainStartRuntimeStore),
		stepHook(appMainConfigureTaskSnapshotStore),
		stepHook(appMainInitializeRepository),
		stepHook(appMainRunStartupMigrations),
		stepHook(appMainReconcileLocalMediaCache),
		stepHook(appMainStartAccountDirectory),
		stepHook(appMainStartRefreshRuntime),
		stepHook(appMainStartProxyScheduler),
	}
	shutdownHooks := []Hook{
		func(ctx context.Context) error {
			defer func() {
				cleanups = nil
			}()
			for i := len(cleanups) - 1; i >= 0; i-- {
				if err := cleanups[i](ctx); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return startupHooks, shutdownHooks
}

func defaultAppMainStartRuntimeStore(ctx context.Context, state *appMainLifecycleState) (Hook, error) {
	store, err := platformruntime.CreateRuntimeStoreFromEnv(appMainRuntimeClientFactory)
	if err != nil {
		return nil, err
	}
	state.runtimeStore = store
	if store == nil {
		return nil, nil
	}
	return func(ctx context.Context) error {
		return store.Close(ctx)
	}, nil
}

func defaultAppMainConfigureTaskSnapshotStore(_ context.Context, state *appMainLifecycleState) (Hook, error) {
	if state.runtimeStore == nil {
		platformruntime.SetTaskSnapshotStore(nil)
		return nil, nil
	}
	taskClient, ok := state.runtimeStore.Redis.(platformruntime.RedisTaskClient)
	if !ok {
		platformruntime.SetTaskSnapshotStore(nil)
		return nil, nil
	}
	ttlS := appMainEnvInt("RUNTIME_TASK_TTL_S", 300)
	if ttlS < 60 {
		ttlS = 60
	}
	platformruntime.SetTaskSnapshotStore(platformruntime.NewRedisTaskSnapshotStore(
		taskClient,
		platformruntime.RedisTaskSnapshotStoreOptions{TTLS: ttlS},
	))
	return func(context.Context) error {
		platformruntime.SetTaskSnapshotStore(nil)
		return nil
	}, nil
}

func defaultAppMainInitializeRepository(ctx context.Context, state *appMainLifecycleState) (Hook, error) {
	repo, err := accountbackends.CreateRepository(appMainEnv(), accountbackends.RepositoryConstructors{})
	if err != nil {
		return nil, err
	}
	if err := repo.Initialize(ctx); err != nil {
		_ = repo.Close(ctx)
		return nil, err
	}
	state.repository = repo
	state.bindAdminRuntime()
	return func(ctx context.Context) error {
		state.clearAdminRuntime()
		return repo.Close(ctx)
	}, nil
}

func defaultAppMainRunStartupMigrations(ctx context.Context, state *appMainLifecycleState) (Hook, error) {
	cleanup, err := runAppMainStartupMigrations(ctx, state)
	if err != nil {
		return nil, err
	}
	if err := appMainLoadRequestConfig(ctx); err != nil {
		return nil, err
	}
	return cleanup, nil
}

func defaultAppMainReconcileLocalMediaCache(context.Context, *appMainLifecycleState) (Hook, error) {
	return nil, platformstorage.ReconcileLocalMediaCache()
}

func defaultAppMainStartAccountDirectory(ctx context.Context, state *appMainLifecycleState) (Hook, error) {
	if state.repository == nil {
		return nil, nil
	}
	directory := accountdataplane.NewAccountDirectory(state.repository)
	restoreDirectory := accountdataplane.BindAccountDirectory(directory)
	state.directory = directory
	state.bindAdminRuntime()

	syncCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = directory.Bootstrap(syncCtx)
		appMainAccountDirectorySyncLoop(syncCtx, directory)
	}()
	return func(context.Context) error {
		cancel()
		<-done
		restoreDirectory()
		return nil
	}, nil
}

func appMainAccountDirectorySyncLoop(ctx context.Context, directory *accountdataplane.AccountDirectory) {
	idleInterval := appMainEnvInt("ACCOUNT_SYNC_INTERVAL", 30)
	activeInterval := appMainEnvInt("ACCOUNT_SYNC_ACTIVE_INTERVAL", 3)
	const idleAfter = 5
	idleStreak := 0
	for {
		interval := idleInterval
		if idleStreak < idleAfter {
			interval = activeInterval
		}
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		changed, err := directory.SyncIfChanged(ctx)
		if err != nil {
			idleStreak = idleAfter
			continue
		}
		if changed {
			idleStreak = 0
		} else if idleStreak < idleAfter {
			idleStreak++
		}
	}
}

func defaultAppMainStartRefreshRuntime(ctx context.Context, state *appMainLifecycleState) (Hook, error) {
	if state.repository == nil {
		return nil, nil
	}
	service := accountcontrol.NewAccountRefreshService(state.repository, accountcontrol.AccountRefreshOptions{})
	scheduler := accountcontrol.GetAccountRefreshScheduler(service)
	leader := true
	var localLockCleanup Hook
	if state.runtimeStore != nil {
		lease, err := state.runtimeStore.AcquireLock(ctx, "scheduler-leader", platformruntime.RedisRuntimeLockOptions{
			TTLMS: appMainEnvInt("RUNTIME_REDIS_LOCK_TTL_MS", 300000),
		})
		if err != nil {
			localLockCleanup, err = appMainAcquireSchedulerFileLock(ctx)
			if err != nil {
				return nil, err
			}
			leader = localLockCleanup != nil
		} else {
			state.schedulerKey = lease
			leader = lease != nil
		}
	} else {
		cleanup, err := appMainAcquireSchedulerFileLock(ctx)
		if err != nil {
			return nil, err
		}
		localLockCleanup = cleanup
		leader = localLockCleanup != nil
	}
	state.bindAdminRuntimeWithRefresh(service)
	consoleResetCleanup := appMainStartConsoleQuotaResetLoop(service, appMainConsoleResetInterval)
	accountcontrol.SetRefreshService(service)
	accountcontrol.SetRefreshScheduler(scheduler)
	accountcontrol.SetRefreshSchedulerLeader(leader)
	accountcontrol.ReconcileRefreshRuntime()
	return func(ctx context.Context) error {
		if consoleResetCleanup != nil {
			consoleResetCleanup(ctx)
			consoleResetCleanup = nil
		}
		scheduler.Stop()
		if state.schedulerKey != nil {
			_, _ = state.schedulerKey.Release(ctx)
			state.schedulerKey = nil
		}
		if localLockCleanup != nil {
			if err := localLockCleanup(ctx); err != nil {
				return err
			}
			localLockCleanup = nil
		}
		accountcontrol.SetRefreshScheduler(nil)
		accountcontrol.SetRefreshSchedulerLeader(false)
		accountcontrol.SetRefreshService(nil)
		state.bindAdminRuntime()
		return nil
	}, nil
}

func appMainStartConsoleQuotaResetLoop(service *accountcontrol.AccountRefreshService, interval time.Duration) Hook {
	if service == nil || interval <= 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_, _ = service.ResetExpiredConsoleWindows(ctx)
				timer.Reset(interval)
			}
		}
	}()
	return func(context.Context) error {
		cancel()
		<-done
		return nil
	}
}

func (state *appMainLifecycleState) bindAdminRuntime() {
	state.bindAdminRuntimeWithRefresh(nil)
}

func (state *appMainLifecycleState) bindAdminRuntimeWithRefresh(service *accountcontrol.AccountRefreshService) {
	state.clearAdminRuntime()
	state.adminCleanup = adminproduct.BindAccountRuntime(state.repository, state.directory, service)
}

func (state *appMainLifecycleState) clearAdminRuntime() {
	if state.adminCleanup == nil {
		return
	}
	state.adminCleanup()
	state.adminCleanup = nil
}

func defaultAppMainStartProxyScheduler(ctx context.Context, _ *appMainLifecycleState) (Hook, error) {
	if !accountcontrol.IsRefreshSchedulerLeader() {
		return nil, nil
	}
	directory, err := proxycontrol.GetProxyDirectory(ctx)
	if err != nil {
		return nil, err
	}
	scheduler := proxycontrol.NewProxyClearanceScheduler(directory)
	scheduler.Start(ctx)
	return func(context.Context) error {
		scheduler.Stop()
		return nil
	}, nil
}

func appMainEnv() map[string]string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func appMainEnvInt(key string, defaultValue int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}
