package flynnyml

import "path"

// CommandArgs is the process argv for a flynn.yml run value.
// A scalar is [sh, -c, string] unless entrypoint is already a shell that
// consumes that string. An exec command replaces CMD and keeps a non-shell
// entrypoint. An empty shell and argv keeps the image command.
func CommandArgs(entrypoint, cmd []string, shell string, argv []string, exec bool) []string {
	if exec {
		if shellConsumer(entrypoint) {
			return append([]string{}, argv...)
		}
		return append(append([]string{}, entrypoint...), argv...)
	}
	if shell != "" {
		if shellConsumer(entrypoint) {
			return []string{entrypoint[0], "-c", shell}
		}
		if len(entrypoint) == 0 {
			return []string{"sh", "-c", shell}
		}
		return append(append([]string{}, entrypoint...), "sh", "-c", shell)
	}
	return append(append([]string{}, entrypoint...), cmd...)
}

func shellConsumer(entrypoint []string) bool {
	if len(entrypoint) < 2 || entrypoint[1] != "-c" {
		return false
	}
	base := path.Base(entrypoint[0])
	return base == "sh" || base == "bash" || base == "dash"
}
