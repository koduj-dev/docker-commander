package docker

// Domain types returned to the frontend. They are intentionally decoupled from
// the raw Docker SDK structs so the API surface stays stable across SDK bumps.

// ContainerSummary is a compact view used in lists.
type ContainerSummary struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Image    string            `json:"image"`
	State    string            `json:"state"`  // running, exited, paused, ...
	Status   string            `json:"status"` // human text, e.g. "Up 3 hours"
	Created  int64             `json:"created"`
	Ports    []PortMapping     `json:"ports"`
	Networks []string          `json:"networks"`
	Labels   map[string]string `json:"labels"`
}

// PortMapping describes one published port.
type PortMapping struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort uint16 `json:"privatePort"`
	PublicPort  uint16 `json:"publicPort,omitempty"`
	Type        string `json:"type"`
}

// ContainerDetail is the full inspect view shown on the detail page.
type ContainerDetail struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	State        string            `json:"state"`
	Status       string            `json:"status"`
	Health       string            `json:"health,omitempty"`
	Created      string            `json:"created"`
	StartedAt    string            `json:"startedAt,omitempty"`
	RestartCount int               `json:"restartCount"`
	Command      []string          `json:"command"`
	Env          []string          `json:"env"`
	Labels       map[string]string `json:"labels"`
	Mounts       []MountInfo       `json:"mounts"`
	Ports        []PortMapping     `json:"ports"`
	Networks     []NetworkAttach   `json:"networks"`
	RestartPolicy string           `json:"restartPolicy,omitempty"`
}

// MountInfo describes a volume or bind mount.
type MountInfo struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

// NetworkAttach links a container to a network with its assigned address.
type NetworkAttach struct {
	Name       string `json:"name"`
	NetworkID  string `json:"networkId"`
	IPAddress  string `json:"ipAddress"`
	Gateway    string `json:"gateway"`
	MacAddress string `json:"macAddress"`
}

// NetworkSummary describes a Docker network for the networks/topology views.
type NetworkSummary struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Scope      string   `json:"scope"`
	Internal   bool     `json:"internal"`
	Subnets    []string `json:"subnets"`
	Containers []string `json:"containers"` // container IDs attached
}

// StatsSample is one point in a container's real-time resource time series.
type StatsSample struct {
	ContainerID string  `json:"containerId"`
	Timestamp   int64   `json:"timestamp"` // unix millis
	CPUPercent  float64 `json:"cpuPercent"`
	MemUsage    uint64  `json:"memUsage"`
	MemLimit    uint64  `json:"memLimit"`
	MemPercent  float64 `json:"memPercent"`
	NetRx       uint64  `json:"netRx"`
	NetTx       uint64  `json:"netTx"`
	BlkRead     uint64  `json:"blkRead"`
	BlkWrite    uint64  `json:"blkWrite"`
	PIDs        uint64  `json:"pids"`
}

// SystemInfo summarises the Docker host itself.
type SystemInfo struct {
	HostName          string `json:"hostName"`
	ServerVersion     string `json:"serverVersion"`
	OperatingSystem   string `json:"operatingSystem"`
	Architecture      string `json:"architecture"`
	CPUs              int    `json:"cpus"`
	MemTotal          int64  `json:"memTotal"`
	ContainersRunning int    `json:"containersRunning"`
	ContainersStopped int    `json:"containersStopped"`
	Images            int    `json:"images"`
}
