package runtime

import (
	"context"
	"net"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/daemon"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/version"
)

// buildServer composes the cc-runtime daemon: the interaction ops, the edit
// gate, the projection schema, the channel presence lifecycle, and the access
// plane (bind, token, Bonjour, tailnet TLS) from persisted access config.
func buildServer(ctx context.Context) (*daemon.Server, error) {
	st := access.Store{Dir: interaction.AppPaths().StateDir()}
	acfg, err := st.ReadConfig()
	if err != nil {
		return nil, err
	}
	token, err := st.ReadToken()
	if err != nil {
		return nil, err
	}

	conn := channel.Connectivity{}
	cfg := daemon.Config{
		AppName:           interaction.AppName,
		Paths:             interaction.AppPaths(),
		Version:           version.Version,
		ActiveStatuses:    interaction.ActiveStatuses,
		Gate:              interaction.Gate(),
		GateErrorReason:   interaction.GateErrorReason,
		Migrate:           interaction.Migrate,
		PresenceEventType: conn.Type(),
		OnPresenceChange:  conn.OnPresenceChange,
		BootReconcile:     conn.BootReconcile,
		BindAddr:          acfg.Bind,
		HTTPToken:         token,
		OnHTTPStart:       access.BonjourHook(acfg.Bind),
	}
	if !access.IsLoopbackBind(acfg.Bind) {
		if ts, ok := access.DetectTailscale(ctx); ok {
			certs := access.NewCertProvider(st.CertDir(), ts.FQDN)
			cfg.ExtraHTTPListeners = []func(context.Context) (net.Listener, error){
				access.TLSListenerFactory(ts, certs),
			}
		}
	}
	s, err := daemon.New(cfg)
	if err != nil {
		return nil, err
	}
	interaction.Register(s)
	return s, nil
}

func serve(ctx context.Context) error {
	s, err := buildServer(ctx)
	if err != nil {
		return err
	}
	return s.Serve(ctx)
}
