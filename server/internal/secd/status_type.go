package secd

// ServiceStatus is secd's view of one supervised daemon, as shown on /v1/status. secd no longer
// supervises the daemons itself (ghost.watchd does); this type is populated from watchd's snapshot
// fetched over the control socket (see backend.SupervisorStatus). Kept in the secd package because
// it is part of the /v1/status response shape the app consumes.
type ServiceStatus struct {
	Name     string `json:"name"`
	Critical bool   `json:"critical"`
	State    string `json:"state"`
	Restarts int    `json:"restarts"`
	LastErr  string `json:"lastErr,omitempty"`
	Code     uint8  `json:"code"`
	// Live health, fetched from the daemon's own /health at status time. watchd says whether the
	// process RUNS; the daemon itself says whether it is WELL ("model loading", "backlog: 2314
	// frames", "pg unreachable"). Both truths belong on the status screen.
	Detail string `json:"detail,omitempty"`
}
