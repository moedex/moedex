package index

// BuildTarget is the common write target used by shard/build loops that can run
// either the eager all-trigram Index path or the selective two-pass Builder path.
type BuildTarget struct {
	eag *Index
	bld *Builder
}

// NewBuildTarget returns an AddFile/NumBlobs/Finalize target. A nil selector
// uses the eager Index path, preserving the default all-trigram behavior; a
// non-nil selector uses the selective Builder and applies it at Finalize.
func NewBuildTarget(sel GramSelector) *BuildTarget {
	if sel == nil {
		return &BuildTarget{eag: New()}
	}
	return &BuildTarget{bld: NewSelective(sel)}
}

// AddFile records content in the selected build path.
func (bt *BuildTarget) AddFile(repo, rel, abs, sha string, content []byte) {
	if bt.eag != nil {
		bt.eag.AddFile(repo, rel, abs, sha, content)
		return
	}
	bt.bld.AddFile(repo, rel, abs, sha, content)
}

// NumBlobs reports the number of distinct blobs added so far.
func (bt *BuildTarget) NumBlobs() int {
	if bt.eag != nil {
		return bt.eag.NumBlobs()
	}
	return bt.bld.NumBlobs()
}

// Finalize returns the built index.
func (bt *BuildTarget) Finalize() *Index {
	if bt.eag != nil {
		return bt.eag
	}
	return bt.bld.Finalize()
}
