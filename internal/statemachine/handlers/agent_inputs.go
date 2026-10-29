// Package handlers registra los handlers de cada estado de la máquina de estados del bot.
package handlers

import sm "github.com/neuro-bot/neuro-bot/internal/statemachine"

// optionsFromRows deriva las opciones del agente de las filas de lista que ve el paciente, para
// no duplicar el catálogo: el rótulo es el título de la fila y el payload es su ID, que el
// handler del paso acepta tal cual (llega como postback y como texto).
func optionsFromRows(rows []sm.ListRow) []sm.ReturnOption {
	options := make([]sm.ReturnOption, len(rows))
	for i, r := range rows {
		options[i] = sm.ReturnOption{Label: r.Title, Value: r.ID, Data: r.ID}
	}
	return options
}

// documentTypeOptions son los tipos de documento del catálogo; el código se inyecta como lo
// escribiría el paciente (`/bot resume ASK_DOCUMENT_TYPE CC`). Lo comparten ASK_DOCUMENT_TYPE y
// REG_DOCUMENT_TYPE, que aceptan los mismos códigos.
func documentTypeOptions() []sm.ReturnOption {
	options := make([]sm.ReturnOption, 0, len(documentTypeCatalog))
	for _, d := range documentTypeCatalog {
		options = append(options,
			sm.ReturnOption{Label: d.Label + " (" + d.Code + ")", Value: d.Code, Data: d.Code})
	}
	return options
}

// payloadOption es la opción que inyecta `payload` como lo haría el botón del paciente.
func payloadOption(label, payload string) sm.ReturnOption {
	return sm.ReturnOption{Label: label, Value: payload, Data: payload}
}
