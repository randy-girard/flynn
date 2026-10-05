package main

import (
	"html"
	"net/url"
	"strings"
)

const oauthLoginCSS = `:root {
  --font-sans: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  --radius: 6px;
  --radius-sm: 4px;
}
:root, html[data-theme="dark"] {
  color-scheme: dark;
  --color-bg: #121018;
  --color-surface: #1b1824;
  --color-surface-hover: #242030;
  --color-border: #2d2838;
  --color-border-strong: #3d3750;
  --color-primary: #b4a6ff;
  --color-primary-hover: #c7bcff;
  --color-primary-soft: rgba(180, 166, 255, 0.14);
  --color-on-primary: #16131e;
  --color-danger: #ff6b7a;
  --color-danger-soft: rgba(255, 107, 122, 0.12);
  --color-danger-border: rgba(255, 107, 122, 0.35);
  --color-text: #f4f2f8;
  --color-text-muted: #a39bb3;
  --color-text-dim: #7a7388;
  --shadow-sm: 0 1px 2px rgba(0, 0, 0, 0.28);
}
html[data-theme="light"] {
  color-scheme: light;
  --color-bg: #f4f6f9;
  --color-surface: #ffffff;
  --color-surface-hover: #eef1f6;
  --color-border: #e4e0d9;
  --color-border-strong: #d4cfc6;
  --color-primary: #5341d6;
  --color-primary-hover: #4334c4;
  --color-primary-soft: rgba(83, 65, 214, 0.10);
  --color-on-primary: #ffffff;
  --color-danger: #c23b4e;
  --color-danger-soft: rgba(194, 59, 78, 0.10);
  --color-danger-border: rgba(194, 59, 78, 0.32);
  --color-text: #1f1b2d;
  --color-text-muted: #5f586c;
  --color-text-dim: #817a8c;
  --shadow-sm: 0 1px 2px rgba(31, 27, 45, 0.05);
}
* { box-sizing: border-box; margin: 0; padding: 0; }
html, body {
  font-family: var(--font-sans);
  background: var(--color-bg);
  color: var(--color-text);
  line-height: 1.5;
  letter-spacing: -0.01em;
  -webkit-font-smoothing: antialiased;
  min-height: 100%;
}
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: 1.5rem;
}
.login-theme { position: fixed; top: 1rem; right: 1rem; z-index: 10; }
.theme-toggle {
  display: inline-flex;
  gap: 0.15rem;
  padding: 0.15rem;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.theme-toggle button {
  appearance: none;
  border: none;
  background: transparent;
  color: var(--color-text-muted);
  font-size: 0.72rem;
  font-weight: 600;
  letter-spacing: 0.01em;
  padding: 0.28rem 0.5rem;
  border-radius: 4px;
  cursor: pointer;
  font-family: var(--font-sans);
}
.theme-toggle button:hover { color: var(--color-text); }
.theme-toggle button.active {
  background: var(--color-surface);
  color: var(--color-text);
  box-shadow: var(--shadow-sm);
}
.login-card {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  padding: 2.25rem 2rem 1.85rem;
  width: 100%;
  max-width: 380px;
  box-shadow: var(--shadow-sm);
}
.login-card .brand { margin-bottom: 0.15rem; }
.brand { display: inline-flex; align-items: center; gap: 0.65rem; color: var(--color-text); }
.brand-mark {
  flex-shrink: 0;
  width: 1.55rem;
  height: 1.55rem;
  border-radius: 5px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: var(--color-primary);
  color: var(--color-on-primary);
  font-size: 0.72rem;
  font-weight: 700;
  letter-spacing: -0.04em;
}
.brand-text { display: flex; flex-direction: column; line-height: 1.15; }
.brand-name { font-size: 0.95rem; font-weight: 650; letter-spacing: -0.03em; }
.brand-product {
  font-size: 0.68rem;
  font-weight: 500;
  color: var(--color-text-dim);
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.login-card h1 {
  font-size: 1.45rem;
  font-weight: 550;
  letter-spacing: -0.03em;
  margin: 1.15rem 0 0.35rem;
}
.login-card p { color: var(--color-text-muted); margin-bottom: 1.35rem; font-size: 0.9rem; }
.form-group { margin-bottom: 1rem; }
.form-group label {
  display: block;
  font-size: 0.8rem;
  font-weight: 500;
  color: var(--color-text-muted);
  margin-bottom: 0.35rem;
}
.form-group input {
  width: 100%;
  padding: 0.55rem 0.75rem;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text);
  font-size: 0.925rem;
  font-family: var(--font-sans);
  outline: none;
}
.form-group input:focus {
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px var(--color-primary-soft);
}
.form-error {
  margin-bottom: 1rem;
  padding: 0.65rem 0.75rem;
  border-radius: var(--radius-sm);
  background: var(--color-danger-soft);
  border: 1px solid var(--color-danger-border);
  color: var(--color-danger);
  font-size: 0.85rem;
}
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0.5rem 0.95rem;
  min-height: 2.15rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  color: var(--color-text);
  font-size: 0.875rem;
  font-weight: 600;
  cursor: pointer;
  font-family: var(--font-sans);
}
.btn-primary {
  background: var(--color-primary);
  color: var(--color-on-primary);
  border-color: var(--color-primary);
}
.btn-primary:hover { background: var(--color-primary-hover); border-color: var(--color-primary-hover); }
.btn-block { width: 100%; }
code, kbd, pre {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.82rem;
}
pre {
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  padding: 1rem;
  border-radius: var(--radius-sm);
  margin: 0 0 1rem;
}`

const oauthThemeScript = `<script>
(function () {
  var root = document.documentElement;
  var key = "flynn-theme";
  function pref() {
    try { return localStorage.getItem(key); } catch (e) { return null; }
  }
  function apply(stored) {
    var theme = stored;
    if (theme !== "light" && theme !== "dark") {
      theme = window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
      stored = "system";
    }
    root.setAttribute("data-theme", theme);
    document.querySelectorAll(".theme-toggle [data-theme-value]").forEach(function (btn) {
      btn.classList.toggle("active", btn.getAttribute("data-theme-value") === stored);
    });
  }
  apply(pref());
  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-theme-value]");
    if (!btn) return;
    var v = btn.getAttribute("data-theme-value");
    try { localStorage.setItem(key, v); } catch (err) {}
    apply(v);
  });
})();
</script>`

func oauthShell(title, product, heading, lede, inner string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + html.EscapeString(title) + `</title>
  <style>` + oauthLoginCSS + `</style>
</head>
<body>
  <div class="login-page">
    <div class="login-theme">
      <div class="theme-toggle" role="group" aria-label="Appearance">
        <button type="button" data-theme-value="light">Light</button>
        <button type="button" data-theme-value="dark">Dark</button>
        <button type="button" data-theme-value="system">Auto</button>
      </div>
    </div>
    <div class="login-card">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true">F</span>
        <span class="brand-text">
          <span class="brand-name">Flynn</span>
          <span class="brand-product">` + html.EscapeString(product) + `</span>
        </span>
      </div>
      <h1>` + html.EscapeString(heading) + `</h1>
      <p>` + lede + `</p>
      ` + inner + `
    </div>
  </div>
  ` + oauthThemeScript + `
</body>
</html>`
}

func authorizeLoginHTML(q url.Values, errMsg string) string {
	errBlock := ""
	if errMsg != "" {
		errBlock = `<div class="form-error" role="alert">` + html.EscapeString(errMsg) + `</div>`
	}
	fields := []string{"client_id", "redirect_uri", "response_type", "state", "code_challenge", "code_challenge_method", "nonce", "scope", "audience"}
	var hidden strings.Builder
	for _, name := range fields {
		v := q.Get(name)
		if v == "" {
			continue
		}
		hidden.WriteString(`<input type="hidden" name="` + html.EscapeString(name) + `" value="` + html.EscapeString(v) + `">`)
	}
	inner := errBlock + `
      <form method="post" action="/oauth/authorize">
        ` + hidden.String() + `
        <div class="form-group">
          <label for="login">Email</label>
          <input id="login" name="login" type="email" autocomplete="username" required autofocus spellcheck="false">
        </div>
        <div class="form-group">
          <label for="password">Password</label>
          <input id="password" name="password" type="password" autocomplete="current-password" required>
        </div>
        <button type="submit" class="btn btn-primary btn-block">Log in</button>
      </form>`
	return oauthShell("Flynn login", "Cluster login", "Log in",
		"Sign in with your cluster email. After login, this page returns a code to the Flynn CLI on 127.0.0.1.",
		inner)
}

func oobAuthorizeHTML(code, state, redirectURI string) string {
	ec := html.EscapeString(code)
	stateBlock := ""
	if state != "" {
		stateBlock = `<p><small>State: <code>` + html.EscapeString(state) + `</code></small></p>`
	}
	inner := `<pre id="code">` + ec + `</pre>` + stateBlock +
		`<p><small>redirect_uri: ` + html.EscapeString(redirectURI) + `</small></p>`
	return oauthShell("Authorization code", "Cluster login", "Copy this code into the Flynn CLI",
		`Paste at the prompt that says <kbd>Code:</kbd> (when you used <code>flynn login --oauth</code>).`,
		inner)
}
