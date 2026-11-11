package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Client side of a session (see agent.go). KC_ENV_SESSION holds only the
// session ID. It is not a secret: the agent decides who belongs to the
// session by looking at the connecting process, not at what it sends.

type session struct {
	id, path string
	info     sessionInfo
}

type agentConn struct {
	c   net.Conn
	enc *json.Encoder
	dec *json.Decoder
}

var errNotAgent = errors.New("socket is not served by this session's agent")

// dialAgent connects to an agent socket. The listening process must run as
// this user. With ownAgent it must also be an ancestor of this process, which
// the agent of this process's session always is: a look-alike socket put in
// its place by another program is never asked for, or handed, a profile.
func dialAgent(path string, ownAgent bool) (*agentConn, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, errNotAgent
	}
	uid, pid, err := peerOf(uc)
	if err != nil || uid != uint32(os.Getuid()) || (ownAgent && !descendsFrom(os.Getpid(), pid)) {
		c.Close()
		return nil, errNotAgent
	}
	return &agentConn{c: c, enc: json.NewEncoder(c), dec: json.NewDecoder(c)}, nil
}

// call sends one request. wait bounds the answer; 0 waits without limit
// (another process may be showing Touch ID for the same profile).
func (ac *agentConn) call(rq request, wait time.Duration) (response, error) {
	var rs response
	if wait > 0 {
		ac.c.SetDeadline(time.Now().Add(wait))
	} else {
		ac.c.SetDeadline(time.Time{})
	}
	if err := ac.enc.Encode(rq); err != nil {
		return rs, err
	}
	err := ac.dec.Decode(&rs)
	return rs, err
}

// ask opens a connection for a single request (see dialAgent for ownAgent).
// Answers from an agent that is not this process's own are only ever used
// for display and are cleaned first.
func ask(path string, rq request, ownAgent bool) (response, error) {
	ac, err := dialAgent(path, ownAgent)
	if err != nil {
		return response{}, err
	}
	defer ac.c.Close()
	rs, err := ac.call(rq, idleTimeout)
	if rs.Info != nil {
		cleanInfo(rs.Info)
	}
	return rs, err
}

// cleanInfo makes session metadata safe to print.
func cleanInfo(si *sessionInfo) {
	si.Cmd = printable(si.Cmd)
	var only []string
	for _, p := range si.Only {
		if reProfile.MatchString(p) {
			only = append(only, p)
		}
	}
	si.Only = only
	var cached []cachedInfo
	for _, c := range si.Cached {
		if reProfile.MatchString(c.Name) {
			cached = append(cached, c)
		}
	}
	si.Cached = cached
}

// activeSession returns the session this process belongs to, or nil. The
// agent, not the environment, has the final say (sessionInfo.Member).
func (a *app) activeSession() *session {
	if a.sessChecked {
		return a.sess
	}
	a.sessChecked = true
	id := os.Getenv("KC_ENV_SESSION")
	if !reSession.MatchString(id) {
		return nil
	}
	path := a.sockPath(id)
	rs, err := ask(path, request{Op: "status"}, true)
	if err != nil || !rs.OK || rs.Info == nil || rs.Info.ID != id || !rs.Info.Member {
		return nil
	}
	a.sess = &session{id: id, path: path, info: *rs.Info}
	return a.sess
}

// lease is the answer to "acquire": either the cached profile (hit) or the
// profile's lock, held until put or close.
type lease struct {
	ac      *agentConn
	profile string
	hit     bool
	sum     string // with a hit: SHA-256 of the vault file the data came from
	data    []byte
}

// acquire asks the session for a profile. nil means the session does not
// cover it (outside --only, expired, gone): decrypt as if there were none.
func (s *session) acquire(p string) *lease {
	ac, err := dialAgent(s.path, true)
	if err != nil {
		return nil
	}
	rs, err := ac.call(request{Op: "acquire", Profile: p}, 0)
	if err != nil || !rs.OK {
		ac.c.Close()
		return nil
	}
	return &lease{ac: ac, profile: p, hit: rs.Hit, sum: rs.Sum, data: rs.Data}
}

// put hands a freshly decrypted profile to the session and releases the lock.
func (l *lease) put(sum string, plain []byte) {
	_, _ = l.ac.call(request{Op: "put", Profile: l.profile, Sum: sum, Data: plain}, idleTimeout)
}

func (l *lease) close() {
	wipe(l.data)
	l.ac.c.Close() // also releases the lock if put was never sent
}

// refresh replaces a profile the session already holds (after a save).
func (s *session) refresh(p, sum string, plain []byte) {
	_, _ = ask(s.path, request{Op: "put", Profile: p, Sum: sum, Data: plain}, true)
}

func (s *session) forget(p string) { _, _ = ask(s.path, request{Op: "forget", Profile: p}, true) }

// ---- all sessions of this user (for `cache list` / `cache rm`) ----------------

// sessions asks every live agent for its metadata. Sockets whose agent is
// gone (killed without cleaning up) are removed.
func (a *app) sessions() []sessionInfo {
	m, _ := filepath.Glob(filepath.Join(a.runDir, "*.sock"))
	var out []sessionInfo
	for _, path := range m {
		id := strings.TrimSuffix(filepath.Base(path), ".sock")
		if !reSession.MatchString(id) {
			continue
		}
		rs, err := ask(path, request{Op: "status"}, false)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				os.Remove(path)
			}
			continue
		}
		if rs.OK && rs.Info != nil && rs.Info.ID == id {
			out = append(out, *rs.Info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// endSession tells an agent to wipe its cache and stop.
func (a *app) endSession(id string) { _, _ = ask(a.sockPath(id), request{Op: "end"}, false) }

// ---- presentation helpers ------------------------------------------------------

func (a *app) scopeText(si sessionInfo) string {
	if len(si.Only) == 0 {
		return a.t.S("word_all")
	}
	return strings.Join(si.Only, ", ")
}

func (a *app) cachedText(si sessionInfo, none string) string {
	var parts []string
	for _, c := range si.Cached {
		parts = append(parts, c.Name+"("+time.Unix(c.At, 0).Format("15:04")+")")
	}
	if len(parts) == 0 {
		return a.t.S(none)
	}
	return strings.Join(parts, ", ")
}

func (a *app) remaining(exp int64) string {
	left := exp - time.Now().Unix()
	if left <= 0 {
		return a.t.S("word_expired")
	}
	return a.t.N("remaining_minutes", int((left+59)/60))
}
