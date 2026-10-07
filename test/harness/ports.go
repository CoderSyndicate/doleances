package harness

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fmt"
	"net"
)

// Test runs use the 53xx range, deliberately away from the services' own
// defaults (7300/8200/8201 with maintenance on 93xx/92xx). A local run must
// never collide with another project's services on this machine, nor with a
// production-shaped instance of this one.
//
//	530x  application ports
//	531x  maintenance ports
type Ports struct {
	BackendApp          int
	BackendMaintenance  int
	ConsoleApp          int
	ConsoleMaintenance  int
	FrontendApp         int
	FrontendMaintenance int
}

// StaticPorts returns the fixed test-run ports, so URLs stay bookmarkable
// between runs.
func StaticPorts() Ports {
	return Ports{
		BackendApp:          5300,
		ConsoleApp:          5301,
		FrontendApp:         5302,
		BackendMaintenance:  5310,
		ConsoleMaintenance:  5311,
		FrontendMaintenance: 5312,
	}
}

// AllocatePorts assigns ephemeral ports instead of the fixed ones, so several
// runs can be in flight at once.
func AllocatePorts() (Ports, error) {
	var (
		ports Ports
		err   error
	)
	for _, target := range []*int{
		&ports.BackendApp, &ports.ConsoleApp, &ports.FrontendApp,
		&ports.BackendMaintenance, &ports.ConsoleMaintenance, &ports.FrontendMaintenance,
	} {
		if *target, err = FreePort(); err != nil {
			return Ports{}, err
		}
	}
	return ports, nil
}

// FreePort returns an available TCP port.
func FreePort() (int, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a free port: %w", err)
	}
	defer ln.Close() //nolint:errcheck
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// CheckAvailable reports which of the given ports are already taken.
//
// Both the wildcard and the loopback address are probed: with SO_REUSEADDR a
// wildcard bind succeeds even when something else holds the loopback address,
// and the run would then silently talk to that other process instead.
func CheckAvailable(ports map[string]int) error {
	var taken []string
	for name, port := range ports {
		for _, host := range []string{"", "127.0.0.1"} {
			ln, err := net.Listen("tcp4", fmt.Sprintf("%s:%d", host, port))
			if err != nil {
				taken = append(taken, fmt.Sprintf("%s (:%d)", name, port))
				break
			}
			ln.Close() //nolint:errcheck
		}
	}
	if len(taken) == 0 {
		return nil
	}
	return fmt.Errorf("ports already in use: %v\nstop whatever holds them, or run with -dynamic-ports", taken)
}

// ReclaimPorts stops whatever is listening on the run's ports.
//
// A development runner that refuses to start because the last one is still
// alive is correct and useless: the answer is always "stop it and try again",
// and making somebody do that by hand teaches them to reach for kill -9, which
// is how a service ends up skipping its shutdown.
//
// So this asks politely first. SIGTERM reaches the harness and the services
// through their own process groups, which is what lets a backend flip to
// not-ready, finish what it is doing and close its database — the same path
// Ctrl-C takes. Only what is still holding a port afterwards is killed.
//
// Nothing here is portable and nothing here needs to be: it runs on a
// developer's machine, and an environment without lsof simply gets the old
// behaviour of being told the port is busy.
func ReclaimPorts(ports map[string]int) []string {
	var reclaimed []string

	for name, port := range ports {
		pids := listenersOn(port)
		if len(pids) == 0 {
			continue
		}
		reclaimed = append(reclaimed, fmt.Sprintf("%s (:%d)", name, port))
		signalAll(pids, syscall.SIGTERM)
	}
	if len(reclaimed) == 0 {
		return nil
	}

	// Long enough for a graceful shutdown to finish, short enough that nobody
	// wonders whether the runner has hung.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !anyListener(ports) {
			return reclaimed
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Whatever ignored SIGTERM is not going to honour anything else either.
	for _, port := range ports {
		signalAll(listenersOn(port), syscall.SIGKILL)
	}
	time.Sleep(500 * time.Millisecond)
	return reclaimed
}

func anyListener(ports map[string]int) bool {
	for _, port := range ports {
		if len(listenersOn(port)) > 0 {
			return true
		}
	}
	return false
}

// listenersOn returns the process ids listening on a port, or nothing at all
// when it cannot tell.
func listenersOn(port int) []int {
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		// Either nothing is listening or lsof is not here. Both mean "do not
		// go killing things on a guess".
		return nil
	}

	var pids []int
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		// Never the runner itself: -dynamic-ports aside, a mistake here would
		// have the harness kill the process asking the question.
		if err == nil && pid > 0 && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}

func signalAll(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		// The group, so `go run` and the service it started both hear it —
		// signalling the parent alone leaves the child running and holding
		// the port, which is the failure this whole function exists to clear.
		if pgid, err := syscall.Getpgid(pid); err == nil {
			_ = syscall.Kill(-pgid, sig)
			continue
		}
		_ = syscall.Kill(pid, sig)
	}
}
