package cli

import (
	"errors"
	"os"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/config"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// errSetupStopped reports a setup the user left at a question.
var errSetupStopped = errors.New("setup stopped before it changed anything")

type setupAnswers struct {
	cfg config.Config
	key string
}

// askDetails runs "Your details": the key, budget and reports folder, then
// the telemetry question when nothing has decided it yet (spec §7, step 4).
func askDetails(env Env, cfg config.Config, opts *Options) (setupAnswers, error) {
	d := tui.Details{Budget: cfg.BudgetUSD, ReportsDir: cfg.ReportsDir}
	form := tui.DetailsForm(&d, tui.FormDeps{CheckKey: tui.KeyChecker{}.Check, Home: env.Home})
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return setupAnswers{}, errSetupStopped
		}
		return setupAnswers{}, err
	}
	budget, err := config.NormaliseBudget(d.Budget)
	if err != nil {
		return setupAnswers{}, err
	}
	cfg.BudgetUSD, cfg.ReportsDir = budget, d.ReportsDir

	decision := telemetry.Resolve(telemetry.ConsentInputs{
		DoNotTrack: env.Environ["DO_NOT_TRACK"], NoTelemetry: opts.NoTelemetry,
		AssumeYes: opts.Yes, TTY: env.TTY, Config: cfg.Telemetry,
	})
	if decision == telemetry.Ask {
		th := theme.New(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
		c := tui.NewConsent(th, buildinfo.Version)
		if _, err := tea.NewProgram(c).Run(); err != nil {
			return setupAnswers{}, err
		}
		if c.Aborted() {
			return setupAnswers{}, errSetupStopped
		}
		choice := c.Choice()
		cfg.Telemetry = &choice
	}
	return setupAnswers{cfg: cfg, key: d.APIKey}, nil
}
