package openai

import (
	"context"
	"fmt"
	controlaccount "github.com/dslzl/gork/app/control/account"
	"github.com/dslzl/gork/app/control/model"
	dataaccount "github.com/dslzl/gork/app/dataplane/account"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	"github.com/dslzl/gork/app/dataplane/reverse/transport"
	platformconfig "github.com/dslzl/gork/app/platform/config"
	appruntime "github.com/dslzl/gork/app/platform/runtime"
	"github.com/dslzl/gork/app/platform/storage"
	"github.com/dslzl/gork/app/products"
	"io"
	"regexp"
)

var sourcesStripRE = regexp.MustCompile(`(?is)\n*\s*(?:Sources|Citations):\s*\n(?:[-*]\s+.*(?:\n|$))+`)

var (
	imageFormatConfig        = "grok_url"
	proxyImaginePublicConfig = false
	appURLConfig             = ""
	downloadImageBytes       = func(ctx context.Context, token string, rawURL string) ([]byte, string, error) {
		result, err := transport.DownloadAsset(ctx, token, rawURL)
		if err != nil {
			return nil, "", err
		}
		defer result.Stream.Close()
		raw, err := io.ReadAll(result.Stream)
		if err != nil {
			return nil, "", err
		}
		mime := "image/jpeg"
		if result.ContentType != nil && *result.ContentType != "" {
			mime = *result.ContentType
		}
		return raw, mime, nil
	}
	saveImage = func(raw []byte, mime string, imageID string) string {
		fileID, err := storage.SaveLocalImage(raw, mime, imageID)
		if err != nil {
			return ""
		}
		return fileID
	}
	uploadInput = func(ctx context.Context, token string, fileInput string) (string, string, error) {
		result, err := transport.UploadFromInput(ctx, token, fileInput)
		if err != nil {
			return "", "", err
		}
		return result.FileID, result.FileURI, nil
	}
	streamPost              = defaultChatStreamPost
	currentAccountStrategy  = dataaccount.CurrentStrategy
	chatRefreshService      = defaultChatRefreshService
	isInvalidCredentials    = protocol.IsInvalidCredentialsError
	chatFeatureStream       = func() bool { return platformconfig.GlobalConfig.GetBool("features.stream", true) }
	chatFeatureThinking     = func() bool { return platformconfig.GlobalConfig.GetBool("features.thinking", true) }
	chatSelectionMaxRetries = func() int { return products.SelectionMaxRetries() }
	chatRetryConfig         = defaultChatRetryConfig
	chatTimeoutSeconds      = func() float64 { return platformconfig.GlobalConfig.GetFloat("chat.timeout", 120.0) }
	chatResponseID          = func() string { return MakeResponseID() }
	chatDirectoryProvider   = defaultChatDirectoryProvider
	consoleCompletions      = func(context.Context, chatCompletionOptions) (chatCompletionResult, error) {
		return chatCompletionResult{}, fmt.Errorf("console chat completions are not configured")
	}
)

type chatDataDirectory struct {
	directory *dataaccount.AccountDirectory
}

type chatReserveDirectory struct {
	directory *dataaccount.AccountDirectory
}

type chatRuntimeRefreshProvider interface {
	RefreshCallAsync(context.Context, string, int) error
	RecordFailureAsync(context.Context, string, int, error) error
	RefreshOnDemand(context.Context) (controlaccount.RefreshResult, error)
}

type chatRefreshAdapter struct {
	service chatRuntimeRefreshProvider
}

func defaultChatDirectoryProvider() chatDirectory {
	directory, err := dataaccount.GetAccountDirectory(context.Background(), nil)
	if err != nil || directory == nil {
		return nil
	}
	return chatDataDirectory{directory: directory}
}

func (d chatDataDirectory) ReserveChatAccount(ctx context.Context, spec model.ModelSpec, excluded []string) (chatAccount, bool, error) {
	nowS := appruntime.NowS()
	lease, selectedMode, ok, err := products.ReserveAccount(ctx, chatReserveDirectory{directory: d.directory}, spec, products.ReserveAccountOptions{
		ExcludeTokens: excluded,
		NowSOverride:  &nowS,
	})
	if err != nil || !ok {
		return chatAccount{}, ok, err
	}
	accountLease, ok := lease.(dataaccount.AccountLease)
	if !ok {
		return chatAccount{}, false, fmt.Errorf("unexpected account lease type %T", lease)
	}
	return chatAccount{Token: accountLease.Token, ModeID: selectedMode, lease: accountLease}, true, nil
}

func (d chatDataDirectory) ReleaseChatAccount(_ context.Context, account chatAccount) error {
	d.directory.Release(account.lease)
	return nil
}

func (d chatDataDirectory) FeedbackChatAccount(_ context.Context, feedback chatFeedback) error {
	d.directory.Feedback(feedback.Token, controlaccount.FeedbackKind(feedback.Kind), int(feedback.ModeID), dataaccount.FeedbackOptions{NowS: chatIntPtr(int(appruntime.NowS()))})
	return nil
}

func (d chatReserveDirectory) Reserve(_ context.Context, query products.ReserveAccountQuery) (any, error) {
	lease, ok := d.directory.Reserve(query.PoolCandidates, int(query.ModeID), dataaccount.ReserveOptions{
		ExcludeTokens: query.ExcludeTokens,
		NowS:          chatInt64PtrToIntPtr(query.NowSOverride),
	})
	if !ok {
		return nil, nil
	}
	return lease, nil
}

func defaultChatRetryConfig() map[string]any {
	raw := platformconfig.GlobalConfig.Get("retry.on_codes", nil)
	if raw == nil {
		return map[string]any{"retry.retry_status_codes": platformconfig.GlobalConfig.Get("retry.retry_status_codes", "429,401,503")}
	}
	return map[string]any{"retry.on_codes": raw}
}

func defaultChatRefreshService() chatRefreshProvider {
	service := controlaccount.GetRefreshService()
	if service == nil {
		return nil
	}
	provider, ok := any(service).(chatRuntimeRefreshProvider)
	if !ok {
		return nil
	}
	return chatRefreshAdapter{service: provider}
}

func (a chatRefreshAdapter) RefreshCall(ctx context.Context, token string, modeID int) error {
	return a.service.RefreshCallAsync(ctx, token, modeID)
}

func (a chatRefreshAdapter) RecordFailure(ctx context.Context, token string, modeID int, err error) error {
	return a.service.RecordFailureAsync(ctx, token, modeID, err)
}

func (a chatRefreshAdapter) RefreshOnDemand(ctx context.Context) (chatRefreshResult, error) {
	result, err := a.service.RefreshOnDemand(ctx)
	if err != nil {
		return chatRefreshResult{}, err
	}
	return chatRefreshResult{Refreshed: result.Refreshed, Failed: result.Failed, RateLimited: result.RateLimited}, nil
}

func chatIntPtr(value int) *int {
	return &value
}

func chatInt64PtrToIntPtr(value *int64) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}
