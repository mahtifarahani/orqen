package learnbench

import "sort"

// Arms.
const (
	ArmOn  = "on"
	ArmOff = "off"
)

// reworkTokens is the fixed token cost the harness charges per rework (a harness constant, not a measurement).
const reworkTokens = 500

// PacketFn builds the packet of a task from what has been learned so far in this arm.
type PacketFn func(t Task, learned []string) Packet

// DefaultPacket gives the knowledge arm every learned fact and the off arm nothing.
func DefaultPacket(arm string) PacketFn {
	return func(_ Task, learned []string) Packet {
		if arm != ArmOn {
			return Packet{}
		}
		facts := append([]string{}, learned...)
		sort.Strings(facts)
		return Packet{Facts: facts, Bytes: 64 * len(facts)}
	}
}

type TaskResult struct {
	TaskID  string   `json:"task_id"`
	Reworks int      `json:"reworks"`
	Missing []string `json:"missing"`
	Packet  int      `json:"packet_bytes"`
	First   bool     `json:"first_pass"`
}
type Metrics struct {
	ReworkRate            float64 `json:"rework_rate"`
	RepeatedIssueCount    int     `json:"repeated_issue_count"`
	SourcesNeeded         int     `json:"sources_needed"`
	EstTokens             int     `json:"est_tokens"`
	ReviewerFirstPassRate float64 `json:"reviewer_first_pass_rate"`
}
type ArmResult struct {
	Arm     string       `json:"arm"`
	Metrics Metrics      `json:"metrics"`
	PerTask []TaskResult `json:"per_task"`
}

// Run runs the suite sequentially in one arm. With the knowledge arm each task's learning step adds what it teaches.
func Run(s Suite, e Engine, arm string, packetFn PacketFn) ArmResult {
	if packetFn == nil {
		packetFn = DefaultPacket(arm)
	}
	res := ArmResult{Arm: arm, PerTask: []TaskResult{}}
	var learned []string
	seenMissing := map[string]int{}
	var m Metrics
	first := 0
	for _, t := range s.Tasks {
		p := packetFn(t, learned)
		o := e.RunTask(t, p)
		res.PerTask = append(res.PerTask, TaskResult{TaskID: t.ID, Reworks: o.Reworks, Missing: nonNil(o.Missing), Packet: p.Bytes, First: o.FirstPassApproved})
		m.SourcesNeeded += o.Reworks
		m.EstTokens += (p.Bytes+3)/4 + o.Reworks*reworkTokens
		for _, f := range o.Missing {
			seenMissing[f]++
			if seenMissing[f] > 1 {
				m.RepeatedIssueCount++
			}
		}
		if o.FirstPassApproved {
			first++
		}
		m.ReworkRate += float64(o.Reworks)
		if arm == ArmOn {
			learned = append(learned, t.Teaches...)
		}
	}
	if n := len(s.Tasks); n > 0 {
		m.ReworkRate /= float64(n)
		m.ReviewerFirstPassRate = float64(first) / float64(n)
	}
	res.Metrics = m
	return res
}

func nonNil(x []string) []string {
	if x == nil {
		return []string{}
	}
	return x
}

type Result struct {
	SuiteID    string               `json:"suite_id"`
	SuiteHash  string               `json:"suite_hash"`
	Engine     string               `json:"engine"`
	ClaimLevel string               `json:"claim_level"`
	Arms       map[string]ArmResult `json:"arms"`
}

// RunBoth runs both arms. The claim level is harness_only for every engine this package ships.
func RunBoth(s Suite, e Engine) Result {
	return Result{SuiteID: s.ID, SuiteHash: s.Hash, Engine: e.Name(), ClaimLevel: ClaimLevelHarnessOnly,
		Arms: map[string]ArmResult{ArmOn: Run(s, e, ArmOn, nil), ArmOff: Run(s, e, ArmOff, nil)}}
}
