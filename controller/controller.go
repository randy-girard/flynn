package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flynn/que-go"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/name"
	"github.com/randy-girard/flynn/controller/schema"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	logaggc "github.com/randy-girard/flynn/logaggregator/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/pkg/status"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

var logger = log15.New("component", "controller")

var ErrNotFound = ct.ErrNotFound
var ErrShutdown = errors.New("controller: shutting down")

var schemaRoot = "/etc/flynn-controller/jsonschema"

// controllerHTTPInstance is the discoverd registration for the controller HTTP
// API. AUTH_KEY is intentionally omitted (SEC-028); consumers use AUTH_KEY or
// CONTROLLER_KEY from their environment.
func controllerHTTPInstance(addr string) *discoverd.Instance {
	return &discoverd.Instance{
		Addr:  addr,
		Proto: "http",
	}
}

func main() {
	defer shutdown.Exit()

	httpPort := os.Getenv("PORT")
	if httpPort == "" {
		httpPort = "3000"
	}
	httpAddr := ":" + httpPort

	grpcPort := os.Getenv("PORT_2")
	if grpcPort == "" {
		grpcPort = "3001"
	}
	grpcAddr := ":" + grpcPort

	if seed := os.Getenv("NAME_SEED"); seed != "" {
		s, err := hex.DecodeString(seed)
		if err != nil {
			log.Fatalln("error decoding NAME_SEED:", err)
		}
		name.SetSeed(s)
	}

	tokenKey, err := authorizer.ParseTokenKey(os.Getenv("ACCESS_TOKEN_KEY"))
	if err != nil {
		log.Fatalln("error decoding ACCESS_TOKEN_KEY:", err)
	}
	tokenMaxValidity, err := authorizer.ParseTokenMaxValidity(os.Getenv("ACCESS_TOKEN_MAX_VALIDITY"))
	if err != nil {
		log.Fatalln("error parsing ACCESS_TOKEN_MAX_VALIDITY:", err)
	}

	db := data.OpenAndMigrateDB(nil)
	shutdown.BeforeExit(func() { db.Close() })

	tokenSigner, err := tokenSignerFromEnvAndDB(db)
	if err != nil {
		log.Fatalln("error decoding ACCESS_TOKEN_SIGNING_KEY:", err)
	}

	lc, err := logaggc.New("")
	if err != nil {
		shutdown.Fatal(err)
	}

	doneCh := make(chan struct{})
	shutdown.BeforeExit(func() { close(doneCh) })

	// Listen for database migration, reset connpool on new migration
	go postgres.ResetOnMigration(db, logger, doneCh)

	httpService, err := discoverd.DefaultClient.AddServiceAndRegisterInstance("controller", controllerHTTPInstance(httpAddr))
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() {
		httpService.Close()
	})

	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { grpcListener.Close() })
	grpcService, err := discoverd.DefaultClient.AddServiceAndRegisterInstance("controller-grpc", &discoverd.Instance{
		Addr:  grpcAddr,
		Proto: "tcp",
	})
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() {
		grpcService.Close()
	})

	handler, grpcServer, api := appHandler(handlerConfig{
		db:               db,
		cc:               utils.ClusterClientWrapper(cluster.NewClient()),
		lc:               lc,
		keys:             strings.Split(os.Getenv("AUTH_KEY"), ","),
		keyIDs:           strings.Split(os.Getenv("AUTH_KEY_IDS"), ","),
		tokenKey:         tokenKey,
		tokenSigner:      tokenSigner,
		tokenMaxValidity: tokenMaxValidity,
		caCert:           []byte(os.Getenv("CA_CERT")),
	})
	go api.ensureAuthRouteLoop()
	go api.purgeExpiredOAuthLoop()
	go grpcServer.Serve(grpcListener)
	shutdown.Fatal(http.ListenAndServe(httpAddr, handler))
}

type logClient interface {
	GetLog(channelID string, options *logagg.LogOpts) (io.ReadCloser, error)
}

type handlerConfig struct {
	db               *postgres.DB
	cc               utils.ClusterClient
	lc               logClient
	keys             []string
	keyIDs           []string
	tokenKey         *ecdsa.PublicKey
	tokenSigner      *tokensigner.Signer
	tokenMaxValidity time.Duration
	caCert           []byte
	githubHTTP       *http.Client
	githubAPI        githubAPI
	taffy            taffyLauncher
	githubDomain     string
	githubPluginURL  string
}

// NOTE: this is temporary until httphelper supports custom errors
func respondWithError(w http.ResponseWriter, err error) {
	switch v := err.(type) {
	case ct.ValidationError:
		httphelper.ValidationError(w, v.Field, v.Message)
	default:
		if err == ErrNotFound {
			w.WriteHeader(404)
			return
		}
		httphelper.Error(w, err)
	}
}

func appHandler(c handlerConfig) (http.Handler, *grpc.Server, *controllerAPI) {
	err := schema.Load(schemaRoot)
	if err != nil {
		shutdown.Fatal(err)
	}

	q := que.NewClient(c.db.ConnPool)
	domainMigrationRepo := data.NewDomainMigrationRepo(c.db)
	providerRepo := data.NewProviderRepo(c.db)
	resourceRepo := data.NewResourceRepo(c.db)
	routeRepo := data.NewRouteRepo(c.db)
	appRepo := data.NewAppRepo(c.db, os.Getenv("DEFAULT_ROUTE_DOMAIN"), routeRepo)
	artifactRepo := data.NewArtifactRepo(c.db)
	releaseRepo := data.NewReleaseRepo(c.db, artifactRepo, q)
	jobRepo := data.NewJobRepo(c.db)
	formationRepo := data.NewFormationRepo(c.db, appRepo, releaseRepo, artifactRepo)
	deploymentRepo := data.NewDeploymentRepo(c.db, appRepo, releaseRepo, formationRepo)
	eventRepo := data.NewEventRepo(c.db)
	backupRepo := data.NewBackupRepo(c.db)
	tenantBackupRepo := data.NewTenantBackupRepo(c.db)
	sinkRepo := data.NewSinkRepo(c.db)
	volumeRepo := data.NewVolumeRepo(c.db)
	managedCertificateRepo := data.NewManagedCertificateRepo(c.db)
	acmeConfigRepo := data.NewACMEConfigRepo(c.db)
	runtimeProfileRepo := data.NewRuntimeProfileRepo(c.db)
	dbRuntimeRepo := data.NewDBRuntimeRepo(c.db)
	githubAppRepo := data.NewGitHubAppRepo(c.db)

	api := controllerAPI{
		domainMigrationRepo:    domainMigrationRepo,
		appRepo:                appRepo,
		releaseRepo:            releaseRepo,
		providerRepo:           providerRepo,
		formationRepo:          formationRepo,
		artifactRepo:           artifactRepo,
		jobRepo:                jobRepo,
		routeRepo:              routeRepo,
		resourceRepo:           resourceRepo,
		deploymentRepo:         deploymentRepo,
		eventRepo:              eventRepo,
		backupRepo:             backupRepo,
		tenantBackupRepo:       tenantBackupRepo,
		sinkRepo:               sinkRepo,
		volumeRepo:             volumeRepo,
		managedCertificateRepo: managedCertificateRepo,
		acmeConfigRepo:         acmeConfigRepo,
		runtimeProfileRepo:     runtimeProfileRepo,
		dbRuntimeRepo:          dbRuntimeRepo,
		buildLockRepo:          data.NewBuildLockRepo(c.db),
		tenancy:                data.NewTenancyRepo(c.db),
		githubStore:            githubAppRepo,
		githubHTTP:             c.githubHTTP,
		githubAPI:              c.githubAPI,
		taffy:                  c.taffy,
		githubDomain:           c.githubDomain,
		githubPluginURL:        c.githubPluginURL,
		clusterClient:          c.cc,
		logaggc:                c.lc,
		que:                    q,
		caCert:                 c.caCert,
		config:                 c,
		authorizer:             authorizer.New(c.keys, c.keyIDs, c.tokenKey, c.tokenMaxValidity),
		tokenSigner:            c.tokenSigner,
		limiter:                limiterFromEnv(),
		loginLimiter:           newLoginLimiter(loginLimitsFromEnv()),
		patLifetime:            patLifetimeFromEnv(),
	}
	if strings.TrimSpace(api.githubPluginURL) == "" {
		api.githubPluginURL = "http://github.discoverd"
	}

	shutdown.BeforeExit(api.Shutdown)

	rt := newRouteTable()
	registerRoutes(rt, &api, func() error { return c.db.Exec("ping") })
	httpRouter := rt.router

	grpcAPI := &grpcAPI{&api, c.db}
	grpcSrv := grpcAPI.grpcServer()

	handler := muxHandler(httpRouter, grpcSrv, api.authorizer, &api)
	if os.Getenv("AUDIT_LOG") == "true" {
		handler = httphelper.NewRequestLoggerCustom(handler, auditLoggerFn)
	} else {
		handler = httphelper.NewRequestLogger(handler)
	}
	return httphelper.ContextInjector("controller", handler), grpcSrv, &api
}

func muxHandler(main http.Handler, grpcSrv *grpc.Server, authorizer *authorizer.Authorizer, api *controllerAPI) http.Handler {
	grpcWeb := grpcweb.WrapServer(grpcSrv)
	return httphelper.CORSAllowAll.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shutdown.IsActive() {
			httphelper.ServiceUnavailableError(w, ErrShutdown.Error())
			return
		}

		if r.URL.Path == "/ping" {
			w.WriteHeader(200)
			return
		}

		// handle grpcweb requests before auth (grpc does its own auth)
		if grpcWeb.IsGrpcWebRequest(r) {
			grpcWeb.ServeHTTP(w, r)
			return
		}

		if preAuthPublic(r.Method, r.URL.Path) {
			main.ServeHTTP(w, r)
			return
		}
		auth, err := authorizer.AuthorizeRequest(r)
		if err != nil && api != nil {
			auth, err = api.authenticatePAT(r)
		}
		if err != nil {
			w.WriteHeader(401)
			return
		}
		// Count after a credential is known and before expandToken, which
		// reads the user and their grants. A 401 is not counted. Public
		// routes returned above and stay unlimited.
		if api != nil && !clusterKeyStatusExempt(r, auth) {
			decision := api.limiter.allow(auth)
			if !decision.allowed {
				if decision.log {
					logRateLimit(decision.class, decision.userID, decision.key)
				}
				writeRateLimited(w, decision.retryAfter)
				return
			}
		}
		if api != nil {
			if err := api.expandToken(auth); err != nil {
				w.WriteHeader(401)
				return
			}
		}
		if auth != nil && auth.Blocked {
			httphelper.Forbidden(w, "this account is suspended")
			return
		}
		var resolveApp func(string) string
		if api != nil && api.appRepo != nil {
			resolveApp = func(ref string) string {
				v, err := api.appRepo.Get(ref)
				if err != nil {
					return ""
				}
				if app, ok := v.(*ct.App); ok && app != nil {
					return app.ID
				}
				return ""
			}
		}
		if !routeAllowed(auth, r.Method, r.URL.Path, resolveApp) {
			if auth != nil && !auth.HasClusterAdmin() && len(auth.Scopes) > 0 {
				authz.NoteScopedReject(auth, r.Method, r.URL.Path, "route")
				logger.Info("scoped token rejected", "scope", strings.Join(auth.Scopes, ","), "caller", auth.UserID, "method", r.Method, "path", r.URL.Path, "reason", "route")
			}
			httphelper.Forbidden(w, "this credential is not allowed to perform this operation on the controller")
			return
		}
		rw, isRW := w.(*httphelper.ResponseWriter)
		if isRW {
			ctx := context.WithValue(rw.Context(), authz.TokenContextKey, auth)
			ctx = context.WithValue(ctx, apiContextKey{}, api)
			rw.SetContext(ctx)
		}
		if auth.ID != "" {
			r.Header.Set("Flynn-Auth-ID", auth.ID)
			r.Header.Set("Flynn-Auth-User", auth.User)
		}
		main.ServeHTTP(w, r)
		if isRW && api != nil && api.tenancy != nil {
			api.auditRequest(auth, r, rw.Status())
		}
	}))
}

type controllerAPI struct {
	domainMigrationRepo    *data.DomainMigrationRepo
	appRepo                *data.AppRepo
	releaseRepo            *data.ReleaseRepo
	providerRepo           *data.ProviderRepo
	formationRepo          *data.FormationRepo
	artifactRepo           *data.ArtifactRepo
	jobRepo                *data.JobRepo
	routeRepo              *data.RouteRepo
	resourceRepo           *data.ResourceRepo
	deploymentRepo         *data.DeploymentRepo
	eventRepo              *data.EventRepo
	backupRepo             *data.BackupRepo
	tenantBackupRepo       *data.TenantBackupRepo
	sinkRepo               *data.SinkRepo
	volumeRepo             *data.VolumeRepo
	managedCertificateRepo *data.ManagedCertificateRepo
	acmeConfigRepo         *data.ACMEConfigRepo
	runtimeProfileRepo     *data.RuntimeProfileRepo
	dbRuntimeRepo          *data.DBRuntimeRepo
	buildLockRepo          *data.BuildLockRepo
	tenancy                *data.TenancyRepo
	// hostedCheck is the production:check guard PUT /tenancy runs before a
	// switch to hosted. Nil runs the real rows (hostedProductionCheck);
	// tests replace it.
	hostedCheck func() error
	// buildJobMemoryLimits overrides the shipped host default for one-off
	// build jobs. Nil counts 12 GiB. False rejects the job.
	buildJobMemoryLimits *bool
	githubStore          githubStore
	githubAPI            githubAPI
	githubHTTP           *http.Client
	taffy                taffyLauncher
	githubDomain         string
	githubPluginURL      string
	clusterClient        utils.ClusterClient
	logaggc              logClient
	// archiveOutputSync stores deploy output before PutJob returns.
	// Production leaves it false and archives in the background. The
	// background read must not race a test that turns the flag on.
	archiveOutputSync atomic.Bool
	que               *que.Client
	caCert            []byte
	config            handlerConfig
	authorizer        *authorizer.Authorizer
	tokenSigner       *tokensigner.Signer
	limiter           *accountLimiter
	loginLimiter      *loginLimiter
	// patLifetime bounds personal access token expiry (CORE-50). The zero
	// value is the shipped default (90 days, at most 365).
	patLifetime patLifetimePolicy
	// lookupTXT resolves domain verification records. Nil uses the system
	// resolver. Tests replace it so they do not need live DNS.
	lookupTXT func(ctx context.Context, name string) ([]string, error)

	eventListener    *data.EventListener
	eventListenerMtx sync.Mutex
}

func (c *controllerAPI) getApp(ctx context.Context) *ct.App {
	return ctx.Value("app").(*ct.App)
}

func (c *controllerAPI) getRelease(ctx context.Context) (*ct.Release, error) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	data, err := c.releaseRepo.Get(params.ByName("releases_id"))
	if err != nil {
		return nil, err
	}
	return data.(*ct.Release), nil
}

func (c *controllerAPI) getProvider(ctx context.Context) (*ct.Provider, error) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	data, err := c.providerRepo.Get(params.ByName("providers_id"))
	if err != nil {
		return nil, err
	}
	return data.(*ct.Provider), nil
}

func (c *controllerAPI) appLookup(handler httphelper.HandlerFunc) httphelper.HandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, req *http.Request) {
		params, _ := ctxhelper.ParamsFromContext(ctx)
		data, err := c.appRepo.Get(params.ByName("apps_id"))
		if err != nil {
			respondWithError(w, err)
			return
		}
		app := data.(*ct.App)
		if !authz.SystemAppAllowed(authz.TokenFromContext(ctx), app.System()) {
			httphelper.Forbidden(w, "system apps require the cluster controller credential")
			return
		}
		ctx = context.WithValue(ctx, "app", app)
		handler(ctx, w, req)
	}
}

func routeParentRef(appID string) string {
	return ct.RouteParentRefPrefix + appID
}

func (c *controllerAPI) getRoute(ctx context.Context) (*router.Route, error) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	route, err := c.routeRepo.Get(params.ByName("routes_type"), params.ByName("routes_id"))
	if err == data.ErrRouteNotFound || err == nil && route.ParentRef != routeParentRef(c.getApp(ctx).ID) {
		err = ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return route, err
}

func (c *controllerAPI) GetCACert(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Write(c.caCert)
}

func (c *controllerAPI) Shutdown() {
	if c.eventListener != nil {
		c.eventListener.CloseWithError(ErrShutdown)
	}
}

// registerRoutes adds every controller HTTP route to rt. ping is the
// database check behind the status route.
//
// Each registration names who may call the route (an authz.Requirement).
// muxHandler checks it before the handler runs; a request no route serves
// is cluster-only. testdata/route_authz.golden pins the result for a matrix
// of callers (CORE-15).
func registerRoutes(rt *routeTable, api *controllerAPI, ping func() error) {
	crud(rt, "apps", ct.App{}, api.appRepo, crudAuthz{
		Create: authz.UserSession(),
		Get:    authz.AppAnyGrant(),
		List:   authz.UserSession().Also(authz.ScopeAccountQuota, authz.ScopeACMEIssue),
	})
	crudNoCreate(rt, "releases", ct.Release{}, api.releaseRepo, crudAuthz{
		Get:  authz.ClusterOnly(),
		List: authz.ClusterOnly(),
	})
	crud(rt, "providers", ct.Provider{}, api.providerRepo, crudAuthz{
		Create: authz.ClusterOnly(),
		Get:    authz.AnyAuth(),
		List:   authz.AnyAuth(),
	})
	crud(rt, "artifacts", ct.Artifact{}, api.artifactRepo, crudAuthz{
		Create: authz.BuildArtifact(),
		Get:    authz.ArtifactGet(),
		List:   authz.ClusterOnly(),
	})

	rt.handler("GET", status.Path, authz.ClusterOnly(), status.Handler(func() status.Status {
		if err := ping(); err != nil {
			return status.Unhealthy
		}
		return status.Healthy
	}))

	rt.GET("/ca-cert", authz.Public("TOFU: cluster:add downloads the CA before it can check the key"), httphelper.WrapHandler(api.GetCACert))
	rt.GET("/.well-known/oauth-authorization-server", authz.Public("OAuth metadata for the CLI login"), httphelper.WrapHandler(api.OAuthMetadata))
	rt.GET("/oauth/authorize", authz.Public("OAuth login page; the user signs in here"), httphelper.WrapHandler(api.OAuthAuthorize))
	rt.POST("/oauth/authorize", authz.Public("OAuth login form; checks the user's password"), httphelper.WrapHandler(api.OAuthAuthorize))
	rt.POST("/oauth/token", authz.Public("OAuth code exchange; checks the code and PKCE verifier"), httphelper.WrapHandler(api.OAuthToken))
	rt.GET("/oauth/audiences", authz.Public("OAuth audiences the CLI may request"), httphelper.WrapHandler(api.OAuthAudiences))

	rt.GET("/backup", authz.ClusterOnly(), httphelper.WrapHandler(api.GetBackup))

	rt.PUT("/domain", authz.ClusterOnly(), httphelper.WrapHandler(api.MigrateDomain))

	rt.POST("/apps/:apps_id", authz.App(authz.PermAppWrite), httphelper.WrapHandler(api.UpdateApp))
	rt.GET("/apps/:apps_id/log", authz.App(authz.PermAppLogsRead), httphelper.WrapHandler(api.appLookup(api.AppLog)))
	rt.DELETE("/apps/:apps_id", authz.App(authz.PermAppDelete), httphelper.WrapHandler(api.appLookup(api.DeleteApp)))
	rt.DELETE("/apps/:apps_id/releases/:releases_id", authz.App(authz.PermAppWrite), httphelper.WrapHandler(api.appLookup(api.DeleteRelease)))
	rt.POST("/apps/:apps_id/gc", authz.App(authz.PermAppDelete), httphelper.WrapHandler(api.appLookup(api.ScheduleAppGarbageCollection)))

	rt.PUT("/apps/:apps_id/formations/:releases_id", authz.App(authz.PermAppScaleWrite), httphelper.WrapHandler(api.appLookup(api.PutFormation)))
	rt.GET("/apps/:apps_id/formations/:releases_id", authz.App(authz.PermAppScaleRead), httphelper.WrapHandler(api.appLookup(api.GetFormation)))
	rt.DELETE("/apps/:apps_id/formations/:releases_id", authz.App(authz.PermAppScaleWrite), httphelper.WrapHandler(api.appLookup(api.DeleteFormation)))
	rt.GET("/apps/:apps_id/formations", authz.App(authz.PermAppScaleRead).Also(authz.ScopeAccountQuota), httphelper.WrapHandler(api.appLookup(api.ListFormations)))
	rt.GET("/formations", authz.ClusterOnly(), httphelper.WrapHandler(api.GetFormations))

	rt.PUT("/apps/:apps_id/scale/:releases_id", authz.App(authz.PermAppScaleWrite), httphelper.WrapHandler(api.appLookup(api.PutScaleRequest)))

	rt.POST("/apps/:apps_id/jobs", authz.App(authz.PermAppJobsRun), httphelper.WrapHandler(api.appLookup(api.RunJob)))
	// The cluster key, or a gitreceive:build token for that app. A
	// cluster:admin JWT may not create compiles.
	rt.POST("/apps/:apps_id/builds", authz.GitreceiveBuild(), httphelper.WrapHandler(api.appLookup(api.CreateBuild)))
	rt.GET("/apps/:apps_id/builds/:jobs_id/spec", authz.ClusterOnly(), httphelper.WrapHandler(api.appLookup(api.GetBuildSpec)))
	rt.POST("/apps/:apps_id/builds/:jobs_id/token", authz.ClusterOnly(), httphelper.WrapHandler(api.appLookup(api.MintBuildToken)))
	rt.GET("/builds/pending", authz.ClusterOnly(), httphelper.WrapHandler(api.ListPendingBuilds))
	rt.GET("/apps/:apps_id/jobs/:jobs_id", authz.App(authz.PermAppJobsRead), httphelper.WrapHandler(api.GetJob))
	// PUT is the scheduler's job-state upsert. A tenant's app:jobs:run may
	// start jobs (POST) but must not forge job rows.
	rt.PUT("/apps/:apps_id/jobs/:jobs_id", authz.ClusterOnly(), httphelper.WrapHandler(api.PutJob))
	rt.GET("/apps/:apps_id/jobs/:jobs_id/output", authz.App(authz.PermAppLogsRead), httphelper.WrapHandler(api.appLookup(api.GetJobOutput)))
	rt.GET("/apps/:apps_id/jobs", authz.App(authz.PermAppJobsRead), httphelper.WrapHandler(api.appLookup(api.ListJobs)))
	rt.DELETE("/apps/:apps_id/jobs/:jobs_id", authz.App(authz.PermAppJobsStop), httphelper.WrapHandler(api.appLookup(api.KillJob)))
	rt.GET("/apps/:apps_id/releases/:releases_id/output", authz.App(authz.PermAppLogsRead), httphelper.WrapHandler(api.appLookup(api.GetReleaseOutput)))
	rt.PUT("/apps/:apps_id/releases/:releases_id/output", authz.App(authz.PermAppWrite), httphelper.WrapHandler(api.appLookup(api.PutReleaseOutput)))
	rt.GET("/active-jobs", authz.ClusterOnly(), httphelper.WrapHandler(api.ListActiveJobs))

	rt.POST("/apps/:apps_id/deploy", authz.App(authz.PermAppDeploy), httphelper.WrapHandler(api.appLookup(api.CreateDeployment)))
	rt.GET("/apps/:apps_id/deployments", authz.App(authz.PermAppActivityRead), httphelper.WrapHandler(api.appLookup(api.ListDeployments)))
	rt.GET("/deployments/:deployment_id", authz.ClusterOnly(), httphelper.WrapHandler(api.GetDeployment))

	rt.PUT("/apps/:apps_id/release", authz.AppReleaseWrite(), httphelper.WrapHandler(api.appLookup(api.SetAppRelease)))
	rt.GET("/apps/:apps_id/release", authz.AppAnyGrant(), httphelper.WrapHandler(api.appLookup(api.GetAppRelease)))
	rt.GET("/apps/:apps_id/releases", authz.App(authz.PermAppActivityRead), httphelper.WrapHandler(api.appLookup(api.GetAppReleases)))
	rt.POST("/releases", authz.CreateRelease(), httphelper.WrapHandler(api.CreateRelease))

	// The handler keeps a user's list to the resources its accounts own.
	// App-scoped and compile tokens use /apps/:id/resources.
	rt.GET("/resources", authz.UserSession(), httphelper.WrapHandler(api.GetResources))
	rt.GET("/plugin-catalog", authz.UserSession(), httphelper.WrapHandler(api.GetPluginCatalog))
	// A provider row holds the plugin URL the controller calls with resource
	// credentials. Only bootstrap, flynn-host plugin installs and plugin
	// controller keys may change it (KNOWN-01, NEW-25).
	rt.PUT("/providers/:providers_id", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateProvider))
	rt.POST("/providers/:providers_id/resources", authz.ManageProvider(), httphelper.WrapHandler(api.ProvisionResource))
	rt.GET("/providers/:providers_id/resources", authz.AnyAuth(), httphelper.WrapHandler(api.GetProviderResources))
	rt.GET("/providers/:providers_id/resources/:resources_id", authz.ManageProvider(), httphelper.WrapHandler(api.GetResource))
	// One resource carries its credentials. Reading it is for a user or an
	// app grant (the handler checks the owner). Writing it is bootstrap,
	// migration and plugin controller keys only.
	rt.PUT("/providers/:providers_id/resources/:resources_id", authz.ClusterOnly(), httphelper.WrapHandler(api.PutResource))
	rt.DELETE("/providers/:providers_id/resources/:resources_id", authz.ManageProvider(), httphelper.WrapHandler(api.DeleteResource))
	rt.PUT("/providers/:providers_id/resources/:resources_id/apps/:app_id", authz.ManageProvider(), httphelper.WrapHandler(api.AddResourceApp))
	rt.DELETE("/providers/:providers_id/resources/:resources_id/apps/:app_id", authz.ManageProvider(), httphelper.WrapHandler(api.DeleteResourceApp))
	rt.GET("/apps/:apps_id/resources", authz.App(authz.PermAppResourcesRead), httphelper.WrapHandler(api.appLookup(api.GetAppResources)))
	rt.GET("/apps/:apps_id/plugin-addons", authz.App(authz.PermAppResourcesRead), httphelper.WrapHandler(api.appLookup(api.GetPluginAddons)))
	rt.PUT("/apps/:apps_id/plugin-addons/:addon_type", authz.App(authz.PermAppResourcesWrite), httphelper.WrapHandler(api.appLookup(api.PutPluginAddon)))
	rt.DELETE("/apps/:apps_id/plugin-addons/:addon_type", authz.App(authz.PermAppResourcesWrite), httphelper.WrapHandler(api.appLookup(api.DeletePluginAddon)))

	rt.GET("/routes", authz.UserSession().Also(authz.ScopeACMEIssue), httphelper.WrapHandler(api.GetRouteList))
	rt.POST("/apps/:apps_id/routes", authz.App(authz.PermAppRoutesWrite), httphelper.WrapHandler(api.appLookup(api.CreateRoute)))
	rt.GET("/apps/:apps_id/routes", authz.App(authz.PermAppRoutesRead), httphelper.WrapHandler(api.appLookup(api.GetAppRouteList)))
	rt.GET("/apps/:apps_id/routes/:routes_type/:routes_id", authz.App(authz.PermAppRoutesRead), httphelper.WrapHandler(api.appLookup(api.GetRoute)))
	// acme:issue (the letsencrypt plugin) may only turn HTTPS on or off with
	// this PUT; UpdateRoute keeps every other field (NEW-30).
	rt.PUT("/apps/:apps_id/routes/:routes_type/:routes_id", authz.App(authz.PermAppRoutesWrite).Also(authz.ScopeACMEIssue), httphelper.WrapHandler(api.appLookup(api.UpdateRoute)))
	rt.DELETE("/apps/:apps_id/routes/:routes_type/:routes_id", authz.App(authz.PermAppRoutesWrite), httphelper.WrapHandler(api.appLookup(api.DeleteRoute)))

	rt.POST("/apps/:apps_id/meta", authz.App(authz.PermAppWrite), httphelper.WrapHandler(api.appLookup(api.UpdateApp)))
	// The build lease (EDGE-09) is gitreceive's and flynn-receiver's with the
	// cluster key. A tenant holding it could block every deploy.
	rt.PUT("/apps/:apps_id/build-lock", authz.ClusterOnly(), httphelper.WrapHandler(api.appLookup(api.AcquireBuildLock)))
	rt.DELETE("/apps/:apps_id/build-lock/:holder", authz.ClusterOnly(), httphelper.WrapHandler(api.appLookup(api.ReleaseBuildLock)))
	rt.PUT("/build-locks/:key", authz.ClusterOnly(), httphelper.WrapHandler(api.AcquireKeyedBuildLock))
	rt.DELETE("/build-locks/:key/:holder", authz.ClusterOnly(), httphelper.WrapHandler(api.ReleaseKeyedBuildLock))

	rt.GET("/events", authz.Events(), httphelper.WrapHandler(api.Events))
	rt.GET("/events/:id", authz.Events(), httphelper.WrapHandler(api.GetEvent))
	rt.POST("/apps/:apps_id/scheduler-events", authz.App(authz.PermAppSchedulerWrite), httphelper.WrapHandler(api.appLookup(api.CreateSchedulerEvent)))
	rt.POST("/apps/:apps_id/job-restart-events", authz.App(authz.PermAppWrite), httphelper.WrapHandler(api.appLookup(api.CreateJobRestartEvent)))

	rt.GET("/volumes", authz.ClusterOnly(), httphelper.WrapHandler(api.GetVolumes))
	rt.PUT("/volumes/:volume_id", authz.ClusterOnly(), httphelper.WrapHandler(api.PutVolume))
	rt.GET("/apps/:apps_id/volumes", authz.App(authz.PermAppResourcesRead), httphelper.WrapHandler(api.appLookup(api.GetAppVolumes)))
	rt.GET("/apps/:apps_id/volumes/:volume_id", authz.App(authz.PermAppResourcesRead), httphelper.WrapHandler(api.appLookup(api.GetVolume)))
	rt.PUT("/apps/:apps_id/volumes/:volume_id/decommission", authz.App(authz.PermAppResourcesWrite), httphelper.WrapHandler(api.appLookup(api.DecommissionVolume)))

	rt.POST("/sinks", authz.ClusterOnly(), httphelper.WrapHandler(api.CreateSink))
	rt.GET("/sinks", authz.ClusterOnly(), httphelper.WrapHandler(api.GetSinks))
	rt.GET("/sinks/:sink_id", authz.ClusterOnly(), httphelper.WrapHandler(api.GetSink))
	rt.DELETE("/sinks/:sink_id", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteSink))

	rt.GET("/managed-certificates", authz.ClusterOnly().Also(authz.ScopeACMEIssue), httphelper.WrapHandler(api.GetManagedCertificates))
	rt.GET("/managed-certificates/:managed_certificate_id", authz.ClusterOnly(), httphelper.WrapHandler(api.GetManagedCertificate))
	rt.PUT("/managed-certificates/:managed_certificate_id", authz.ClusterOnly().Also(authz.ScopeACMEIssue), httphelper.WrapHandler(api.UpdateManagedCertificate))

	rt.GET("/acme/config", authz.ClusterOnly().Also(authz.ScopeACMEIssue), httphelper.WrapHandler(api.GetACMEConfig))
	rt.PUT("/acme/config", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateACMEConfig))

	rt.GET("/db-runtimes", authz.AnyAuth(), httphelper.WrapHandler(api.ListDBRuntimes))
	rt.POST("/db-runtimes", authz.ClusterOnly(), httphelper.WrapHandler(api.CreateDBRuntime))
	rt.PUT("/db-runtimes", authz.ClusterOnly(), httphelper.WrapHandler(api.ReplaceDBRuntimes))
	rt.PUT("/db-runtimes/:engine/:name", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateDBRuntime))
	rt.DELETE("/db-runtimes/:engine/:name", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteDBRuntime))
	rt.PUT("/cluster/db-runtime-settings", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateDBRuntimeSettings))

	rt.GET("/runtimes", authz.AnyAuth(), httphelper.WrapHandler(api.ListRuntimeProfiles))
	rt.POST("/runtimes", authz.ClusterOnly(), httphelper.WrapHandler(api.CreateRuntimeProfile))
	rt.GET("/runtimes/:runtimes_id", authz.AnyAuth(), httphelper.WrapHandler(api.GetRuntimeProfile))
	rt.PUT("/runtimes/:runtimes_id", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateRuntimeProfile))
	rt.DELETE("/runtimes/:runtimes_id", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteRuntimeProfile))
	rt.GET("/cluster/runtime-settings", authz.AnyAuth(), httphelper.WrapHandler(api.GetRuntimeSettings))
	rt.PUT("/cluster/runtime-settings", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateRuntimeSettings))
	rt.GET("/cluster/app-domain", authz.AnyAuth(), httphelper.WrapHandler(api.GetAppDomain))
	rt.PUT("/cluster/app-domain", authz.ClusterOnly(), httphelper.WrapHandler(api.PutAppDomain))
	rt.GET("/cluster/region", authz.Public("peer dial: the body is only this cluster's region name"), httphelper.WrapHandler(api.GetClusterRegion))
	rt.PUT("/cluster/region", authz.ClusterOnly(), httphelper.WrapHandler(api.PutClusterRegion))
	rt.GET("/cluster/regions", authz.AnyAuth(), httphelper.WrapHandler(api.GetClusterRegions))
	rt.POST("/cluster/regions", authz.ClusterOnly(), httphelper.WrapHandler(api.PostClusterRegion))
	rt.DELETE("/cluster/regions/:region", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteClusterRegion))
	rt.GET("/cluster/postgres-availability", authz.AnyAuth(), httphelper.WrapHandler(api.GetPostgresAvailability))
	rt.PUT("/cluster/postgres-availability", authz.ClusterOnly(), httphelper.WrapHandler(api.PutPostgresAvailability))
	rt.GET("/cluster/mysql-availability", authz.AnyAuth(), httphelper.WrapHandler(api.GetMySQLAvailability))
	rt.PUT("/cluster/mysql-availability", authz.ClusterOnly(), httphelper.WrapHandler(api.PutMySQLAvailability))
	rt.GET("/cluster/redis-availability", authz.AnyAuth(), httphelper.WrapHandler(api.GetRedisAvailability))
	rt.PUT("/cluster/redis-availability", authz.ClusterOnly(), httphelper.WrapHandler(api.PutRedisAvailability))
	rt.GET("/cluster/mongodb-availability", authz.AnyAuth(), httphelper.WrapHandler(api.GetMongoDBAvailability))
	rt.PUT("/cluster/mongodb-availability", authz.ClusterOnly(), httphelper.WrapHandler(api.PutMongoDBAvailability))
	rt.GET("/cluster/kafka-availability", authz.AnyAuth(), httphelper.WrapHandler(api.GetKafkaAvailability))
	rt.PUT("/cluster/kafka-availability", authz.ClusterOnly(), httphelper.WrapHandler(api.PutKafkaAvailability))
	rt.GET("/cluster/clickhouse-availability", authz.AnyAuth(), httphelper.WrapHandler(api.GetClickHouseAvailability))
	rt.PUT("/cluster/clickhouse-availability", authz.ClusterOnly(), httphelper.WrapHandler(api.PutClickHouseAvailability))
	rt.GET("/cluster/policy", authz.ClusterOnly().Also(authz.ScopeGitreceiveBuild), httphelper.WrapHandler(api.GetClusterPolicy))
	rt.PUT("/cluster/policy", authz.ClusterOnly(), httphelper.WrapHandler(api.PutClusterPolicy))
	rt.GET("/cluster/audit", authz.ClusterOnly(), httphelper.WrapHandler(api.ListClusterAudit))
	rt.DELETE("/cluster/audit", authz.ClusterOnly(), httphelper.WrapHandler(api.PruneClusterAudit))

	rt.GET("/github/app", authz.AnyAuth(), httphelper.WrapHandler(api.maybeGitHubPlugin(api.GetGitHubApp)))
	rt.PUT("/github/app", authz.ClusterOnly(), httphelper.WrapHandler(api.maybeGitHubPlugin(api.UpdateGitHubApp)))
	rt.GET("/github/installations", authz.GitHubCatalog(), httphelper.WrapHandler(api.maybeGitHubPlugin(api.ListGitHubInstallations)))
	rt.GET("/github/installations/:installation_id/repos", authz.GitHubCatalog(), httphelper.WrapHandler(api.maybeGitHubPlugin(api.ListGitHubInstallationRepos)))
	rt.POST("/github/webhook", authz.Public("GitHub signs the body; the handler checks the signature"), httphelper.WrapHandler(api.maybeGitHubPlugin(api.GitHubWebhook)))
	rt.GET("/github/export", authz.ClusterOnly(), httphelper.WrapHandler(api.ExportGitHub))
	rt.GET("/apps/:apps_id/github", authz.App(authz.PermAppGitHubRead), httphelper.WrapHandler(api.maybeGitHubPlugin(api.appLookup(api.GetAppGitHub))))
	rt.PUT("/apps/:apps_id/github", authz.App(authz.PermAppGitHubWrite), httphelper.WrapHandler(api.maybeGitHubPlugin(api.appLookup(api.PutAppGitHub))))
	rt.DELETE("/apps/:apps_id/github", authz.App(authz.PermAppGitHubWrite), httphelper.WrapHandler(api.maybeGitHubPlugin(api.appLookup(api.DeleteAppGitHub))))
	rt.POST("/apps/:apps_id/github/deploy", authz.App(authz.PermAppDeploy), httphelper.WrapHandler(api.maybeGitHubPlugin(api.appLookup(api.DeployAppGitHub))))

	// Host and stats endpoints
	// Host list and stats are open to any credential (dashboard). See the
	// golden file for exactly who that is.
	rt.GET("/hosts", authz.AnyAuth(), httphelper.WrapHandler(api.GetHosts))
	rt.PUT("/hosts/:host_id", authz.ClusterOnly(), httphelper.WrapHandler(api.UpdateHost))
	rt.GET("/hosts/:host_id/stats", authz.AnyAuth(), httphelper.WrapHandler(api.GetHostStats))
	rt.GET("/cluster/stats", authz.ClusterOnly(), httphelper.WrapHandler(api.GetClusterStats))
	rt.GET("/cluster/jobs-stats", authz.ClusterOnly(), httphelper.WrapHandler(api.GetClusterJobsStats))
	rt.GET("/cluster/router-metrics", authz.ClusterOnly(), httphelper.WrapHandler(api.GetRouterMetrics))
	rt.GET("/apps/:apps_id/jobs-stats", authz.App(authz.PermAppMetricsRead), httphelper.WrapHandler(api.appLookup(api.GetAppJobsStats)))

	rt.GET("/tenancy", authz.AnyAuth(), httphelper.WrapHandler(api.GetTenancy))
	rt.PUT("/tenancy", authz.ClusterOnly(), httphelper.WrapHandler(api.PutTenancy))
	rt.GET("/cluster/placement", authz.AnyAuth(), httphelper.WrapHandler(api.GetClusterPlacement))
	rt.GET("/cluster/capacity", authz.ClusterOnly(), httphelper.WrapHandler(api.GetClusterCapacity))
	rt.GET("/cluster/cross-app-releases", authz.ClusterOnly(), httphelper.WrapHandler(api.ListCrossAppReleases))
	rt.PUT("/cluster/placement", authz.ClusterOnly(), httphelper.WrapHandler(api.PutClusterPlacement))
	rt.GET("/tenant-backups", authz.ClusterOnly(), httphelper.WrapHandler(api.ListTenantBackups))
	rt.GET("/tenant-backups/:resource_id", authz.ClusterOnly(), httphelper.WrapHandler(api.GetTenantBackup))
	rt.POST("/tenant-backups/:resource_id/begin", authz.ClusterOnly(), httphelper.WrapHandler(api.BeginTenantBackup))
	rt.POST("/tenant-backups/:resource_id/heartbeat", authz.ClusterOnly(), httphelper.WrapHandler(api.HeartbeatTenantBackup))
	rt.POST("/tenant-backups/:resource_id/finish", authz.ClusterOnly(), httphelper.WrapHandler(api.FinishTenantBackup))
	rt.POST("/tenant-backups/:resource_id/fail", authz.ClusterOnly(), httphelper.WrapHandler(api.FailTenantBackup))
	rt.GET("/tenant-backups/:resource_id/dumps", authz.ClusterOnly(), httphelper.WrapHandler(api.ListTenantBackupDumps))
	rt.DELETE("/tenant-backups/:resource_id/dumps/:timestamp", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteTenantBackupDump))
	// A user session could squat an address and receive the grants made
	// when its owner is later invited, so only a cluster admin creates users.
	rt.POST("/users", authz.ClusterOnly(), httphelper.WrapHandler(api.CreateUser))
	rt.GET("/users", authz.ClusterOnly(), httphelper.WrapHandler(api.ListUsers))
	rt.GET("/users/:users_id", authz.ClusterOnly(), httphelper.WrapHandler(api.GetUser))
	rt.PATCH("/users/:users_id", authz.ClusterOnly(), httphelper.WrapHandler(api.PatchUser))
	rt.DELETE("/users/:users_id", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteUser))
	rt.POST("/users/:users_id/bootstrap-token", authz.ClusterOnly(), httphelper.WrapHandler(api.BootstrapUserToken))
	rt.POST("/accounts/:accounts_id/suspend", authz.AccountQuota(), httphelper.WrapHandler(api.SuspendAccount))
	rt.POST("/accounts/:accounts_id/unsuspend", authz.AccountQuota(), httphelper.WrapHandler(api.UnsuspendAccount))
	rt.GET("/accounts/:accounts_id/quota", authz.AccountQuota(), httphelper.WrapHandler(api.GetQuota))
	rt.PUT("/accounts/:accounts_id/quota", authz.AccountQuota(), httphelper.WrapHandler(api.PutQuota))
	rt.GET("/accounts/:accounts_id/collaborators", authz.UserSession(), httphelper.WrapHandler(api.ListAccountCollaborators))
	rt.POST("/accounts/:accounts_id/collaborators", authz.UserSession(), httphelper.WrapHandler(api.AddAccountCollaborator))
	rt.DELETE("/accounts/:accounts_id/collaborators/:users_id", authz.UserSession(), httphelper.WrapHandler(api.DeleteAccountCollaborator))
	rt.POST("/handles", authz.ClusterOnly(), httphelper.WrapHandler(api.CreateHandle))
	rt.GET("/handles/:handles_id", authz.UserSession(), httphelper.WrapHandler(api.GetHandle))
	rt.DELETE("/handles/:handles_id", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteHandle))
	rt.PUT("/memberships", authz.ClusterOnly(), httphelper.WrapHandler(api.PutMembership))
	rt.DELETE("/memberships", authz.ClusterOnly(), httphelper.WrapHandler(api.DeleteMembership))
	rt.GET("/memberships", authz.ClusterOnly(), httphelper.WrapHandler(api.ListMemberships))
	rt.GET("/whoami", authz.UserSession(), httphelper.WrapHandler(api.WhoAmI))
	rt.GET("/whoami/grants", authz.UserSession(), httphelper.WrapHandler(api.WhoAmIGrants))
	rt.POST("/tokens", authz.UserSession(), httphelper.WrapHandler(api.CreateToken))
	rt.GET("/tokens", authz.UserSession(), httphelper.WrapHandler(api.ListTokens))
	rt.DELETE("/tokens/:tokens_id", authz.UserSession(), httphelper.WrapHandler(api.DeleteToken))
	rt.POST("/apps/:apps_id/transfer", authz.UserSession(), httphelper.WrapHandler(api.appLookup(api.TransferApp)))
	rt.GET("/apps/:apps_id/collaborators", authz.UserSession(), httphelper.WrapHandler(api.appLookup(api.ListAppCollaborators)))
	rt.POST("/apps/:apps_id/collaborators", authz.UserSession(), httphelper.WrapHandler(api.appLookup(api.AddAppCollaborator)))
	rt.DELETE("/apps/:apps_id/collaborators/:users_id", authz.UserSession(), httphelper.WrapHandler(api.appLookup(api.DeleteAppCollaborator)))
	rt.GET("/domains", authz.UserSession(), httphelper.WrapHandler(api.ListDomains))
	rt.DELETE("/domains/:hostname", authz.UserSession(), httphelper.WrapHandler(api.DeleteDomain))
	rt.POST("/domains/verify", authz.UserSession(), httphelper.WrapHandler(api.VerifyDomain))
	rt.POST("/domains/verify/:hostname/check", authz.UserSession(), httphelper.WrapHandler(api.CheckDomain))
}
