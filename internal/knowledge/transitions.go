package knowledge

import "fmt"

type Actor string

const (
	ActorUser     Actor = "user"
	ActorPolicy   Actor = "policy"
	ActorDetector Actor = "detector"
	ActorSystem   Actor = "system"
)

var transitions = map[Status]map[Status][]Actor{
	StatusDraft: {
		StatusVerified:   {ActorUser, ActorPolicy},
		StatusConflicted: {ActorDetector, ActorSystem},
		StatusArchived:   {ActorUser, ActorPolicy, ActorSystem},
	},
	StatusConflicted: {
		StatusVerified: {ActorUser},
		StatusArchived: {ActorUser, ActorSystem},
	},
	StatusVerified: {
		StatusSuperseded: {ActorSystem},
		StatusArchived:   {ActorUser, ActorSystem},
	},
}

// Transition checks K6. Superseded and archived are terminal.
func Transition(from, to Status, actor Actor) error {
	for _, a := range transitions[from][to] {
		if a == actor {
			return nil
		}
	}
	return fmt.Errorf("transition %s -> %s by %s is not allowed", from, to, actor)
}

func Terminal(s Status) bool { return s == StatusSuperseded || s == StatusArchived }
