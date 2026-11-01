package observe

import (
	"io"
	"os"
	"syscall"
)

// System bounds file reads and abstracts statfs without exposing OS-specific
// syscall structs to fakes. Implementations must reject reads exceeding limit.
// These synchronous local reads do not promise cancellation of a blocked kernel
// filesystem operation; operators should allowlist local filesystems only.
type System interface {
	ReadFile(path string, limit int) ([]byte, error)
	StatFS(path string) (FSStats, error)
}

type FSStats struct {
	BlockSize       uint64
	Blocks          uint64
	FreeBlocks      uint64
	AvailableBlocks uint64
	Files           uint64
	FreeFiles       uint64
}

type localSystem struct{}

func (localSystem) ReadFile(path string, limit int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // Only fixed OS sources and validated interface names reach this method.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errTruncated
	}
	return data, nil
}

func (localSystem) StatFS(path string) (FSStats, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return FSStats{}, err
	}
	if s.Bsize <= 0 {
		return FSStats{}, errInvalid
	}
	return FSStats{BlockSize: uint64(s.Bsize), Blocks: s.Blocks, FreeBlocks: s.Bfree,
		AvailableBlocks: s.Bavail, Files: s.Files, FreeFiles: s.Ffree}, nil
}
