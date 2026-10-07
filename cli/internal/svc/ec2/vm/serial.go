package vm

// SerialAttachCommand is what a docker exec with a TTY runs in the VM
// container to connect the terminal to the guest's serial console. socat puts
// the TTY in raw mode, so keystrokes reach the guest's getty unmodified. QEMU
// serves one client at a time; the next connects when the previous one leaves.
func SerialAttachCommand() []string {
	return []string{"socat", "-,raw,echo=0", "UNIX-CONNECT:" + SerialSocket}
}

// QGAAttachCommand is what a docker exec (stdin open) runs to talk to the guest agent.
// -t 1 lets socat end a second after stdin closes.
func QGAAttachCommand() []string {
	return []string{"socat", "-t", "1", "-", "UNIX-CONNECT:" + QGASocket}
}
