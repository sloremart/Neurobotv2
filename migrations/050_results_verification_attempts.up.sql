-- Migration 050: tope persistente de intentos fallidos de verificación de «Resultados».
--
-- Tres fallos en 24 h, por documento O por teléfono, bloquean la consulta 24 h para ese documento
-- y para ese teléfono; ni «menú» ni una sesión nueva lo reinician.
-- subject vale "doc:<sha256 hex de tipo:número>" (el número nunca se guarda en claro) o
-- "phone:<E.164>". window_start es el primer fallo de la ventana de 24 h; locked_until, NULL si
-- no hay bloqueo. Tabla nueva y aditiva: nada existente cambia.
CREATE TABLE results_verification_attempts (
  subject VARCHAR(80) NOT NULL PRIMARY KEY,
  failures INT NOT NULL DEFAULT 0,
  window_start DATETIME NOT NULL,
  locked_until DATETIME NULL
);
