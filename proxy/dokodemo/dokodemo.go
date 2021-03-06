package dokodemo

//go:generate go run $GOPATH/src/v2ray.com/core/tools/generrorgen/main.go -pkg dokodemo -path Proxy,Dokodemo

import (
	"context"
	"runtime"
	"time"

	"v2ray.com/core/app"
	"v2ray.com/core/app/dispatcher"
	"v2ray.com/core/app/log"
	"v2ray.com/core/common"
	"v2ray.com/core/common/buf"
	"v2ray.com/core/common/net"
	"v2ray.com/core/common/signal"
	"v2ray.com/core/proxy"
	"v2ray.com/core/transport/internet"
)

type DokodemoDoor struct {
	config  *Config
	address net.Address
	port    net.Port
}

func New(ctx context.Context, config *Config) (*DokodemoDoor, error) {
	space := app.SpaceFromContext(ctx)
	if space == nil {
		return nil, newError("no space in context")
	}
	if config.NetworkList == nil || config.NetworkList.Size() == 0 {
		return nil, newError("no network specified")
	}
	d := &DokodemoDoor{
		config:  config,
		address: config.GetPredefinedAddress(),
		port:    net.Port(config.Port),
	}
	return d, nil
}

func (d *DokodemoDoor) Network() net.NetworkList {
	return *(d.config.NetworkList)
}

func (d *DokodemoDoor) Process(ctx context.Context, network net.Network, conn internet.Connection, dispatcher dispatcher.Interface) error {
	log.Trace(newError("processing connection from: ", conn.RemoteAddr()).AtDebug())
	dest := net.Destination{
		Network: network,
		Address: d.address,
		Port:    d.port,
	}
	if d.config.FollowRedirect {
		if origDest, ok := proxy.OriginalTargetFromContext(ctx); ok {
			dest = origDest
		}
	}
	if !dest.IsValid() || dest.Address == nil {
		return newError("unable to get destination")
	}

	timeout := time.Second * time.Duration(d.config.Timeout)
	if timeout == 0 {
		timeout = time.Minute * 2
	}
	ctx, timer := signal.CancelAfterInactivity(ctx, timeout)

	inboundRay, err := dispatcher.Dispatch(ctx, dest)
	if err != nil {
		return err
	}

	requestDone := signal.ExecuteAsync(func() error {
		defer inboundRay.InboundInput().Close()

		chunkReader := buf.NewReader(conn)

		if err := buf.PipeUntilEOF(timer, chunkReader, inboundRay.InboundInput()); err != nil {
			return newError("failed to transport request").Base(err)
		}

		return nil
	})

	responseDone := signal.ExecuteAsync(func() error {
		v2writer := buf.NewWriter(conn)

		if err := buf.PipeUntilEOF(timer, inboundRay.InboundOutput(), v2writer); err != nil {
			return newError("failed to transport response").Base(err)
		}
		return nil
	})

	if err := signal.ErrorOrFinish2(ctx, requestDone, responseDone); err != nil {
		inboundRay.InboundInput().CloseError()
		inboundRay.InboundOutput().CloseError()
		return newError("connection ends").Base(err)
	}

	runtime.KeepAlive(timer)

	return nil
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*Config))
	}))
}
