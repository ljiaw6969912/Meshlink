package networklifecycle

import (
	"errors"
	"reflect"
	"testing"

	"meshlink/internal/winservice"
)

type fakeLeaveManager struct {
	leave func(string) error
}

func (f fakeLeaveManager) LeaveNetwork(serviceName string) error {
	return f.leave(serviceName)
}

func TestLeaveStopsThenUninstallsThenClearsIdentity(t *testing.T) {
	var calls []string
	ops := ServiceOps{
		Status: func(string) (winservice.ServiceStatus, error) {
			return winservice.ServiceStatus{Installed: true, State: "running"}, nil
		},
		Stop: func(string) error {
			calls = append(calls, "stop")
			return nil
		},
		Uninstall: func(string) error {
			calls = append(calls, "uninstall")
			return nil
		},
	}
	manager := fakeLeaveManager{leave: func(string) error {
		calls = append(calls, "clear")
		return nil
	}}

	if err := LeaveWithOps(manager, "MeshlinkAgent", ops); err != nil {
		t.Fatal(err)
	}
	if want := []string{"stop", "uninstall", "clear"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestLeaveSkipsServiceActionsWhenNotNeeded(t *testing.T) {
	tests := []struct {
		name   string
		status winservice.ServiceStatus
		want   []string
	}{
		{name: "not installed", status: winservice.ServiceStatus{}, want: []string{"clear"}},
		{name: "already stopped", status: winservice.ServiceStatus{Installed: true, State: "stopped"}, want: []string{"uninstall", "clear"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			ops := ServiceOps{
				Status:    func(string) (winservice.ServiceStatus, error) { return tc.status, nil },
				Stop:      func(string) error { calls = append(calls, "stop"); return nil },
				Uninstall: func(string) error { calls = append(calls, "uninstall"); return nil },
			}
			manager := fakeLeaveManager{leave: func(string) error {
				calls = append(calls, "clear")
				return nil
			}}

			if err := LeaveWithOps(manager, "MeshlinkAgent", ops); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("calls = %v, want %v", calls, tc.want)
			}
		})
	}
}

func TestLeavePropagatesFirstFailureAndStops(t *testing.T) {
	errStatus := errors.New("status failed")
	errStop := errors.New("stop failed")
	errUninstall := errors.New("uninstall failed")
	errClear := errors.New("clear failed")
	tests := []struct {
		name         string
		statusErr    error
		stopErr      error
		uninstallErr error
		clearErr     error
		wantErr      error
		wantCalls    []string
	}{
		{name: "status", statusErr: errStatus, wantErr: errStatus},
		{name: "stop", stopErr: errStop, wantErr: errStop, wantCalls: []string{"stop"}},
		{name: "uninstall", uninstallErr: errUninstall, wantErr: errUninstall, wantCalls: []string{"stop", "uninstall"}},
		{name: "clear", clearErr: errClear, wantErr: errClear, wantCalls: []string{"stop", "uninstall", "clear"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			ops := ServiceOps{
				Status: func(string) (winservice.ServiceStatus, error) {
					return winservice.ServiceStatus{Installed: true, State: "running"}, tc.statusErr
				},
				Stop:      func(string) error { calls = append(calls, "stop"); return tc.stopErr },
				Uninstall: func(string) error { calls = append(calls, "uninstall"); return tc.uninstallErr },
			}
			manager := fakeLeaveManager{leave: func(string) error {
				calls = append(calls, "clear")
				return tc.clearErr
			}}

			if err := LeaveWithOps(manager, "MeshlinkAgent", ops); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tc.wantCalls)
			}
		})
	}
}
