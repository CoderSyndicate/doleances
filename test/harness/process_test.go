package harness

import "testing"

// TestSuperuserIsPassedOnAndIsNotDevelopment.
//
// Two flags that look alike and are opposites: --development disables
// authentication, --superuser disables nothing and reopens one page on a
// console that is otherwise locked. A runner that confused them would turn
// "repair the directory configuration" into "turn authentication off for
// everybody first", which is backwards.
func TestSuperuserIsPassedOnAndIsNotDevelopment(t *testing.T) {
	args := argsFor(RunDir{}, Service{Name: "console", AppPort: 1, MaintenancePort: 2, Superuser: true}, "info")
	if !has(args, "--superuser") {
		t.Errorf("args = %v, want --superuser passed on", args)
	}
	if has(args, "--development") {
		t.Errorf("args = %v, want authentication left on", args)
	}

	plain := argsFor(RunDir{}, Service{Name: "console", AppPort: 1, MaintenancePort: 2}, "info")
	if has(plain, "--superuser") {
		t.Errorf("args = %v, want the setup door shut unless it is asked for", plain)
	}
}

func has(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
