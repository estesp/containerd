package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	LockFile       = "waitlock"
	ExitStatusFile = "exitStatus"
	StateFile      = "state.json"
)

type state struct {
	Bundle string `json:"bundle"`
}

// New returns a new container
func New(root, id, bundle string) (Container, error) {
	c := &container{
		root:   root,
		id:     id,
		bundle: bundle,
	}
	if err := os.Mkdir(filepath.Join(root, id), 0755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(root, id, StateFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(state{
		Bundle: bundle,
	}); err != nil {
		return nil, err
	}
	return c, nil
}

func Load(root, id string) (Container, error) {
	return nil, nil
}

type container struct {
	// path to store runtime state information
	root   string
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
	processRoot := filepath.Join(c.root, c.id, "proc")
	if err := os.MkdirAll(processRoot, 0755); err != nil {
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
