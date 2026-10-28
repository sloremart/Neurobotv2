package statemachine

// UnregisterReturnStepForTest borra TODO lo que un paso registró en el menú de retornos (menú,
// entrada, rótulo de reanudar, reanudar oculto y candado). SOLO PARA PRUEBAS: los registros son
// globales del paquete, y una prueba de otro paquete que registra un paso lo limpia con
// t.Cleanup para no contaminar a las demás. Vive fuera de un _test.go porque un _test.go no se
// puede importar desde otro paquete.
func UnregisterReturnStepForTest(step string) {
	returnMenusMu.Lock()
	defer returnMenusMu.Unlock()
	delete(returnMenus, step)
	delete(returnInputs, step)
	delete(resumeLabels, step)
	delete(hiddenPlainResume, step)
	delete(resolveGuards, step)
}
