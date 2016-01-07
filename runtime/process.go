package runtime

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func newProcess(root, id string, c *container) (*process, error) {
	p := &process{
		root:      root,
		id:        id,
		container: c,
	}
	// create fifo's for the process
	for name, fd := range map[string]*string{
		"stdin":  &p.stdin,
		"stdout": &p.stdout,
		"stderr": &p.stderr,
	} {
		path := filepath.Join(root, name)
		if err := syscall.Mkfifo(path, 0755); err != nil && !os.IsExist(err) {
			return nil, err
		}
		*fd = path
	}
	exit, err := createExitPipe(filepath.Join(root, ExitFile))
	if err != nil {
		return nil, err
	}
	p.exitPipe = exit
	return p, nil
}

func createExitPipe(path string) (*os.File, error) {
	if err := syscall.Mkfifo(path, 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, syscall.O_RDONLY, 0)
}

type process struct {
	root string
	id   string
	// stdio fifos
	stdin  string
	stdout string
	stderr string

	exitPipe  *os.File
	container *container
}

func (p *process) ID() string {
	return p.id
}

func (p *process) Container() Container {
	return p.container
}

// ExitFD returns the fd of the exit pipe
func (p *process) ExitFD() int {
	return int(p.exitPipe.Fd())
}

func (p *process) ExitStatus() (int, error) {
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

func (p *process) Stdin() string {
	return p.stdin
}

func (p *process) Stdout() string {
	return p.stdout
}

func (p *process) Stderr() string {
	return p.stderr
}

// Close closes any open files and/or resouces on the process
func (p *process) Close() error {
	return p.exitPipe.Close()
}
