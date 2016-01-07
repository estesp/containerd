package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	LockFile       = "waitlock"
	ExitStatusFile = "exitStatus"
)

// New returns a new container
func New(id, bundle string) (Container, error) {
	return &container{
		id:     id,
		bundle: bundle,
	}, nil
}

type container struct {
	id     string
	bundle string
}

func (c *container) ID() string {
	return c.id
}

func (c *container) Path() string {
	return c.bundle
}

func (c *container) Start() (Process, error) {
	processRoot := filepath.Join(c.bundle, "proc")
	if err := os.Mkdir(processRoot, 0755); err != nil {
		return nil, err
	}
	lock, err := c.createLock(processRoot)
	if err != nil {
		return nil, err
	}
	// TODO: this may not work and it may release the lock
	defer lock.Close()
	cmd := exec.Command("containerd-shim", processRoot, c.id)
	cmd.Dir = c.bundle
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.ExtraFiles = []*os.File{
		lock,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p, err := newProcess(processRoot)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (c *container) Pause() error {
	return errNotImplemented
}

func (c *container) Resume() error {
	return errNotImplemented
}

func (c *container) Delete() error {
	return nil
}

func (c *container) createLock(root string) (*os.File, error) {
	lock, err := os.Create(filepath.Join(root, LockFile))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}
