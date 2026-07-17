package runtime

import (
	"context"
	"database/sql"
	"net"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/sse"
	"github.com/yasyf/synckit/meshtrust"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/internal/web"
	"github.com/yasyf/cc-runtime/mesh"
	"github.com/yasyf/cc-runtime/version"
)

// buildServer composes the cc-runtime daemon: the interaction ops, the edit
// gate, the projection schema, the channel presence lifecycle, the access
// plane (bind, token, Bonjour, tailnet TLS) from persisted access config,
// tokenless mesh trust when the shared synckit mesh state exists, and the
// push lanes — Web Push always, direct APNs when configured — feeding every
// question/notification append.
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
	apnsCfg, err := st.ReadAPNSConfig()
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
		// The plain-HTTP plane never leaves loopback (the zero BindAddr);
		// remote access rides the TLS extra listeners below, so the bearer
		// token never crosses a network in cleartext.
		HTTPToken:   token,
		OnHTTPStart: access.BonjourHook(acfg.Bind),
		// The SPA shell serves outside the auth guard: a remote browser must
		// fetch assets and sw.js before any script can attach the token. Data
		// routes (/events, /api) stay on the auth-wrapped mux.
		PublicHandler: sse.StaticHandler(web.Dist()),
	}
	if !access.IsLoopbackBind(acfg.Bind) {
		lanCert, err := st.EnsureLANCert()
		if err != nil {
			return nil, err
		}
		cfg.ExtraHTTPListeners = []func(context.Context) (net.Listener, error){
			access.LANTLSListenerFactory(lanCert),
		}
		if ts, ok := access.DetectTailscale(ctx); ok {
			certs := access.NewCertProvider(st.CertDir(), ts.FQDN)
			cfg.ExtraHTTPListeners = append(cfg.ExtraHTTPListeners, access.TLSListenerFactory(ts, certs))
		}
	}
	// Tokenless mesh trust: no synckit state ⇒ nil provider ⇒ the pair/token
	// and TLS legs above run exactly as before.
	if tp := meshtrust.Detect(); tp != nil {
		cfg.TrustedPeer = tp.TrustedPeer
		cfg.TrustedOrigin = tp.TrustedOrigin
		cfg.ExtraHTTPListeners = append(cfg.ExtraHTTPListeners, tailnetListeners(interaction.AppPaths(), acfg.Bind, tp.SelfAddrs(ctx))...)
	}
	s, err := daemon.New(cfg)
	if err != nil {
		return nil, err
	}
	sender := access.NewPushSender(vapid, s.DB(), s.Append)
	if err := sender.ReconcileGrants(ctx, token, acfg.Bind); err != nil {
		return nil, err
	}
	access.MountPush(s.Mux(), sender)
	senders := []pushSender{sender}
	// A disabled APNs lane holds no delivery grants: purging on boot keeps
	// `apns off` from resurrecting old registrations when re-enabled.
	if apnsCfg.Enabled() {
		apns, err := access.NewAPNSSender(apnsCfg, s.DB(), s.Append)
		if err != nil {
			return nil, err
		}
		access.MountAPNS(s.Mux(), apns, token)
		senders = append(senders, apns)
	} else if err := access.PurgeDeviceTokens(ctx, s.DB(), s.Append); err != nil {
		return nil, err
	}
	interaction.MountREST(s)
	s.Register(mesh.OpPresence, mesh.PresenceHandler)
	router := mesh.NewRouter(mesh.NewExecRunner(), rpcDial)
	interaction.Register(s, pushFanout{senders: senders, background: s.Background, router: router})
	return s, nil
}

// migrate layers the interaction projection and the push-plane schema onto
// cc-interact's core tables.
func migrate(ctx context.Context, db *sql.DB) error {
	if err := interaction.Migrate(ctx, db); err != nil {
		return err
	}
	if err := access.PushMigrate(ctx, db); err != nil {
		return err
	}
	return access.APNSMigrate(ctx, db)
}

func serve(ctx context.Context) error {
	s, err := buildServer(ctx)
	if err != nil {
		return err
	}
	return s.Serve(ctx)
}
