/*
runj-entrypoint is a small helper program for starting processes inside OCI
jails.  This program is used for ensuring that the jail process's STDIO is
hooked up to the right STDIO streams.

When used for the jail's init process, the STDIO streams should match that of
`runj create`.  In this scenario, this program is started when `runj create` is
invoked, but blocks until `runj start` is invoked.

Unfortunately, this program works through indirection that is not obvious.  When
`runj create` is run, it creates a fifo (see mkfifo(2)) and then starts this
program, passing the jail ID, the path to the fifo, and the program that should
be invoked as arguments.  This program then opens the fifo for writing, which
should block to wait for the right time to actually exec into the target
program.  `runj start` will open the fifo for reading, which unblocks this
program and the jail process can start.

The above procedure is skipped when secondary processes are started, since there
is no create/start split involved for these processes and the STDIO of `runj
extension exec` is used directly.

This program exec(2)s to jexec(8), which is then responsible for jail_attach(2)
and another exec(2) into the final target program.  The sequence of `exec(2)`
preserves the PID so that it can be the target of a future invocation of `runj
kill`.
*/
package main

// #cgo LDFLAGS: -ljail
// #include <stdlib.h>
// #include <sys/param.h>
// #include <sys/jail.h>
// #include <jail.h>
import "C"

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/containerd/console"
	"golang.org/x/sys/unix"
)

func main() {
	exit, err := _main()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(exit)
}

var usage = errors.New("usage: runj-entrypoint JAIL-ID FIFO-PATH PROGRAM [ARGS...]")

const (
	jexecPath        = "/usr/sbin/jexec"
	consoleSocketEnv = "__RUNJ_CONSOLE_SOCKET"

	// skipExecFifo signals that the exec fifo sync procedure should be skipped
	skipExecFifo = "-"
)

func _main() (int, error) {
	if len(os.Args) < 4 {
		return 1, usage
	}
	jid := os.Args[1]
	fifoPath := os.Args[2]
	command := os.Args[3]
	argv := os.Args[4:]

	if err := setupConsole(); err != nil {
		return 2, err
	}

	if fifoPath != skipExecFifo {
		// Block until `runj start` is invoked
		fifofd, err := unix.Open(fifoPath, unix.O_WRONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return 3, fmt.Errorf("failed to open fifo: %w", err)
		}
		if _, err := unix.Write(fifofd, []byte("0")); err != nil {
			return 4, fmt.Errorf("failed to write to fifo: %w", err)
		}
	}

	cJailName := C.CString(jid)
	cJailID := C.jail_getid(cJailName)
	C.free(unsafe.Pointer(cJailName))//deferring this would mean it never gets executed after execve!
	C.jail_attach(cJailID)
	cwd := os.Getenv("_RUNJENTRYPOINTCWD")
	os.Unsetenv("_RUNJENTRYPOINTCWD")
	if cwd == "" {
		cwd = "/"
	}
	err := os.Chdir(cwd)
	if err != nil {
		return 5, fmt.Errorf("could not change into: %s, %w", err)
	}

	cmdpath, err := exec.LookPath(command)
	if err != nil {
		return 6, fmt.Errorf("Could not find start command %w", err)
	}
	// call unix.Exec (which is execve(2)) to replace this process with the jexec
	if err := unix.Exec(cmdpath, append([]string{command}, argv...), unix.Environ()); err != nil {
		return 6, fmt.Errorf("failed to exec: %w", err)
	}
	return 0, nil
}

func setupConsole() error {
	socketFdArg := os.Getenv(consoleSocketEnv)
	if socketFdArg == "" {
		return nil
	}
	os.Unsetenv(consoleSocketEnv)
	socketFd, err := strconv.Atoi(socketFdArg)
	if err != nil {
		return fmt.Errorf("console: bad socket fd: %w", err)
	}
	socket := os.NewFile(uintptr(socketFd), "console-socket")
	// TODO clear env variable
	defer socket.Close()

	pty, slavePath, err := console.NewPty()
	if err != nil {
		return err
	}
	defer pty.Close()

	if err := SendFd(socket, pty.Name(), pty.Fd()); err != nil {
		return err
	}
	return dupStdio(slavePath)
}

// dupStdio opens the slavePath for the console and dups the fds to the current
// processes stdio, fd 0,1,2.
func dupStdio(slavePath string) error {
	fd, err := unix.Open(slavePath, unix.O_RDWR, 0)
	if err != nil {
		return &os.PathError{
			Op:   "open",
			Path: slavePath,
			Err:  err,
		}
	}

	if _, err := syscall.Setsid(); err != nil {
		return err
	}

	if ret, _, err := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCSCTTY, uintptr(0)); ret != 0 || err != 0 {
		return fmt.Errorf("Unable to set controlling tty")
	}
	for _, i := range []int{0, 1, 2} {
		if err := unix.Dup2(fd, i); err != nil {
			return err
		}
	}
	return nil
}
