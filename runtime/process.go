package runtime

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func newProcess(root string) (*process, error) {
	p := &process{
		root: root,
	}
	// create fifo's for the process
	for name, fd := range map[string]**os.File{
		"stdin":  &p.stdin,
		"stdout": &p.stdout,
		"stderr": &p.stderr,
	} {
		path := filepath.Join(root, name)
		if err := syscall.Mkfifo(path, 0755); err != nil && !os.IsExist(err) {
			return nil, err
		}
		f, err := os.OpenFile(path, syscall.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		*fd = f
	}
	return p, nil
}

type process struct {
	root string
	// stdio fifos
	stdin  *os.File
	stdout *os.File
	stderr *os.File
}

// Wait opens the file lock as a shared lock and blocks until the process exits
// and returns the exit status
func (p *process) Wait() (int, error) {
	fd, err := syscall.Open(filepath.Join(p.root, LockFile), syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_SH); err != nil {
		return -1, err
	}
	data, err := ioutil.ReadFile(filepath.Join(p.root, ExitStatusFile))
	if err != nil {
		return -1, err
	}
	i, err := strconv.Atoi(string(data))
	if err != nil {
		return -1, err
	}
	return i, nil
}

// Signal sends the provided signal to the process
func (p *process) Signal(s os.Signal) error {
	return errNotImplemented
}

func (p *process) Stdin() *os.File {
	return p.stdin
}

func (p *process) Stdout() *os.File {
	return p.stdout
}

func (p *process) Stderr() *os.File {
	return p.stderr
}

// Close closes any open files and/or resouces on the process
func (p *process) Close() error {
	err := p.stdin.Close()
	if oerr := p.stdout.Close(); err == nil {
		err = oerr
	}
	if oerr := p.stderr.Close(); err == nil {
		err = oerr
	}
	return err
}
