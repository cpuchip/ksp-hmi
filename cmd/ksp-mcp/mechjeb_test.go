package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cpuchip/ksp-hmi/krpc"
)

// TestMechJebToolsDegradeWhenDown holds the seven MechJeb-backed planners to the
// same contract as every other tool: with kRPC unreachable, each returns a graceful
// Available:false answer — never a hard error, never a panic, and (being writes)
// attempting no mutation because they never reach a Call.
func TestMechJebToolsDegradeWhenDown(t *testing.T) {
	srv := newKSPServer(krpc.DialConfig{
		Host: "127.0.0.1", RPCPort: 59327, StreamPort: 0, Timeout: 400 * time.Millisecond,
	})
	defer srv.Close()

	if o, err := srv.planIntercept(interceptInput{}); err != nil || o.Available {
		t.Errorf("plan_intercept degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planRendezvous(rendezvousInput{}); err != nil || o.Available {
		t.Errorf("plan_rendezvous degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planMatchVelocity(); err != nil || o.Available {
		t.Errorf("plan_match_velocity degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planInterplanetary(interplanetaryInput{}); err != nil || o.Available {
		t.Errorf("plan_interplanetary degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planReturn(returnInput{}); err != nil || o.Available {
		t.Errorf("plan_return degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.planMatchPlanes(); err != nil || o.Available {
		t.Errorf("plan_match_planes degraded wrong: avail=%v err=%v", o.Available, err)
	}
	if o, err := srv.refineClosestApproach(refineInput{}); err != nil || o.Available {
		t.Errorf("refine_closest_approach degraded wrong: avail=%v err=%v", o.Available, err)
	}
}

// TestMechJebRefusalClassification verifies the honest error rendering: a bare
// NullReferenceException (the KRPC.MechJeb/MechJeb2 version-mismatch signature) is
// named as such and pointed at the fix, while a clean MechJeb reason is relayed
// verbatim, and an empty error still reads as "nothing changed".
func TestMechJebRefusalClassification(t *testing.T) {
	nre := mechjebRefusal("Object reference not set to an instance of an object")
	if !strings.Contains(nre, "version") {
		t.Errorf("NRE refusal should name the version mismatch, got: %q", nre)
	}
	if !isBindingNRE("Object reference not set to an instance of an object") {
		t.Error("isBindingNRE should recognize the NRE signature")
	}
	if isBindingNRE("must select a target") {
		t.Error("isBindingNRE should not match a clean MechJeb reason")
	}
	clean := mechjebRefusal("must select a target")
	if !strings.Contains(clean, "must select a target") {
		t.Errorf("clean refusal should relay MechJeb's reason, got: %q", clean)
	}
	empty := mechjebRefusal("")
	if !strings.Contains(empty, "nothing was changed") {
		t.Errorf("empty refusal should say nothing changed, got: %q", empty)
	}
}

func TestContains(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        bool
	}{
		{"Object reference not set to an instance", "reference not set", true},
		{"abc", "abc", true},
		{"abc", "", true},
		{"abc", "abcd", false},
		{"", "x", false},
	}
	for _, tc := range cases {
		if got := contains(tc.hay, tc.needle); got != tc.want {
			t.Errorf("contains(%q,%q)=%v want %v", tc.hay, tc.needle, got, tc.want)
		}
	}
}
