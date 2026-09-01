package corpus

import "moedex/internal/corpus/catalog"

// Managed-corpus catalog/lock schema (the on-disk ownership marker and exact
// acquisition snapshot). The types and (de)serialization logic actually live
// in the leaf package internal/corpus/catalog, which has no os/exec
// dependency — internal/ingest depends on that leaf package directly (never
// on this one) so that recognizing a managed root and reading its locked
// commit stays exec-free in every default-build production binary, per the
// corpus-isolation invariant in CLAUDE.md/ARCHITECTURE.md ("internal/corpus
// ... is never imported by the engine or daemon"; see also docs/adr/0019 and
// review finding F-17).
//
// Everything below just re-exports that schema under its historical names,
// so every other file in this package (and every existing caller of it, in
// the `moe corpus` adapter and tests) is unchanged.
const (
	ManagedSchemaVersion = catalog.ManagedSchemaVersion
	ManagedDirName       = catalog.ManagedDirName
	CatalogFileName      = catalog.CatalogFileName
	LockFileName         = catalog.LockFileName
	RefPolicyDefault     = catalog.RefPolicyDefault

	LockStatusCurrent        = catalog.LockStatusCurrent
	LockStatusCarriedForward = catalog.LockStatusCarriedForward
)

type (
	// GroupPolicy records the curated top-level GitLab namespaces.
	GroupPolicy = catalog.GroupPolicy
	// Catalog is the versioned ownership marker for a Moedex-managed corpus.
	Catalog = catalog.Catalog
	// LockStatus describes whether a project advanced during the most recent sync.
	LockStatus = catalog.LockStatus
	// LockedProject is one exact, stable-ID project snapshot.
	LockedProject = catalog.LockedProject
	// Lock is the deterministic acquisition-to-indexing handoff.
	Lock = catalog.Lock
)

// NewCatalog builds the canonical version-1 marker for cfg.
func NewCatalog(cfg Config) (Catalog, error) {
	return catalog.NewCatalog(cfg.Host, cfg.Groups)
}

var (
	// CatalogPath returns the marker path beneath root.
	CatalogPath = catalog.CatalogPath
	// IsManagedRoot reports whether root carries the Moedex ownership marker.
	IsManagedRoot = catalog.IsManagedRoot
	// LoadCatalog strictly decodes and validates root's ownership marker.
	LoadCatalog = catalog.LoadCatalog
	// WriteCatalog writes a canonical, fsync-durable ownership marker.
	WriteCatalog = catalog.WriteCatalog

	// NewLock constructs and validates a canonical lock for host.
	NewLock = catalog.NewLock
	// LockPath returns the acquisition snapshot path beneath root.
	LockPath = catalog.LockPath
	// LoadLock strictly decodes and validates root's acquisition snapshot.
	LoadLock = catalog.LoadLock
	// WriteLock writes a canonical, fsync-durable acquisition snapshot.
	WriteLock = catalog.WriteLock

	writeCanonicalJSON  = catalog.WriteCanonicalJSON
	validGitObjectID    = catalog.ValidGitObjectID
	validateManagedPath = catalog.ValidateManagedPath
	validateCloneURL    = catalog.ValidateCloneURL
	managedPathsOverlap = catalog.ManagedPathsOverlap
)

// writeLock is WriteLock with an internal fault-injection seam (beforeRename)
// used by lifecycle tests.
func writeLock(root, host string, l Lock, beforeRename func(string) error) error {
	return catalog.WriteLockWithHook(root, host, l, beforeRename)
}
