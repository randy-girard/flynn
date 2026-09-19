package host

import (
	"fmt"
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
	MetaControllerRuntimeProfile = "flynn-controller.runtime_profile"
	MetaControllerApp            = "flynn-controller.app"
)

// Additional H-codes for why a process was created (H10 remains a generic create).
const (
	CodeJobRestart   = "H16" // started to replace a crashed process
	CodeJobReplace   = "H17" // started because the release/profile changed
	CodeJobScaleUp   = "H18" // started because the formation scaled up
	CodeJobScaleDown = "H19" // stopped because the formation scaled down
)

// JobShortName is the allocated process name (web.1), from metadata or FLYNN_JOB_NAME.
func JobShortName(job *Job) string {
	if job == nil {
		return ""
	}
	if n := strings.TrimSpace(job.Metadata[MetaControllerName]); n != "" {
		return n
	}
	if job.Config.Env != nil {
		return strings.TrimSpace(job.Config.Env["FLYNN_JOB_NAME"])
	}
	return ""
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
		case JobReasonReplace:
			verb = "Replacing"
		case JobReasonScale:
			verb = "Scaling up"
		}
		return joinLifecycle(fmt.Sprintf("%s %s process", verb, typ), extra)
	case JobEventStart:
		return joinLifecycle(fmt.Sprintf("%s process started", typ), extra)
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
		case JobReasonReplace:
			return CodeJobReplace, description, SeverityInfo
		case JobReasonScale:
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
		out["runtime_profile"] = v
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
		parts = append(parts, "runtime profile "+p)
	}
	return strings.Join(parts, ", ")
}

func joinLifecycle(msg, extra string) string {
	if extra == "" {
		return msg
	}
	return msg + " (" + extra + ")"
}
