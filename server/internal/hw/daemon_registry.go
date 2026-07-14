package hw

// The daemon registry: the ONE place a ghost.*d daemon is declared. Everything else derives from this
// , the services.conf Daemons map (name->health port), the Critical list, ghost.watchd's start order,
// and the setup seedVolume copy list. Before this, that roster was hand-maintained in three separate
// places (services_config, watchd main, seedVolume) and a miss was silent: a daemon seeded but never
// supervised, or supervised but never seeded. Now adding a daemon is one line here.
//
// Order MATTERS: the slice order is the start order watchd uses (a map cannot carry it, Go map
// iteration is random). shadowd is first because it is the critical answer/deniability path and should
// be up before the rest.

// DaemonSpec declares one daemon once.
type DaemonSpec struct {
	Name       string // e.g. "ghost.shadowd"
	HealthPort int    // loopback health port; 0 for a seed-only entry with no health server
	Critical   bool   // failure surfaced as critical (watchd + Ghost Status)
	Supervised bool   // watchd supervises it (in the Daemons map + start order). false = seeded only.
	Seeded     bool   // its binary is copied to <mount>/bin at provision.
}

// daemonRegistry is the authoritative, ordered roster. Slice order == watchd start order.
//
// ghost.watchd is Seeded but NOT Supervised: it IS the supervisor, started by secd directly, so it is
// copied to the volume but never appears in the map it supervises. It has no health port of its own in
// this list (secd health-checks it over the control socket, not an HTTP port).
//
// ghost.cued is the environment-reading cueing daemon (the four mechanisms from the "Before You Ask"
// Hard Truth); it runs its full loop against a stubbed ghost.synthd, so it stays silent until synthd's
// retrieval is built , running-but-blind, not a stub that pretends to think.
var daemonRegistry = []DaemonSpec{
	{Name: "ghost.watchd", HealthPort: 0, Critical: false, Supervised: false, Seeded: true},
	{Name: "ghost.shadowd", HealthPort: 9110, Critical: true, Supervised: true, Seeded: true},
	{Name: "ghost.framed", HealthPort: 9112, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.noted", HealthPort: 9113, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.synthd", HealthPort: 9114, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.tallyd", HealthPort: 9115, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.voiced", HealthPort: 9116, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.cued", HealthPort: 9117, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.oracled", HealthPort: 9118, Critical: false, Supervised: true, Seeded: true},
	{Name: "ghost.searchd", HealthPort: 9119, Critical: false, Supervised: true, Seeded: true},
}

// SupervisedDaemons returns the name->health-port map watchd supervises, derived from the registry.
// This is what fills ServicesConfig.Daemons.
func SupervisedDaemons() map[string]int {
	m := make(map[string]int)
	for _, d := range daemonRegistry {
		if d.Supervised {
			m[d.Name] = d.HealthPort
		}
	}
	return m
}

// CriticalDaemons returns the names flagged critical, derived from the registry.
func CriticalDaemons() []string {
	var out []string
	for _, d := range daemonRegistry {
		if d.Critical {
			out = append(out, d.Name)
		}
	}
	return out
}

// StartOrder returns the supervised daemon names in the order watchd should start them (registry
// order). watchd registers these first, then any extra Daemons found in the config that are not in the
// list (forward-compat with a hand-edited services.conf).
func StartOrder() []string {
	var out []string
	for _, d := range daemonRegistry {
		if d.Supervised {
			out = append(out, d.Name)
		}
	}
	return out
}

// ExtraSeeded are non-daemon binaries that also live on the volume and die with the mount. The
// inference engine is the canonical member: everything except secd belongs on the encrypted volume,
// and llama-server doubly so , the engine binary reveals what the box runs. Seeded-if-present, never
// required (a box without inference still archives).
var ExtraSeeded = []string{"llama-server"}

// SeededBinaries returns every binary name to copy to <mount>/bin at provision , the supervised
// cohort plus seed-only entries like ghost.watchd, plus non-daemon extras (the inference engine).
func SeededBinaries() []string {
	var out []string
	for _, d := range daemonRegistry {
		if d.Seeded {
			out = append(out, d.Name)
		}
	}
	out = append(out, ExtraSeeded...)
	return out
}

// RequiredBinaries are the ones whose absence at provision is fatal (the box cannot function without
// them). Derived: the supervisor and the one critical daemon.
func RequiredBinaries() map[string]bool {
	req := map[string]bool{"ghost.watchd": true}
	for _, d := range daemonRegistry {
		if d.Critical {
			req[d.Name] = true
		}
	}
	return req
}

// StatsTargets is the canonical set of observability targets , every name the SYSTEM HEALTH
// sampler (ghost.watchd) writes ring buffers for, and the exact set secd's /v1/services/* endpoints
// will answer about. One list, two consumers, no drift.
func StatsTargets() map[string]bool {
	names := map[string]bool{
		"postgres": true, "redis": true,
		"host.cpu": true, "host.mem": true, "host.gpu": true, "host.disk": true, "volume": true,
	}
	for name, port := range SupervisedDaemons() {
		if port != 0 {
			names[name] = true
		}
	}
	return names
}
