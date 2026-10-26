package amneziawgkern

// InterfaceName is the kernel interface the panel runs for one inbound, for the
// callers that read it (diagnostics, the logs page) rather than manage it.
func InterfaceName(id int) string { return ownedInterfaceName(id) }
