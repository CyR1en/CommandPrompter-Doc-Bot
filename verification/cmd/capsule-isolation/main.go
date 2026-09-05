// capsule-isolation verifies the two-slot supervisor boundary from the worker UID.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	socketA = "/run/slot-a/capsule.sock"
	socketB = "/run/slot-b/capsule.sock"
)

type childReport struct {
	PID                  int    `json:"pid"`
	UID                  int    `json:"uid"`
	GID                  int    `json:"gid"`
	Groups               int    `json:"groups"`
	CapEff               uint64 `json:"cap_eff"`
	NoNewPrivileges      int    `json:"no_new_privs"`
	PID1UID              uint64 `json:"pid1_uid"`
	PID1CapEff           uint64 `json:"pid1_cap_eff"`
	PID1NoNewPrivileges  uint64 `json:"pid1_no_new_privs"`
	KillPID1Result       int    `json:"kill_pid1_result"`
	KillPID1Errno        int    `json:"kill_pid1_errno"`
	SocketStatErrno      int    `json:"socket_stat_errno"`
	SocketWriteErrno     int    `json:"socket_write_errno"`
	SocketUnlinkErrno    int    `json:"socket_unlink_errno"`
	SocketConnectErrno   int    `json:"socket_connect_errno"`
	RootWriteErrno       int    `json:"root_write_errno"`
	TmpWriteErrno        int    `json:"tmp_write_errno"`
	DevShmWriteErrno     int    `json:"dev_shm_write_errno"`
	HomeExists           bool   `json:"home_exists"`
	TmpdirExists         bool   `json:"tmpdir_exists"`
	EnvironmentCount     int    `json:"environment_count"`
	SensitiveEnvironment int    `json:"sensitive_environment"`
	ForbiddenPaths       int    `json:"forbidden_paths_present"`
	InitialUIDProcesses  int    `json:"initial_uid_processes"`
	InitialZombies       int    `json:"initial_zombies"`
	EscapedUIDProcesses  int    `json:"escaped_uid_processes"`
	EscapedZombies       int    `json:"escaped_zombies"`
	DoubleForkCreated    bool   `json:"setsid_double_fork_created"`
}

type childConnection struct {
	connection *net.UnixConn
	reader     *bufio.Reader
	report     childReport
}

func fail(format string, arguments ...any) { panic(fmt.Sprintf(format, arguments...)) }

func verifySocket(directory, socket string) {
	for path, mode := range map[string]uint32{directory: 0o2750, socket: 0o660} {
		info, err := os.Stat(path)
		if err != nil {
			fail("stat %s: %v", path, err)
		}
		status, ok := info.Sys().(*syscall.Stat_t)
		if !ok || uint32(status.Mode)&0o7777 != mode || status.Uid != 0 || status.Gid != 2000 {
			fail("unexpected ownership or mode for %s", path)
		}
	}
}

func connect(path string) childConnection {
	deadline := time.Now().Add(30 * time.Second)
	for {
		connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err == nil {
			_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
			reader := bufio.NewReader(connection)
			line, readErr := reader.ReadBytes('\n')
			if readErr != nil {
				fail("read child report from %s: %v", path, readErr)
			}
			var report childReport
			if json.Unmarshal(line, &report) != nil {
				fail("decode child report from %s", path)
			}
			return childConnection{connection, reader, report}
		}
		if time.Now().After(deadline) {
			fail("connect %s: %v", path, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertChild(report childReport) {
	if report.UID != 1001 || report.GID != 1001 || report.Groups != 0 || report.CapEff != 0 || report.NoNewPrivileges != 1 ||
		report.PID1UID != 0 || report.PID1CapEff != (1<<5)|(1<<6)|(1<<7) || report.PID1NoNewPrivileges != 1 ||
		report.KillPID1Result != -1 || report.KillPID1Errno != int(syscall.EPERM) {
		fail("child identity or privilege boundary failed: %+v", report)
	}
	for name, value := range map[string]int{
		"socket_stat": report.SocketStatErrno, "socket_write": report.SocketWriteErrno,
		"socket_unlink": report.SocketUnlinkErrno, "socket_connect": report.SocketConnectErrno,
	} {
		if value != int(syscall.EACCES) {
			fail("%s errno=%d", name, value)
		}
	}
	for name, value := range map[string]int{
		"root_write": report.RootWriteErrno, "tmp_write": report.TmpWriteErrno, "dev_shm_write": report.DevShmWriteErrno,
	} {
		if value != int(syscall.EACCES) && value != int(syscall.EROFS) {
			fail("%s errno=%d", name, value)
		}
	}
	if report.HomeExists || report.TmpdirExists || report.EnvironmentCount != 3 || report.SensitiveEnvironment != 0 ||
		report.ForbiddenPaths != 0 || report.InitialUIDProcesses != 1 || report.InitialZombies != 0 ||
		!report.DoubleForkCreated || report.EscapedUIDProcesses < 3 || report.EscapedZombies < 1 {
		fail("child filesystem, environment, or process boundary failed: %+v", report)
	}
}

func queuedConnection(path string) *net.UnixConn {
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		fail("queue slot attempt: %v", err)
	}
	return connection
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		if _, err := os.Stat("/tmp/verified"); err != nil {
			panic(err)
		}
		return
	}
	if len(os.Args) != 1 {
		panic("usage: capsule-isolation [--healthcheck]")
	}
	verifySocket("/run/slot-a", socketA)
	verifySocket("/run/slot-b", socketB)
	firstA, firstB := connect(socketA), connect(socketB)
	assertChild(firstA.report)
	assertChild(firstB.report)

	queuedA := queuedConnection(socketA)
	_ = queuedA.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var one [1]byte
	if count, err := queuedA.Read(one[:]); count != 0 || err == nil || !isTimeout(err) {
		fail("slot A started a queued child before cleanup: count=%d err=%v", count, err)
	}
	_ = firstA.connection.Close()
	_ = firstB.connection.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := firstB.connection.Write([]byte("ping\n")); err != nil {
		fail("ping slot B: %v", err)
	}
	var pong struct {
		Pong bool `json:"pong"`
		PID  int  `json:"pid"`
	}
	line, err := firstB.reader.ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &pong) != nil || !pong.Pong || pong.PID != firstB.report.PID {
		fail("slot B did not survive slot A cancellation")
	}

	_ = queuedA.SetReadDeadline(time.Now().Add(10 * time.Second))
	queuedReader := bufio.NewReader(queuedA)
	secondLine, err := queuedReader.ReadBytes('\n')
	if err != nil {
		fail("read second slot A child: %v", err)
	}
	var secondA childReport
	if json.Unmarshal(secondLine, &secondA) != nil {
		fail("decode second slot A child")
	}
	assertChild(secondA)
	if secondA.PID == firstA.report.PID || secondA.InitialUIDProcesses != 1 {
		fail("slot A did not reopen after complete process cleanup")
	}
	_ = queuedA.Close()
	result := map[string]any{
		"status": "ok", "slot_a_first_pid": firstA.report.PID, "slot_a_second_pid": secondA.PID,
		"slot_b_pid": firstB.report.PID, "slot_b_survived_slot_a_cancel": true,
		"slot_b_left_active_for_supervisor_shutdown": true, "slot_a_reopened_after_zero_uid_processes": true,
		"slot_a_second_attempt_queued_until_cleanup": true,
	}
	encoded, _ := json.Marshal(result)
	fmt.Println(string(encoded))
	if err := os.WriteFile("/tmp/verified", nil, 0o600); err != nil {
		panic(err)
	}
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	<-shutdown
	signal.Stop(shutdown)
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
