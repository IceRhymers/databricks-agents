package profile

import (
	"errors"
	"testing"
)

// fakePatcher is a minimal SettingsPatcher used to prove the interface is
// implementable outside package main (compile-time conformance).
type fakePatcher struct{}

func (fakePatcher) Patch(PatchRequest) error     { return nil }
func (fakePatcher) Restore(RestoreRequest) error { return nil }

// fakeDaemon is a minimal DaemonStrategy that returns ErrDaemonUnsupported from
// Install, exercising the forward-looking sentinel contract.
type fakeDaemon struct{}

func (fakeDaemon) Install(DaemonInstallRequest) error { return ErrDaemonUnsupported }
func (fakeDaemon) Uninstall() error                   { return nil }
func (fakeDaemon) Status(int) (DaemonStatus, error)   { return DaemonStatus{}, nil }
func (fakeDaemon) Diagnostics() (string, error)       { return "", nil }

// fakeHooks is a minimal HookInstaller.
type fakeHooks struct{}

func (fakeHooks) Install() error   { return nil }
func (fakeHooks) Uninstall() error { return nil }

// Compile-time conformance: the three seams are implementable by types outside
// package main.
var (
	_ SettingsPatcher = fakePatcher{}
	_ DaemonStrategy  = fakeDaemon{}
	_ HookInstaller   = fakeHooks{}
)

// TestErrDaemonUnsupported_DistinctSentinel verifies ErrDaemonUnsupported is a
// distinct, errors.Is-comparable sentinel.
func TestErrDaemonUnsupported_DistinctSentinel(t *testing.T) {
	if ErrDaemonUnsupported == nil {
		t.Fatal("ErrDaemonUnsupported is nil")
	}
	err := fakeDaemon{}.Install(DaemonInstallRequest{})
	if !errors.Is(err, ErrDaemonUnsupported) {
		t.Errorf("fakeDaemon.Install err = %v, want ErrDaemonUnsupported", err)
	}
	if errors.Is(errors.New("other"), ErrDaemonUnsupported) {
		t.Error("unrelated error matched ErrDaemonUnsupported")
	}
}

// TestProfile_FieldsAssignable is a trivial construction check that all Profile
// fields accept the fake implementations.
func TestProfile_FieldsAssignable(t *testing.T) {
	p := Profile{
		Name:           "x",
		ChildBinary:    "y",
		ConfigPath:     func() (string, error) { return "", nil },
		GatewayPath:    "/g",
		PatchSettings:  fakePatcher{},
		DaemonStrategy: fakeDaemon{},
		HookInstaller:  fakeHooks{},
	}
	if p.PatchSettings == nil || p.DaemonStrategy == nil || p.HookInstaller == nil {
		t.Error("Profile interface fields must be non-nil after assignment")
	}
}
