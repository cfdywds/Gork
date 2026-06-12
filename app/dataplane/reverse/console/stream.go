package console

import (
	"context"

	controlproxy "github.com/dslzl/gork/app/control/proxy"
	"github.com/dslzl/gork/app/dataplane/reverse/protocol"
	reverseruntime "github.com/dslzl/gork/app/dataplane/reverse/runtime"
	"github.com/dslzl/gork/app/dataplane/reverse/transport"
)

func StreamChat(ctx context.Context, token string, payload map[string]any, timeoutS float64) ([]protocol.ConsoleStreamEvent, error) {
	return protocol.StreamConsoleChat(ctx, token, payload, protocol.ConsoleStreamOptions{
		Proxy:    &proxyDirectoryAdapter{},
		Poster:   transport.ConsoleStreamPoster{},
		TimeoutS: timeoutS,
	})
}

type proxyDirectoryAdapter struct {
	directory *controlproxy.ProxyDirectory
}

func (a *proxyDirectoryAdapter) Acquire(ctx context.Context) (controlproxy.ProxyLease, error) {
	directory, err := controlproxy.GetProxyDirectory(ctx)
	if err != nil {
		return controlproxy.ProxyLease{}, err
	}
	a.directory = directory
	return directory.Acquire(ctx, controlproxy.AcquireOptions{
		Scope:           controlproxy.ProxyScopeApp,
		Kind:            controlproxy.RequestKindHTTP,
		ClearanceOrigin: reverseruntime.ConsoleBase,
	})
}

func (a *proxyDirectoryAdapter) Feedback(ctx context.Context, lease controlproxy.ProxyLease, feedback controlproxy.ProxyFeedback) error {
	directory := a.directory
	if directory == nil {
		var err error
		directory, err = controlproxy.GetProxyDirectory(ctx)
		if err != nil {
			return err
		}
	}
	return directory.Feedback(ctx, lease, feedback)
}
