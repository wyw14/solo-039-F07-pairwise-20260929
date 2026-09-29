package model

import "github.com/google/uuid"

type Phase string

const (
	Preparing     Phase = "preparing"
	Warming       Phase = "warming"
	Stabilizing   Phase = "stabilizing"
	OnSpec        Phase = "on-spec"
	Transitioning Phase = "transitioning"
	Shutting      Phase = "shutting"
	Stopped       Phase = "stopped"
)

type Campaign struct {
	ID       uuid.UUID
	Grade    string
	Revision uuid.UUID
	Phase    Phase
	Epoch    uint64
	Pressure float64
	Level    float64
	Fencing  uint64
}

type Sample struct {
	ID           uuid.UUID
	FeedRevision uuid.UUID
	Calibration  uuid.UUID
	At           int64
	Component    string
	Value        float64
	Valid        bool
}

type Proof struct {
	ID          uuid.UUID
	Revision    uuid.UUID
	Source      string
	Digest      string
	Durable     bool
	Independent bool
	At          int64
}

type ProductPlan struct {
	Campaign uuid.UUID
	Revision uuid.UUID
	Grade    string
	Target   string
}

type Lease struct {
	ID       uuid.UUID
	Owner    string
	Capacity float64
	Fence    uint64
	Active   bool
}

type Event struct {
	ID       uuid.UUID
	Kind     string
	Campaign uuid.UUID
	Revision uuid.UUID
	Epoch    uint64
	Payload  string
	At       int64
}

type Snapshot struct {
	Campaign Campaign
	Column   Column
	Reflux   Reflux
	Reboiler Reboiler
	Products []string
	Relief   Relief
}

type Column struct {
	ID           string
	Pressure     float64
	Temperature  float64
	Level        float64
	FeedRevision uuid.UUID
	Phase        Phase
	Fencing      uint64
}

type Reflux struct {
	Ratio        float64
	PumpCapacity float64
	Lease        Lease
	CommandEpoch uint64
}

type Reboiler struct {
	Steam        float64
	Closed       bool
	CommandEpoch uint64
	Trip         uint64
}

type Relief struct {
	Valve            string
	Pressure         float64
	PressureObserved bool
	TimedOut         bool
	Released         bool
}
