package semanticrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type ProcessSpec struct {
	Executable               string
	Args                     []string
	Dir                      string
	Env                      []string
	StdoutLimit, StderrLimit int64
}
type ProcessResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// RunProcess runs trusted compiler/build code with explicit environment and
// bounded capture. Process-group cleanup is lifecycle management, not a sandbox.
func RunProcess(ctx context.Context, s ProcessSpec) (ProcessResult, error) {
	var out ProcessResult
	if s.Executable == "" || s.Dir == "" || s.StdoutLimit < 1 || s.StdoutLimit > 256<<20 || s.StderrLimit < 1 || s.StderrLimit > 1<<20 {
		return out, fmt.Errorf("semanticrun: invalid process specification")
	}
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); ok {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Executable, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = append([]string{}, s.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		if cmd.Process == nil {
			return nil
		}
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(e, syscall.ESRCH) {
			return nil
		}
		return e
	}
	cmd.Cancel = kill
	cmd.WaitDelay = 2 * time.Second
	stdout := &boundedCapture{limit: s.StdoutLimit, cancel: cancel}
	stderr := &boundedCapture{limit: s.StderrLimit, cancel: cancel}
	// Own the pipes so Wait reports leader exit before waiting for inherited
	// pipe handles in children. This lets us kill descendants immediately even
	// when the leader exits successfully and a child retains stdout/stderr.
	outR, outW, e := os.Pipe()
	if e != nil {
		return out, e
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, e := os.Pipe()
	if e != nil {
		return out, e
	}
	defer errR.Close()
	defer errW.Close()
	cmd.Stdout = outW
	cmd.Stderr = errW
	if e := cmd.Start(); e != nil {
		return out, e
	}
	outW.Close()
	errW.Close()
	drained := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stdout, outR); drained <- struct{}{} }()
	go func() { _, _ = io.Copy(stderr, errR); drained <- struct{}{} }()
	e = cmd.Wait()
	_ = kill() // descendants must not outlive even a successful parent
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-drained:
		case <-timer.C:
			outR.Close()
			errR.Close()
			for ; i < 2; i++ {
				<-drained
			}
			i = 2
		}
	}
	out.Stdout, out.Stderr = stdout.bytes(), stderr.bytes()
	out.ExitCode = cmd.ProcessState.ExitCode()
	if stdout.exceeded || stderr.exceeded {
		return out, fmt.Errorf("semanticrun: process output limit exceeded")
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if e != nil {
		return out, fmt.Errorf("semanticrun: process failed: %w", e)
	}
	return out, nil
}

type boundedCapture struct {
	mu       sync.Mutex
	data     []byte
	limit    int64
	cancel   context.CancelFunc
	exceeded bool
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - int64(len(b.data))
	if int64(n) > remaining {
		b.data = append(b.data, p[:int(remaining)]...)
		b.exceeded = true
		b.cancel()
		return n, nil
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedCapture) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}
