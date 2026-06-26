package domain

import "errors"

// ErrSlotTaken indica que el horario elegido ya no está disponible al momento de agendar.
// Cubre los dos modos en que SIESA lo manifiesta (ver AppointmentRepo.Create):
//   - El INSERT en `citas` viola PK_citas (cod_medi,fecha,hora,meridiano,estado): el médico ya
//     tiene una cita activa 'P' a esa fecha/hora (otra reserva, o un grupo previo del mismo paciente).
//   - El UPDATE de `programacion_medico_detalle` no afecta filas (IdCita ya no es NULL): otro
//     proceso reclamó el slot entre que se mostró la lista y se confirmó (carrera).
//
// El handler de agendamiento lo trata como "slot tomado": avisa al paciente y re-busca horarios
// frescos, en vez de devolver un error genérico de callejón sin salida.
var ErrSlotTaken = errors.New("slot_taken")
