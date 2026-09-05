//go:build linux

package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	socketDirectory = "/run/capsule"
	socketPath      = socketDirectory + "/capsule.sock"
	workerUID       = 1000
	childUID        = 1001
	childGID        = 1001
	socketGID       = 2000
	cleanupGrace    = time.Second
	cleanupLimit    = 4 * time.Second
	stderrLimit     = 64 * 1024
	prSetSubreaper  = 36
)

var stopping atomic.Bool

type discardBounded struct{ remaining int }

func (writer *discardBounded) Write(value []byte) (int, error) {
	if len(value) < writer.remaining {
		writer.remaining -= len(value)
	} else {
		writer.remaining = 0
	}
	return len(value), nil
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credential *syscall.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		credential, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	return credential.Uid, nil
}

func statMatches(path string, mode uint32, uid, gid uint32, socket bool) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if socket && info.Mode()&os.ModeSocket == 0 {
		return false
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint32(status.Mode)&07777 == mode && status.Uid == uid && status.Gid == gid
}

func directoryReady() bool {
	return statMatches(socketDirectory, 02750, 0, socketGID, false)
}

func socketReady() bool {
	return statMatches(socketPath, 0660, 0, socketGID, true)
}

func uidProcesses(uid int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		file, err := os.Open(filepath.Join("/proc", entry.Name(), "status"))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		matched := false
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 && fields[0] == "Uid:" {
				matched = fields[1] == strconv.Itoa(uid)
				break
			}
		}
		_ = file.Close()
		if matched {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func signalUID(uid int, signal syscall.Signal) {
	pids, _ := uidProcesses(uid)
	for _, pid := range pids {
		_ = syscall.Kill(pid, signal)
	}
}

func reapAdopted() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || (err != nil && !errors.Is(err, syscall.ECHILD)) {
			return
		}
	}
}

func cleanupProcessTree(command *exec.Cmd, waitDone <-chan struct{}) bool {
	pid := command.Process.Pid
	deadline := time.Now().Add(cleanupLimit)
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	signalUID(childUID, syscall.SIGTERM)
	select {
	case <-waitDone:
	case <-time.After(cleanupGrace):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		signalUID(childUID, syscall.SIGKILL)
		select {
		case <-waitDone:
		case <-time.After(time.Until(deadline)):
			return false
		}
	}

	for time.Now().Before(deadline) {
		reapAdopted()
		pids, err := uidProcesses(childUID)
		if err != nil {
			return false
		}
		if len(pids) == 0 {
			return true
		}
		for _, remaining := range pids {
			_ = syscall.Kill(remaining, syscall.SIGKILL)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func runAttempt(connection *net.UnixConn, child string, shutdown <-chan struct{}) bool {
	command := exec.Command(child)
	if child == "/usr/local/bin/node" {
		command.Args = []string{
			child,
			"/opt/capsule/dist/src/main.js",
		}
	}
	command.Env = []string{
		"HOME=/nonexistent",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"TMPDIR=/nonexistent",
	}
	command.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
		Credential: &syscall.Credential{
			Uid:         childUID,
			Gid:         childGID,
			Groups:      []uint32{},
			NoSetGroups: false,
		},
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return true
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return true
	}
	command.Stderr = &discardBounded{remaining: stderrLimit}
	if err := command.Start(); err != nil {
		return true
	}

	inputDone := make(chan struct{})
	waitDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdin, connection)
		_ = stdin.Close()
		close(inputDone)
	}()
	go func() {
		_, _ = io.Copy(connection, stdout)
		_ = connection.CloseWrite()
	}()
	go func() {
		_ = command.Wait()
		close(waitDone)
	}()

	select {
	case <-inputDone:
	case <-waitDone:
	case <-shutdown:
	}
	return cleanupProcessTree(command, waitDone)
}

func main() {
	child := "/usr/local/bin/node"
	if len(os.Args) == 2 && os.Args[1] == "--check" {
		if directoryReady() && socketReady() {
			return
		}
		os.Exit(1)
	}
	if len(os.Args) == 3 && os.Args[1] == "--child" {
		child = os.Args[2]
	}
	if !directoryReady() {
		panic("capsule socket directory ownership or mode is invalid")
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		panic(errno)
	}
	_ = os.Remove(socketPath)
	if err := syscall.Setegid(socketGID); err != nil {
		panic(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if restoreErr := syscall.Setegid(0); restoreErr != nil {
		panic(restoreErr)
	}
	if err != nil {
		panic(err)
	}
	if err := os.Chmod(socketPath, 0660); err != nil || !socketReady() {
		panic("capsule socket ownership or mode is invalid")
	}

	shutdown := make(chan struct{})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signals
		stopping.Store(true)
		close(shutdown)
		_ = listener.Close()
	}()

	for !stopping.Load() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if stopping.Load() {
				break
			}
			continue
		}
		uid, credentialErr := peerUID(connection)
		if credentialErr != nil || uid != workerUID {
			_ = connection.Close()
			continue
		}
		clean := runAttempt(connection, child, shutdown)
		_ = connection.Close()
		if !clean {
			os.Exit(1)
		}
	}
	if pids, err := uidProcesses(childUID); err != nil || len(pids) != 0 {
		os.Exit(1)
	}
}
