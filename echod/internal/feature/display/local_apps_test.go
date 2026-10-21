//go:build !dot && !spot

package display

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Exercise a real child process and process group rather than a mocked launcher.
func TestLocalAppsLeaseThroughReap(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	var a localApps
	a.mu.Lock()
	err = a.startCommand(exec.Command(sleep, "30"), func() {})
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pid := a.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if !a.active() {
		t.Fatal("lease must precede child execution")
	}
	a.request("youtube")
	a.mu.Lock()
	queued := a.requested
	a.mu.Unlock()
	if queued != "" {
		t.Fatal("overlapping launch was queued")
	}
	a.request("stop")
	deadline := time.Now().Add(5 * time.Second)
	for {
		owned, released := a.step(false, time.Now(), func() {})
		if released {
			if owned || a.active() {
				t.Fatal("lease retained after reap")
			}
			if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
				t.Fatalf("process group survived: %v", err)
			}
			break
		}
		if !owned {
			t.Fatal("lease lost before completion")
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not reap child")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocalAppsFailedStartReleasesLease(t *testing.T) {
	var a localApps
	a.mu.Lock()
	err := a.startCommand(exec.Command("/this-launcher-does-not-exist"), func() {})
	a.mu.Unlock()
	if err == nil || a.active() {
		t.Fatal("failed start retained the draw lease")
	}
}

func TestLocalAppsConcurrentRequests(t *testing.T) {
	var a localApps
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { a.request("web"); a.request("youtube"); _ = a.active() })
	}
	wg.Wait()
	a.request("stop")
	a.request("web")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requested != "stop" {
		t.Fatal("start displaced pending stop")
	}
}

func TestTrustedLauncherRejectsWritableAndSymlink(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("ownership validation requires root fixture")
	}
	dir := t.TempDir()
	path := dir + "/launcher"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if trustedLauncher(path) == nil {
		t.Fatal("accepted writable launcher")
	}
	link := dir + "/link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if trustedLauncher(link) == nil {
		t.Fatal("accepted launcher symlink")
	}
}
