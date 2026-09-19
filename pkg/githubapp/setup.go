package githubapp

// Permission is a GitHub App repository permission Flynn needs.
type Permission struct {
	Name        string `json:"name"`
	Access      string `json:"access"`
	Description string `json:"description"`
}

// Event is a GitHub App webhook event Flynn subscribes to.
type Event struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SetupStep is one instruction for creating the cluster GitHub App.
type SetupStep struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// SetupGuide is the exact GitHub App configuration Flynn documents in the
// dashboard admin UI and in Flynn docs.
type SetupGuide struct {
	Permissions []Permission `json:"permissions"`
	Events      []Event      `json:"events"`
	Steps       []SetupStep  `json:"steps"`
}

// DefaultAPI is the GitHub.com API root.
const DefaultAPI = "https://api.github.com"

// Setup returns the cluster-admin GitHub App instructions.
func Setup(clusterDomain, controllerWebhook, gitWebhook string) SetupGuide {
	if clusterDomain == "" {
		clusterDomain = "<cluster-domain>"
	}
	if controllerWebhook == "" {
		controllerWebhook = "https://controller." + clusterDomain + "/github/webhook"
	}
	if gitWebhook == "" {
		gitWebhook = "https://git." + clusterDomain + "/github/webhook"
	}
	dashboard := "https://dashboard." + clusterDomain
	return SetupGuide{
		Permissions: []Permission{
			{Name: "Contents", Access: "Read-only", Description: "Clone the repository so taffy can build with gitreceive/flynn-receiver."},
			{Name: "Metadata", Access: "Read-only", Description: "List installations and repositories the GitHub App can access."},
			{Name: "Commit statuses", Access: "Read-only", Description: "Wait for CI (legacy GitHub statuses) before auto-deploy."},
			{Name: "Checks", Access: "Read-only", Description: "Wait for GitHub Checks to pass before auto-deploy."},
		},
		Events: []Event{
			{Name: "Push", Description: "Trigger a deploy of the connected branch (or queue it until checks pass)."},
			{Name: "Check suite", Description: "Finish a waiting auto-deploy when GitHub Checks complete."},
			{Name: "Check run", Description: "Same as check suite; used when individual checks finish."},
			{Name: "Status", Description: "Finish a waiting auto-deploy when legacy commit statuses complete."},
		},
		Steps: []SetupStep{
			{
				Title: "Create a GitHub App",
				Body:  "GitHub → Settings → Developer settings → GitHub Apps → New GitHub App. Name it (for example Flynn Deploy). Homepage URL: " + dashboard + ".",
			},
			{
				Title: "Set the webhook",
				Body:  "Webhook URL: " + controllerWebhook + " (gitreceive also accepts " + gitWebhook + "). Create a webhook secret and paste the same value into Flynn. Leave SSL verification enabled.",
			},
			{
				Title: "Repository permissions",
				Body:  "Contents: Read-only. Metadata: Read-only. Commit statuses: Read-only. Checks: Read-only. Do not grant Administration or Contents write.",
			},
			{
				Title: "Subscribe to events",
				Body:  "Push, Check suite, Check run, and Status. Uncheck other events.",
			},
			{
				Title: "Create the app and copy credentials",
				Body:  "After creating the app, copy the App ID and optional slug. Generate a private key and download the PEM. Optional Client ID and Client secret are only needed for GitHub user OAuth; listing installations of this app is enough to connect repos.",
			},
			{
				Title: "Install the app",
				Body:  "Install the GitHub App on the user account or organization that owns the repositories. Grant it only the repos Flynn should be able to deploy.",
			},
			{
				Title: "Paste credentials into Flynn",
				Body:  "Cluster administrators save App ID, private key, and webhook secret with flynn-host github:configure or Cluster → GitHub in the dashboard. App owners then connect a repo on the app Deploy page or with flynn github:connect owner/repo.",
			},
		},
	}
}

// WebhookURLs returns the controller and gitreceive webhook endpoints.
func WebhookURLs(clusterDomain string) (controllerURL, gitURL string) {
	if clusterDomain == "" {
		clusterDomain = "<cluster-domain>"
	}
	return "https://controller." + clusterDomain + "/github/webhook",
		"https://git." + clusterDomain + "/github/webhook"
}

// InstallURL is the public GitHub App installation page.
func InstallURL(slug string) string {
	if slug == "" {
		return "https://github.com/settings/apps"
	}
	return "https://github.com/apps/" + slug + "/installations/new"
}
