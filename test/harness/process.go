package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	// readinessTimeout is generous because the first `go run` of a session
	// compiles the whole dependency tree before the service even starts.
	readinessTimeout = 120 * time.Second

	// shutdownTimeout must exceed a service's own drain delay, or the
	// launcher would kill services that are shutting down correctly.
	shutdownTimeout = 45 * time.Second
)

// Service is one binary the launcher starts.
type Service struct {
	Name            string
	AppPort         int
	MaintenancePort int

	// Database is whether this service owns data. Only the backend does; the
	// other two reach it through its API.
	Database bool

	// BackendURL is where this service reaches the backend. Empty for the
	// backend itself. It has to be passed explicitly: on ephemeral ports the
	// compiled-in default points nowhere, and the service would quietly
	// degrade instead of failing visibly.
	BackendURL string

	// SiteURL is where the public frontend answers.
	SiteURL string

	// ConsoleURL is where staff reach the console, which is a different
	// address from SiteURL and is what the OAuth redirect is built from. Only
	// the console is told.
	ConsoleURL string

	// Development disables authentication. True for an ordinary local run,
	// which is what every checker in `.local/` expects; false under -sso,
	// where the point is to exercise the real sign-in.
	Development bool

	// Superuser reopens the setup wizard on an installation that is already
	// provisioned. Only the console understands it.
	//
	// It is **not** Development with another name — it disables nothing. It is
	// the one way to re-point a console whose directory configuration has gone
	// wrong, and the local runner needs it for the same reason a deployment
	// does: without it, repairing a run means throwing the run away, and the
	// commonest repair is routine. An authentik API token expires after thirty
	// minutes by default.
	Superuser bool
}

// Process is a started service.
//
// Exactly one goroutine waits on the underlying command — started by Start and
// owned by the Process — because exec.Cmd.Wait is not safe to call twice, and
// two callers racing on it deadlock rather than failing visibly.
type Process struct {
	Service Service

	cmd        *exec.Cmd
	transcript *os.File

	done chan struct{}
	err  error
}

// argsFor builds a service's command line.
//
// Separate from Start so it can be examined without launching a process: two
// of these flags decide whether the console authenticates anybody, and the
// kind of mistake worth catching here is one flag silently going missing.
func argsFor(dir RunDir, svc Service, logLevel string) []string {
	args := []string{
		"run", "./cmd/" + svc.Name,
		"--log-level", logLevel,
		"--log-file", dir.LogPath(svc.Name),
		"--app-port", fmt.Sprint(svc.AppPort),
		"--maintenance-port", fmt.Sprint(svc.MaintenancePort),
	}
	if svc.Database {
		args = append(args,
			"--database-driver", "sqlite",
			"--database-dsn", dir.DatabasePath(svc.Name),
			// Snapshots are written here rather than into the working tree.
			"--storage-url", dir.StorageURL(),
		)
	}
	if svc.BackendURL != "" {
		args = append(args, "--backend-url", svc.BackendURL)
	}
	if svc.SiteURL != "" {
		args = append(args, "--site-url", svc.SiteURL)
	}
	if svc.ConsoleURL != "" {
		args = append(args, "--console-url", svc.ConsoleURL)
	}
	if svc.Development {
		// Local only: this is what disables authentication. Under -sso it is
		// absent, and the services authenticate for real.
		args = append(args, "--development")
	}
	if svc.Superuser {
		// Disables nothing: it reopens the setup wizard on the maintenance
		// port of a console that is otherwise locked.
		args = append(args, "--superuser")
	}
	return args
}

// Start launches a service with `go run`, wiring its output to the terminal
// and to a transcript file.
func Start(root string, dir RunDir, svc Service, logLevel string, env []string) (*Process, error) {
	args := argsFor(dir, svc, logLevel)

	// Appended, not truncated: a resumed run keeps the transcript of what it
	// did before, which is most of the reason for going back to it.
	transcript, err := os.OpenFile(dir.ConsolePath(svc.Name),
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create %s transcript: %w", svc.Name, err)
	}

	cmd := exec.Command("go", args...)
	cmd.Dir = root
	// test/.env first, then the shell's environment. exec.Cmd takes the LAST
	// value for a duplicated key, so this is what makes a variable exported in
	// the terminal override the file — the ad-hoc override is the one somebody
	// is actively thinking about, and the other order silently ignores it.
	cmd.Env = append(append([]string{}, env...), os.Environ()...)
	cmd.Stdout = io.MultiWriter(prefixer{name: svc.Name, out: os.Stdout}, transcript)
	cmd.Stderr = io.MultiWriter(prefixer{name: svc.Name, out: os.Stderr}, transcript)
	// `go run` compiles, then runs the service as a child of its own. Putting
	// it in its own process group lets the launcher signal the group, which is
	// the only way the signal reaches the service itself — signalling `go run`
	// alone would leave the service running and never let it drain.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		transcript.Close() //nolint:errcheck
		return nil, fmt.Errorf("start %s: %w", svc.Name, err)
	}

	proc := &Process{Service: svc, cmd: cmd, transcript: transcript, done: make(chan struct{})}
	go func() {
		proc.err = proc.cmd.Wait()
		close(proc.done)
	}()
	return proc, nil
}

// Done is closed once the service has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err reports why the service exited. Only valid once Done is closed.
func (p *Process) Err() error { return p.err }

// Signal sends a signal to the service's whole process group.
func (p *Process) Signal(sig syscall.Signal) {
	if p.cmd.Process == nil {
		return
	}
	// The negative pid addresses the process group, which is what reaches the
	// service binary behind `go run`.
	if err := syscall.Kill(-p.cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(os.Stderr, "warning: cannot signal %s: %v\n", p.Service.Name, err)
	}
}

// WaitReady blocks until every service reports ready, or the wait times out.
func WaitReady(ctx context.Context, running []*Process) error {
	deadline := time.Now().Add(readinessTimeout)
	client := &http.Client{Timeout: time.Second}

	for _, proc := range running {
		url := fmt.Sprintf("http://127.0.0.1:%d/healthz/ready", proc.Service.MaintenancePort)
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s never became ready", proc.Service.Name)
			}

			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close() //nolint:errcheck
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return nil
}

// Stop asks every service to stop, gives them time to drain, and only then
// insists. Draining is the point: a service killed outright never exercises
// the graceful shutdown this project requires of it.
func Stop(running []*Process) {
	for _, proc := range running {
		proc.Signal(syscall.SIGTERM)
	}

	if !waitAll(running, shutdownTimeout) {
		fmt.Fprintln(os.Stderr, "services did not stop in time, killing")
		for _, proc := range running {
			proc.Signal(syscall.SIGKILL)
		}
		waitAll(running, 5*time.Second)
	}

	for _, proc := range running {
		proc.transcript.Close() //nolint:errcheck
	}
}

// prefixer labels each line with the service it came from, so three services
// sharing one terminal stay readable.
type prefixer struct {
	name string
	out  io.Writer
}

func (p prefixer) Write(b []byte) (int, error) {
	start := 0
	for i, c := range b {
		if c != '\n' {
			continue
		}
		if _, err := fmt.Fprintf(p.out, "%-9s | %s\n", p.name, b[start:i]); err != nil {
			return 0, err
		}
		start = i + 1
	}
	if start < len(b) {
		if _, err := fmt.Fprintf(p.out, "%-9s | %s\n", p.name, b[start:]); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// waitAll reports whether every service exited within the timeout.
func waitAll(running []*Process, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for _, proc := range running {
		select {
		case <-proc.Done():
		case <-deadline:
			return false
		}
	}
	return true
}
