package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// ErrDoctorFailed reports a doctor run with at least one failed check.
var ErrDoctorFailed = errors.New("doctor found problems")

func newDoctorCommand(env Env, opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check every part of the setup and report problems",
		Long: "Runs every setup step's check: the platform, the packages, the host\n" +
			"network (including the installed rules and units against what setup\n" +
			"renders), and the guest image's hashes. It exits 1 on any failure.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := doctorRows(cmd.Context(), env, opts)
			if err != nil {
				return err
			}
			return printDoctor(cmd.OutOrStdout(), env, rows)
		},
	}
	return cmd
}

// doctorRows runs every check. A check that finds the privilege login lapsed
// gets one password prompt (Elevate), then a retry.
func doctorRows(ctx context.Context, env Env, opts *Options) ([]tui.DoctorRow, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	host, err := env.OpenSetup(*opts)
	if err != nil {
		return nil, err
	}
	c, _, err := env.LoadConfig()
	if err != nil {
		return nil, err
	}
	d := host.Deps
	d.CodeCheckCommit = c.CodeCheckCommit
	steps, err := host.Steps(d)
	if err != nil {
		return nil, err
	}
	elevated := false
	var rows []tui.DoctorRow
	for _, s := range steps {
		st, err := s.Check(ctx)
		if errors.Is(err, system.ErrLoginExpired) && !elevated && host.Elevate != nil {
			elevated = true
			if lerr := host.Elevate(ctx); lerr == nil {
				st, err = s.Check(ctx)
			}
		}
		row := tui.DoctorRow{Name: s.Title(), OK: err == nil && st.OK, Detail: st.Detail}
		if err != nil {
			row.Detail = "couldn't check: " + err.Error()
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func printDoctor(out io.Writer, env Env, rows []tui.DoctorRow) error {
	dark := true
	if env.TTY {
		dark = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	}
	// Colour only reaches a terminal; a pipe or file gets plain text.
	w := &colorprofile.Writer{Forward: out, Profile: colorprofile.Detect(out, env.environList())}
	fmt.Fprintln(w, tui.DoctorView(theme.New(dark), rows, 100))
	for _, r := range rows {
		if !r.OK {
			return ErrDoctorFailed
		}
	}
	return nil
}
