package cli

// Status labels printed by status, dns status, and expose status.
// Kept short and uppercase so operators can grep them across logs
// without worrying about word-boundary collisions ("OK" lands in
// "POKED" without these constants being treated as identifiers
// elsewhere). Keep the set small: four states cover every probe
// outcome the CLI reports today.
const (
	LabelOK   = "OK"
	LabelFAIL = "FAIL"
	LabelWARN = "WARN"
	LabelSKIP = "SKIP"
)
