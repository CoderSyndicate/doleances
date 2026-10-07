// Command test starts all three doléances services locally, each with its own
// log file and a SQLite database, under a fresh timestamped run directory.
//
// Usage:
//
//	go run ./test                 # fixed 53xx ports
//	go run ./test -dynamic-ports  # ephemeral ports, for a parallel run
//	go run ./test -log-level trace
//	go run ./test -data latest    # start from a previous run's state
//
// Every run gets its own directory, so one session's logs and database never
// overwrite the last one's. With -data, no new directory is made at all: the
// services run in the directory named, against the database, storage and logs
// already there — so yesterday's register and curation queue are simply picked
// back up:
//
//	test/run/2026-09-14-17-42-05/
//	  logs/       backend.json, backend.console, …
//	  databases/  backend.db
//	  storage/    snapshots and other generated artefacts
//	test/run/latest -> the newest run
//
// The ten most recent runs are kept and older ones are pruned, so a run worth
// returning to should be marked: `touch test/run/<run>/KEEP` exempts it from
// pruning for good.
//
// Authentication is disabled (--development) and the database is SQLite: this
// is a development launcher and is not fit for anything else.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/CoderSyndicate/doleances/test/harness"
)

// keepRuns is how many past run directories survive; older ones are pruned at
// startup so the tree does not grow without limit.
const keepRuns = 10

func main() {
	logLevel := flag.String("log-level", "trace", "log level for every service")
	dynamicPorts := flag.Bool("dynamic-ports", false, "use ephemeral ports instead of the fixed 53xx ones")
	data := flag.String("data", "",
		"start from a previous run's state: a run directory, a timestamp, or \"latest\"")
	latest := flag.Bool("latest", false,
		"carry on with the most recent run — the same as -data latest")
	sso := flag.Bool("sso", false,
		"authenticate for real: run the console without --development, so the setup "+
			"wizard and the Authentik sign-in are what is being exercised")
	superuser := flag.Bool("superuser", false,
		"reopen the setup wizard on a run that is already provisioned, to point it at a "+
			"different directory or replace an expired token — implies -sso")
	seeding := flag.Bool("seeding", false,
		"put doléances, groups and actions in once the services are up")
	flag.Parse()

	// -latest is shorthand, not a second mechanism: it resolves through the
	// same path -data does, so the two can never disagree about which run is
	// the most recent one.
	if *latest {
		// Naming a run *and* asking for the most recent one is two
		// instructions, and quietly obeying one of them is how somebody
		// spends an afternoon reading the wrong database.
		if *data != "" && *data != "latest" {
			fmt.Fprintf(os.Stderr,
				"\ntest: -latest and -data %s contradict each other; pass one\n", *data)
			os.Exit(1)
		}
		*data = "latest"
	}

	// Reopening the wizard on a console whose authentication is off would be a
	// door into a building with no walls — and the only reason to want the
	// wizard is to fix the sign-in that -sso exercises.
	if *superuser {
		*sso = true
	}

	if err := run(*logLevel, *dynamicPorts, *data, *seeding, *sso, *superuser); err != nil {
		fmt.Fprintf(os.Stderr, "\ntest: %v\n", err)
		os.Exit(1)
	}
}

func run(logLevel string, dynamicPorts bool, data string, seeding, sso, superuser bool) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}

	ports, err := resolvePorts(dynamicPorts)
	if err != nil {
		return err
	}
	backendURL := fmt.Sprintf("http://127.0.0.1:%d", ports.BackendApp)
	// Where a reader reaches this run, and the one setting a local instance
	// cannot get away with being sloppy about: **a passkey is bound to this
	// host**, and the relying party id is derived from it.
	//
	// `localhost` rather than `127.0.0.1`, deliberately. Both are secure
	// contexts as far as the browser is concerned, but an IP address is not a
	// registrable domain and browsers refuse it as a relying party id — so a
	// run addressed by its loopback number would offer a signup that no
	// browser can complete, with the error appearing only in the console of
	// whoever tried.
	frontendURL := fmt.Sprintf("http://localhost:%d", ports.FrontendApp)
	// Where staff reach the console, which is **not** frontendURL: authentik
	// matches a redirect URI strictly, so a console registered at the public
	// register's address would fail every sign-in.
	//
	// `localhost` for the same reason the frontend uses it — and here it is the
	// OAuth redirect rather than a passkey origin, but an operator typing two
	// different hostnames for one deployment is how a strict match stops
	// matching.
	consoleURL := fmt.Sprintf("http://localhost:%d", ports.ConsoleApp)

	// -sso turns authentication on by leaving --development off. Only the
	// console has anything to authenticate, so only the console changes: the
	// backend is not publicly exposed and the frontend's readers use passkeys,
	// which --development never touched.
	development := !sso

	services := []harness.Service{
		{Name: "backend", AppPort: ports.BackendApp, MaintenancePort: ports.BackendMaintenance,
			Database: true, SiteURL: frontendURL, Development: true},
		{Name: "console", AppPort: ports.ConsoleApp, MaintenancePort: ports.ConsoleMaintenance,
			BackendURL: backendURL, SiteURL: frontendURL, ConsoleURL: consoleURL,
			Development: development, Superuser: superuser},
		{Name: "frontend", AppPort: ports.FrontendApp, MaintenancePort: ports.FrontendMaintenance,
			BackendURL: backendURL, SiteURL: frontendURL, Development: true},
	}

	base := filepath.Join(root, "test", "run")

	// -data resumes a run: the services run in that directory, against its
	// database and its storage. No new directory is made, because the state
	// somebody named is the state they want to carry on with — and a copy
	// would leave them reading logs in one folder and data in another.
	//
	// The run is resolved before anything creates or moves a directory:
	// "latest" is a symlink that a new run repoints at itself, so resolving
	// afterwards would find the new empty directory instead.
	var dir harness.RunDir
	if data != "" {
		resumed, err := harness.ResolveRun(root, data)
		if err != nil {
			return err
		}
		harness.Prune(base, keepRuns, resumed)

		dir, err = harness.OpenRunDir(base, resumed)
		if err != nil {
			return err
		}
		fmt.Printf("resuming run:  %s\n", dir.Root)
	} else {
		harness.Prune(base, keepRuns)

		dir, err = harness.NewRunDir(base)
		if err != nil {
			return err
		}
		fmt.Printf("run directory: %s\n", dir.Root)
	}
	fmt.Println()

	// The launcher owns the shutdown: it catches the interrupt and passes it
	// on, so every service gets to drain rather than being cut off.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Secrets — the LLM API key above all — come from test/.env rather than
	// from the command line, where they would sit in shell history and in ps
	// output. A missing file is normal: most local work needs no secret.
	env, err := harness.LoadEnv(root)
	if err != nil {
		return err
	}
	if len(env) > 0 {
		fmt.Printf("env:       test/%s (%s)\n", harness.EnvFile,
			strings.Join(harness.EnvNames(env), ", "))
	}

	var (
		running []*harness.Process
		exits   = make(chan error, len(services))
	)
	for _, svc := range services {
		proc, err := harness.Start(root, dir, svc, logLevel, env)
		if err != nil {
			harness.Stop(running)
			return err
		}
		running = append(running, proc)
		fmt.Printf("starting %-9s app :%d  maintenance :%d\n", svc.Name, svc.AppPort, svc.MaintenancePort)

		go func() {
			// A service that stops on its own ends the session: two of three
			// running is not a useful development environment.
			<-proc.Done()
			if err := proc.Err(); err != nil && ctx.Err() == nil {
				exits <- fmt.Errorf("%s exited: %w", proc.Service.Name, err)
			}
		}()
	}

	fmt.Println("\ncompiling and starting, this takes a moment on a cold build cache…")
	if err := harness.WaitReady(ctx, running); err != nil {
		harness.Stop(running)
		return err
	}
	report(running, dir, sso, superuser, consoleURL)

	// After the services answer, and through their own API. A fixture written
	// straight into the database would exercise nothing and would keep working
	// long after the path a real submission takes had broken.
	if seeding {
		seed(ctx, backendURL, frontendURL)
	}

	select {
	case <-ctx.Done():
		fmt.Println("\ninterrupt received, stopping services")
	case err := <-exits:
		harness.Stop(running)
		return err
	}

	harness.Stop(running)
	fmt.Println("all services stopped")
	return nil
}

// seed fills the run with something to look at, and never stops it.
//
// A development convenience that failed loudly would be worse than one that
// failed quietly: somebody who asked for fixtures and got none can see that
// from the count, and the services they actually came for are already up.
func seed(ctx context.Context, backendURL, origin string) {
	fmt.Println("\nseeding…")

	summary, err := harness.Seed(ctx, backendURL, origin, os.Stdout)
	if err != nil {
		fmt.Printf("seeding failed: %v\n", err)
		return
	}

	fmt.Printf("seeded %d doléances, %d groups, %d actions",
		summary.Messages, summary.Groups, summary.Actions)
	if summary.Skipped > 0 {
		// Usually not a failure. A run that was already seeded has these
		// doléances in its register and these group names taken, and both
		// guards say so — which is them working rather than seeding breaking.
		fmt.Printf(" (%d already there)", summary.Skipped)
	}
	fmt.Println("\nthey reach the register as the classifier gets to them.")
}

func resolvePorts(dynamic bool) (harness.Ports, error) {
	if dynamic {
		return harness.AllocatePorts()
	}

	ports := harness.StaticPorts()
	wanted := map[string]int{
		"backend":              ports.BackendApp,
		"backend maintenance":  ports.BackendMaintenance,
		"console":              ports.ConsoleApp,
		"console maintenance":  ports.ConsoleMaintenance,
		"frontend":             ports.FrontendApp,
		"frontend maintenance": ports.FrontendMaintenance,
	}

	// A previous run is stopped rather than complained about. Being told to go
	// and find it teaches people to reach for kill -9, which is how a service
	// ends up skipping the shutdown this project bothered to write.
	if reclaimed := harness.ReclaimPorts(wanted); len(reclaimed) > 0 {
		fmt.Printf("stopped a previous run holding: %v\n", reclaimed)
	}

	// Still checked afterwards: something that is not ours may hold a port,
	// and starting anyway would have the run silently talk to it.
	return ports, harness.CheckAvailable(wanted)
}

// report prints where the run can be reached.
//
// **Every address here says `localhost`, and that is load-bearing rather than
// cosmetic.** A passkey is bound to a registrable domain, and an IP address is
// not one: a browser pointed at `http://127.0.0.1:5302` refuses to create a
// credential with *"The effective domain of the document is not a valid
// domain"* — thrown by the browser before any request leaves it, so nothing on
// the server side can detect it, explain it, or be configured around it.
//
// Adding `127.0.0.1` to the relying party's allowed origins does not help and
// is not worth trying. The refusal is about the document, not about what this
// server would accept.
//
// So the printed address is the fix, because the printed address is the one
// somebody clicks.
func report(running []*harness.Process, dir harness.RunDir, sso, superuser bool, consoleURL string) {
	fmt.Printf("\n%-9s  %-26s  %-26s  %s\n", "service", "application", "maintenance", "docs")
	for _, proc := range running {
		svc := proc.Service
		app := fmt.Sprintf("http://localhost:%d", svc.AppPort)
		maintenance := fmt.Sprintf("http://localhost:%d", svc.MaintenancePort)
		fmt.Printf("%-9s  %-26s  %-26s  %s/docs\n", svc.Name, app, maintenance, app)
	}

	fmt.Printf("\nlogs:      %s\n", dir.Logs)
	fmt.Printf("databases: %s\n", dir.Databases)
	fmt.Printf("storage:   %s\n", dir.Storage)
	if sso {
		var consoleMaintenance int
		for _, proc := range running {
			if proc.Service.Name == "console" {
				consoleMaintenance = proc.Service.MaintenancePort
			}
		}
		fmt.Println("\nconsole authentication is ON: sign in through authentik")
		fmt.Printf("  set it up at   http://localhost:%d/setup   (the console's MAINTENANCE port)\n",
			consoleMaintenance)
		fmt.Printf("  redirect URI   %s/auth/callback\n", consoleURL)
		fmt.Println("  you need an authentik instance and an API token from it:")
		fmt.Println("    Directory → Tokens and App passwords → Create, intent 'API Token'")
		// It is only needed for setup: provisioning mints a non-expiring
		// token of its own and keeps that instead.
		fmt.Println("    it is only used for setup, so authentik's 30-minute default is fine")
		if superuser {
			fmt.Println("\n  --superuser: the wizard is open on a run that is already set up.")
			fmt.Println("  Running it again adopts what is already there — nothing is recreated.")
		}
	} else {
		fmt.Println("\nauthentication is DISABLED (--development) — use -sso to exercise it")
	}
	fmt.Println("open localhost, not 127.0.0.1: passkeys refuse an IP address as an origin")
	fmt.Println("press ctrl-c to stop")
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod, so the launcher works from anywhere in the repository.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found: run this from inside the repository")
		}
		dir = parent
	}
}
