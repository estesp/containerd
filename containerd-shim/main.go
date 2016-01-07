package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/docker/containerd/util"
	"github.com/opencontainers/runc/libcontainer/utils"
)

const (
	bufferSize = 2048
)

type exit struct {
	pid    int
	status int
}

type stdio struct {
	stdin  *os.File
	stdout *os.File
	stderr *os.File
}

func (s *stdio) Close() error {
	err := s.stdin.Close()
	if oerr := s.stdout.Close(); err == nil {
		err = oerr
	}
	if oerr := s.stderr.Close(); err == nil {
		err = oerr
	}
	return err
}

// containerd-shim is a small shim that sits in front of a runc implementation
// that allows it to be repartented to init and handle reattach from the caller.
//
// the cwd of the shim should be the bundle for the container.  Arg1 should be the path
// to the state directory where the shim can locate fifos and other information.
//
//   └── shim
//    ├── control
//    ├── stderr
//    ├── stdin
//    ├── stdout
//    ├── pid
//    └── exit
func main() {
	if len(os.Args) < 2 {
		logrus.Fatal("shim: no arguments provided")
	}
	// start handling signals as soon as possible so that things are properly reaped
	// or if runc exits before we hit the handler
	signals := make(chan os.Signal, bufferSize)
	signal.Notify(signals)
	// set the shim as the subreaper for all orphaned processes created by the container
	if err := util.SetSubreaper(1); err != nil {
		logrus.WithField("error", err).Fatal("shim: set as subreaper")
	}
	// open the exit pipe
	f, err := os.OpenFile(filepath.Join(os.Args[1], "exit"), syscall.O_WRONLY, 0)
	if err != nil {
		logrus.WithField("error", err).Fatal("shim: open exit pipe")
	}
	defer f.Close()
	// open the fifos for use with the command
	std, err := openContainerSTDIO(os.Args[1])
	if err != nil {
		logrus.WithField("error", err).Fatal("shim: open container STDIO from fifo")
	}
	// star the container process by invoking runc
	runcPid, err := startRunc(std, os.Args[2])
	if err != nil {
		logrus.WithField("error", err).Fatal("shim: start runc")
	}
	var exitShim bool
	for s := range signals {
		logrus.WithField("signal", s).Debug("shim: received signal")
		switch s {
		case syscall.SIGCHLD:
			exits, err := reap()
			if err != nil {
				logrus.WithField("error", err).Error("shim: reaping child processes")
			}
			for _, e := range exits {
				// check to see if runc is one of the processes that has exited
				if e.pid == runcPid {
					exitShim = true
					logrus.WithFields(logrus.Fields{"pid": e.pid, "status": e.status}).Info("shim: runc exited")
					if err := writeInt(filepath.Join(os.Args[1], "exitStatus"), e.status); err != nil {
						logrus.WithFields(logrus.Fields{"error": err, "status": e.status}).Error("shim: write exit status")
					}
				}
			}
		}
		// runc has exited so the shim can also exit
		if exitShim {
			if err := std.Close(); err != nil {
				logrus.WithField("error", err).Error("shim: close stdio")
			}
			return
		}
	}
}

// startRunc starts runc and returns RUNC's pid
func startRunc(s *stdio, id string) (int, error) {
	cmd := exec.Command("runc", "--id", id, "start")
	cmd.Stdin = s.stdin
	cmd.Stdout = s.stdout
	cmd.Stderr = s.stderr
	// set the parent death signal to SIGKILL so that if the shim dies the container
	// process also dies
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	return cmd.Process.Pid, nil
}

// openContainerSTDIO opens the pre-created fifo's for use with the container
// in RDWR so that they remain open if the other side stops listening
func openContainerSTDIO(dir string) (*stdio, error) {
	s := &stdio{}
	for name, dest := range map[string]**os.File{
		"stdin":  &s.stdin,
		"stdout": &s.stdout,
		"stderr": &s.stderr,
	} {
		f, err := os.OpenFile(filepath.Join(dir, name), syscall.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		*dest = f
	}
	return s, nil
}

func writeInt(path string, i int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", i)
	return err
}

// reap performs a wait4 on all child processes of the shim to collect their pid
// and exit status
func reap() (exits []exit, err error) {
	var (
		ws  syscall.WaitStatus
		rus syscall.Rusage
	)
	for {
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, &rus)
		if err != nil {
			if err == syscall.ECHILD {
				return exits, nil
			}
			return exits, err
		}
		if pid <= 0 {
			return exits, nil
		}
		exits = append(exits, exit{
			pid:    pid,
			status: utils.ExitStatus(ws),
		})
	}
}
