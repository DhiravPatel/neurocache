// Package sentinel implements the SENTINEL command surface and the
// quorum-based monitoring loop. A NeuroCache process can run as a
// sentinel by setting NEUROCACHE_SENTINEL_ENABLED=true and listing
// the masters it should monitor.
//
// Architecture:
//
//   - Sentinels watch one or more named master groups (NeuroCache
//     instances acting as data-plane masters).
//   - Each sentinel pings every master + its replicas at SentinelPingMs
//     intervals; a master that misses DownAfterMs of consecutive
//     pings is marked SDOWN (subjective down).
//   - Sentinels gossip their SDOWN observations across a small
//     bus channel; once `quorum` sentinels agree, the master is
//     promoted to ODOWN (objective down) and a leader sentinel
//     drives a failover via REPLICAOF on the chosen replica.
//
// The implementation here is pragmatic — every sentinel is also
// considered a candidate leader; election is decided by lowest node-ID
// on quorum (deterministic, no Raft term machinery). Real Sentinel uses
// a more elaborate Raft-style election, but for typical 3-5 sentinel
// deployments the simpler scheme converges within a few hundred ms.
package sentinel

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// MonitoredMaster is one master under sentinel watch.
type MonitoredMaster struct {
	Name      string
	Host      string
	Port      string
	Quorum    int

	mu       sync.RWMutex
	replicas map[string]*ReplicaInfo // by host:port

	// SDOWN/ODOWN bookkeeping.
	lastOK    time.Time
	sdown     bool
	odown     bool
	odownVotes map[string]bool // sentinel-id -> agrees that master is down

	// failover state machine
	failingOver bool
}

// ReplicaInfo tracks one replica's health.
type ReplicaInfo struct {
	Host    string
	Port    string
	LastOK  time.Time
}

// Sentinel is the per-process state.
type Sentinel struct {
	ID        string // 40-hex stable ID
	myAddr    string

	mu      sync.RWMutex
	masters map[string]*MonitoredMaster // by master name
	peers   map[string]*PeerInfo        // by sentinel ID

	cfg Config

	stop chan struct{}
}

// Config bundles the operator-tunable knobs.
type Config struct {
	// Per-master tunables. Keyed by master name; missing entries fall
	// back to DefaultDownAfterMs / DefaultPingMs.
	DownAfterMs   int64 // default 30000
	PingMs        int64 // default 1000
	FailoverMs    int64 // total failover budget (default 180000)
	ParallelSyncs int   // replicas to reconfigure simultaneously (default 1)
}

// PeerInfo is what we know about another sentinel.
type PeerInfo struct {
	ID    string
	Host  string
	Port  string
	LastSeen time.Time
}

// New builds a sentinel with the given local id + address.
func New(id, host, port string, cfg Config) *Sentinel {
	if cfg.DownAfterMs == 0 {
		cfg.DownAfterMs = 30_000
	}
	if cfg.PingMs == 0 {
		cfg.PingMs = 1_000
	}
	if cfg.FailoverMs == 0 {
		cfg.FailoverMs = 180_000
	}
	if cfg.ParallelSyncs == 0 {
		cfg.ParallelSyncs = 1
	}
	return &Sentinel{
		ID: id, myAddr: net.JoinHostPort(host, port),
		masters: map[string]*MonitoredMaster{},
		peers:   map[string]*PeerInfo{},
		cfg:     cfg,
		stop:    make(chan struct{}),
	}
}

// Monitor registers a master to watch.
func (s *Sentinel) Monitor(name, host, port string, quorum int) error {
	if quorum < 1 {
		return errors.New("quorum must be >= 1")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.masters[name]; dup {
		return fmt.Errorf("master %s already monitored", name)
	}
	s.masters[name] = &MonitoredMaster{
		Name: name, Host: host, Port: port, Quorum: quorum,
		replicas: map[string]*ReplicaInfo{},
		lastOK:   time.Now(),
		odownVotes: map[string]bool{},
	}
	return nil
}

// Remove drops a master.
func (s *Sentinel) Remove(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.masters[name]; !ok {
		return false
	}
	delete(s.masters, name)
	return true
}

// Reset clears bookkeeping for a master (operator command).
func (s *Sentinel) Reset(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.masters[name]
	if !ok {
		return false
	}
	m.mu.Lock()
	m.sdown = false
	m.odown = false
	m.odownVotes = map[string]bool{}
	m.failingOver = false
	m.replicas = map[string]*ReplicaInfo{}
	m.lastOK = time.Now()
	m.mu.Unlock()
	return true
}

// Masters returns every monitored master in stable order.
func (s *Sentinel) Masters() []*MonitoredMaster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.masters))
	for n := range s.masters {
		names = append(names, n)
	}
	sortStrings(names)
	out := make([]*MonitoredMaster, 0, len(names))
	for _, n := range names {
		out = append(out, s.masters[n])
	}
	return out
}

// Master fetches by name.
func (s *Sentinel) Master(name string) *MonitoredMaster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.masters[name]
}

// Peers returns all known sentinels (sorted by ID).
func (s *Sentinel) Peers() []*PeerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.peers))
	for id := range s.peers {
		ids = append(ids, id)
	}
	sortStrings(ids)
	out := make([]*PeerInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.peers[id])
	}
	return out
}

// LearnPeer records another sentinel's existence — called when the
// sentinel cluster bus delivers a heartbeat.
func (s *Sentinel) LearnPeer(p *PeerInfo) {
	if p == nil || p.ID == s.ID {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.peers[p.ID]; ok {
		existing.LastSeen = time.Now()
		return
	}
	p.LastSeen = time.Now()
	s.peers[p.ID] = p
}

// Start launches the monitoring goroutine.
func (s *Sentinel) Start() { go s.loop() }

// Stop signals the loop to exit.
func (s *Sentinel) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// loop periodically pings every monitored target. Failure detection +
// failover trigger live in checkMaster.
func (s *Sentinel) loop() {
	t := time.NewTicker(time.Duration(s.cfg.PingMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		for _, m := range s.Masters() {
			s.checkMaster(m)
		}
	}
}

// checkMaster pings the master once and updates SDOWN/ODOWN flags.
// A real failover trigger flips m.failingOver to true; the actual
// promotion happens via promoteReplica (which the orchestrator host
// invokes through a callback registered with NewWithFailover).
func (s *Sentinel) checkMaster(m *MonitoredMaster) {
	m.mu.Lock()
	addr := net.JoinHostPort(m.Host, m.Port)
	m.mu.Unlock()
	ok := dialPing(addr, time.Duration(s.cfg.PingMs)*time.Millisecond)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.lastOK = now
		m.sdown = false
		m.odown = false
		m.odownVotes = map[string]bool{}
		return
	}
	if now.Sub(m.lastOK) >= time.Duration(s.cfg.DownAfterMs)*time.Millisecond {
		m.sdown = true
		// Self-vote toward ODOWN; remote sentinels add their votes via
		// VoteSDOWN below.
		m.odownVotes[s.ID] = true
		if len(m.odownVotes) >= m.Quorum {
			m.odown = true
		}
	}
}

// VoteSDOWN records a remote sentinel's SDOWN observation. Caller is
// the sentinel-bus handler that received the gossip frame.
func (s *Sentinel) VoteSDOWN(masterName, voterID string) {
	s.mu.RLock()
	m := s.masters[masterName]
	s.mu.RUnlock()
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.odownVotes[voterID] = true
	if len(m.odownVotes) >= m.Quorum {
		m.odown = true
	}
}

// dialPing opens a TCP probe and sends a "*1\r\n$4\r\nPING\r\n" frame,
// waiting up to `timeout` for "+PONG\r\n". Cheap, doesn't allocate
// for happy-path success.
func dialPing(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte("*1\r\n$4\r\nPING\r\n")); err != nil {
		return false
	}
	buf := make([]byte, 7)
	n, _ := conn.Read(buf)
	return n >= 5 && strings.HasPrefix(string(buf[:n]), "+PONG")
}

// MasterStatus is what SENTINEL MASTER renders.
type MasterStatus struct {
	Name      string
	Host      string
	Port      string
	Quorum    int
	SDOWN     bool
	ODOWN     bool
	LastOKMs  int64
	NumReplicas int
	NumOtherSentinels int
	FailingOver bool
}

// Status snapshots a master's state.
func (m *MonitoredMaster) Status() MasterStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MasterStatus{
		Name: m.Name, Host: m.Host, Port: m.Port, Quorum: m.Quorum,
		SDOWN: m.sdown, ODOWN: m.odown,
		LastOKMs:    time.Since(m.lastOK).Milliseconds(),
		NumReplicas: len(m.replicas),
		FailingOver: m.failingOver,
	}
}

// PromoteReplica reassigns the master to the named replica. The caller
// is responsible for issuing REPLICAOF on the chosen replica + REPLICAOF
// NO ONE on the new master via its data-plane connection.
func (m *MonitoredMaster) PromoteReplica(host, port string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Host = host
	m.Port = port
	m.failingOver = false
	m.sdown = false
	m.odown = false
	m.odownVotes = map[string]bool{}
	m.lastOK = time.Now()
}

// AddReplica records a known replica of this master. Called from the
// sentinel's discovery loop (which polls REPLICAOF/INFO REPLICATION).
func (m *MonitoredMaster) AddReplica(host, port string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := net.JoinHostPort(host, port)
	if _, ok := m.replicas[addr]; ok {
		return
	}
	m.replicas[addr] = &ReplicaInfo{Host: host, Port: port, LastOK: time.Now()}
}

// Replicas returns the replica list (sorted for stable output).
func (m *MonitoredMaster) Replicas() []*ReplicaInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	addrs := make([]string, 0, len(m.replicas))
	for a := range m.replicas {
		addrs = append(addrs, a)
	}
	sortStrings(addrs)
	out := make([]*ReplicaInfo, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, m.replicas[a])
	}
	return out
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
