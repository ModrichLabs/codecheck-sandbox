// Package pins holds every fixed external reference the tool trusts: release
// versions, checksums and endpoints. M5 adds the download pins.
package pins

// PostHog telemetry (spec §11). The project key only writes events, so it may
// sit in public code. Both stay variables so a fork can point at its own
// project at build time:
//
//	go build -ldflags "-X github.com/ModrichLabs/codecheck-sandbox/internal/pins.PostHogKey=phc_…"
//
// An empty key switches telemetry to the no-op client.
var (
	PostHogKey  = "phc_pR2TVwdPiaMaERt5bD6h77nvDkhq59G7bsvip8aPNyiP"
	PostHogHost = "https://us.i.posthog.com"
)
