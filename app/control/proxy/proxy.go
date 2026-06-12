package proxy

import (
	"context"
	"reflect"
	"sync"

	platformruntime "github.com/dslzl/gork/app/platform/runtime"
)

const defaultClearanceOrigin = "https://grok.com"

type BundleKey struct {
	Affinity      string
	ClearanceHost string
}

type DirectoryConfig interface {
	StringConfig
	GetList(key string, defaultValue []string) []string
	GetInt(key string, defaultValue int) int
}

type ManualClearanceProvider interface {
	BuildBundle(affinityKey string, clearanceHost ...string) (ClearanceBundle, bool, error)
}

type FlareClearanceProvider interface {
	RefreshBundle(ctx context.Context, affinityKey, proxyURL string, targetURL ...string) (ClearanceBundle, bool, error)
}

type DirectoryOptions struct {
	Config         DirectoryConfig
	ManualProvider ManualClearanceProvider
	FlareProvider  FlareClearanceProvider
	IDGenerator    func() string
	Clock          func() int64
}

type AcquireOptions struct {
	Scope           ProxyScope
	Kind            RequestKind
	Resource        bool
	ClearanceOrigin string
}

type ProxyDirectory struct {
	mu            sync.Mutex
	nodes         []EgressNode
	resourceNodes []EgressNode
	bundles       map[BundleKey]ClearanceBundle
	refreshEvents map[BundleKey]*refreshEvent
	egressMode    EgressMode
	clearanceMode ClearanceMode
	configSig     *directoryConfigSignature
	poolCursor    int
	config        DirectoryConfig
	manual        ManualClearanceProvider
	flare         FlareClearanceProvider
	idGenerator   func() string
	clock         func() int64
}

type directoryConfigSignature struct {
	EgressMode    string
	ClearanceMode string
	BaseURL       string
	ResourceURL   string
	BasePool      []string
	ResourcePool  []string
	FlareURL      string
	CFCookies     string
	UserAgent     string
	CFClearance   string
	Browser       string
	TimeoutSec    int
}

type emptyDirectoryConfig struct{}

func (emptyDirectoryConfig) GetString(_ string, defaultValue string) string { return defaultValue }
func (emptyDirectoryConfig) GetList(_ string, defaultValue []string) []string {
	return append([]string(nil), defaultValue...)
}
func (emptyDirectoryConfig) GetInt(_ string, defaultValue int) int { return defaultValue }

func NewProxyDirectory(options ...DirectoryOptions) *ProxyDirectory {
	var option DirectoryOptions
	if len(options) > 0 {
		option = options[0]
	}
	cfg := option.Config
	if cfg == nil {
		cfg = globalDirectoryConfig{}
	}
	manual := option.ManualProvider
	if manual == nil {
		manual = configManualClearanceProvider{config: cfg}
	}
	flare := option.FlareProvider
	if flare == nil {
		flare = noopFlareClearanceProvider{}
	}
	idGenerator := option.IDGenerator
	if idGenerator == nil {
		idGenerator = func() string { return platformruntime.NextHex() }
	}
	clock := option.Clock
	if clock == nil {
		clock = platformruntime.NowMS
	}
	return &ProxyDirectory{
		bundles:       map[BundleKey]ClearanceBundle{},
		refreshEvents: map[BundleKey]*refreshEvent{},
		egressMode:    EgressModeDirect,
		clearanceMode: ClearanceModeNone,
		config:        cfg,
		manual:        manual,
		flare:         flare,
		idGenerator:   idGenerator,
		clock:         clock,
	}
}

func (d *ProxyDirectory) Load(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := d.config
	egressMode, err := parseEgressMode(cfg.GetString("proxy.egress.mode", "direct"))
	if err != nil {
		return err
	}
	clearanceMode, err := ParseClearanceMode(cfg.GetString("proxy.clearance.mode", "none"))
	if err != nil {
		return err
	}
	baseURL := cfg.GetString("proxy.egress.proxy_url", "")
	resourceURL := cfg.GetString("proxy.egress.resource_proxy_url", "")
	basePool := cfg.GetList("proxy.egress.proxy_pool", nil)
	resourcePool := cfg.GetList("proxy.egress.resource_proxy_pool", nil)
	clearance := ResolveClearanceConfig(cfg)
	sig := directoryConfigSignature{
		EgressMode:    string(egressMode),
		ClearanceMode: string(clearanceMode),
		BaseURL:       baseURL,
		ResourceURL:   resourceURL,
		BasePool:      append([]string(nil), basePool...),
		ResourcePool:  append([]string(nil), resourcePool...),
		FlareURL:      cfg.GetString("proxy.clearance.flaresolverr_url", ""),
		CFCookies:     clearance.CFCookies,
		UserAgent:     clearance.UserAgent,
		CFClearance:   clearance.CFClearance,
		Browser:       clearance.Browser,
		TimeoutSec:    cfg.GetInt("proxy.clearance.timeout_sec", 60),
	}

	nodes, resourceNodes := buildEgressNodes(egressMode, baseURL, resourceURL, basePool, resourcePool)
	validAffinities := validAffinities(nodes, resourceNodes)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.configSig != nil && reflect.DeepEqual(*d.configSig, sig) {
		return nil
	}
	d.egressMode = egressMode
	d.clearanceMode = clearanceMode
	d.nodes = nodes
	d.resourceNodes = resourceNodes
	d.poolCursor = 0
	d.bundles = invalidateMatchingBundles(d.bundles, validAffinities)
	d.refreshEvents = filterRefreshEvents(d.refreshEvents, validAffinities)
	d.configSig = &sig
	return nil
}

func (d *ProxyDirectory) Acquire(ctx context.Context, options ...AcquireOptions) (ProxyLease, error) {
	option := defaultAcquireOptions(options...)
	proxyURL, err := d.pickProxyURL(option.Resource)
	if err != nil {
		return ProxyLease{}, err
	}
	affinity := "direct"
	proxyValue := ""
	if proxyURL != nil {
		affinity = *proxyURL
		proxyValue = *proxyURL
	}
	origin := option.ClearanceOrigin
	if origin == "" {
		origin = defaultClearanceOrigin
	}
	bundle, err := d.getOrBuildBundle(ctx, affinity, proxyValue, origin)
	if err != nil {
		return ProxyLease{}, err
	}
	lease := NewProxyLease(d.idGenerator())
	lease.ProxyURL = proxyURL
	lease.ClearanceHost = clearanceHost(origin)
	lease.Scope = option.Scope
	lease.Kind = option.Kind
	lease.AcquiredAt = d.clock()
	if bundle != nil {
		lease.CFCookies = bundle.CFCookies
		lease.UserAgent = bundle.UserAgent
	}
	return lease, nil
}

func (d *ProxyDirectory) Feedback(_ context.Context, lease ProxyLease, result ProxyFeedback) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if result.Kind == ProxyFeedbackChallenge || result.Kind == ProxyFeedbackUnauthorized {
		key := BundleKey{Affinity: leaseAffinity(lease), ClearanceHost: leaseClearanceHost(lease)}
		if bundle, ok := d.bundles[key]; ok {
			bundle.State = ClearanceBundleInvalid
			d.bundles[key] = bundle
		}
	}
	if d.egressMode == EgressModeProxyPool && lease.HasProxy() && rotatesPool(result.Kind) {
		d.poolCursor++
	}
	return nil
}

func (d *ProxyDirectory) EgressMode() EgressMode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.egressMode
}

func (d *ProxyDirectory) ClearanceMode() ClearanceMode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clearanceMode
}

func (d *ProxyDirectory) NodeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.nodes)
}

func (d *ProxyDirectory) Nodes() []EgressNode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copyNodes(d.nodes)
}

func (d *ProxyDirectory) Bundles() map[BundleKey]ClearanceBundle {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[BundleKey]ClearanceBundle, len(d.bundles))
	for key, bundle := range d.bundles {
		out[key] = bundle
	}
	return out
}

func (d *ProxyDirectory) pickProxyURL(resource bool) (*string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.egressMode == EgressModeDirect {
		return nil, nil
	}
	nodes := d.nodes
	if resource && len(d.resourceNodes) > 0 {
		nodes = d.resourceNodes
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	if d.egressMode == EgressModeSingleProxy {
		return cloneStringPtr(nodes[0].ProxyURL), nil
	}
	idx := d.poolCursor % len(nodes)
	return cloneStringPtr(nodes[idx].ProxyURL), nil
}

var (
	globalDirectoryMu sync.Mutex
	globalDirectory   *ProxyDirectory
)

func GetProxyDirectory(ctx context.Context, options ...DirectoryOptions) (*ProxyDirectory, error) {
	globalDirectoryMu.Lock()
	if globalDirectory == nil {
		globalDirectory = NewProxyDirectory(options...)
	}
	directory := globalDirectory
	globalDirectoryMu.Unlock()
	if err := directory.Load(ctx); err != nil {
		return nil, err
	}
	return directory, nil
}
