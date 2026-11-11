package main

import (
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
)

// On SIGINT/SIGTERM/SIGHUP se-env stops its own child processes (the
// age-plugin-se instance waiting for Touch ID) before exiting, so no orphaned
// Touch ID prompt is left on screen. Locks need no cleanup: the vault lock is
// released by the kernel and a session lock by the closed connection.
// `session` does not use this: it forwards signals to the command it runs.

func stopChildren() {
	_ = exec.Command("/usr/bin/pkill", "-TERM", "-P", strconv.Itoa(os.Getpid())).Run()
}

func installInterruptHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s := <-sigs
		stopChildren()
		code := 1
		if sig, ok := s.(syscall.Signal); ok {
			code = 128 + int(sig)
		}
		os.Exit(code)
	}()
}
