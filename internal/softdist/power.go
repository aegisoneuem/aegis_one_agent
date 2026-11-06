package softdist

// acPowerBlocked decides whether a RequireACPower deploy must be refused. It
// blocks ONLY when power state is known AND the machine is on battery — an
// unknown state (desktop / VM / no battery subsystem) is treated as "not on
// battery" so the gate never wrongly blocks a mains-powered desktop. Pure, so
// the decision is unit-tested without touching real power hardware.
func acPowerBlocked(require, onAC, known bool) (blocked bool, reason string) {
	if !require {
		return false, ""
	}
	if known && !onAC {
		return true, "deploy requires AC power but the machine is on battery — refused (a power loss during a BIOS/firmware flash can brick the device)"
	}
	return false, ""
}
