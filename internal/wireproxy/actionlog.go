package wireproxy

import (
	"io"
	"os"
)

// actionLogs makes the two files an action writes to. Pipes would be owned by the panel: when it exits their read ends
// close and the action's next write is a SIGPIPE, which would cut a package manager in half.
func actionLogs() (stdout, stderr *os.File, err error) {
	if stdout, err = os.CreateTemp("", "warpwp-stdout-*"); err != nil {
		return nil, nil, err
	}
	if stderr, err = os.CreateTemp("", "warpwp-stderr-*"); err != nil {
		closeActionLog(stdout)
		return nil, nil, err
	}
	return stdout, stderr, nil
}

// closeActionLog unlinks the file and drops this process's handle; an action still running keeps writing to the inode.
func closeActionLog(f *os.File) {
	_ = os.Remove(f.Name())
	_ = f.Close()
}

// readActionLog reads what the action wrote so far, from the start and bounded, without moving the shared file offset.
func readActionLog(f *os.File) []byte {
	out, err := io.ReadAll(io.NewSectionReader(f, 0, 1<<20))
	if err != nil {
		return nil
	}
	return out
}
