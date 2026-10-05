package login

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/cli/config"
	"github.com/randy-girard/flynn/cli/login/internal/oauth"
	"github.com/randy-girard/flynn/cli/login/tokensource"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/term"
	"golang.org/x/oauth2"
)

const (
	oauthHostPort  = "127.0.0.1:8085"
	oobRedirectURI = "urn:ietf:wg:oauth:2.0:oob"
	cliClientID    = "flynn-cli"
)

func looksLikeIssuerURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return strings.Contains(s, "://") || strings.HasPrefix(s, "http:") || strings.HasPrefix(s, "https:")
}

func issuerFromCluster(c *config.Cluster) string {
	if c == nil {
		return ""
	}
	if auth := flynnAuthIssuer(c.OAuthURL, c.ControllerURL); auth != "" {
		return auth
	}
	if strings.TrimSpace(c.OAuthURL) != "" {
		return strings.TrimSpace(c.OAuthURL)
	}
	return strings.TrimSpace(c.ControllerURL)
}

func Run(args *docopt.Args, globalCluster string) error {
	flynnrc, err := config.ReadFile(config.DefaultPath())
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error reading flynnrc: %s", err)
	}

	existingClusters := make(map[string]*config.Cluster)
	for _, c := range flynnrc.Clusters {
		existingClusters[c.Name] = c
	}

	oob := useOOB(args)
	positional := strings.TrimSpace(args.String["<issuer-or-cluster>"])
	force := args.Bool["--force"]
	clusterNameArg := strings.TrimSpace(args.String["--cluster-name"])
	controllerURL := strings.TrimSpace(args.String["--controller-url"])
	email := firstNonEmpty(strings.TrimSpace(args.String["--email"]), strings.TrimSpace(os.Getenv("FLYNN_EMAIL")))
	password := firstNonEmpty(strings.TrimSpace(args.String["--password"]), os.Getenv("FLYNN_PASSWORD"))
	var issuer string

	clusterName := clusterNameArg
	if globalCluster != "" {
		if clusterName != "" && clusterName != globalCluster {
			return fmt.Errorf("conflicting cluster selection: -n %q vs global -c %q", clusterName, globalCluster)
		}
		clusterName = globalCluster
	}

	if positional != "" {
		if looksLikeIssuerURL(positional) {
			issuer = positional
		} else {
			if clusterName != "" && clusterName != positional {
				return fmt.Errorf("conflicting cluster names %q and %q", clusterName, positional)
			}
			clusterName = positional
		}
	}

	var selected *config.Cluster
	if clusterName != "" {
		selected = existingClusters[clusterName]
		if selected == nil {
			return fmt.Errorf("unknown cluster %q in %s", clusterName, config.DefaultPath())
		}
	} else {
		def := strings.TrimSpace(flynnrc.Default)
		if def != "" {
			selected = existingClusters[def]
		}
		if selected == nil && len(flynnrc.Clusters) == 1 {
			selected = flynnrc.Clusters[0]
		}
	}
	if selected != nil {
		if clusterName == "" {
			clusterName = selected.Name
		}
		if controllerURL == "" {
			controllerURL = selected.ControllerURL
		}
		if issuer == "" {
			issuer = issuerFromCluster(selected)
		}
	}

	if issuer == "" {
		issuer = controllerURL
	}
	if issuer == "" {
		return fmt.Errorf("no cluster to log in to: run `flynn cluster:add` first, or pass a controller URL")
	}
	if clusterName == "" {
		clusterName = "default"
	}
	if controllerURL == "" {
		controllerURL = issuer
	}

	creds := Credentials{Email: email, Password: password}
	if !oob {
		if err := creds.FillMissing(term.IsTerminal(os.Stdin.Fd())); err != nil {
			return err
		}
	}

	var hc *http.Client
	if selected != nil {
		hc, err = selected.HTTPClient()
		if err != nil {
			return err
		}
	}
	usedIssuer, err := Authenticate(clusterName, issuer, controllerURL, creds, oob, hc)
	if err != nil {
		return err
	}

	clusterConfig := &config.Cluster{
		Name:          clusterName,
		OAuthURL:      usedIssuer,
		DashboardURL:  "",
		ControllerURL: controllerURL,
	}
	if selected != nil {
		clusterConfig = selected
		clusterConfig.OAuthURL = usedIssuer
		if clusterConfig.ControllerURL == "" {
			clusterConfig.ControllerURL = controllerURL
		}
	} else {
		domain := strings.TrimPrefix(controllerURL, "https://controller.")
		clusterConfig.GitURL = "https://git." + domain
		clusterConfig.ImageURL = "https://images." + domain
		clusterConfig.DashboardURL = "https://dashboard." + domain
		mergeForce := force || existingClusters[clusterName] != nil
		if err := flynnrc.Add(clusterConfig, mergeForce); err != nil {
			return fmt.Errorf("error saving config: %s", err)
		}
		if flynnrc.Default == "" {
			flynnrc.SetDefault(clusterConfig.Name)
		}
	}
	if err := flynnrc.SaveTo(config.DefaultPath()); err != nil {
		return fmt.Errorf("error writing flynnrc: %s", err)
	}
	fmt.Printf("Logged in to cluster %q.\n", clusterName)
	return nil
}

// Credentials is a controller password-grant login.
type Credentials struct {
	Email    string
	Password string
}

func (c *Credentials) FillMissing(interactive bool) error {
	if strings.TrimSpace(c.Email) == "" {
		if !interactive {
			return fmt.Errorf("email is required: pass --email or set FLYNN_EMAIL")
		}
		fmt.Fprint(os.Stderr, "Email: ")
		s, err := readLine()
		if err != nil {
			return err
		}
		c.Email = s
	}
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if !strings.Contains(c.Email, "@") {
		return fmt.Errorf("email is required")
	}
	if c.Password == "" {
		if !interactive {
			return fmt.Errorf("password is required: pass --password or set FLYNN_PASSWORD")
		}
		s, err := readPassword("Password: ")
		if err != nil {
			return err
		}
		c.Password = s
	}
	if strings.TrimSpace(c.Email) == "" || c.Password == "" {
		return fmt.Errorf("email and password are required")
	}
	return nil
}

// LoginCluster authenticates against a configured cluster and stores tokens
// under ~/.flynn/tokens/<cluster>/flynn-cli.json.
func LoginCluster(c *config.Cluster, creds Credentials, oob bool) error {
	if c == nil {
		return fmt.Errorf("cluster is required")
	}
	issuer := issuerFromCluster(c)
	if issuer == "" {
		return fmt.Errorf("cluster %q has no controller URL", c.Name)
	}
	hc, err := c.HTTPClient()
	if err != nil {
		return err
	}
	usedIssuer, err := Authenticate(c.Name, issuer, c.ControllerURL, creds, oob, hc)
	if err != nil {
		return err
	}
	c.OAuthURL = usedIssuer
	return nil
}

func oauthIssuerCandidates(issuer, controllerURL string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(config.AuthURLFromController(controllerURL))
	add(issuer)
	add(controllerURL)
	return out
}

func flynnAuthIssuer(issuer, controllerURL string) string {
	for _, raw := range []string{controllerURL, issuer} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		host := u.Hostname()
		if strings.HasPrefix(host, "controller.") {
			return "https://auth." + strings.TrimPrefix(host, "controller.")
		}
		if strings.HasPrefix(host, "auth.") {
			scheme := u.Scheme
			if scheme == "" {
				scheme = "https"
			}
			return scheme + "://" + host
		}
	}
	return ""
}

func flynnControllerIssuer(controllerURL, issuer string) string {
	for _, raw := range []string{controllerURL, issuer} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		host := u.Hostname()
		if strings.HasPrefix(host, "controller.") {
			scheme := u.Scheme
			if scheme == "" {
				scheme = "https"
			}
			return scheme + "://" + host
		}
	}
	return ""
}

func oauthIssuerReachable(hc *http.Client, issuer string) bool {
	metadataURL, _, err := oauth.BuildMetadataURL(issuer)
	if err != nil {
		return false
	}
	_, err = oauth.GetMetadata(hc, metadataURL)
	return err == nil
}

// AuthHostsHint is printed when auth.<domain> does not resolve. macOS
// /etc/hosts has no wildcards, so controller.1.localflynn.com can work while
// auth.1.localflynn.com does not.
func AuthHostsHint(authURL, controllerURL string) string {
	u, err := url.Parse(strings.TrimSpace(authURL))
	if err != nil {
		return ""
	}
	authHost := u.Hostname()
	if authHost == "" || !strings.HasPrefix(authHost, "auth.") {
		return ""
	}
	if hostResolves(authHost) {
		return ""
	}
	ip := "127.0.0.1"
	if cu, err := url.Parse(strings.TrimSpace(controllerURL)); err == nil {
		if addrs := lookupHostFast(cu.Hostname()); len(addrs) > 0 {
			ip = addrs[0]
		}
	}
	return fmt.Sprintf("%s did not resolve. /etc/hosts has no wildcards; add the same address as controller:\n  %s %s\n", authHost, ip, authHost)
}

func hostResolves(host string) bool {
	return len(lookupHostFast(host)) > 0
}

func lookupHostFast(host string) []string {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	return addrs
}

func syntheticFlynnMetadata(auth string) *oauth.IssuerMetadata {
	base := strings.TrimRight(auth, "/")
	return &oauth.IssuerMetadata{
		AuthorizationEndpoint: base + "/oauth/authorize",
		TokenEndpoint:         base + "/oauth/token",
		AudiencesEndpoint:     base + "/oauth/audiences",
	}
}

func rewriteOAuthEndpoints(meta *oauth.IssuerMetadata, authBase string) {
	if meta == nil || strings.TrimSpace(authBase) == "" {
		return
	}
	meta.AuthorizationEndpoint = rewriteOAuthURL(meta.AuthorizationEndpoint, authBase)
	meta.TokenEndpoint = rewriteOAuthURL(meta.TokenEndpoint, authBase)
	if meta.AudiencesEndpoint != "" {
		meta.AudiencesEndpoint = rewriteOAuthURL(meta.AudiencesEndpoint, authBase)
	}
}

func rewriteOAuthURL(raw, authBase string) string {
	raw = strings.TrimSpace(raw)
	base := strings.TrimRight(authBase, "/")
	if raw == "" {
		return base
	}
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Hostname()
	if strings.HasPrefix(host, "controller.") || strings.HasPrefix(host, "dashboard.") {
		b, err := url.Parse(base)
		if err != nil {
			return raw
		}
		u.Scheme = b.Scheme
		u.Host = b.Host
		return u.String()
	}
	return raw
}

func Authenticate(clusterName, issuer, controllerURL string, creds Credentials, oob bool, hc *http.Client) (string, error) {
	var (
		metadata *oauth.IssuerMetadata
		used     string
		clientID string
		lastErr  error
	)
	if auth := flynnAuthIssuer(issuer, controllerURL); auth != "" {
		used = auth
		if !oauthIssuerReachable(hc, auth) {
			if hint := AuthHostsHint(auth, controllerURL); hint != "" {
				fmt.Fprint(os.Stderr, hint)
			}
			if ctrl := flynnControllerIssuer(controllerURL, issuer); ctrl != "" {
				used = ctrl
			}
		}
		metadata = syntheticFlynnMetadata(used)
		clientID = cliClientID
	} else {
		for _, iss := range oauthIssuerCandidates(issuer, controllerURL) {
			metadataURL, cid, err := oauth.BuildMetadataURL(iss)
			if err != nil {
				lastErr = err
				continue
			}
			if cid == "" {
				cid = cliClientID
			}
			meta, err := oauth.GetMetadata(hc, metadataURL)
			if err != nil {
				lastErr = err
				continue
			}
			metadata = meta
			used = iss
			clientID = cid
			break
		}
	}
	if metadata == nil {
		if lastErr != nil {
			return "", lastErr
		}
		return "", fmt.Errorf("no cluster to log in to")
	}

	cfg := &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:   metadata.AuthorizationEndpoint,
			TokenURL:  metadata.TokenEndpoint,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	ctx := context.Background()
	if hc != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, hc)
	}

	var t *oauth2.Token
	if oob {
		code, err := loginAuto(cfg)
		if err != nil {
			return "", err
		}
		t, err = exchangeAuthCode(ctx, cfg, code, controllerURL)
		if err != nil {
			return "", fmt.Errorf("error exchanging code for auth token: %s", err)
		}
	} else {
		var err error
		t, err = oauth.PasswordToken(hc, metadata.TokenEndpoint, clientID, creds.Email, creds.Password, controllerURL)
		if err != nil {
			return "", fmt.Errorf("login failed: %s", err)
		}
	}

	cache := config.TokenCache()
	if err := cache.SetToken(clusterName, clientID, t); err != nil {
		return "", fmt.Errorf("error saving access token: %s", err)
	}

	ts, err := tokensource.New(clusterName, used, controllerURL, cache, hc)
	if err != nil {
		return "", fmt.Errorf("error creating tokensource: %s", err)
	}
	cc, err := controller.NewClientWithHTTP(controllerURL, "", oauth2.NewClient(ctx, ts))
	if err != nil {
		return "", fmt.Errorf("error creating controller client: %s", err)
	}
	if _, err := cc.Status(); err != nil {
		return "", fmt.Errorf("error getting controller status: %s", err)
	}
	return used, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func readLine() (string, error) {
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("no input")
	}
	return strings.TrimSpace(s.Text()), nil
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd, isTerm := term.GetFdInfo(os.Stdin)
	if !isTerm {
		return readLine()
	}
	state, err := term.SaveState(fd)
	if err != nil {
		return readLine()
	}
	if err := term.DisableEcho(fd, state); err != nil {
		return readLine()
	}
	defer term.RestoreTerminal(fd, state)
	s, err := readLine()
	fmt.Fprintln(os.Stderr)
	return s, err
}

func loginOOB(config *oauth2.Config) (*codeInfo, error) {
	config.RedirectURL = oobRedirectURI
	info := buildAuthCodeURL(config)
	fmt.Printf("To login, open the URL below and then paste the resulting code here:\n  %s\nCode: ", info.URL)

	s := bufio.NewScanner(os.Stdin)
	if s.Scan() {
		info.Code = strings.TrimSpace(s.Text())
	} else {
		return nil, fmt.Errorf("error reading code: %s", s.Err())
	}

	return info, nil
}

func loginAuto(config *oauth2.Config) (*codeInfo, error) {
	config.RedirectURL = "http://" + oauthHostPort + "/"
	info := buildAuthCodeURL(config)

	waitForCode, err := listenForCode(info.State)
	if err != nil {
		fmt.Printf("Error starting automatic code listener: %s\nFalling back to out-of-band code.\n\n", err)
		return loginOOB(config)
	}

	doneCh := make(chan error)
	go func() {
		var err error
		info.Code, err = waitForCode()
		doneCh <- err
	}()

	if err := openURL(info.URL); err != nil {
		fmt.Printf("Unable to open browser, open this URL or re-run this command with --oauth\n  %s\n\n", info.URL)
	} else {
		fmt.Printf("Your browser has been opened to this URL, waiting for authentication to complete...\n  %s\n\n", info.URL)
	}

	return info, <-doneCh
}

func buildAuthCodeURL(config *oauth2.Config) *codeInfo {
	res := &codeInfo{
		Nonce:    random.Base64(32),
		Verifier: random.Base64(32),
	}
	if config.RedirectURL != oobRedirectURI {
		res.State = random.Base64(32)
	}
	challBytes := sha256.Sum256([]byte(res.Verifier))
	res.URL = config.AuthCodeURL(res.State,
		oauth2.SetAuthURLParam("nonce", res.Nonce),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("code_challenge", strings.TrimRight(base64.URLEncoding.EncodeToString(challBytes[:]), "=")),
	)
	return res
}

func exchangeAuthCode(ctx context.Context, config *oauth2.Config, info *codeInfo, audience string) (*oauth2.Token, error) {
	params := []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("code_verifier", info.Verifier)}
	if audience != "" {
		params = append(params, oauth2.SetAuthURLParam("audience", audience))
	}
	t, err := config.Exchange(ctx, info.Code, params...)
	if err != nil {
		return nil, err
	}
	nonce, ok := t.Extra("nonce").(string)
	if !ok || nonce != info.Nonce {
		return nil, fmt.Errorf("oauth2 auth response has invalid nonce, expected %q, got %q", info.Nonce, nonce)
	}

	extra := make(map[string]interface{})
	extra["audience"] = t.Extra("audience")

	iss, _ := t.Extra("refresh_token_issue_time").(string)
	if iss != "" {
		issueTime, err := time.Parse(time.RFC3339Nano, iss)
		if err != nil {
			return nil, fmt.Errorf("error parsing refresh_token_issue_time %q: %s", iss, err)
		}
		extra[oauth.RefreshTokenIssueTime] = issueTime
	}
	exp, ok := t.Extra("refresh_token_expires_in").(float64)
	if ok {
		extra[oauth.RefreshTokenExpiry] = time.Now().Add(time.Duration(exp) * time.Second)
	}

	return t.WithExtra(extra), nil
}

type codeInfo struct {
	URL      string
	Verifier string
	State    string
	Nonce    string
	Code     string
}

func openURL(url string) error {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = oauthErrFallback
	}
	return err
}

var oauthErrFallback = errors.New("oob fallback")

func useOOB(args *docopt.Args) bool {
	return args.Bool["--oauth"] || args.Bool["--oob-code"]
}

func listenForCode(state string) (func() (string, error), error) {
	l, err := net.Listen("tcp", oauthHostPort)
	if err != nil {
		return nil, err
	}

	return func() (code string, err error) {
		http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer l.Close()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<p>Flynn authentication redirect received, close this page and return to the CLI.</p>"))
			if errCode := r.FormValue("error"); errCode != "" {
				msg := "error from oauth server: " + errCode

				if errDesc := r.FormValue("error_description"); errDesc != "" {
					msg += ": " + errDesc
				}
				if errURI := r.FormValue("error_uri"); errURI != "" {
					msg += " - " + errURI
				}
				err = errors.New(msg)
				return
			}
			if resState := r.FormValue("state"); state != resState {
				err = fmt.Errorf("invalid state in oauth code redirect, wanted %q, got %q", state, resState)
			}
			code = r.FormValue("code")
			if code == "" {
				err = fmt.Errorf("missing code in oauth redirect, got %q", r.URL.RawQuery)
			}
		}))

		return code, err
	}, nil
}
