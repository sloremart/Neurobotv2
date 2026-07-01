package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// nowFunc is the clock used by business-hours check. Override in tests.
var nowFunc = time.Now

// RegisterGreetingHandlers registra CHECK_BUSINESS_HOURS, GREETING, MAIN_MENU, OUT_OF_HOURS, OUT_OF_HOURS_MENU
func RegisterGreetingHandlers(m *sm.Machine, cfg *config.Config, locationRepo LocationReader) {
	m.Register(sm.StateCheckBusinessHours, checkBusinessHoursHandler(cfg))
	m.Register(sm.StateOutOfHours, outOfHoursHandler(cfg))
	m.Register(sm.StateOutOfHoursMenu, outOfHoursMenuHandler(cfg, locationRepo))
	m.Register(sm.StateGreeting, greetingHandler(cfg))
	m.RegisterWithConfig(sm.StateMainMenu, sm.HandlerConfig{
		InputType: sm.InputButton,
		Options:   []string{"pet_ct", "agendar", "consultar", "resultados", "ubicacion", "ayuda"},
		RetryPrompt: func(sess *session.Session, result *sm.StateResult) {
			list := buildMainMenuList()
			list.Body = "Por favor selecciona una opción del menú.\n\n" + list.Body
			result.Messages = append(result.Messages, list)
		},
		Handler: mainMenuHandler(),
	})
	m.Register(sm.StateShowHelp, showHelpHandler())
}

// CHECK_BUSINESS_HOURS (automático)
func checkBusinessHoursHandler(cfg *config.Config) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		// Testing bypass: TESTING_ALWAYS_OPEN=true skips business hours check
		if cfg.TestingAlwaysOpen {
			return sm.NewResult(sm.StateGreeting), nil
		}

		now := nowFunc() // Ya en timezone America/Bogota
		weekday := now.Weekday()
		hour := now.Hour()

		inHours := false
		switch weekday {
		case time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday:
			inHours = hour >= 7 && hour < 18
		case time.Saturday:
			inHours = hour >= 7 && hour < 12
		}

		if !inHours {
			return sm.NewResult(sm.StateOutOfHours).
				WithEvent("out_of_hours", map[string]interface{}{
					"day":  weekday.String(),
					"hour": hour,
				}), nil
		}

		return sm.NewResult(sm.StateGreeting), nil
	}
}

// OUT_OF_HOURS (automático) — muestra bienvenida fuera de horario con menú interactivo (3 opciones).
// Bird V2: list with Consultar Resultados + Ubicacion + Ayuda (no agendar/consultar citas fuera de horario).
func outOfHoursHandler(cfg *config.Config) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		welcomeText := fmt.Sprintf("Soy *%s*, tu asistente virtual de *%s*.\n\n"+
			"Antes de continuar, ten en cuenta que al seguir conversando aceptas "+
			"el tratamiento de tus datos personales conforme a nuestra Política de "+
			"Protección de Datos (Ley 1581 de 2012), disponibles en www.neuroelectrodx.com. "+
			"También podemos usar inteligencia artificial para ayudarte a completar tus datos.\n\n"+
			"Podrás ejercer tus derechos de acceso, rectificación o supresión en cualquier momento.",
			cfg.BotName, cfg.CenterName)

		return sm.NewResult(sm.StateOutOfHoursMenu).
			WithList(welcomeText+"\n\n¿En qué puedo ayudarte hoy?", "Ver opciones",
				sm.ListSection{
					Title: "Opciones disponibles",
					Rows: []sm.ListRow{
						{ID: "ooh_resultados", Title: "Consultar Resultados", Description: "Si quieres descargar resultados de tus consultas"},
						{ID: "ooh_ubicacion", Title: "Ubicación", Description: "Conoce nuestras sedes"},
						{ID: "ooh_ayuda", Title: "Cómo usar el bot", Description: "Guía rápida de cómo interactuar conmigo"},
					},
				}).
			WithEvent("out_of_hours_menu_shown", nil), nil
	}
}

// OUT_OF_HOURS_MENU (interactivo) — procesa selección del menú fuera de horario.
// Muestra resultados, ubicaciones o ayuda y termina la conversación (excepto ayuda que vuelve al menú OOH).
func outOfHoursMenuHandler(cfg *config.Config, locationRepo LocationReader) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		result, selected := sm.ValidateButtonResponse(sess, msg, "ooh_resultados", "ooh_ubicacion", "ooh_ayuda")
		if result != nil {
			if result.NextState == sm.StateEscalateToAgent {
				return result, nil
			}
			// Clear default error text — combine into list body (1 message)
			result.Messages = nil
			result.Messages = append(result.Messages, &sm.ListMessage{
				Body:  "Por favor selecciona una opción.\n\n¿En qué puedo ayudarte hoy?",
				Title: "Ver opciones",
				Sections: []sm.ListSection{{
					Title: "Opciones disponibles",
					Rows: []sm.ListRow{
						{ID: "ooh_resultados", Title: "Consultar Resultados", Description: "Si quieres descargar resultados de tus consultas"},
						{ID: "ooh_ubicacion", Title: "Ubicación", Description: "Conoce nuestras sedes"},
						{ID: "ooh_ayuda", Title: "Cómo usar el bot", Description: "Guía rápida de cómo interactuar conmigo"},
					},
				}},
			})
			return result, nil
		}

		switch selected {
		case "ooh_resultados":
			text := "Ingresa a nuestra página: https://neuroelectrodx.com/\n\n" +
				"En el siguiente video encontrarás el paso a paso para descargar tus resultados.\n" +
				"https://www.youtube.com/watch?v=kEx51t6OlyQ"
			if cfg.ResultsURL != "" {
				text = fmt.Sprintf("Ingresa a nuestra página: %s\n\n"+
					"En el siguiente video encontrarás el paso a paso para descargar tus resultados.\n"+
					"https://www.youtube.com/watch?v=kEx51t6OlyQ", cfg.ResultsURL)
			}
			return sm.NewResult(sm.StateTerminated).
				WithText(text).
				WithEvent("ooh_results_shown", nil), nil

		case "ooh_ubicacion":
			text := buildLocationsText(ctx, locationRepo)
			return sm.NewResult(sm.StateTerminated).
				WithText(text).
				WithEvent("ooh_locations_shown", nil), nil

		case "ooh_ayuda":
			return sm.NewResult(sm.StateShowHelp).
				WithContext("help_source", "ooh").
				WithEvent("ooh_help_selected", nil), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// buildLocationsText creates location text from DB or fallback.
func buildLocationsText(ctx context.Context, locationRepo LocationReader) string {
	if locationRepo != nil {
		locations, err := locationRepo.FindActive(ctx)
		if err == nil && len(locations) > 0 {
			text := ""
			for i, loc := range locations {
				if i > 0 {
					text += "\n"
				}
				text += fmt.Sprintf("*%s* %s\n", loc.Name, loc.Address)
				if loc.GoogleMapsURL != "" {
					text += loc.GoogleMapsURL + "\n"
				}
				if loc.Phone != "" {
					text += fmt.Sprintf("Tel: %s\n", loc.Phone)
				}
			}
			return text
		}
	}
	return "Actualmente no tenemos sedes configuradas. Comunícate con un agente para más información."
}

// GREETING (automático) — envía bienvenida + lista del menú (5 opciones Bird V2)
func greetingHandler(cfg *config.Config) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		welcomeText := fmt.Sprintf("Soy *%s*, tu asistente virtual de *%s*.\n\n"+
			"Antes de continuar, ten en cuenta que al seguir conversando aceptas "+
			"el tratamiento de tus datos personales conforme a nuestra Política de "+
			"Protección de Datos (Ley 1581 de 2012), disponibles en www.neuroelectrodx.com. "+
			"También podemos usar inteligencia artificial para ayudarte a completar tus datos.\n\n"+
			"Podrás ejercer tus derechos de acceso, rectificación o supresión en cualquier momento.",
			cfg.BotName, cfg.CenterName)

		r := sm.NewResult(sm.StateMainMenu).
			WithEvent("greeting_sent", nil)
		list := buildMainMenuList()
		list.Body = welcomeText + "\n\n" + list.Body
		r.Messages = append(r.Messages, list)
		return r, nil
	}
}

// MAIN_MENU — solo lógica de negocio (validación declarativa en RegisterWithConfig).
func mainMenuHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		selected := sm.ValidatedPayload(ctx)

		switch selected {
		case "pet_ct":
			text := "Te contamos sobre nuestro servicio de PET-CT, un examen diagnóstico avanzado de tomografía por emisión de positrones, para FDG F-18 y PSMA, que permite detectar enfermedades como cáncer, afecciones cardíacas y trastornos neurológicos.\n\n" +
				"- - - - - - - - - - - - - - - - - - - - - - - - -\n\n" +
				"📌 *¿Cómo agendar tu examen?*\n\n" +
				"🔹 *Si eres paciente particular:*\n" +
				"• Si cuentas con orden médica e historia clínica, envíalas y programamos tu cita.\n" +
				"• Si no cuentas con esto, te ofrecemos valoración médica previa al examen, sin costo adicional.\n\n" +
				"💰 Valor del examen: *$5.500.000*\n" +
				"✅ Paga el 50% para programar tu cita y solicitar el radiofármaco.\n" +
				"✅ Paga el otro 50% el día del examen.\n" +
				"🎉 ¡Aprovecha *20% de descuento adicional* por tiempo limitado!\n\n" +
				"🔹 *Si eres paciente por EPS:*\n" +
				"Debes enviarnos:\n" +
				"• Historia clínica\n" +
				"• Orden médica\n" +
				"• Autorización de la EPS\n\n" +
				"📞 Incluye dos números de contacto\n\n" +
				"Una vez recibamos la información, nuestra coordinadora se comunicará contigo para indicarte preparación, 📆 fecha y hora de tu examen.\n\n" +
				"🎥 *Mira cómo es el examen:*\n" +
				"https://www.instagram.com/reel/DTfqm7sAvhL/?igsh=MTBhZHV5dWh4ejNp\n\n" +
				"¿Deseas agendar o tienes alguna duda? Estoy aquí para ayudarte 😊"
			return sm.NewResult(sm.StateEscalateToAgent).
				WithText(text).
				WithContext("escalation_reason", "pet_ct").
				WithEvent("menu_selected", map[string]interface{}{"option": "pet_ct"}), nil
		case "consultar":
			return sm.NewResult(sm.StateAskDocumentType).
				WithContext("menu_option", "consultar").
				WithText(docTypeMenuText()).
				WithEvent("menu_selected", map[string]interface{}{"option": "consultar"}), nil
		case "agendar":
			// Bird V2: entity type selection BEFORE document entry
			return sm.NewResult(sm.StateAskClientType).
				WithContext("menu_option", "agendar").
				WithList("Selecciona el tipo de entidad a la que perteneces", "Tipo de entidad",
					sm.ListSection{
						Title: "Tipos de entidad",
						Rows:  buildEntityTypeRows(),
					}).
				WithEvent("menu_selected", map[string]interface{}{"option": "agendar"}), nil
		case "resultados":
			return sm.NewResult(sm.StateShowResults).
				WithEvent("menu_selected", map[string]interface{}{"option": "resultados"}), nil
		case "ubicacion":
			return sm.NewResult(sm.StateShowLocations).
				WithEvent("menu_selected", map[string]interface{}{"option": "ubicacion"}), nil
		case "ayuda":
			return sm.NewResult(sm.StateShowHelp).
				WithEvent("menu_selected", map[string]interface{}{"option": "ayuda"}), nil
		}

		return nil, fmt.Errorf("unreachable: selected=%s", selected)
	}
}

// buildMainMenuList creates the main menu list message with 6 options.
func buildMainMenuList() *sm.ListMessage {
	return &sm.ListMessage{
		Body:  "¿En qué puedo ayudarte hoy?",
		Title: "Ver opciones",
		Sections: []sm.ListSection{{
			Title: "Menú principal",
			Rows: []sm.ListRow{
				{ID: "pet_ct", Title: "☢️ Agendar cita PET-CT", Description: "Tomografía por emisión de positrones FDG F-18 / PSMA"},
				{ID: "agendar", Title: "Agendar cita", Description: "Si buscas una cita como particular o cuentas con una orden de tu IPS"},
				{ID: "consultar", Title: "Citas Programadas", Description: "Consulta, confirma o cancela tus citas"},
				{ID: "resultados", Title: "Consultar Resultados", Description: "Si quieres descargar resultados de tus consultas"},
				{ID: "ubicacion", Title: "Ubicación", Description: "Conoce nuestras sedes"},
				{ID: "ayuda", Title: "Cómo usar el bot", Description: "Guía rápida de cómo interactuar conmigo"},
			},
		}},
	}
}

// buildEntityTypeRows creates list rows for the 6 entity type options.
func buildEntityTypeRows() []sm.ListRow {
	rows := make([]sm.ListRow, 6)
	for i := 1; i <= 6; i++ {
		rows[i-1] = sm.ListRow{
			ID:          fmt.Sprintf("ct_%d", i),
			Title:       domain.EntityCategoryLabels[i],
			Description: "",
		}
	}
	return rows
}

// SHOW_HELP (automático) — muestra guia de uso del bot y vuelve al menu correspondiente.
// Si viene del menú fuera de horario (help_source=ooh), vuelve a OUT_OF_HOURS_MENU.
// Si viene del menú principal, vuelve a MAIN_MENU.
func showHelpHandler() sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		msg1 := "📋 *GUÍA RÁPIDA DEL BOT*\n\n" +
			"Soy tu asistente virtual para gestionar citas médicas por WhatsApp. " +
			"Esto es lo que puedo hacer:\n\n" +
			"*1. Agendar cita*\n" +
			"Seleccionas tu tipo de entidad (EPS, particular, etc.), " +
			"ingresas tu documento, y envías una foto de tu orden médica. " +
			"Yo la leo automáticamente y te muestro los horarios disponibles.\n\n" +
			"*2. Consultar citas*\n" +
			"Ingresas tu documento y te muestro tus citas programadas. " +
			"Puedes confirmarlas o cancelarlas.\n\n" +
			"*3. Consultar resultados*\n" +
			"Te comparto el enlace para descargar tus resultados médicos.\n\n" +
			"*4. Ubicación*\n" +
			"Te muestro las direcciones de nuestras sedes con enlace a Google Maps."

		msg2 := "💡 *CONSEJOS PARA USAR EL BOT*\n\n" +
			"• *Documento:* Ingresa solo números, sin puntos ni espacios\n" +
			"• *Orden médica:* Envía una foto clara donde se lean bien los procedimientos\n" +
			"• *Seleccionar opciones:* Cuando te muestre una lista, toca el botón para ver las opciones\n" +
			"• *Horarios:* Te mostraré los horarios disponibles más cercanos. Si no hay, puedes quedar en lista de espera\n" +
			"• *Volver al menú:* Escribe *menú* o *0* en cualquier momento para volver al inicio\n\n" +
			"Si en algún momento necesitas ayuda humana, el bot te conectará con un agente.\n\n" +
			"*Horario de atención:*\n" +
			"Lunes a viernes: 7:00 AM - 6:00 PM\n" +
			"Sábados: 7:00 AM - 12:00 PM"

		// Auto-close: combinar ambos mensajes y cerrar sesion
		if sess.GetContext("help_source") == "ooh" {
			return buildAutoCloseResult(msg1+"\n\n"+msg2).
				WithClearCtx("help_source").
				WithEvent("help_shown", nil), nil
		}

		return buildAutoCloseResult(msg1+"\n\n"+msg2).
			WithEvent("help_shown", nil), nil
	}
}
