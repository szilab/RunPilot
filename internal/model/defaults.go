package model

func DefaultRestartPolicy() RestartPolicy {
	return RestartPolicy{
		Mode:                RestartOnFailure,
		InitialDelaySeconds: 2,
		MaxDelaySeconds:     60,
		MaxRetries:          0,
	}
}

func NormalizeProcess(p *ProcessDefinition) {
	if p.Restart.Mode == "" {
		p.Restart = DefaultRestartPolicy()
	}
	if p.Restart.InitialDelaySeconds <= 0 {
		p.Restart.InitialDelaySeconds = 2
	}
	if p.Restart.MaxDelaySeconds <= 0 {
		p.Restart.MaxDelaySeconds = 60
	}
}

func NormalizeJob(j *JobDefinition) {
	if j.OverlapPolicy == "" {
		j.OverlapPolicy = "skip"
	}
	if j.Type == JobBackup && j.Backup != nil {
		if j.Backup.Engine == "" {
			j.Backup.Engine = "robocopy"
		}
		if j.Backup.Mode == "" {
			j.Backup.Mode = BackupCopy
		}
		if j.Backup.Retries < 0 {
			j.Backup.Retries = 0
		}
		if j.Backup.RetryWaitSeconds <= 0 {
			j.Backup.RetryWaitSeconds = 5
		}
	}
}
