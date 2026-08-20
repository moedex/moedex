package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const lockName = ".snapshot-build.lock"

var ErrBuildLocked = errors.New("snapshot: another build holds the writer lock")

// Transaction owns one staging directory and the single-writer publication
// lock. Abort removes only this transaction's private stage.
type Transaction struct {
	indexDir string
	id       string
	dir      string
	lock     *os.File
	closed   bool
}

func Begin(indexDir, id string) (*Transaction, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(indexDir, SnapshotsDir), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(indexDir, StagingDir), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(SnapshotPath(indexDir, id)); err == nil {
		return nil, fmt.Errorf("snapshot: %q already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lockPath := filepath.Join(indexDir, lockName)
	lock, err := acquireBuildLock(lockPath)
	if errors.Is(err, ErrBuildLocked) {
		owner, _ := os.ReadFile(lockPath)
		return nil, fmt.Errorf("%w (%s)", ErrBuildLocked, string(owner))
	}
	if err != nil {
		return nil, err
	}
	if err := lock.Truncate(0); err != nil {
		releaseBuildLock(lock)
		return nil, err
	}
	if _, err := lock.Seek(0, 0); err != nil {
		releaseBuildLock(lock)
		return nil, err
	}
	if _, err := fmt.Fprintf(lock, "pid=%d started=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		releaseBuildLock(lock)
		return nil, err
	}
	_ = lock.Sync()
	dir, err := os.MkdirTemp(filepath.Join(indexDir, StagingDir), id+"-")
	if err != nil {
		releaseBuildLock(lock)
		return nil, err
	}
	return &Transaction{indexDir: indexDir, id: id, dir: dir, lock: lock}, nil
}

func (tx *Transaction) Dir() string {
	if tx == nil {
		return ""
	}
	return tx.dir
}

func (tx *Transaction) Commit(m *Manifest) error {
	if tx == nil || tx.closed {
		return errors.New("snapshot: transaction is closed")
	}
	if m == nil {
		return errors.New("snapshot: nil manifest")
	}
	if m.ID == "" {
		m.ID = tx.id
	}
	if m.ID != tx.id {
		return fmt.Errorf("snapshot: transaction id %q does not match manifest id %q", tx.id, m.ID)
	}
	if m.Version == 0 {
		m.Version = FormatVersion
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Sequence == 0 {
		list, err := List(tx.indexDir)
		if err != nil {
			return err
		}
		m.Sequence = 1
		if len(list) > 0 {
			m.Sequence = list[0].Sequence + 1
		}
	}
	if m.ParentID == "" {
		if current, err := readCurrentID(tx.indexDir); err == nil {
			m.ParentID = current
		} else if !errors.Is(err, ErrNoCurrent) {
			return err
		}
	}
	if err := ValidateFiles(tx.dir, m); err != nil {
		return err
	}
	if err := syncTree(tx.dir); err != nil {
		return err
	}
	if err := writeManifest(filepath.Join(tx.dir, ManifestName), m); err != nil {
		return err
	}
	if err := syncDir(tx.dir); err != nil {
		return err
	}

	final := SnapshotPath(tx.indexDir, tx.id)
	if err := os.Rename(tx.dir, final); err != nil {
		return fmt.Errorf("snapshot: publish immutable directory: %w", err)
	}
	tx.dir = ""
	if err := syncDir(filepath.Join(tx.indexDir, SnapshotsDir)); err != nil {
		tx.release()
		return fmt.Errorf("snapshot: published %q but could not sync snapshot directory: %w", tx.id, err)
	}
	if err := setCurrentUnlocked(tx.indexDir, tx.id); err != nil {
		tx.release()
		return fmt.Errorf("snapshot: published %q but could not advance CURRENT: %w", tx.id, err)
	}
	tx.release()
	return nil
}

// Abort discards this transaction's private staging directory and releases its
// writer lock. A published snapshot is never removed by Abort.
func (tx *Transaction) Abort() error {
	if tx == nil || tx.closed {
		return nil
	}
	var err error
	if tx.dir != "" {
		err = os.RemoveAll(tx.dir)
	}
	tx.release()
	return err
}

func (tx *Transaction) release() {
	if tx.closed {
		return
	}
	tx.closed = true
	if tx.lock != nil {
		releaseBuildLock(tx.lock)
		tx.lock = nil
	}
}

// SetCurrent atomically switches readers to an already complete snapshot. It is
// also the rollback operation; artifacts are never copied or rewritten.
func SetCurrent(indexDir, id string) error {
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(indexDir, lockName)
	lock, err := acquireBuildLock(lockPath)
	if errors.Is(err, ErrBuildLocked) {
		owner, _ := os.ReadFile(lockPath)
		return fmt.Errorf("%w (%s)", ErrBuildLocked, string(owner))
	}
	if err != nil {
		return err
	}
	defer releaseBuildLock(lock)
	return setCurrentUnlocked(indexDir, id)
}

func setCurrentUnlocked(indexDir, id string) error {
	m, err := Load(indexDir, id)
	if err != nil {
		return err
	}
	if err := ValidateFiles(SnapshotPath(indexDir, id), m); err != nil {
		return err
	}
	dir := indexDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".CURRENT-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, CurrentName)); err != nil {
		return err
	}
	return syncDir(dir)
}

func writeManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func syncTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot: staged symlink %s is not allowed", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("snapshot: staged artifact %s is not a regular file", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		// Some supported filesystems reject directory fsync. File fsync and atomic
		// rename still hold; do not turn that platform limitation into data loss.
		return nil
	}
	return closeErr
}

// LockOwner returns the diagnostic writer-lock contents, if any.
func LockOwner(indexDir string) string {
	data, _ := os.ReadFile(filepath.Join(indexDir, lockName))
	return strings.TrimSpace(string(data))
}

// nextID is useful to CLI callers that want a sortable default ID.
func NextID(now time.Time, sequence uint64) string {
	return now.UTC().Format("20060102T150405Z") + "-" + strconv.FormatUint(sequence, 10)
}
