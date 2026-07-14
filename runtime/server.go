package runtime

import (
	"context"
	"database/sql"
	"net"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/sse"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/internal/web"
	"github.com/yasyf/cc-runtime/version"
)

// buildServer composes the cc-runtime daemon: the interaction ops, the edit
// gate, the projection schema, the channel presence lifecycle, the access
// plane (bind, token, Bonjour, tailnet TLS) from persisted access config, and
// the Web Push surface feeding every question/notification append.
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
	vapid, err := st.EnsureVAPID()
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
		Migrate:           migrate,
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
	sender := access.NewPushSender(vapid, s.DB(), s.Append)
	access.MountPush(s.Mux(), sender)
	interaction.MountREST(s)
	// Catch-all SPA mount; the pattern mux keeps /events and /api routes ahead
	// of it, and the auth handler wraps them all.
	s.Mux().Handle("/", sse.StaticHandler(web.Dist()))
	interaction.Register(s, pushFanout{sender: sender})
	return s, nil
}

// migrate layers the interaction projection and the push-plane schema onto
// cc-interact's core tables.
func migrate(ctx context.Context, db *sql.DB) error {
	if err := interaction.Migrate(ctx, db); err != nil {
		return err
	}
	return access.PushMigrate(ctx, db)
}

func serve(ctx context.Context) error {
	s, err := buildServer(ctx)
	if err != nil {
		return err
	}
	return s.Serve(ctx)
}
