package telemetry

import "time"

// SampleStep names the step the sample's step and error events mention; the
// host network package registers it.
const SampleStep = "host-tap-service"

// Sample returns one event of each type with typical values, for the
// consent screen. Render each through Payload to show exactly what Capture
// would queue.
func Sample(cliVersion string) []Event {
	v := Version(cliVersion)
	return []Event{
		CommandRun{Command: "setup", Outcome: OutcomeOK, Duration: DurationBandOf(4 * time.Minute), CLIVersion: v},
		SetupPhaseReached{Phase: "host"},
		StepFinished{Step: SampleStep, Outcome: OutcomeOK, Duration: DurationBandOf(2 * time.Second)},
		ReviewFinished{
			Mode: "pr", Outcome: OutcomeOK, Duration: DurationBandOf(12 * time.Minute),
			Cost: CostBandOf(3.4), SubAgents: CountBandOf(4), ProxyDenials: CountBandOf(0),
			CLIVersion: v, ClaudeVersion: "2.1.327",
		},
		EnvironmentProfile{
			Distro: "debian", DistroVersion: "24.04", Arch: "amd64", Kernel: "6.8.0-51-generic",
			RAM: SizeBandOf(32 << 30), Disk: SizeBandOf(200 << 30), Firewall: "ufw",
			SecurityModule: "apparmor", SecurityMode: "enforcing", PrivilegeTool: "sudo",
			FirecrackerVersion: "v1.18.0",
		},
		SelftestProbeFailed{Probe: "docker-port"},
		LaunchRefused{Reason: "rule_hash"},
		Exception{Kind: "login_expired", Step: SampleStep, Stack: []StackFrame{
			{Function: "internal/step.(*engine).runStep", File: "internal/step/engine.go", Line: 171},
			{Function: "internal/step.Run", File: "internal/step/engine.go", Line: 95},
		}},
	}
}

// Allowed reports whether a property name may leave the machine.
func Allowed(name string) bool { return allowedProperty(name) }
