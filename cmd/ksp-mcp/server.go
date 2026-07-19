package main

import (
	"errors"
	"fmt"
	"sync"

	"github.com/cpuchip/ksp-hmi/krpc"
)

// kspServer holds the (lazily established, auto-reconnecting) kRPC connection and
// implements each read-only tool as a plain method returning a typed output. The
// MCP handlers (tools.go) and the -smoke oracle (main.go) both call these methods,
// so there is exactly one implementation of each tool's behavior.
//
// Graceful degradation is baked into the outputs: "kRPC unreachable" and "no
// active vessel" are ANSWERS (Available:false + a spoken-friendly Message), not
// errors — the CAPCOM can relay them. Only unexpected protocol failures return a
// non-nil error (surfaced as an MCP tool error).
type kspServer struct {
	cfg krpc.DialConfig
	mu  sync.Mutex
	c   *krpc.Conn
}

func newKSPServer(cfg krpc.DialConfig) *kspServer { return &kspServer{cfg: cfg} }

// conn returns a live connection, dialing on first use or after a drop.
func (s *kspServer) conn() (*krpc.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		return s.c, nil
	}
	c, err := krpc.Dial(s.cfg)
	if err != nil {
		return nil, err
	}
	s.c = c
	return c, nil
}

// drop closes and forgets the connection so the next call redials — used after an
// unexpected read failure (e.g. the game closed mid-session).
func (s *kspServer) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		s.c.Close()
		s.c = nil
	}
}

// Close tears down the connection.
func (s *kspServer) Close() {
	s.drop()
}

// connectMsg is the spoken-friendly "can't reach the game" message.
func (s *kspServer) connectMsg(err error) string {
	return fmt.Sprintf("Can't reach kRPC at %s:%d — is KSP running with the kRPC server started "+
		"(open the kRPC window in-game and click \"Start server\")? [dial error: %v]",
		s.cfg.Host, s.cfg.RPCPort, err)
}

// noVesselMsg explains there is no active vessel, naming the current scene when
// it can be read.
func (s *kspServer) noVesselMsg(c *krpc.Conn) string {
	scene := ""
	if _, name, err := c.CurrentGameScene(); err == nil {
		scene = fmt.Sprintf(" (scene: %s)", name)
	}
	return "No active vessel" + scene + " — the game isn't in flight, so there's nothing to report " +
		"until a craft is launched or loaded. Ask about game_state to see the current scene."
}

// ---- shared output fields ----

type base struct {
	Available bool   `json:"available"`         // false = a graceful "can't answer yet" (see Message)
	Message   string `json:"message,omitempty"` // spoken-friendly explanation when Available is false
}

// resolveVessel returns the active vessel id, or a filled-in base explaining why
// it can't (kRPC down / no vessel). ok=false means return the base as the answer.
func (s *kspServer) resolveVessel() (c *krpc.Conn, vessel uint64, b base, ok bool, err error) {
	c, err = s.conn()
	if err != nil {
		return nil, 0, base{Available: false, Message: s.connectMsg(err)}, false, nil
	}
	vessel, verr := c.ActiveVessel()
	if errors.Is(verr, krpc.ErrNoVessel) {
		return c, 0, base{Available: false, Message: s.noVesselMsg(c)}, false, nil
	}
	if verr != nil {
		s.drop()
		return nil, 0, base{}, false, verr
	}
	return c, vessel, base{Available: true}, true, nil
}

// ---- vessel_status ----

type vesselStatusOut struct {
	base
	Name      string `json:"name,omitempty"`
	Situation string `json:"situation,omitempty"`
	Body      string `json:"body,omitempty"`
	// Numeric telemetry is NOT omitempty: a real zero (MET 0 at prelaunch) must
	// show, not vanish. When Available is false these read 0 and the base Message
	// tells the CAPCOM to disregard them.
	METSeconds float64 `json:"met_seconds"`
	MET        string  `json:"met,omitempty"`
}

func (s *kspServer) vesselStatus() (vesselStatusOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return vesselStatusOut{}, err
	}
	if !ok {
		return vesselStatusOut{base: b}, nil
	}
	st, err := c.VesselStatus(vessel)
	if err != nil {
		s.drop()
		return vesselStatusOut{}, err
	}
	return vesselStatusOut{
		base:       base{Available: true},
		Name:       st.Name,
		Situation:  st.Situation,
		Body:       st.Body,
		METSeconds: round2(st.METSeconds),
		MET:        fmtDuration(st.METSeconds),
	}, nil
}

// ---- orbit ----

type orbitOut struct {
	base
	Body string `json:"body,omitempty"`
	// Numeric orbital elements are NOT omitempty: eccentricity 0 (a perfect
	// circle) is a real, important reading that must not be dropped.
	ApoapsisAltitudeM    float64 `json:"apoapsis_altitude_m"`
	PeriapsisAltitudeM   float64 `json:"periapsis_altitude_m"`
	Eccentricity         float64 `json:"eccentricity"`
	InclinationDeg       float64 `json:"inclination_deg"`
	PeriodSeconds        float64 `json:"period_seconds"`
	Period               string  `json:"period,omitempty"`
	TimeToApoapsisSecond float64 `json:"time_to_apoapsis_seconds"`
	TimeToApoapsis       string  `json:"time_to_apoapsis,omitempty"`
	TimeToPeriapsisSec   float64 `json:"time_to_periapsis_seconds"`
	TimeToPeriapsis      string  `json:"time_to_periapsis,omitempty"`
	SemiMajorAxisM       float64 `json:"semi_major_axis_m"`
}

func (s *kspServer) orbit() (orbitOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return orbitOut{}, err
	}
	if !ok {
		return orbitOut{base: b}, nil
	}
	o, err := c.Orbit(vessel)
	if err != nil {
		s.drop()
		return orbitOut{}, err
	}
	return orbitOut{
		base:                 base{Available: true},
		Body:                 o.Body,
		ApoapsisAltitudeM:    round2(o.ApoapsisAltitude),
		PeriapsisAltitudeM:   round2(o.PeriapsisAltitude),
		Eccentricity:         round2(o.Eccentricity),
		InclinationDeg:       round2(o.InclinationDeg),
		PeriodSeconds:        round2(o.PeriodSeconds),
		Period:               fmtDuration(o.PeriodSeconds),
		TimeToApoapsisSecond: round2(o.TimeToApoapsis),
		TimeToApoapsis:       fmtDuration(o.TimeToApoapsis),
		TimeToPeriapsisSec:   round2(o.TimeToPeriapsis),
		TimeToPeriapsis:      fmtDuration(o.TimeToPeriapsis),
		SemiMajorAxisM:       round2(o.SemiMajorAxisMeter),
	}, nil
}

// ---- flight_telemetry ----

type flightOut struct {
	base
	// Numeric telemetry is NOT omitempty: zero vertical speed (level flight) and
	// zero mach (vacuum) are real readings a pilot needs, not "missing".
	AltitudeM        float64 `json:"altitude_m"`
	SurfaceAltitudeM float64 `json:"surface_altitude_m"`
	VerticalSpeedMS  float64 `json:"vertical_speed_ms"`
	HorizontalSpeed  float64 `json:"horizontal_speed_ms"`
	GForce           float64 `json:"g_force"`
	Mach             float64 `json:"mach"`
	PitchDeg         float64 `json:"pitch_deg"`
	HeadingDeg       float64 `json:"heading_deg"`
	RollDeg          float64 `json:"roll_deg"`
}

func (s *kspServer) flightTelemetry() (flightOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return flightOut{}, err
	}
	if !ok {
		return flightOut{base: b}, nil
	}
	ft, err := c.FlightTelemetry(vessel)
	if err != nil {
		s.drop()
		return flightOut{}, err
	}
	return flightOut{
		base:             base{Available: true},
		AltitudeM:        round2(ft.MeanAltitudeMeter),
		SurfaceAltitudeM: round2(ft.SurfaceAltitudeMeter),
		VerticalSpeedMS:  round2(ft.VerticalSpeed),
		HorizontalSpeed:  round2(ft.HorizontalSpeed),
		GForce:           round2(ft.GForce),
		Mach:             round2(ft.Mach),
		PitchDeg:         round2(ft.PitchDeg),
		HeadingDeg:       round2(ft.HeadingDeg),
		RollDeg:          round2(ft.RollDeg),
	}, nil
}

// ---- resources ----

type resourceOut struct {
	Name    string  `json:"name"`
	Amount  float64 `json:"amount"`
	Max     float64 `json:"max"`
	Percent float64 `json:"percent"`
}

type resourcesOut struct {
	base
	Total       []resourceOut `json:"total,omitempty"`
	Stage       []resourceOut `json:"stage,omitempty"`
	StageNumber int32         `json:"stage_number"` // not omitempty: stage 0 (the final stage) is real
	Note        string        `json:"note,omitempty"`
}

func toResourceOut(in []krpc.ResourceLevel) []resourceOut {
	out := make([]resourceOut, 0, len(in))
	for _, r := range in {
		out = append(out, resourceOut{
			Name:    r.Name,
			Amount:  round2(r.Amount),
			Max:     round2(r.Max),
			Percent: round2(r.Percent),
		})
	}
	return out
}

func (s *kspServer) resources() (resourcesOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return resourcesOut{}, err
	}
	if !ok {
		return resourcesOut{base: b}, nil
	}
	ri, err := c.Resources(vessel)
	if err != nil {
		s.drop()
		return resourcesOut{}, err
	}
	out := resourcesOut{
		base:        base{Available: true},
		Total:       toResourceOut(ri.Total),
		Stage:       toResourceOut(ri.Stage),
		StageNumber: ri.StageNumber,
	}
	if ri.StageErr != "" {
		out.Note = "stage resources unavailable: " + ri.StageErr + " (totals are still valid)"
	}
	return out, nil
}

// ---- maneuver_nodes ----

type nodeOut struct {
	DeltaVMS            float64 `json:"delta_v_ms"`
	RemainingDeltaVMS   float64 `json:"remaining_delta_v_ms"`
	TimeToSeconds       float64 `json:"time_to_seconds"`
	TimeTo              string  `json:"time_to"`
	UT                  float64 `json:"ut_seconds"`
	BurnEstimateSeconds float64 `json:"burn_estimate_seconds,omitempty"`
	BurnEstimate        string  `json:"burn_estimate,omitempty"`
	Note                string  `json:"note,omitempty"`
}

type nodesOut struct {
	base
	Count int       `json:"count"`
	Nodes []nodeOut `json:"nodes,omitempty"`
}

func (s *kspServer) maneuverNodes() (nodesOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return nodesOut{}, err
	}
	if !ok {
		return nodesOut{base: b}, nil
	}
	nodes, err := c.ManeuverNodes(vessel)
	if err != nil {
		s.drop()
		return nodesOut{}, err
	}
	out := nodesOut{base: base{Available: true}, Count: len(nodes)}
	for _, n := range nodes {
		no := nodeOut{
			DeltaVMS:          round2(n.DeltaV),
			RemainingDeltaVMS: round2(n.RemainingDeltaV),
			TimeToSeconds:     round2(n.TimeToSeconds),
			TimeTo:            fmtDuration(n.TimeToSeconds),
			UT:                round2(n.UT),
			Note:              n.BurnEstimateNote,
		}
		if n.BurnEstimateSeconds > 0 {
			no.BurnEstimateSeconds = round2(n.BurnEstimateSeconds)
			no.BurnEstimate = fmtDuration(n.BurnEstimateSeconds)
		}
		out.Nodes = append(out.Nodes, no)
	}
	return out, nil
}

// ---- crew ----

type crewOut struct {
	base
	Count int      `json:"count"`
	Names []string `json:"names,omitempty"`
}

func (s *kspServer) crew() (crewOut, error) {
	c, vessel, b, ok, err := s.resolveVessel()
	if err != nil {
		return crewOut{}, err
	}
	if !ok {
		return crewOut{base: b}, nil
	}
	names, err := c.CrewMembers(vessel)
	if err != nil {
		s.drop()
		return crewOut{}, err
	}
	return crewOut{base: base{Available: true}, Count: len(names), Names: names}, nil
}

// ---- game_state ----

type gameStateOut struct {
	Connected    bool   `json:"krpc_connected"`
	KRPCVersion  string `json:"krpc_version,omitempty"`
	Scene        string `json:"scene,omitempty"`
	Paused       bool   `json:"paused,omitempty"`
	ActiveVessel bool   `json:"active_vessel,omitempty"`
	Message      string `json:"message"`
}

// gameState is the honest "can I even answer" tool: it never returns an error,
// always reporting connection + scene + whether a vessel exists.
func (s *kspServer) gameState() gameStateOut {
	c, err := s.conn()
	if err != nil {
		return gameStateOut{Connected: false, Message: s.connectMsg(err)}
	}
	out := gameStateOut{Connected: true}
	if st, err := c.Status(); err == nil {
		out.KRPCVersion = st.Version
	} else {
		// A failed status read means the connection went stale; report it and drop.
		s.drop()
		return gameStateOut{Connected: false, Message: s.connectMsg(err)}
	}
	if _, name, err := c.CurrentGameScene(); err == nil {
		out.Scene = name
	}
	if p, err := c.Paused(); err == nil {
		out.Paused = p
	}
	_, verr := c.ActiveVessel()
	out.ActiveVessel = verr == nil
	if out.ActiveVessel {
		out.Message = fmt.Sprintf("Connected to kRPC %s. Scene: %s. An active vessel is present — the flight tools will answer.",
			out.KRPCVersion, out.Scene)
	} else {
		out.Message = fmt.Sprintf("Connected to kRPC %s. Scene: %s. No active vessel — flight tools will report 'not in flight' until a craft is launched or loaded.",
			out.KRPCVersion, out.Scene)
	}
	return out
}
