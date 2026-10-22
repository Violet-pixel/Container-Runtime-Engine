package store

import "time"

type State string

const (
	StateCreated  State = "created"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateExited   State = "exited"
)

type ImageConfig struct {
	Env        []string          `json:"env"`
	Entrypoint []string          `json:"entrypoint"`
	Cmd        []string          `json:"cmd"`
	WorkingDir string            `json:"workingDir"`
	User       string            `json:"user"`
	Labels     map[string]string `json:"labels,omitempty"`
	Arch       string            `json:"architecture"`
	OS         string            `json:"os"`
}

type Image struct {
	ID        string      `json:"id"`
	Reference string      `json:"reference"`
	Digest    string      `json:"digest"`
	Source    string      `json:"source"`
	SizeBytes int64       `json:"sizeBytes"`
	Layers    int         `json:"layers"`
	Config    ImageConfig `json:"config"`
	Rootfs    string      `json:"rootfs"`
	CreatedAt time.Time   `json:"createdAt"`
}

type PortMapping struct {
	HostIP        string `json:"hostIp"`
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

type Resources struct {
	CPUs      float64 `json:"cpus"`
	CPUWeight int     `json:"cpuWeight"`
	MemoryMB  int64   `json:"memoryMb"`
	PidsLimit int64   `json:"pidsLimit"`
}

type RestartPolicy struct {
	Name       string `json:"name"`
	MaxRetries int    `json:"maxRetries"`
}

type Spec struct {
	Image          string            `json:"image"`
	Entrypoint     []string          `json:"entrypoint,omitempty"`
	Cmd            []string          `json:"cmd,omitempty"`
	Env            []string          `json:"env,omitempty"`
	WorkingDir     string            `json:"workingDir,omitempty"`
	Hostname       string            `json:"hostname,omitempty"`
	User           string            `json:"user,omitempty"`
	Privileged     bool              `json:"privileged"`
	Network        string            `json:"network"`
	Ports          []PortMapping     `json:"ports,omitempty"`
	Mounts         []Mount           `json:"mounts,omitempty"`
	Resources      Resources         `json:"resources"`
	Restart        RestartPolicy     `json:"restart"`
	ReadOnlyRootfs bool              `json:"readOnlyRootfs"`
	AutoRemove     bool              `json:"autoRemove"`
	Labels         map[string]string `json:"labels,omitempty"`
	DNS            []string          `json:"dns,omitempty"`
}

type Container struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	ImageID       string     `json:"imageId"`
	ImageRef      string     `json:"image"`
	Spec          Spec       `json:"spec"`
	Command       []string   `json:"command"`
	State         State      `json:"state"`
	Pid           int        `json:"pid"`
	PidStart      uint64     `json:"-"`
	ShimPid       int        `json:"shimPid"`
	IP            string     `json:"ip"`
	Storage       string     `json:"storage"`
	ExitCode      int        `json:"exitCode"`
	Error         string     `json:"error"`
	RestartCount  int        `json:"restartCount"`
	StopRequested bool       `json:"-"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
}

type Event struct {
	ID         int64             `json:"id"`
	Time       time.Time         `json:"time"`
	Type       string            `json:"type"`
	Action     string            `json:"action"`
	ActorID    string            `json:"actorId"`
	ActorName  string            `json:"actorName"`
	Attributes map[string]string `json:"attributes,omitempty"`
}
