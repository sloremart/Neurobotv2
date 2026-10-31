// Package nectarreports empareja las escalaciones del bot con los tramos de espera de Néctar
// (spec D5 §5.2/§5.3). Es lógica pura: sin E/S, sin base de datos.
package nectarreports

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
)

// ClockTolerance es la holgura de reloj entre el bot y Néctar al emparejar.
const ClockTolerance = 2 * time.Minute

// Escalation es la entrada mínima del emparejamiento: una escalación del bot de la línea Néctar.
type Escalation struct {
	ID          uint64
	Phone       string // "nectar:<conversation_id>"
	EscalatedAt time.Time
}

// Attention es lo que Néctar sabe de una escalación: su tramo emparejado más los traspasos.
type Attention struct {
	EscalationID    uint64
	CycleIDs        []int64 // orden de apertura
	FirstResponseAt *time.Time
	AgentID         *int64
	ContactMsgs     int // mensajes del contacto hasta la primera respuesta (incluido ese tramo)
	Closed          bool
}

// ConversationID extrae el id de conversación de un pending "nectar:<id>".
func ConversationID(phone string) (int64, bool) {
	rest, ok := strings.CutPrefix(phone, "nectar:")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Pair empareja cada escalación con su atención. Las escalaciones sin tramo no aparecen.
// El resultado va ordenado por id de escalación.
func Pair(escalations []Escalation, cycles []nectar.WaitingCycle) []Attention {
	// Escalaciones por conversación, en orden cronológico.
	escByConv := map[int64][]Escalation{}
	for _, e := range escalations {
		if conv, ok := ConversationID(e.Phone); ok {
			escByConv[conv] = append(escByConv[conv], e)
		}
	}
	// Tramos por conversación, en orden de apertura.
	cycByConv := map[int64][]nectar.WaitingCycle{}
	for _, c := range cycles {
		cycByConv[c.ConversationID] = append(cycByConv[c.ConversationID], c)
	}
	for _, cs := range cycByConv {
		sort.Slice(cs, func(i, j int) bool {
			if !cs[i].OpenedAt.Equal(cs[j].OpenedAt) {
				return cs[i].OpenedAt.Before(cs[j].OpenedAt)
			}
			return cs[i].ID < cs[j].ID
		})
	}

	var out []Attention
	for conv, escs := range escByConv {
		cs := cycByConv[conv]
		if len(cs) == 0 {
			continue
		}
		sort.Slice(escs, func(i, j int) bool {
			if !escs[i].EscalatedAt.Equal(escs[j].EscalatedAt) {
				return escs[i].EscalatedAt.Before(escs[j].EscalatedAt)
			}
			return escs[i].ID < escs[j].ID
		})

		// 1) Tramo emparejado de cada escalación.
		taken := map[int64]uint64{} // cycle id -> escalation id
		matched := map[uint64]nectar.WaitingCycle{}
		for i, e := range escs {
			lower := e.EscalatedAt.Add(-ClockTolerance)
			var upper *time.Time
			if i+1 < len(escs) {
				u := escs[i+1].EscalatedAt.Add(-ClockTolerance)
				upper = &u
			}
			for _, c := range cs {
				if c.OpenedAt.Before(lower) {
					continue
				}
				if upper != nil && !c.OpenedAt.Before(*upper) {
					break
				}
				if _, used := taken[c.ID]; used {
					continue
				}
				taken[c.ID] = e.ID
				matched[e.ID] = c
				break
			}
		}

		// 2) Atención = tramo + sucesores por previous_cycle_id, cortando en el tramo de otra escalación.
		children := map[int64][]nectar.WaitingCycle{}
		for _, c := range cs {
			if c.PreviousCycleID != nil {
				children[*c.PreviousCycleID] = append(children[*c.PreviousCycleID], c)
			}
		}
		for _, e := range escs {
			root, ok := matched[e.ID]
			if !ok {
				continue
			}
			seen := map[int64]bool{root.ID: true}
			chain := []nectar.WaitingCycle{root}
			for queue := []nectar.WaitingCycle{root}; len(queue) > 0; {
				cur := queue[0]
				queue = queue[1:]
				for _, ch := range children[cur.ID] {
					if seen[ch.ID] {
						continue
					}
					if owner, isMatched := taken[ch.ID]; isMatched && owner != e.ID {
						continue
					}
					seen[ch.ID] = true
					chain = append(chain, ch)
					queue = append(queue, ch)
				}
			}
			sort.Slice(chain, func(i, j int) bool {
				if !chain[i].OpenedAt.Equal(chain[j].OpenedAt) {
					return chain[i].OpenedAt.Before(chain[j].OpenedAt)
				}
				return chain[i].ID < chain[j].ID
			})
			out = append(out, summarize(e.ID, chain))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EscalationID < out[j].EscalationID })
	return out
}

// summarize resume una cadena de tramos (ya en orden de apertura) en una atención.
func summarize(escalationID uint64, chain []nectar.WaitingCycle) Attention {
	a := Attention{EscalationID: escalationID, Closed: true}
	respIdx := -1
	for i, c := range chain {
		a.CycleIDs = append(a.CycleIDs, c.ID)
		if c.ClosedAt == nil {
			a.Closed = false
		}
		if c.FirstResponseAt != nil && (respIdx < 0 || c.FirstResponseAt.Before(*chain[respIdx].FirstResponseAt)) {
			respIdx = i
		}
	}
	last := len(chain) - 1
	if respIdx >= 0 {
		fr := *chain[respIdx].FirstResponseAt
		a.FirstResponseAt = &fr
		a.AgentID = chain[respIdx].AgentID
		last = respIdx
	} else {
		for i := len(chain) - 1; i >= 0; i-- {
			if chain[i].AgentID != nil {
				a.AgentID = chain[i].AgentID
				break
			}
		}
	}
	for _, c := range chain[:last+1] {
		a.ContactMsgs += c.ContactMessagesBeforeFirstResponse
	}
	return a
}
