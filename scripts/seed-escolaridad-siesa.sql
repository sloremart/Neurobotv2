-- Seed del catálogo de NIVEL EDUCATIVO de SIESA (ZeusSalud_Neuro.escolaridad).
--
-- Contexto: el bot captura el nivel educativo al crear pacientes y lo guarda como int en
-- sis_paci.escolaridad. NO hay FK, así que el INSERT del paciente funciona aunque el catálogo esté
-- vacío; este seed solo sirve para que la UI de SIESA MUESTRE la etiqueta del código (id → nombre).
--
-- Los ids DEBEN coincidir con educationLevelListRows() en
-- internal/statemachine/handlers/registration.go. Si el operador externo YA pobló este catálogo en
-- producción con OTROS ids, NO corras este seed: en su lugar, ajusta el mapa del bot a esos ids.
--
-- Idempotente: solo inserta los ids que falten (no toca los existentes).
-- Uso: sqlcmd -S <servidor> -d ZeusSalud_Neuro -E -No -i scripts/seed-escolaridad-siesa.sql

SET NOCOUNT ON;

DECLARE @niveles TABLE (id INT PRIMARY KEY, nombre VARCHAR(255));
INSERT INTO @niveles (id, nombre) VALUES
    (1, 'Ninguno'),
    (2, 'Preescolar'),
    (3, 'Primaria'),
    (4, 'Secundaria'),
    (5, 'Bachiller / Media'),
    (6, 'Técnico'),
    (7, 'Tecnólogo'),
    (8, 'Universitario'),
    (9, 'Posgrado');
-- Nota: el id 0 ("Prefiero no decir") NO va al catálogo — el bot lo guarda como NULL.

-- escolaridad.id es columna IDENTITY: hay que habilitar IDENTITY_INSERT para fijar los ids del bot.
SET IDENTITY_INSERT escolaridad ON;
INSERT INTO escolaridad (id, nombre)
SELECT n.id, n.nombre
FROM @niveles n
WHERE NOT EXISTS (SELECT 1 FROM escolaridad e WHERE e.id = n.id);
SET IDENTITY_INSERT escolaridad OFF;

PRINT 'Catálogo escolaridad tras el seed:';
SELECT id, nombre FROM escolaridad ORDER BY id;
