package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// A session keeps decrypted profiles in the memory of the `se-env session`
// process (the agent) and nowhere else: nothing is written to disk and no key
// is put in the environment. se-env processes started under the session's
// command ask the agent over a Unix socket. The agent answers only peers that
//   - run as the same user, and
//   - descend from the session's command (checked through the kernel's
//     process table, so it cannot be claimed by setting a variable).
// Lifetime (TTL) and scope (--only) are held and enforced by the agent.

const (
	maxMessage   = 32 << 20 // bytes per connection
	idleTimeout  = 10 * time.Second
	touchTimeout = 5 * time.Minute // a client holding a profile lock while Touch ID is shown
	sockPathMax  = 103             // sun_path on macOS is 104 bytes including the NUL
)

type request struct {
	Op      string `json:"op"` // status | acquire | put | forget | lock | end
	Profile string `json:"profile,omitempty"`
	Sum     string `json:"sum,omitempty"`  // SHA-256 of the vault file the data came from
	Data    []byte `json:"data,omitempty"` // decrypted profile
}

type response struct {
	OK   bool         `json:"ok"`
	Hit  bool         `json:"hit,omitempty"`
	Sum  string       `json:"sum,omitempty"` // with a hit: SHA-256 of the vault file the data came from
	Data []byte       `json:"data,omitempty"`
	Info *sessionInfo `json:"info,omitempty"`
}

type cachedInfo struct {
	Name string `json:"name"`
	At   int64  `json:"at"`
}

type sessionInfo struct {
	ID     string       `json:"id"`
	Cmd    string       `json:"cmd"`
	Pid    int          `json:"pid"` // the agent (se-env session) process
	Start  int64        `json:"start"`
	Exp    int64        `json:"exp"`
	Only   []string     `json:"only,omitempty"`
	Cached []cachedInfo `json:"cached,omitempty"`
	Member bool         `json:"member"` // the asking process belongs to this session
}

type cacheEntry struct {
	sum  string
	data []byte
	at   time.Time
}

type agent struct {
	a          *app
	id, path   string
	ln         *net.UnixListener
	root       int // pid of the session's command
	only       []string
	cmd        string
	start, exp time.Time // exp carries no monotonic reading: sleep counts towards the TTL

	mu      sync.Mutex
	entries map[string]*cacheEntry
	locks   map[string]chan struct{}
	closed  bool
	done    chan struct{}
}

func (a *app) sockPath(id string) string { return filepath.Join(a.runDir, id+".sock") }

// newAgent creates the listening socket under a temporary name. Nothing can
// reach it until serve() publishes it.
func (a *app) newAgent(ttl time.Duration, only []string, cmdName string) (*agent, error) {
	if err := os.MkdirAll(a.runDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(a.runDir, 0o700); err != nil {
		return nil, err
	}
	os.RemoveAll(filepath.Join(a.runDir, "sessions")) // on-disk caches of se-env 0.4 and earlier
	id := randomHex(8)
	ag := &agent{a: a, id: id, path: a.sockPath(id), only: only, cmd: cmdName,
		entries: map[string]*cacheEntry{}, locks: map[string]chan struct{}{}, done: make(chan struct{})}
	if len(ag.path) > sockPathMax {
		return nil, a.fail("err_socket_path", "Path", ag.path)
	}
	tmp := filepath.Join(a.runDir, "."+id+".tmp")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, err
	}
	ln.SetUnlinkOnClose(false)
	ag.ln = ln
	if err := os.Chmod(tmp, 0o600); err != nil {
		ln.Close()
		os.Remove(tmp)
		return nil, err
	}
	now := time.Now()
	ag.start, ag.exp = now, now.Add(ttl).Round(0)
	return ag, nil
}

func (ag *agent) tmpPath() string { return filepath.Join(ag.a.runDir, "."+ag.id+".tmp") }

// serve starts answering for the process tree rooted at pid and publishes the
// socket. The accept loop runs before the socket has its final name, so no
// connection ever waits unexamined.
func (ag *agent) serve(root int) error {
	ag.root = root
	go ag.acceptLoop()
	go ag.expiryLoop()
	if err := os.Rename(ag.tmpPath(), ag.path); err != nil {
		ag.shutdown()
		return err
	}
	return nil
}

func (ag *agent) acceptLoop() {
	for {
		c, err := ag.ln.AcceptUnix()
		if err != nil {
			return
		}
		go ag.handle(c, time.Now())
	}
}

func (ag *agent) expiryLoop() {
	for {
		left := time.Until(ag.exp)
		if left <= 0 {
			ag.shutdown()
			return
		}
		select {
		case <-ag.done:
			return
		case <-time.After(min(left, 5*time.Second)): // short steps: timers pause while the Mac sleeps
		}
	}
}

// shutdown ends the session: the cache is wiped and the socket disappears.
// The command keeps running; se-env inside it asks for Touch ID again.
func (ag *agent) shutdown() {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.closed {
		return
	}
	ag.closed = true
	ag.wipeAllLocked()
	close(ag.done)
	ag.ln.Close()
	os.Remove(ag.path)
	os.Remove(ag.tmpPath())
}

func (ag *agent) wipeAllLocked() {
	for p, e := range ag.entries {
		wipe(e.data)
		delete(ag.entries, p)
	}
}

func (ag *agent) over() bool {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return ag.closed || !time.Now().Before(ag.exp)
}

func (ag *agent) allows(p string) bool {
	if !reProfile.MatchString(p) {
		return false
	}
	if len(ag.only) == 0 {
		return true
	}
	for _, o := range ag.only {
		if o == p {
			return true
		}
	}
	return false
}

// lock takes the per-profile lock; false if the session ended while waiting.
func (ag *agent) lock(p string) bool {
	ag.mu.Lock()
	ch := ag.locks[p]
	if ch == nil {
		ch = make(chan struct{}, 1)
		ag.locks[p] = ch
	}
	ag.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return true
	case <-ag.done:
		return false
	}
}

func (ag *agent) unlock(p string) {
	ag.mu.Lock()
	ch := ag.locks[p]
	ag.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
		}
	}
}

func (ag *agent) info(member bool) *sessionInfo {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	si := &sessionInfo{ID: ag.id, Cmd: ag.cmd, Pid: os.Getpid(), Start: ag.start.Unix(), Exp: ag.exp.Unix(),
		Only: ag.only, Member: member}
	for p, e := range ag.entries {
		si.Cached = append(si.Cached, cachedInfo{Name: p, At: e.at.Unix()})
	}
	sort.Slice(si.Cached, func(i, j int) bool { return si.Cached[i].Name < si.Cached[j].Name })
	return si
}

// peerOf returns the uid and pid of the process that connected.
func peerOf(c *net.UnixConn) (uid uint32, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cerr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		if cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); cerr != nil {
			return
		}
		uid = cred.Uid
		pid, cerr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err == nil {
		err = cerr
	}
	return uid, pid, err
}

func procInfo(pid int) (ppid int, started time.Time, err error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, time.Time{}, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, time.Time{}, errors.New("no such process")
	}
	st := kp.Proc.P_starttime
	return int(kp.Eproc.Ppid), time.Unix(int64(st.Sec), int64(st.Usec)*1000), nil
}

// descendsFrom reports whether pid is root or one of its descendants. A
// process whose ancestors have exited is re-parented to launchd and so no
// longer counts.
func descendsFrom(pid, root int) bool {
	for i := 0; i < 256 && pid > 1; i++ {
		if pid == root {
			return true
		}
		ppid, _, err := procInfo(pid)
		if err != nil {
			return false
		}
		pid = ppid
	}
	return false
}

// isMember decides whether the connecting process belongs to this session.
func (ag *agent) isMember(pid int, accepted time.Time) bool {
	_, started, err := procInfo(pid)
	if err != nil {
		return false
	}
	// The pid was read from the socket; a process that began after the
	// connection was accepted is a different process that reused the pid.
	if started.After(accepted) {
		return false
	}
	return descendsFrom(pid, ag.root)
}

func (ag *agent) handle(c *net.UnixConn, accepted time.Time) {
	defer c.Close()
	uid, pid, err := peerOf(c)
	if err != nil || uid != uint32(os.Getuid()) {
		return
	}
	member := ag.isMember(pid, accepted)
	dec := json.NewDecoder(io.LimitReader(c, maxMessage))
	enc := json.NewEncoder(c)
	held := "" // profile whose lock this connection holds after a miss
	defer func() {
		if held != "" {
			ag.unlock(held)
		}
	}()
	reply := func(r response) bool {
		c.SetWriteDeadline(time.Now().Add(idleTimeout))
		err := enc.Encode(r)
		wipe(r.Data)
		return err == nil
	}
	for {
		wait := idleTimeout
		if held != "" {
			wait = touchTimeout
		}
		c.SetReadDeadline(time.Now().Add(wait))
		var rq request
		if dec.Decode(&rq) != nil {
			return
		}
		c.SetReadDeadline(time.Time{})
		var rs response
		switch rq.Op {
		case "status": // metadata only; any process of this user may ask
			rs = response{OK: true, Info: ag.info(member)}
		case "lock", "end": // dropping the cache only ever makes se-env ask again
			ag.mu.Lock()
			ag.wipeAllLocked()
			ag.mu.Unlock()
			if rq.Op == "end" {
				ag.shutdown()
			}
			rs.OK = true
		case "acquire":
			if held != "" || !member || !ag.allows(rq.Profile) || ag.over() || !ag.lock(rq.Profile) {
				break
			}
			if data, sum, ok := ag.lookup(rq.Profile); ok {
				ag.unlock(rq.Profile)
				rs = response{OK: true, Hit: true, Sum: sum, Data: data}
			} else {
				held = rq.Profile
				rs.OK = true
			}
		case "put":
			if !member || !ag.allows(rq.Profile) || len(rq.Sum) != 64 || ag.over() {
				break
			}
			if held == rq.Profile { // after a miss: this client decrypted the profile
				ag.store(rq.Profile, rq.Sum, rq.Data, false)
				ag.unlock(held)
				held, rs.OK = "", true
			} else if held == "" && ag.lock(rq.Profile) { // after a save: refresh only
				rs.OK = ag.store(rq.Profile, rq.Sum, rq.Data, true)
				ag.unlock(rq.Profile)
			}
		case "forget":
			if member && held == "" && ag.allows(rq.Profile) && ag.lock(rq.Profile) {
				ag.drop(rq.Profile)
				ag.unlock(rq.Profile)
				rs.OK = true
			}
		}
		wipe(rq.Data)
		if !reply(rs) {
			return
		}
	}
}

// lookup returns a copy of the cached profile if it still matches the vault
// file. A stale entry is wiped at once.
func (ag *agent) lookup(p string) (data []byte, sum string, ok bool) {
	sum, err := fileSHA256(ag.a.profileFile(p))
	ag.mu.Lock()
	defer ag.mu.Unlock()
	e := ag.entries[p]
	if e == nil {
		return nil, "", false
	}
	if err != nil || e.sum != sum {
		wipe(e.data)
		delete(ag.entries, p)
		return nil, "", false
	}
	return append([]byte(nil), e.data...), sum, true
}

// store keeps a profile. With refreshOnly it replaces an existing entry and
// never adds one: a profile enters the cache only after a decryption.
func (ag *agent) store(p, sum string, data []byte, refreshOnly bool) bool {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	old := ag.entries[p]
	if ag.closed || (refreshOnly && old == nil) {
		return false
	}
	if old != nil {
		wipe(old.data)
	}
	ag.entries[p] = &cacheEntry{sum: sum, data: append([]byte(nil), data...), at: time.Now()}
	return true
}

func (ag *agent) drop(p string) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if e := ag.entries[p]; e != nil {
		wipe(e.data)
		delete(ag.entries, p)
	}
}
