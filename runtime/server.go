package runtime

import (
	"context"
	"errors"
	"net"

	"github.com/yasyf/cc-interact/channel"
	"github.com/yasyf/cc-interact/daemon"
	"github.com/yasyf/cc-interact/sse"
	"github.com/yasyf/cc-interact/store"
	"github.com/yasyf/daemonkit"
	"github.com/yasyf/synckit/meshtrust"

	"github.com/yasyf/cc-runtime/access"
	"github.com/yasyf/cc-runtime/interaction"
	"github.com/yasyf/cc-runtime/internal/processowner"
	"github.com/yasyf/cc-runtime/internal/web"
	"github.com/yasyf/cc-runtime/mesh"
	"github.com/yasyf/cc-runtime/version"
)

// daemonProcessStore records the daemon's owned mesh processes, so a crash
// leaves children the next generation reclaims rather than orphans.
const daemonProcessStore = "daemon-mesh-processes.db"

// buildServer composes the cc-runtime daemon: the interaction ops, the edit
// gate, the projection schema, the channel presence lifecycle, the access
// plane (bind, token, Bonjour, tailnet TLS) from persisted access config,
// tokenless mesh trust when the shared synckit mesh state exists, and the
// push lanes — Web Push always, direct APNs when configured — feeding every
// question/notification append.
func buildServer(ctx context.Context, spec daemonkit.Daemon, processes *daemonkit.Owned) (*daemon.Server, error) {
	if err := mesh.Initialize(ctx); err != nil {
		return nil, err
	}
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
		Daemon:            spec,
		RuntimeBuild:      version.Version,
		ActiveStatuses:    interaction.ActiveStatuses,
		Gate:              interaction.Gate(),
		GateErrorReason:   interaction.GateErrorReason,
		StoreSchema:       store.Compose(interaction.StoreSchema, access.PushStoreSchema, access.APNSStoreSchema),
		PresenceEventType: conn.Type(),
		OnPresenceChange:  conn.OnPresenceChange,
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
	runner := mesh.NewExecRunner(processes)
	router := mesh.NewRouter(runner, func(string) mesh.Runner { return runner })
	fanout := &pushFanout{router: router}
	cfg.BootReconcile = func(bootCtx context.Context, active *daemon.Server) error {
		sender := access.NewPushSender(vapid, active.DB(), active.Append)
		if err := sender.ReconcileGrants(bootCtx, token, acfg.Bind); err != nil {
			return err
		}
		senders := []pushSender{sender}
		// A disabled APNs lane holds no delivery grants: purging on boot keeps
		// `apns off` from resurrecting old registrations when re-enabled.
		if apnsCfg.Enabled() {
			apns, err := access.NewAPNSSender(apnsCfg, active.DB(), active.Append)
			if err != nil {
				return err
			}
			senders = append(senders, apns)
		} else if err := access.PurgeDeviceTokens(bootCtx, active.DB(), active.Append); err != nil {
			return err
		}
		fanout.setSenders(senders)
		if err := interaction.ReplayNotificationDeliveries(bootCtx, active.DB(), fanout); err != nil {
			return err
		}
		return conn.BootReconcile(bootCtx, active)
	}
	s, err := daemon.New(cfg)
	if err != nil {
		return nil, err
	}
	access.RegisterPush(s, vapid)
	access.MountPush(s)
	if apnsCfg.Enabled() {
		access.RegisterAPNS(s, token)
		access.MountAPNS(s, token)
	}
	interaction.MountREST(s)
	s.Register(mesh.OpPresence, mesh.PresenceHandler)
	fanout.background = s.Background
	interaction.Register(s, fanout)
	return s, nil
}

func serve(ctx context.Context) (err error) {
	spec, err := interaction.DaemonSpec()
	if err != nil {
		return err
	}
	processes, err := processowner.Open(ctx, appPaths().StateDir(), daemonProcessStore)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, processowner.Close(ctx, processes)) }()
	s, err := buildServer(ctx, spec, processes)
	if err != nil {
		return err
	}
	return s.Serve(ctx)
}
