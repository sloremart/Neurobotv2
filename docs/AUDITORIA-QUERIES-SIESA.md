# Auditoría de consultas a SIESA — Neurobotv2 + neuro-dashboard (04-ago-2026)

> Foco: consultas que con volumen de producción pueden degradar o tumbar la BD SIESA
> (compartida con la UI clínica). Inventario por 2 agentes (bot + dashboard) y **verificado
> contra la réplica local de prod** (tamaños de tabla e índices reales).

## 0. Datos duros (réplica de prod, backup ~22-jul)

| Tabla | Filas | Crecimiento |
|---|---|---|
| `sis_proc_precios` | **1.513.015** | casi estático (tarifarios) |
| `sis_paci` | 113.317 | lento |
| `programacion_medico_detalle` | 31.564 | **sin tope** (1 fila/slot/agenda) |
| `log_citas` | 20.839 | sin tope (el bot solo escribe) |
| `citas` | 13.969 | **sin tope** |
| `citas_procedimientos` | 11.904 | sin tope |

**Índices verificados que EXISTEN** (corrigen los peores miedos del inventario):
- `programacion_medico_detalle`: PK clustered `(Fecha, Medico)`, `IX_…_IdCita_Fecha (IdCita, Fecha)`,
  `(IdProgramacionMedico, Medico, PuntoAtencion)`, `IDX_Fecha_PMD (Fecha)`.
  → El `UPDATE … WHERE IdCita=@id` de Cancel y las subconsultas `WHERE pmd.IdCita=c.id` **hacen seek**
  (no scan). ⚠️ NO hay índice que lidere por `Medico`: todo acceso por médico depende de un rango
  sargable sobre `Fecha`.
- `citas`: PK clustered `(cod_medi, fecha, hora, meridiano, estado, …)`, + `(fecha,estado)`,
  `(id)`, `(autoid)`, `(id_programacion)` y combinaciones. Bien cubierta.
- `sis_paci`: `IDX_numid (num_id)` existe → `FindByDocument` hace seek. OK.
- `sis_proc_precios`: `UQ (Codigo_proc, Tipo_proc, Cod_manual)` + `(Cod_manual, Codigo_proc, Tipo_proc)`
  → `FindPrice` y `cupVariantExists` seekean. OK.

**Hoy nada está en tamaño crítico**; el riesgo es (a) patrones no acotados que escalan con los datos,
(b) `CAST` sobre columnas que anulan los índices de arriba, y (c) el pool compartido (§4).

---

## 1. PRIORIDAD ALTA (arreglar primero)

### P1. `FindAvailableSlots` — sin `TOP`, ventana de 90 días, ruta más caliente
`internal/repository/siesa/schedule_repo.go:71-164`
- Se ejecuta en **cada conversación** que muestra horarios (+1 por cada "ver más"). Sin `TOP` ni
  paginación SQL: devuelve TODOS los slots libres de 90 días y Go filtra en memoria para mostrar 5
  (`slot_service.go`). Con agendas pre-generadas a 90 días → miles de filas por llamada.
- 3 subconsultas correlacionadas por fila (`NOT EXISTS citas` — cubierto por PK de citas —,
  `EXISTS/NOT EXISTS sis_asuntoMedico`).
- **Fix**: `TOP (300-500)` manteniendo `ORDER BY pmd.Fecha`; ventana por defecto 30 días (ampliar solo
  al paginar); caché 30-60s por `(asunto, allowedDoctors, afterDate)`.

### P2. Lista de espera: N+1 que ejecuta P1 hasta 200 veces por cancelación
`internal/notifications/waiting_list_check.go:127-196`
- Cada cancelación de cita dispara `CheckWaitingListForSlot`: `freeSlotCapacity` corre P1 una vez por
  asunto, y el loop de hasta 200 entradas corre **`GetAvailableSlots` + `HasFutureForCup` (2 queries)
  por entrada**, en serie.
- **Fix**: resolver slots UNA vez por asunto y matchear en memoria; `HasFutureForCup` batch con `IN`.

### P3. Rangos de fecha sin tope + sin timeout en analytics (bot y dashboard)
`internal/api/internal_handler.go` (`citas-estado`:255, `no-show`:484, `bot-share`:612, `conversion`:690)
y proxy del dashboard `neuro-dashboard/internal/catalog/handlers.go:188` (reenvía `RawQuery` literal).
- `GET /api/siesa/citas-estado?from=1900-01-01&to=2999-12-31` = agregación sobre **toda `citas`**.
  La caché no protege (clave = `from|to`, cada rango distinto es miss). Los handlers de analytics
  **no tienen `context.WithTimeout`** (los de Agenda sí, 8s); por la vía de informes el timeout HTTP
  es de **90s** (`reports/botproxy.go:26`).
- **Fix**: clamp de amplitud (≤180 días) en el bot + whitelist de parámetros en el proxy + timeout 8-10s.

### P4. `CAST(Fecha AS DATE)` sobre `programacion_medico_detalle` — anula la PK `(Fecha, Medico)`
- `FindDoctorAgendasOnDate` (`appointment_repo.go:1857`): `Medico=@d AND CAST(pmd.Fecha AS DATE)=@date`.
  Sin índice por `Medico`, el CAST deja la query sin NINGÚN camino de acceso → scan completo de la
  tabla que más crece. Usada por la pestaña de reprogramación del dashboard, sin caché.
- `RescheduleDayOfAgenda` (`appointment_repo.go:1666-1841`): **transacción larga** con ~9 usos de
  `CAST(fecha AS DATE)` + self-join por `CONVERT(VARCHAR(5),Fecha,108)` + UPDATEs masivos → locks X
  prolongados sobre `citas` y `pmd` con ctx de hasta 5 min. Es el mayor bloqueo posible a la UI clínica.
- **Fix (patrón único)**: rango half-open `Fecha >= @d AND Fecha < DATEADD(DAY,1,@d)` (como ya hace
  `Create`); validaciones read-only fuera de la tx; `SET LOCK_TIMEOUT`.

### P5. Pool SIESA de 10 conexiones compartido entre pacientes y dashboard
`internal/config/config.go:335` (`EXTERNAL_DB_MAX_OPEN=10`), `internal/database/mysql.go:89-92`
- La página SIESA del dashboard dispara ~8 queries a la vez en frío (`pages/Siesa.tsx:39-49`) contra
  el MISMO pool que usa el flujo de agendamiento. Timeouts heredados del llamador: worker 2 min,
  scheduler **1 h** — una query lenta retiene conexión todo ese tiempo.
- **Fix**: timeout por query en los repos SIESA (10-15s vía `context.WithTimeout`); cuota/pool separado
  para KPIs, o al menos rate-limit en los endpoints internos de analytics (hoy solo `/login` lo tiene).

### P6. `lookupSubjectTypeFromHistory` — `LEFT(col)` + `ORDER BY` sobre el histórico, en el flujo de reserva
`appointment_repo.go:2062-2113` — fallback cuando el CUPS no está en el catálogo local; corre por
procedimiento dentro de `Create`. `LEFT(cp.id_procedimiento,@n)=@base` no sargable + sort de todo el
histórico. **Fix**: eliminar el fallback (catálogo local es la fuente) o reescribir con
`= @base OR LIKE @base + '-%'` y quedarse solo con la query a `AsuntoPctos`.

### P7. `CountMonthlyByGroup` — única lectura SIN NOLOCK, con `LEFT(CHARINDEX(...))`
`appointment_repo.go:1369-1410` — ruta caliente para pacientes MRC (validación de tope mensual).
Sin NOLOCK (deliberado) ⇒ toma locks S; el `LEFT(CHARINDEX(…))` fuerza evaluar toda `citas_procedimientos`
del mes, y `CAST(c.id AS VARCHAR)<>@p3` suma. **Fix**: expandir a `IN (bases) OR LIKE base+'-%'`,
comparar `c.id` como int; evaluar `READ COMMITTED SNAPSHOT`.

---

## 2. PRIORIDAD MEDIA

- **M1. `FindAgendaAppointmentsPaged`** (`appointment_repo.go:1576-1593`): la subconsulta a `pmd` está
  en el `ORDER BY` y `COUNT(*) OVER()` materializa todo el conjunto → pedir la página 1 cuesta como la
  última. Además acepta `doctor` sin `agenda_id`/`date` (`internal_handler.go:832`) y el filtro de
  nombre es `LIKE '%x%'` sobre `CONCAT` de 4 columnas. Fix: ordenar por columnas propias, contar aparte,
  exigir `agenda_id` o `date`.
- **M2. Subconsulta correlacionada `pmd` duplicada en 6 lecturas de citas** (`appointment_repo.go:284,
  329, 375, 420, 1433, 1579+1590`): con el índice `IdCita_Fecha` es un seek por fila (no scan), pero en
  `FindPendingByDate` (recordatorios, 3×/día) son cientos de seeks y en la paginada va 2× por fila.
  Fix: `OUTER APPLY`/`LEFT JOIN` agregado único.
- **M3. `FindUpcomingByPatient`** (`:313-342`): sin `TOP` ni cota superior; se llama 2 veces por
  confirmación/cancelación y para filtrar por fecha EN GO. Fix: `TOP (50)` + variante por fecha en SQL.
- **M4. `Create`**: lookups de catálogo (`AsuntoPctos`, `servicios`, `sis_proc_precios`) **dentro** de
  la transacción (`:692-865`) alargan los locks. Fix: moverlos antes de `BeginTx`.
- **M5. N+1 de conciliación del dashboard** (`internal_handler.go:554-578` → una query MySQL por par
  cita×CUPS, sin caché, `dias=30`): miles de round-trips por carga de la vista SIESA. Fix: cargar
  `cups_medico` completo en 1 query y cruzar en memoria.
- **M6. `FindByCode` + `FindPrice` en bucle por CUP al agendar** (`handlers/slots.go:1257-1320`), sin
  caché (`cached_entity_repo.go:12`). Fix: caché TTL (contratos/tarifas casi estáticos).
- **M7. Caché de analytics sin expiración** (`analytics_repo.go:29-57`): mapa crece con cada `from|to`
  distinto. Fix: LRU o TTL con purga (el dashboard ya lo hace bien en `kpi/repository.go:1913`).
- **M8. Auditoría `writeAuditLog`**: goroutine ilimitada por Confirm/Cancel contra el pool de 10.
  Fix: semáforo (2-3) o cola única.
- **M9. Dashboard MySQL**: polling de confirmaciones cada 60s con 2 subconsultas correlacionadas ×500
  filas (`kpi/repository.go:1606-1623`); `page_size` sin tope en `/api/waiting-list`
  (`repository.go:1426`); export lista de espera 10k filas fijas + `LIKE '%x%'`; servidor HTTP del
  dashboard sin Read/Write timeouts (`cmd/server/main.go:175`).

## 3. Lo que está BIEN (no tocar)

- Política `WITH (NOLOCK)` cumplida en ~99% de lecturas; escrituras de 1 fila por PK; claim de slot
  optimista con `TOP(1)` + `IdCita IS NULL`; `FindPrice` ejemplar (fix de conversión implícita
  documentado); `fetchProceduresBatch` resuelve su N+1 con `IN` de ints; catálogos con triple caché
  (bot 30 min + dashboard 30 min + React Query); analytics con `MAXDOP 1` + agregación en servidor +
  caché 10 min; el dashboard NO se conecta directo a SIESA (todo vía API del bot con límite de 8 MB).

## 4. Orden de ataque sugerido

1. P3 (clamp + timeout + validación en proxy) — cierra el vector "tumbar la BD desde el navegador". Barato.
2. P4 (rangos half-open) — 2 archivos, patrón ya existente en el codebase.
3. P1 + P2 (TOP/ventana/caché en slots + des-N+1 de lista de espera) — el mayor costo recurrente.
4. P5 (timeouts por query + cuota de pool) — convierte "query lenta" en error acotado, no en caída.
5. P6, P7, M1-M8 en ese orden.
