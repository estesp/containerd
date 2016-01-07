package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	ExitFile       = "exit"
	ExitStatusFile = "exitStatus"
	StateFile      = "state.json"
	InitProcessID  = "init"
)

type state struct {
	Bundle string `json:"bundle"`
}

// New returns a new container
func New(root, id, bundle string) (Container, error) {
	c := &container{
		root:      root,
		id:        id,
		bundle:    bundle,
		processes: make(map[string]*process),
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
	root      string
	id        string
	bundle    string
	processes map[string]*process
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
	cmd := exec.Command("containerd-shim", processRoot, c.id)
	cmd.Dir = c.bundle
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p, err := newProcess(processRoot, InitProcessID, c)
	if err != nil {
		return nil, err
	}
	c.processes[InitProcessID] = p
	return p, nil
}

func (c *container) Pause() error {
	return errNotImplemented
}

func (c *container) Resume() error {
	return errNotImplemented
}

func (c *container) Delete() error {
	return os.RemoveAll(filepath.Join(c.root, c.id))
}
