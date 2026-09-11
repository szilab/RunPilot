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
		if j.Backup.Engine == "robocopy" && j.Backup.Robocopy == nil {
			j.Backup.Robocopy = &RobocopyBackupSpec{Source: j.Backup.Source, Destination: j.Backup.Destination, Mode: j.Backup.Mode, ExcludeDirs: j.Backup.ExcludeDirs, ExcludeFiles: j.Backup.ExcludeFiles, Retries: j.Backup.Retries, RetryWaitSeconds: j.Backup.RetryWaitSeconds, AdditionalArgs: j.Backup.AdditionalArgs}
			j.Backup.Source, j.Backup.Destination, j.Backup.Mode = "", "", ""
			j.Backup.ExcludeDirs, j.Backup.ExcludeFiles, j.Backup.AdditionalArgs = nil, nil, nil
			j.Backup.Retries, j.Backup.RetryWaitSeconds = 0, 0
		}
		if r := j.Backup.Robocopy; r != nil {
			if r.Mode == "" {
				r.Mode = BackupCopy
			}
			if r.Retries < 0 {
				r.Retries = 0
			}
			if r.RetryWaitSeconds <= 0 {
				r.RetryWaitSeconds = 5
			}
		}
	}
}
