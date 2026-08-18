package corpus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// managedLockFileName is the advisory lock that serializes SyncManaged and
// InitManaged against the same corpus root. It lives inside ".git" — never
// inside ManagedDirName and never at the worktree top level — so it stays
// invisible to `git status`, to the ensureManagedIndexClean and
// ensureManagedActionClean dirty-tree checks (which scope to .gitmodules and
// ManagedDirName), and to every explicit `git add` path list in this
// package: it is never staged, committed, or swept up by those checks.
//
// The lock file itself is never removed once created. Unlinking a flock'd
// file while a waiter races to reopen the same path lets that waiter create
// and lock a *new* inode while the original holder's fd still refers to the
// old one, silently defeating mutual exclusion. Leaving the file in place
// forever avoids that class of bug at the cost of one small, permanent file.
const managedLockFileName = "moedex-corpus.lock"

// errManagedCorpusLocked reports that another SyncManaged/InitManaged
// invocation already holds the corpus root's advisory lock.
var errManagedCorpusLocked = errors.New("another moedex-corpus sync or init is already running against this managed corpus root")

func managedLockPath(root string) string {
	return filepath.Join(root, ".git", managedLockFileName)
}

// acquireManagedLock takes a non-blocking, cross-process exclusive advisory
// lock over root so that two concurrent sync/init invocations — a
// systemd-timer-driven sync overlapping an operator manually running the raw
// moedex-corpus binary, for example — cannot interleave their check-then-act
// sequences and desync corpus.lock.json from the actual gitlinks/.gitmodules.
// It fails fast (rather than blocking) so a second invocation gets a clear,
// immediate error instead of queuing up behind a long-running fetch/commit
// sequence. Callers must not invoke this before root's ".git" directory can
// exist (SyncManaged: true by construction; InitManaged: right after
// "git init" succeeds). The returned release must be called exactly once and
// never removes the lock file — see managedLockFileName.
func acquireManagedLock(root string) (release func() error, err error) {
	dir := filepath.Join(root, ".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("prepare managed corpus lock directory %q: %w", dir, err)
	}
	path := managedLockPath(root)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open managed corpus lock %q: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("managed corpus at %q: %w", root, errManagedCorpusLocked)
		}
		return nil, fmt.Errorf("lock managed corpus %q: %w", root, err)
	}

	released := false
	release = func() error {
		if released {
			return nil
		}
		released = true
		unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		closeErr := f.Close()
		if unlockErr != nil {
			return fmt.Errorf("unlock managed corpus %q: %w", root, unlockErr)
		}
		return closeErr
	}
	return release, nil
}
