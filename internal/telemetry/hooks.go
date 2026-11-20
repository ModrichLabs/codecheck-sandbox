package telemetry

import "github.com/ModrichLabs/codecheck-sandbox/internal/step"

// StepRecorder adapts c to step.Options.OnStepFinished: each finished step
// becomes a step_finished event, and a failure adds an $exception. A declined
// step reports no exception, since the user chose it.
func StepRecorder(c Client) func(step.StepResult) {
	return func(r step.StepResult) {
		c.Capture(StepFinished{
			Step:     StepID(r.ID),
			Outcome:  Outcome(r.Outcome),
			Duration: DurationBandOf(r.Duration),
		})
		if r.Outcome == step.OutcomeFailed && r.Err != nil {
			c.Capture(NewException(r.Err, r.ID))
		}
	}
}
