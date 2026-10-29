package handlers

import (
	"context"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/testutil"
)

// TestWaitingListEntry_NectarStoresPhone: una entrada de lista de espera creada desde una sesión de
// Néctar guarda el teléfono real del paciente, no la clave "nectar:<id>". Es un cambio del DATO
// guardado: cada handler sigue al mismo estado que con Bird.
func TestWaitingListEntry_NectarStoresPhone(t *testing.T) {
	cases := []struct {
		name         string
		key          string
		contact      string // sessions.contact_identifier, como lo da Néctar
		siesaPhone   string // patient_phone cargado de SIESA en el contexto
		wantStoredAs string
	}{
		{"néctar con contacto", "nectar:77", "573001112233", "3104445566", "+573001112233"},
		{"néctar sin contacto, con teléfono de SIESA", "nectar:77", "", "3104445566", "+573104445566"},
		{"néctar con BSUID, con teléfono de SIESA", "nectar:77", "CO.ABC123", "3104445566", "+573104445566"},
		{"néctar sin nada: la clave, como hoy", "nectar:77", "", "", "nectar:77"},
		{"bird: el teléfono de la sesión, como hoy", "+573001234567", "", "3104445566", "+573001234567"},
	}

	// Los tres sitios que crean entradas, con el estado al que llevan (no cambia).
	sites := []struct {
		name      string
		wantState string
		run       func(t *testing.T, repo *testutil.MockWaitingListCreator, prep func(*session.Session) *session.Session) string
	}{
		{"autoAddToWaitingList", sm.StateCheckSpecialCups, func(t *testing.T, repo *testutil.MockWaitingListCreator, prep func(*session.Session) *session.Session) string {
			res, err := autoAddToWaitingList(context.Background(), prep(twoProcSession(sm.StateNoSlotsAvailable)), repo, "RX TORAX")
			if err != nil {
				t.Fatal(err)
			}
			return res.NextState
		}},
		{"offerWaitingList wl_yes", sm.StateTerminated, func(t *testing.T, repo *testutil.MockWaitingListCreator, prep func(*session.Session) *session.Session) string {
			m := sm.NewMachine()
			m.Register(sm.StateOfferWaitingList, offerWaitingListHandler(repo))
			s := prep(testSess(sm.StateOfferWaitingList))
			s.Context["patient_id"] = "PAT001"
			s.Context["cups_code"] = "890271"
			s.Context["cups_name"] = "EMG"
			res, err := m.Process(context.Background(), s, postbackM("wl_yes"))
			if err != nil {
				t.Fatal(err)
			}
			return res.NextState
		}},
		{"askMedicalOrder has_order_no", "", func(t *testing.T, repo *testutil.MockWaitingListCreator, prep func(*session.Session) *session.Session) string {
			s := prep(testSess(sm.StateAskMedicalOrder))
			s.SetContext("entity_category", "PARTICULAR")
			s.SetContext("_prompted_has_order", "1")
			res, err := askMedicalOrderHandler(repo, nil, nil)(context.Background(), s, postbackM("has_order_no"))
			if err != nil {
				t.Fatal(err)
			}
			return res.NextState
		}},
	}

	for _, site := range sites {
		var birdState string
		for _, tc := range cases {
			t.Run(site.name+"/"+tc.name, func(t *testing.T) {
				var stored string
				repo := &testutil.MockWaitingListCreator{CreateFn: func(_ context.Context, e *domain.WaitingListEntry) error {
					stored = e.PhoneNumber
					return nil
				}}
				prep := func(s *session.Session) *session.Session {
					s.PhoneNumber = tc.key
					s.ContactIdentifier = tc.contact
					if tc.siesaPhone != "" {
						s.Context["patient_phone"] = tc.siesaPhone
					}
					return s
				}
				next := site.run(t, repo, prep)
				if stored != tc.wantStoredAs {
					t.Fatalf("la entrada guardó %q, quería %q", stored, tc.wantStoredAs)
				}
				if site.wantState != "" && next != site.wantState {
					t.Fatalf("el flujo no cambia: next=%q, quería %q", next, site.wantState)
				}
				// Néctar y Bird llevan al mismo estado.
				if birdState == "" {
					birdState = next
				} else if next != birdState {
					t.Fatalf("el estado depende del transporte: %q vs %q", next, birdState)
				}
			})
		}
	}
}
