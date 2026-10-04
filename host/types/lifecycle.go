package host

import (
	"fmt"
	"strconv"
	"strings"
)

// Scheduler/host job metadata explaining why a process was started.
const (
	JobReasonStart     = "start"
	JobReasonRestart   = "restart"
	JobReasonReplace   = "replace"
	JobReasonScale     = "scale"
	JobReasonScaleDown = "scale-down"
)

const (
	MetaControllerType           = "flynn-controller.type"
	MetaControllerName           = "flynn-controller.name"
	MetaControllerReason         = "flynn-controller.reason"
	MetaControllerStopReason     = "flynn-controller.stop-reason"
	MetaControllerRuntime        = "flynn-controller.runtime"
	MetaControllerRuntimeProfile = "flynn-controller.runtime_profile" // legacy job metadata
	MetaControllerApp            = "flynn-controller.app"
	MetaControllerCommand        = "flynn-controller.command" // Procfile / Docker command, not slugrunner argv
)

// Additional H-codes for why a process was created (H10 remains a generic create).
const (
	CodeJobRestart   = "H16" // started to replace a crashed process
	CodeJobReplace   = "H17" // started because the release/profile changed
	CodeJobScaleUp   = "H18" // started because the formation scaled up
	CodeJobScaleDown = "H19" // stopped because the formation scaled down
)

const jobNameMax = 9999

// JobShortName is the allocated process name (web.1), from metadata or FLYNN_JOB_NAME.
func JobShortName(job *Job) string {
	if job == nil {
		return ""
	}
	if job.Metadata != nil {
		if n := strings.TrimSpace(job.Metadata[MetaControllerName]); n != "" {
			return n
		}
	}
	if job.Config.Env != nil {
		return strings.TrimSpace(job.Config.Env["FLYNN_JOB_NAME"])
	}
	return ""
}

// JobDisplayName is the process name shown in flynn-host ps. Prefers the
// allocated name; otherwise typ.<digits from the job UUID> so bootstrap jobs
// that started before the scheduler still have a stable label.
func JobDisplayName(job *Job) string {
	if job == nil {
		return ""
	}
	if n := JobShortName(job); n != "" {
		return n
	}
	return jobTypeLabel(job) + "." + digitsFromJobID(job.ID)
}

// EnsureJobProcessName writes flynn-controller.name and FLYNN_JOB_NAME when
// missing so host ps, logs, and inspect share one label.
func EnsureJobProcessName(job *Job) string {
	if job == nil {
		return ""
	}
	name := JobDisplayName(job)
	if name == "" {
		return ""
	}
	if job.Metadata == nil {
		job.Metadata = map[string]string{}
	}
	job.Metadata[MetaControllerName] = name
	if job.Config.Env == nil {
		job.Config.Env = map[string]string{}
	}
	job.Config.Env["FLYNN_JOB_NAME"] = name
	return name
}

func jobTypeLabel(job *Job) string {
	if job == nil {
		return "process"
	}
	if job.Metadata != nil {
		if t := strings.TrimSpace(job.Metadata[MetaControllerType]); t != "" {
			return sanitizeJobType(t)
		}
	}
	if job.Config.Env != nil {
		if t := strings.TrimSpace(job.Config.Env["FLYNN_PROCESS_TYPE"]); t != "" {
			return sanitizeJobType(t)
		}
	}
	return "process"
}

func sanitizeJobType(typ string) string {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return "process"
	}
	var b strings.Builder
	for i, r := range typ {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case (r >= '0' && r <= '9') || r == '_' || r == '-':
			if i == 0 {
				b.WriteByte('j')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "process"
	}
	return b.String()
}

func digitsFromJobID(id string) string {
	seed := id
	if i := strings.IndexByte(id, '-'); i >= 0 && i+1 < len(id) {
		seed = id[i+1:]
	}
	hex := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, seed)
	if len(hex) >= 4 {
		n, err := strconv.ParseUint(hex[:4], 16, 32)
		if err == nil {
			return strconv.FormatUint(n%jobNameMax+1, 10)
		}
	}
	return "1"
}

// JobProcessType is the formation process type (web, worker, …).
func JobProcessType(job *ActiveJob) string {
	if job == nil || job.Job == nil {
		return "process"
	}
	if t := strings.TrimSpace(job.Job.Metadata[MetaControllerType]); t != "" {
		return t
	}
	if job.Job.Config.Env != nil {
		if t := strings.TrimSpace(job.Job.Config.Env["FLYNN_PROCESS_TYPE"]); t != "" {
			return t
		}
	}
	return "process"
}

// JobReason is why the scheduler started this job (start/restart/replace/scale).
func JobReason(job *ActiveJob) string {
	if job == nil || job.Job == nil {
		return ""
	}
	return strings.TrimSpace(job.Job.Metadata[MetaControllerReason])
}

// JobStopReason is why the scheduler asked the host to stop this job.
func JobStopReason(job *ActiveJob) string {
	if job == nil || job.Job == nil {
		return ""
	}
	return strings.TrimSpace(job.Job.Metadata[MetaControllerStopReason])
}

// JobRuntimeProfile is the named CPU/memory preset on the process type.
func JobRuntimeProfile(job *ActiveJob) string {
	if job == nil || job.Job == nil {
		return ""
	}
	if v := strings.TrimSpace(job.Job.Metadata[MetaControllerRuntime]); v != "" {
		return v
	}
	return strings.TrimSpace(job.Job.Metadata[MetaControllerRuntimeProfile])
}

// FormatJobLifecycleLog is the line written to the app log (source=flynn).
func FormatJobLifecycleLog(event JobEventType, job *ActiveJob) string {
	typ := JobProcessType(job)
	reason := JobReason(job)
	extra := lifecycleExtra(job)
	switch event {
	case JobEventCreate:
		verb := "Starting"
		switch reason {
		case JobReasonRestart:
			verb = "Restarting"
		case JobReasonReplace, JobReasonScale:
			verb = "Scaling up"
		}
		return joinLifecycle(withCommand(fmt.Sprintf("%s %s process", verb, typ), job), extra)
	case JobEventStart:
		return joinLifecycle(withCommand(fmt.Sprintf("%s process started", typ), job), extra)
	case JobEventStop:
		if JobStopReason(job) == JobReasonScaleDown {
			return joinLifecycle(fmt.Sprintf("Scaling down %s process", typ), extra)
		}
		if job != nil && job.Status == StatusCrashed {
			msg := fmt.Sprintf("%s process crashed", typ)
			if job.ExitStatus != nil {
				msg = fmt.Sprintf("%s process crashed (exit %d)", typ, *job.ExitStatus)
			}
			return msg
		}
		msg := fmt.Sprintf("%s process stopped", typ)
		if job != nil && job.ExitStatus != nil {
			msg = fmt.Sprintf("%s process stopped (exit %d)", typ, *job.ExitStatus)
		}
		return msg
	case JobEventError:
		msg := fmt.Sprintf("%s process failed to start", typ)
		if job != nil && job.Error != nil && strings.TrimSpace(*job.Error) != "" {
			msg += ": " + strings.TrimSpace(*job.Error)
		}
		return msg
	default:
		return ""
	}
}

// JobLifecycleWebhook maps a host job event to a webhook code, description, and severity.
func JobLifecycleWebhook(event JobEventType, job *ActiveJob) (code, description, severity string) {
	if event == JobEventCleanup {
		return CodeJobCleanup, "Job cleaned up", SeverityInfo
	}
	description = FormatJobLifecycleLog(event, job)
	if description == "" {
		return "", "", ""
	}
	reason := JobReason(job)
	switch event {
	case JobEventCreate:
		switch reason {
		case JobReasonRestart:
			return CodeJobRestart, description, SeverityInfo
		case JobReasonReplace, JobReasonScale:
			// Redeploys and formation increases both show as scale-up so
			// one-down-one-up is not only a scale-down in logs and events.
			return CodeJobScaleUp, description, SeverityInfo
		default:
			return CodeJobCreate, description, SeverityInfo
		}
	case JobEventStart:
		return CodeJobStart, description, SeverityInfo
	case JobEventStop:
		if JobStopReason(job) == JobReasonScaleDown {
			return CodeJobScaleDown, description, SeverityInfo
		}
		if job != nil && job.Status == StatusCrashed {
			return CodeJobCrash, description, SeverityError
		}
		return CodeJobStop, description, SeverityInfo
	case JobEventCleanup:
		return CodeJobCleanup, "Job cleaned up", SeverityInfo
	case JobEventError:
		return CodeJobFailed, description, SeverityError
	default:
		return "", "", ""
	}
}

// JobLifecycleMetadata is extra webhook fields (reason, profile) without secrets.
func JobLifecycleMetadata(job *ActiveJob) map[string]string {
	if job == nil || job.Job == nil {
		return nil
	}
	out := map[string]string{}
	if v := JobReason(job); v != "" {
		out["reason"] = v
	}
	if v := JobStopReason(job); v != "" {
		out["stop_reason"] = v
	}
	if v := JobRuntimeProfile(job); v != "" {
		out["runtime"] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func lifecycleExtra(job *ActiveJob) string {
	var parts []string
	if job != nil && job.Job != nil && job.Job.Metadata != nil {
		if n := strings.TrimSpace(job.Job.Metadata[MetaControllerName]); n != "" {
			parts = append(parts, n)
		}
	}
	if p := JobRuntimeProfile(job); p != "" {
		parts = append(parts, "runtime "+p)
	}
	return strings.Join(parts, ", ")
}

// StampJobCommand records the Procfile or Docker command on the host job so
// scale/start logs can show it next to the runtime argv (/runner/init start web).
func StampJobCommand(job *Job, command string) {
	command = strings.TrimSpace(command)
	if job == nil || command == "" {
		return
	}
	if job.Metadata == nil {
		job.Metadata = map[string]string{}
	}
	job.Metadata[MetaControllerCommand] = command
}

// JobProcfileCommand is the process type Command (Procfile line or Docker CMD).
func JobProcfileCommand(job *ActiveJob) string {
	if job == nil || job.Job == nil || job.Job.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(job.Job.Metadata[MetaControllerCommand])
}

// JobArgvCommand is the container argv, usually slugrunner `/runner/init start web`.
func JobArgvCommand(job *ActiveJob) string {
	if job == nil || job.Job == nil || len(job.Job.Config.Args) == 0 {
		return ""
	}
	return strings.Join(job.Job.Config.Args, " ")
}

// JobCommand is the command shown in start/scale log lines: Procfile when set,
// otherwise the container argv.
func JobCommand(job *ActiveJob) string {
	if c := JobProcfileCommand(job); c != "" {
		return c
	}
	return JobArgvCommand(job)
}

func withCommand(msg string, job *ActiveJob) string {
	proc := JobProcfileCommand(job)
	argv := JobArgvCommand(job)
	switch {
	case proc != "" && argv != "" && proc != argv:
		return msg + " with command `" + proc + "` (`" + argv + "`)"
	case proc != "":
		return msg + " with command `" + proc + "`"
	case argv != "":
		return msg + " with command `" + argv + "`"
	default:
		return msg
	}
}

func joinLifecycle(msg, extra string) string {
	if extra == "" {
		return msg
	}
	return msg + " (" + extra + ")"
}
